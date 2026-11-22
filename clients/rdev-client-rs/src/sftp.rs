use anyhow::Result;
use russh_sftp::{
    protocol::{
        Attrs, Data, File as SftpFile, FileAttributes, Handle, Name, OpenFlags, Status, StatusCode,
        Version,
    },
    server::{Config, Handler},
};
use std::{
    collections::{HashMap, VecDeque},
    io,
    path::PathBuf,
    pin::Pin,
    task::{Context, Poll},
    time::UNIX_EPOCH,
};
use tokio::{
    fs::{self, File, OpenOptions},
    io::{AsyncRead, AsyncReadExt, AsyncSeekExt, AsyncWrite, AsyncWriteExt, ReadBuf},
    sync::{mpsc, oneshot},
};

use crate::{
    protocol::BIN_DATA,
    session::{OutboundEvent, SessionInput},
};

pub async fn run_sftp_session(
    session_id: String,
    rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    let (done_tx, done_rx) = oneshot::channel();
    let stream = RdevSftpStream {
        session_id,
        rx,
        outbound,
        read_buf: VecDeque::new(),
        done_tx: Some(done_tx),
    };
    russh_sftp::server::run_with_config(
        stream,
        FilesystemSftp::default(),
        Config {
            max_client_packet_len: 32 * 1024 * 1024,
        },
    )
    .await;
    let _ = done_rx.await;
    Ok(())
}

struct RdevSftpStream {
    session_id: String,
    rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
    read_buf: VecDeque<u8>,
    done_tx: Option<oneshot::Sender<()>>,
}

impl RdevSftpStream {
    fn signal_done(&mut self) {
        if let Some(done_tx) = self.done_tx.take() {
            let _ = done_tx.send(());
        }
    }

    fn fill_read_buf(&mut self, dst: &mut ReadBuf<'_>) {
        while dst.remaining() > 0 {
            let Some(byte) = self.read_buf.pop_front() else {
                break;
            };
            dst.put_slice(&[byte]);
        }
    }
}

impl Drop for RdevSftpStream {
    fn drop(&mut self) {
        self.signal_done();
    }
}

impl AsyncRead for RdevSftpStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        dst: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        if !self.read_buf.is_empty() {
            self.fill_read_buf(dst);
            return Poll::Ready(Ok(()));
        }

        loop {
            match self.rx.poll_recv(cx) {
                Poll::Ready(Some(SessionInput::Data(data))) => {
                    self.read_buf.extend(data);
                    self.fill_read_buf(dst);
                    return Poll::Ready(Ok(()));
                }
                Poll::Ready(Some(SessionInput::Resize { .. })) => continue,
                Poll::Ready(Some(SessionInput::StdinClose | SessionInput::Close))
                | Poll::Ready(None) => {
                    self.signal_done();
                    return Poll::Ready(Ok(()));
                }
                Poll::Pending => return Poll::Pending,
            }
        }
    }
}

impl AsyncWrite for RdevSftpStream {
    fn poll_write(
        self: Pin<&mut Self>,
        _cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        self.outbound
            .try_send(OutboundEvent::Binary {
                typ: BIN_DATA,
                id: self.session_id.clone(),
                payload: buf.to_vec(),
            })
            .map_err(|err| io::Error::new(io::ErrorKind::BrokenPipe, err))?;
        Poll::Ready(Ok(buf.len()))
    }

    fn poll_flush(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        self.signal_done();
        Poll::Ready(Ok(()))
    }
}

#[derive(Default)]
struct FilesystemSftp {
    handles: HashMap<String, OpenHandle>,
    next_handle: u64,
}

enum OpenHandle {
    File(File),
    Dir { entries: Vec<SftpFile>, sent: bool },
}

impl FilesystemSftp {
    fn next_handle(&mut self) -> String {
        self.next_handle += 1;
        format!("h{}", self.next_handle)
    }

    fn status(id: u32, status_code: StatusCode, message: impl Into<String>) -> Status {
        Status {
            id,
            status_code,
            error_message: message.into(),
            language_tag: "en-US".into(),
        }
    }
}

impl Handler for FilesystemSftp {
    type Error = StatusCode;

    fn unimplemented(&self) -> Self::Error {
        StatusCode::OpUnsupported
    }

    async fn init(
        &mut self,
        _version: u32,
        _extensions: HashMap<String, String>,
    ) -> std::result::Result<Version, Self::Error> {
        Ok(Version::new())
    }

    async fn open(
        &mut self,
        id: u32,
        filename: String,
        pflags: OpenFlags,
        _attrs: FileAttributes,
    ) -> std::result::Result<Handle, Self::Error> {
        let path = sftp_path_to_local(&filename);
        let mut opts = OpenOptions::new();
        opts.read(pflags.contains(OpenFlags::READ))
            .write(pflags.intersects(OpenFlags::WRITE | OpenFlags::APPEND))
            .append(pflags.contains(OpenFlags::APPEND))
            .create(pflags.contains(OpenFlags::CREATE))
            .truncate(pflags.contains(OpenFlags::TRUNCATE))
            .create_new(pflags.contains(OpenFlags::EXCLUDE));
        let file = opts.open(path).await.map_err(status_from_io)?;
        let handle = self.next_handle();
        self.handles.insert(handle.clone(), OpenHandle::File(file));
        Ok(Handle { id, handle })
    }

    async fn close(&mut self, id: u32, handle: String) -> std::result::Result<Status, Self::Error> {
        self.handles.remove(&handle);
        Ok(Self::status(id, StatusCode::Ok, "OK"))
    }

    async fn read(
        &mut self,
        id: u32,
        handle: String,
        offset: u64,
        len: u32,
    ) -> std::result::Result<Data, Self::Error> {
        let Some(OpenHandle::File(file)) = self.handles.get_mut(&handle) else {
            return Err(StatusCode::NoSuchFile);
        };
        file.seek(io::SeekFrom::Start(offset))
            .await
            .map_err(status_from_io)?;
        let mut data = vec![0; (len as usize).min(512 * 1024)];
        let n = file.read(&mut data).await.map_err(status_from_io)?;
        if n == 0 {
            return Err(StatusCode::Eof);
        }
        data.truncate(n);
        Ok(Data { id, data })
    }

    async fn write(
        &mut self,
        id: u32,
        handle: String,
        offset: u64,
        data: Vec<u8>,
    ) -> std::result::Result<Status, Self::Error> {
        let Some(OpenHandle::File(file)) = self.handles.get_mut(&handle) else {
            return Err(StatusCode::NoSuchFile);
        };
        file.seek(io::SeekFrom::Start(offset))
            .await
            .map_err(status_from_io)?;
        file.write_all(&data).await.map_err(status_from_io)?;
        Ok(Self::status(id, StatusCode::Ok, "OK"))
    }

    async fn lstat(&mut self, id: u32, path: String) -> std::result::Result<Attrs, Self::Error> {
        let meta = fs::symlink_metadata(sftp_path_to_local(&path))
            .await
            .map_err(status_from_io)?;
        Ok(Attrs {
            id,
            attrs: attrs_from_meta(&meta),
        })
    }

    async fn stat(&mut self, id: u32, path: String) -> std::result::Result<Attrs, Self::Error> {
        let meta = fs::metadata(sftp_path_to_local(&path))
            .await
            .map_err(status_from_io)?;
        Ok(Attrs {
            id,
            attrs: attrs_from_meta(&meta),
        })
    }

    async fn fstat(&mut self, id: u32, handle: String) -> std::result::Result<Attrs, Self::Error> {
        let Some(OpenHandle::File(file)) = self.handles.get_mut(&handle) else {
            return Err(StatusCode::NoSuchFile);
        };
        let meta = file.metadata().await.map_err(status_from_io)?;
        Ok(Attrs {
            id,
            attrs: attrs_from_meta(&meta),
        })
    }

    async fn setstat(
        &mut self,
        id: u32,
        _path: String,
        _attrs: FileAttributes,
    ) -> std::result::Result<Status, Self::Error> {
        Ok(Self::status(id, StatusCode::Ok, "OK"))
    }

    async fn fsetstat(
        &mut self,
        id: u32,
        _handle: String,
        _attrs: FileAttributes,
    ) -> std::result::Result<Status, Self::Error> {
        Ok(Self::status(id, StatusCode::Ok, "OK"))
    }

    async fn opendir(&mut self, id: u32, path: String) -> std::result::Result<Handle, Self::Error> {
        let mut read_dir = fs::read_dir(sftp_path_to_local(&path))
            .await
            .map_err(status_from_io)?;
        let mut entries = Vec::new();
        while let Some(entry) = read_dir.next_entry().await.map_err(status_from_io)? {
            let filename = entry.file_name().to_string_lossy().to_string();
            let meta = entry.metadata().await.map_err(status_from_io)?;
            entries.push(SftpFile::new(filename, attrs_from_meta(&meta)));
        }
        let handle = self.next_handle();
        self.handles.insert(
            handle.clone(),
            OpenHandle::Dir {
                entries,
                sent: false,
            },
        );
        Ok(Handle { id, handle })
    }

    async fn readdir(&mut self, id: u32, handle: String) -> std::result::Result<Name, Self::Error> {
        let Some(OpenHandle::Dir { entries, sent }) = self.handles.get_mut(&handle) else {
            return Err(StatusCode::NoSuchFile);
        };
        if *sent {
            return Err(StatusCode::Eof);
        }
        *sent = true;
        Ok(Name {
            id,
            files: entries.clone(),
        })
    }

    async fn realpath(&mut self, id: u32, path: String) -> std::result::Result<Name, Self::Error> {
        let path = sftp_path_to_local(&path);
        let resolved = std::fs::canonicalize(&path).unwrap_or(path);
        let filename = local_path_to_sftp(&resolved);
        let attrs = std::fs::metadata(&resolved)
            .map(|meta| attrs_from_meta(&meta))
            .unwrap_or_default();
        Ok(Name {
            id,
            files: vec![SftpFile::new(filename, attrs)],
        })
    }

    async fn remove(
        &mut self,
        id: u32,
        filename: String,
    ) -> std::result::Result<Status, Self::Error> {
        fs::remove_file(sftp_path_to_local(&filename))
            .await
            .map_err(status_from_io)?;
        Ok(Self::status(id, StatusCode::Ok, "OK"))
    }

    async fn mkdir(
        &mut self,
        id: u32,
        path: String,
        _attrs: FileAttributes,
    ) -> std::result::Result<Status, Self::Error> {
        fs::create_dir(sftp_path_to_local(&path))
            .await
            .map_err(status_from_io)?;
        Ok(Self::status(id, StatusCode::Ok, "OK"))
    }

    async fn rmdir(&mut self, id: u32, path: String) -> std::result::Result<Status, Self::Error> {
        fs::remove_dir(sftp_path_to_local(&path))
            .await
            .map_err(status_from_io)?;
        Ok(Self::status(id, StatusCode::Ok, "OK"))
    }

    async fn rename(
        &mut self,
        id: u32,
        oldpath: String,
        newpath: String,
    ) -> std::result::Result<Status, Self::Error> {
        fs::rename(sftp_path_to_local(&oldpath), sftp_path_to_local(&newpath))
            .await
            .map_err(status_from_io)?;
        Ok(Self::status(id, StatusCode::Ok, "OK"))
    }
}

fn sftp_path_to_local(path: &str) -> PathBuf {
    let path = path.replace('\\', "/");
    if cfg!(windows) {
        if let Some(drive_path) = windows_drive_path_tail(&path) {
            return PathBuf::from(drive_path);
        }
    }
    if path.is_empty() {
        PathBuf::from(".")
    } else {
        PathBuf::from(path)
    }
}

fn local_path_to_sftp(path: &std::path::Path) -> String {
    let mut path = path.to_string_lossy().replace('\\', "/");
    if cfg!(windows) {
        path = path
            .strip_prefix("//?/")
            .or_else(|| path.strip_prefix("//./"))
            .unwrap_or(&path)
            .to_string();
        if windows_drive_path_tail(&path).is_some() && !path.starts_with('/') {
            path.insert(0, '/');
        }
    }
    path
}

fn windows_drive_path_tail(path: &str) -> Option<&str> {
    let bytes = path.as_bytes();
    let mut found = None;
    for i in 0..bytes.len().saturating_sub(1) {
        if bytes[i].is_ascii_alphabetic()
            && bytes[i + 1] == b':'
            && (i + 2 == bytes.len() || bytes[i + 2] == b'/' || bytes[i + 2] == b'\\')
            && (i == 0 || bytes[i - 1] == b'/' || bytes[i - 1] == b'\\')
        {
            found = Some(&path[i..]);
        }
    }
    found
}

fn status_from_io(err: io::Error) -> StatusCode {
    match err.kind() {
        io::ErrorKind::NotFound => StatusCode::NoSuchFile,
        io::ErrorKind::PermissionDenied => StatusCode::PermissionDenied,
        _ => StatusCode::Failure,
    }
}

fn attrs_from_meta(meta: &std::fs::Metadata) -> FileAttributes {
    FileAttributes {
        size: Some(meta.len()),
        uid: Some(0),
        user: None,
        gid: Some(0),
        group: None,
        permissions: Some(mode_from_meta(meta)),
        atime: meta.accessed().ok().map(unix_secs_u32),
        mtime: meta.modified().ok().map(unix_secs_u32),
    }
}

#[cfg(unix)]
fn mode_from_meta(meta: &std::fs::Metadata) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    let kind = if meta.is_dir() { 0o040000 } else { 0o100000 };
    kind | (meta.permissions().mode() & 0o7777)
}

#[cfg(not(unix))]
fn mode_from_meta(meta: &std::fs::Metadata) -> u32 {
    if meta.is_dir() {
        0o040000 | 0o755
    } else if meta.permissions().readonly() {
        0o100000 | 0o444
    } else {
        0o100000 | 0o644
    }
}

fn unix_secs_u32(time: std::time::SystemTime) -> u32 {
    time.duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs().min(u32::MAX as u64) as u32)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn windows_drive_path_drops_leading_slash() {
        let path = sftp_path_to_local("/C:/Windows/Temp");
        if cfg!(windows) {
            assert_eq!(path, PathBuf::from("C:/Windows/Temp"));
        } else {
            assert_eq!(path, PathBuf::from("/C:/Windows/Temp"));
        }
    }

    #[test]
    fn windows_drive_path_tail_prefers_embedded_absolute_path() {
        assert_eq!(
            windows_drive_path_tail("//?/C:/Users/Administrator/C:/Windows/Temp"),
            Some("C:/Windows/Temp")
        );
    }
}
