use anyhow::{anyhow, Result};
use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
    time::SystemTime,
};
use tokio::{
    fs::{self, File, OpenOptions},
    io::{AsyncReadExt, AsyncSeekExt, AsyncWriteExt},
    sync::mpsc,
    task::JoinHandle,
};

use crate::{
    protocol::{
        FileEntry, Message, MessageType, BIN_FILE_DOWNLOAD_CHUNK, BIN_FILE_TRANSFER_END,
        BIN_FILE_UPLOAD_ACK,
    },
    session::OutboundEvent,
};

#[derive(Debug)]
struct UploadState {
    path: PathBuf,
    part_path: PathBuf,
    file: File,
    size: i64,
    offset: i64,
}

#[derive(Clone, Default)]
pub struct FileManager {
    uploads: Arc<Mutex<HashMap<String, UploadState>>>,
    downloads: Arc<Mutex<HashMap<String, DownloadHandle>>>,
}

#[derive(Debug)]
struct DownloadHandle {
    handle: JoinHandle<()>,
    outbound: mpsc::Sender<OutboundEvent>,
    path: String,
}

impl FileManager {
    pub fn new() -> Self {
        Self::default()
    }

    pub async fn list(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let request_id = msg.request_id.clone();
        let path = default_path(&msg.path);
        let result = list_entries(&path, 2000).await;
        let response = match result {
            Ok((entries, truncated)) => Message {
                ty: Some(MessageType::FileListResult),
                request_id,
                path: path.to_string_lossy().to_string(),
                parent_path: parent_path(&path).to_string_lossy().to_string(),
                home_path: home_path()
                    .unwrap_or_default()
                    .to_string_lossy()
                    .to_string(),
                file_entries: entries,
                truncated,
                ..Default::default()
            },
            Err(err) => Message {
                ty: Some(MessageType::FileListResult),
                request_id,
                path: path.to_string_lossy().to_string(),
                error: err.to_string(),
                ..Default::default()
            },
        };
        let _ = outbound.send(OutboundEvent::Message(response)).await;
    }

    pub async fn upload_start(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let task_id = msg.task_id.clone();
        if task_id.is_empty() {
            return;
        }
        let path = upload_target(&msg);
        let result = self.open_upload(&task_id, path.clone(), msg.size).await;
        match result {
            Ok(offset) => {
                let _ = outbound
                    .send(OutboundEvent::Message(Message {
                        ty: Some(MessageType::FileUploadReady),
                        task_id,
                        path: path.to_string_lossy().to_string(),
                        offset,
                        size: msg.size,
                        ..Default::default()
                    }))
                    .await;
            }
            Err(err) => send_transfer_error(&outbound, &task_id, &path, err).await,
        }
    }

    pub async fn upload_chunk(
        &self,
        task_id: &str,
        offset: i64,
        data: Vec<u8>,
        outbound: mpsc::Sender<OutboundEvent>,
    ) {
        let state = self.uploads.lock().unwrap().remove(task_id);
        let Some(mut state) = state else {
            send_transfer_error(
                &outbound,
                task_id,
                Path::new(""),
                anyhow!("upload not found"),
            )
            .await;
            return;
        };
        let result = async {
            if offset != state.offset {
                return Err(anyhow!("unexpected offset {offset}, want {}", state.offset));
            }
            state.file.write_all(&data).await?;
            state.offset += data.len() as i64;
            Ok::<_, anyhow::Error>(state.offset)
        }
        .await;
        match result {
            Ok(next) => {
                self.uploads
                    .lock()
                    .unwrap()
                    .insert(task_id.to_string(), state);
                let _ = outbound
                    .send(OutboundEvent::BinaryOffset {
                        typ: BIN_FILE_UPLOAD_ACK,
                        id: task_id.to_string(),
                        offset: next,
                        payload: Vec::new(),
                    })
                    .await;
            }
            Err(err) => send_transfer_error(&outbound, task_id, &state.path, err).await,
        }
    }

    pub async fn upload_end(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let task_id = msg.task_id.clone();
        let state = self.uploads.lock().unwrap().remove(&task_id);
        let Some(mut state) = state else {
            send_transfer_error(
                &outbound,
                &task_id,
                Path::new(&msg.path),
                anyhow!("upload not found"),
            )
            .await;
            return;
        };
        let result = async {
            state.file.flush().await?;
            state.file.sync_all().await?;
            drop(state.file);
            if state.size >= 0 && state.offset != state.size {
                return Err(anyhow!(
                    "size mismatch: wrote {} of {}",
                    state.offset,
                    state.size
                ));
            }
            fs::rename(&state.part_path, &state.path).await?;
            Ok::<_, anyhow::Error>(())
        }
        .await;
        match result {
            Ok(()) => {
                let _ = outbound
                    .send(OutboundEvent::Message(Message {
                        ty: Some(MessageType::FileTransferEnd),
                        task_id,
                        path: state.path.to_string_lossy().to_string(),
                        size: state.offset,
                        success: true,
                        ..Default::default()
                    }))
                    .await;
            }
            Err(err) => send_transfer_error(&outbound, &task_id, &state.path, err).await,
        }
    }

    pub async fn download_start(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let task_id = msg.task_id.clone();
        if task_id.is_empty() || msg.path.is_empty() {
            return;
        }
        self.cancel(&task_id).await;
        let path = msg.path.clone();
        let handle = tokio::spawn(download_task(msg, outbound.clone()));
        self.downloads.lock().unwrap().insert(
            task_id,
            DownloadHandle {
                handle,
                outbound,
                path,
            },
        );
    }

    pub async fn cancel(&self, task_id: &str) {
        if task_id.is_empty() {
            return;
        }
        let upload = self.uploads.lock().unwrap().remove(task_id);
        if let Some(mut up) = upload {
            let _ = up.file.flush().await;
            let _ = fs::remove_file(up.part_path).await;
        }
        let download = self.downloads.lock().unwrap().remove(task_id);
        if let Some(download) = download {
            download.handle.abort();
            send_transfer_error(
                &download.outbound,
                task_id,
                Path::new(&download.path),
                anyhow!("transfer canceled"),
            )
            .await;
        }
    }

    pub async fn close_all(&self) {
        let uploads: Vec<_> = self
            .uploads
            .lock()
            .unwrap()
            .drain()
            .map(|(_, u)| u)
            .collect();
        for mut up in uploads {
            let _ = up.file.flush().await;
            let _ = fs::remove_file(up.part_path).await;
        }
        let downloads: Vec<_> = self
            .downloads
            .lock()
            .unwrap()
            .drain()
            .map(|(_, h)| h)
            .collect();
        for download in downloads {
            download.handle.abort();
        }
    }

    async fn open_upload(&self, task_id: &str, path: PathBuf, size: i64) -> Result<i64> {
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).await?;
        }
        let part_path = PathBuf::from(format!("{}.rdevpart", path.to_string_lossy()));
        let mut file = OpenOptions::new()
            .create(true)
            .truncate(false)
            .read(true)
            .write(true)
            .open(&part_path)
            .await?;
        let mut offset = file.metadata().await?.len() as i64;
        if size >= 0 && offset > size {
            file.set_len(0).await?;
            offset = 0;
        }
        file.seek(std::io::SeekFrom::Start(offset as u64)).await?;
        self.uploads.lock().unwrap().insert(
            task_id.to_string(),
            UploadState {
                path,
                part_path,
                file,
                size,
                offset,
            },
        );
        Ok(offset)
    }
}

async fn download_task(msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
    let task_id = msg.task_id.clone();
    let path = PathBuf::from(&msg.path);
    let result = async {
        let mut file = File::open(&path).await?;
        let meta = file.metadata().await?;
        if meta.is_dir() {
            return Err(anyhow!("cannot download directory"));
        }
        let mut offset = msg.offset.clamp(0, meta.len() as i64);
        file.seek(std::io::SeekFrom::Start(offset as u64)).await?;
        let _ = outbound
            .send(OutboundEvent::Message(Message {
                ty: Some(MessageType::FileDownloadStart),
                task_id: task_id.clone(),
                path: path.to_string_lossy().to_string(),
                name: path
                    .file_name()
                    .and_then(|s| s.to_str())
                    .unwrap_or("download")
                    .to_string(),
                size: meta.len() as i64,
                offset,
                mod_time: system_time_rfc3339(meta.modified().ok()),
                ..Default::default()
            }))
            .await;
        let mut buf = vec![0u8; 512 * 1024];
        loop {
            let n = file.read(&mut buf).await?;
            if n == 0 {
                break;
            }
            outbound
                .send(OutboundEvent::BinaryOffset {
                    typ: BIN_FILE_DOWNLOAD_CHUNK,
                    id: task_id.clone(),
                    offset,
                    payload: buf[..n].to_vec(),
                })
                .await
                .ok();
            offset += n as i64;
        }
        outbound
            .send(OutboundEvent::BinaryOffset {
                typ: BIN_FILE_TRANSFER_END,
                id: task_id.clone(),
                offset,
                payload: Vec::new(),
            })
            .await
            .ok();
        outbound
            .send(OutboundEvent::Message(Message {
                ty: Some(MessageType::FileTransferEnd),
                task_id: task_id.clone(),
                path: path.to_string_lossy().to_string(),
                size: meta.len() as i64,
                offset,
                success: true,
                ..Default::default()
            }))
            .await
            .ok();
        Ok::<_, anyhow::Error>(())
    }
    .await;
    if let Err(err) = result {
        send_transfer_error(&outbound, &task_id, &path, err).await;
    }
}

async fn list_entries(path: &Path, limit: usize) -> Result<(Vec<FileEntry>, bool)> {
    let mut read_dir = fs::read_dir(path).await?;
    let mut entries = Vec::new();
    while let Some(ent) = read_dir.next_entry().await? {
        let meta = ent.metadata().await?;
        entries.push(FileEntry {
            name: ent.file_name().to_string_lossy().to_string(),
            path: ent.path().to_string_lossy().to_string(),
            is_dir: meta.is_dir(),
            size: meta.len() as i64,
            mod_time: system_time_rfc3339(meta.modified().ok()),
        });
    }
    entries.sort_by(|a, b| {
        b.is_dir
            .cmp(&a.is_dir)
            .then_with(|| a.name.to_lowercase().cmp(&b.name.to_lowercase()))
    });
    let truncated = entries.len() > limit;
    if truncated {
        entries.truncate(limit);
    }
    Ok((entries, truncated))
}

fn upload_target(msg: &Message) -> PathBuf {
    if !msg.path.is_empty() {
        return PathBuf::from(&msg.path);
    }
    let parent = if msg.parent_path.is_empty() {
        default_path("")
    } else {
        PathBuf::from(&msg.parent_path)
    };
    if msg.name.is_empty() {
        parent
    } else {
        parent.join(Path::new(&msg.name).file_name().unwrap_or_default())
    }
}

fn default_path(path: &str) -> PathBuf {
    if !path.is_empty() {
        return PathBuf::from(path);
    }
    home_path().unwrap_or_else(|| std::env::current_dir().unwrap_or_else(|_| PathBuf::from(".")))
}

fn home_path() -> Option<PathBuf> {
    std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" }).map(PathBuf::from)
}

fn parent_path(path: &Path) -> PathBuf {
    path.parent()
        .map(Path::to_path_buf)
        .unwrap_or_else(|| path.to_path_buf())
}

fn system_time_rfc3339(time: Option<SystemTime>) -> String {
    time.and_then(|t| t.duration_since(SystemTime::UNIX_EPOCH).ok())
        .and_then(|d| chrono::DateTime::from_timestamp(d.as_secs() as i64, d.subsec_nanos()))
        .map(|dt| dt.to_rfc3339())
        .unwrap_or_default()
}

async fn send_transfer_error(
    outbound: &mpsc::Sender<OutboundEvent>,
    task_id: &str,
    path: &Path,
    err: anyhow::Error,
) {
    let _ = outbound
        .send(OutboundEvent::Message(Message {
            ty: Some(MessageType::FileTransferError),
            task_id: task_id.to_string(),
            path: path.to_string_lossy().to_string(),
            error: err.to_string(),
            ..Default::default()
        }))
        .await;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn upload_target_prefers_absolute_path() {
        let msg = Message {
            path: "/tmp/direct.txt".into(),
            parent_path: "/tmp/base".into(),
            name: "ignored.txt".into(),
            ..Default::default()
        };
        assert_eq!(upload_target(&msg), PathBuf::from("/tmp/direct.txt"));
    }

    #[test]
    fn system_time_is_rfc3339() {
        let ts = SystemTime::UNIX_EPOCH + std::time::Duration::from_secs(1_700_000_000);
        let formatted = system_time_rfc3339(Some(ts));
        assert!(formatted.contains('T'));
        assert!(formatted.ends_with("+00:00"));
    }
}
