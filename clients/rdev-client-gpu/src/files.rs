use anyhow::{anyhow, Result};
use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc, Mutex,
    },
    time::{Duration, Instant, SystemTime},
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
    upload_gate: Arc<tokio::sync::Mutex<()>>,
    downloads: Arc<Mutex<HashMap<String, DownloadHandle>>>,
    listings: Arc<Mutex<HashMap<PathBuf, DirectorySnapshot>>>,
    listing_generation: Arc<AtomicU64>,
}

struct DirectorySnapshot {
    entries: Arc<Vec<FileEntry>>,
    modified: Option<SystemTime>,
    at: Instant,
    bytes: usize,
}
const DIRECTORY_CACHE_BYTES: usize = 32 * 1024 * 1024;
const DIRECTORY_CACHE_TTL: Duration = Duration::from_secs(10);

#[derive(Debug)]
struct DownloadHandle {
    handle: JoinHandle<()>,
    outbound: mpsc::Sender<OutboundEvent>,
    path: String,
}

impl FileManager {
    fn invalidate_listings(&self) {
        let mut cache = self.listings.lock().unwrap();
        self.listing_generation.fetch_add(1, Ordering::Relaxed);
        cache.clear();
    }

    pub fn new() -> Self {
        Self::default()
    }

    pub async fn mutate(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let create = matches!(msg.ty, Some(MessageType::FileCreate));
        let rename = matches!(msg.ty, Some(MessageType::FileRename));
        let result: Result<()> = async {
            if msg.path.trim().is_empty() {
                return Err(anyhow!("path is required"));
            }
            let path = std::path::absolute(&msg.path)?;
            // Canonical parent resolves '..' without following a final symlink.
            let parent = path
                .parent()
                .ok_or_else(|| anyhow!("cannot modify a filesystem root"))?;
            let parent = fs::canonicalize(parent).await?;
            let name = path.file_name().ok_or_else(|| anyhow!("invalid path"))?;
            let path = parent.join(name);
            if create {
                OpenOptions::new()
                    .write(true)
                    .create_new(true)
                    .open(&path)
                    .await?;
            } else if rename {
                if msg.file_path.trim().is_empty() {
                    return Err(anyhow!("destination is required"));
                }
                match fs::symlink_metadata(&msg.file_path).await {
                    Ok(_) => return Err(anyhow!("destination already exists")),
                    Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
                    Err(e) => return Err(e.into()),
                }
                crate::file_rename::no_replace(&path, Path::new(&msg.file_path))?;
            } else {
                let meta = fs::symlink_metadata(&path).await?;
                if meta.is_dir() {
                    if msg.success {
                        fs::remove_dir_all(&path).await?;
                    } else {
                        fs::remove_dir(&path).await?;
                    }
                } else {
                    fs::remove_file(&path).await?;
                }
            }
            Ok(())
        }
        .await;
        self.invalidate_listings();
        let response = Message {
            ty: Some(if create {
                MessageType::FileCreateResult
            } else if rename {
                MessageType::FileRenameResult
            } else {
                MessageType::FileDeleteResult
            }),
            request_id: msg.request_id,
            path: msg.path,
            success: result.is_ok(),
            error: result.err().map(|e| e.to_string()).unwrap_or_default(),
            ..Default::default()
        };
        let _ = outbound.send(OutboundEvent::Message(response)).await;
    }

    pub async fn mkdir(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let result = if msg.path.trim().is_empty() {
            Err(std::io::Error::new(
                std::io::ErrorKind::InvalidInput,
                "missing directory path",
            ))
        } else {
            fs::create_dir_all(&msg.path).await
        };
        self.invalidate_listings();
        let response = Message {
            ty: Some(MessageType::FileMkdirResult),
            request_id: msg.request_id,
            path: msg.path,
            success: result.is_ok(),
            error: result.err().map(|e| e.to_string()).unwrap_or_default(),
            ..Default::default()
        };
        let _ = outbound.send(OutboundEvent::Message(response)).await;
    }

    pub async fn list(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let request_id = msg.request_id.clone();
        let path = default_path(&msg.path);
        let limit = if msg.size <= 0 {
            200
        } else {
            msg.size.min(2000)
        };
        let offset = msg.offset.max(0);
        let result = self.list_page(&path, offset, limit as usize).await;
        let response = match result {
            Ok((entries, total)) => Message {
                ty: Some(MessageType::FileListResult),
                request_id,
                path: path.to_string_lossy().to_string(),
                parent_path: parent_path(&path).to_string_lossy().to_string(),
                home_path: home_path()
                    .unwrap_or_default()
                    .to_string_lossy()
                    .to_string(),
                truncated: offset + (entries.len() as i64) < total as i64,
                file_entries: entries,
                offset,
                size: limit,
                file_mode: total as i32,
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
        let _gate = self.upload_gate.lock().await;
        let state = self.uploads.lock().unwrap().remove(task_id);
        let Some(mut state) = state else {
            drop(_gate);
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
                drop(_gate);
                let _ = outbound
                    .send(OutboundEvent::BinaryOffset {
                        typ: BIN_FILE_UPLOAD_ACK,
                        id: task_id.to_string(),
                        offset: next,
                        payload: Vec::new(),
                    })
                    .await;
            }
            Err(err) => {
                let path = state.path.clone();
                drop(state);
                drop(_gate);
                send_transfer_error(&outbound, task_id, &path, err).await;
            }
        }
    }

    pub async fn upload_end(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let task_id = msg.task_id.clone();
        let _gate = self.upload_gate.lock().await;
        let state = self.uploads.lock().unwrap().remove(&task_id);
        let Some(mut state) = state else {
            drop(_gate);
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
        self.invalidate_listings();
        drop(_gate);
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
        let _gate = self.upload_gate.lock().await;
        let upload = self.uploads.lock().unwrap().remove(task_id);
        if let Some(mut up) = upload {
            // Cancel also implements pause. Keep the partial file so a new
            // protocol task can resume it, matching the Go client.
            let _ = up.file.flush().await;
        }
        let download = self.downloads.lock().unwrap().remove(task_id);
        drop(_gate);
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
        let _gate = self.upload_gate.lock().await;
        let uploads: Vec<_> = self
            .uploads
            .lock()
            .unwrap()
            .drain()
            .map(|(_, u)| u)
            .collect();
        for mut up in uploads {
            // A disconnected transport must not discard acknowledged bytes.
            let _ = up.file.flush().await;
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

    async fn list_page(
        &self,
        path: &Path,
        offset: i64,
        limit: usize,
    ) -> Result<(Vec<FileEntry>, usize)> {
        let path = fs::canonicalize(path).await?;
        let generation = self.listing_generation.load(Ordering::Relaxed);
        let modified = fs::metadata(&path).await?.modified().ok();
        let cached = {
            let mut cache = self.listings.lock().unwrap();
            cache.retain(|_, entry| entry.at.elapsed() < DIRECTORY_CACHE_TTL);
            if offset > 0 {
                cache
                    .get(&path)
                    .filter(|entry| entry.modified == modified)
                    .map(|entry| entry.entries.clone())
            } else {
                None
            }
        };
        let entries = if let Some(entries) = cached {
            entries
        } else {
            let entries = Arc::new(read_entries(&path).await?);
            let bytes = entries
                .iter()
                .map(|e| {
                    e.name.len()
                        + e.path.len()
                        + e.mod_time.len()
                        + std::mem::size_of::<FileEntry>()
                })
                .sum::<usize>();
            let unchanged = fs::metadata(&path).await?.modified().ok() == modified;
            let mut cache = self.listings.lock().unwrap();
            cache.remove(&path);
            if unchanged
                && generation == self.listing_generation.load(Ordering::Relaxed)
                && bytes <= DIRECTORY_CACHE_BYTES
            {
                while cache.len() >= 8
                    || cache.values().map(|e| e.bytes).sum::<usize>() + bytes
                        > DIRECTORY_CACHE_BYTES
                {
                    let oldest = cache
                        .iter()
                        .min_by_key(|(_, e)| e.at)
                        .map(|(p, _)| p.clone());
                    if let Some(oldest) = oldest {
                        cache.remove(&oldest);
                    } else {
                        break;
                    }
                }
                cache.insert(
                    path,
                    DirectorySnapshot {
                        entries: entries.clone(),
                        modified,
                        at: Instant::now(),
                        bytes,
                    },
                );
            }
            entries
        };
        let start = (offset.max(0) as usize).min(entries.len());
        let end = start.saturating_add(limit).min(entries.len());
        Ok((entries[start..end].to_vec(), entries.len()))
    }

    async fn open_upload(&self, task_id: &str, path: PathBuf, size: i64) -> Result<i64> {
        let _gate = self.upload_gate.lock().await;
        let absolute = std::path::absolute(&path)?;
        let parent = absolute.parent().ok_or_else(|| anyhow!("missing parent"))?;
        fs::create_dir_all(parent).await?;
        let path = fs::canonicalize(parent).await?.join(
            absolute
                .file_name()
                .ok_or_else(|| anyhow!("missing filename"))?,
        );
        let key = |p: &Path| {
            let value = p.to_string_lossy().into_owned();
            if cfg!(windows) {
                value.to_lowercase()
            } else {
                value
            }
        };
        {
            let uploads = self.uploads.lock().unwrap();
            if uploads.contains_key(task_id)
                || uploads.values().any(|up| key(&up.path) == key(&path))
            {
                return Err(anyhow!(
                    "upload already active for this task or destination"
                ));
            }
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

async fn read_entries(path: &Path) -> Result<Vec<FileEntry>> {
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
    entries
        .sort_by_cached_key(|entry| (!entry.is_dir, entry.name.to_lowercase(), entry.name.clone()));
    Ok(entries)
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

    #[tokio::test]
    async fn mutation_refuses_overwrite_and_requires_recursive_delete() {
        let dir = tempfile::tempdir().unwrap();
        let a = dir.path().join("a");
        let b = dir.path().join("b");
        fs::write(&a, b"keep").await.unwrap();
        fs::write(&b, b"existing").await.unwrap();
        let manager = FileManager::new();
        let (tx, mut rx) = mpsc::channel(1);
        manager
            .mutate(
                Message {
                    ty: Some(MessageType::FileRename),
                    path: a.to_string_lossy().into_owned(),
                    file_path: b.to_string_lossy().into_owned(),
                    ..Default::default()
                },
                tx.clone(),
            )
            .await;
        match rx.recv().await.unwrap() {
            OutboundEvent::Message(m) => assert!(!m.success),
            _ => panic!("wrong response"),
        }
        fs::remove_file(&b).await.unwrap();
        manager
            .mutate(
                Message {
                    ty: Some(MessageType::FileRename),
                    path: a.to_string_lossy().into_owned(),
                    file_path: b.to_string_lossy().into_owned(),
                    ..Default::default()
                },
                tx.clone(),
            )
            .await;
        match rx.recv().await.unwrap() {
            OutboundEvent::Message(m) => assert!(m.success, "{}", m.error),
            _ => panic!("wrong response"),
        }
        assert_eq!(fs::read(&b).await.unwrap(), b"keep");
        let nested = dir.path().join("nested");
        fs::create_dir(&nested).await.unwrap();
        fs::write(nested.join("child"), b"data").await.unwrap();
        for recursive in [false, true] {
            manager
                .mutate(
                    Message {
                        ty: Some(MessageType::FileDelete),
                        path: nested.to_string_lossy().into_owned(),
                        success: recursive,
                        ..Default::default()
                    },
                    tx.clone(),
                )
                .await;
            match rx.recv().await.unwrap() {
                OutboundEvent::Message(m) => assert_eq!(m.success, recursive, "{}", m.error),
                _ => panic!("wrong response"),
            }
        }
    }

    #[tokio::test]
    async fn mkdir_preserves_empty_directories_and_reports_conflicts() {
        let dir = tempfile::tempdir().unwrap();
        let nested = dir.path().join("nested/empty");
        let file = dir.path().join("existing-file");
        fs::write(&file, b"keep").await.unwrap();
        let manager = FileManager::new();
        let (tx, mut rx) = mpsc::channel(1);
        for (path, success) in [
            (nested.clone(), true),
            (nested.clone(), true),
            (file.clone(), false),
            (file.join("child"), false),
            (PathBuf::new(), false),
        ] {
            manager
                .mkdir(
                    Message {
                        request_id: "mkdir-1".into(),
                        path: path.to_string_lossy().into_owned(),
                        ..Default::default()
                    },
                    tx.clone(),
                )
                .await;
            let Some(OutboundEvent::Message(response)) = rx.recv().await else {
                panic!("missing response")
            };
            let wire = serde_json::to_value(&response).unwrap();
            assert_eq!(wire["type"], "file_mkdir_result");
            assert_eq!(response.request_id, "mkdir-1");
            assert_eq!(response.success, success);
            assert_eq!(response.error.is_empty(), success);
        }
        assert!(nested.is_dir());
        assert_eq!(fs::read(file).await.unwrap(), b"keep");
    }

    #[tokio::test]
    async fn directory_pages_are_stable_and_report_total() {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir(dir.path().join("z-dir")).await.unwrap();
        for name in ["b", "a", "c"] {
            fs::write(dir.path().join(name), b"").await.unwrap();
        }
        let manager = FileManager::new();
        let (tx, mut rx) = mpsc::channel(1);
        for (offset, names, truncated) in [
            (0, vec!["z-dir", "a"], true),
            (2, vec!["b", "c"], false),
            (3, vec!["c"], false),
            (4, vec![], false),
            (i64::MAX, vec![], false),
            (-1, vec!["z-dir", "a"], true),
        ] {
            manager
                .list(
                    Message {
                        request_id: "page".into(),
                        path: dir.path().to_string_lossy().into_owned(),
                        offset,
                        size: 2,
                        ..Default::default()
                    },
                    tx.clone(),
                )
                .await;
            let Some(OutboundEvent::Message(response)) = rx.recv().await else {
                panic!("missing page")
            };
            assert_eq!(
                response
                    .file_entries
                    .iter()
                    .map(|e| e.name.as_str())
                    .collect::<Vec<_>>(),
                names
            );
            assert_eq!(response.truncated, truncated);
            assert_eq!(response.offset, offset.max(0));
            assert_eq!(response.file_mode, 4);
            assert_eq!(response.size, 2);
            let wire = serde_json::to_value(response).unwrap();
            assert_eq!(wire["fileMode"], 4);
        }
    }

    async fn check_upload_resume(disconnect: bool) {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("resumed.bin");
        let part = dir.path().join("resumed.bin.rdevpart");
        let manager = FileManager::new();
        let (tx, mut rx) = mpsc::channel(16);
        let start = |id: &str| Message {
            task_id: id.into(),
            path: path.to_string_lossy().into_owned(),
            size: 6,
            ..Default::default()
        };
        manager.upload_start(start("first"), tx.clone()).await;
        assert!(matches!(rx.recv().await, Some(OutboundEvent::Message(msg))
            if msg.ty == Some(MessageType::FileUploadReady) && msg.offset == 0));
        manager
            .upload_chunk("first", 0, b"abc".to_vec(), tx.clone())
            .await;
        assert!(matches!(
            rx.recv().await,
            Some(OutboundEvent::BinaryOffset { offset: 3, .. })
        ));
        if disconnect {
            manager.close_all().await;
        } else {
            manager.cancel("first").await;
        }
        assert!(manager.uploads.lock().unwrap().is_empty());
        assert_eq!(fs::read(&part).await.unwrap(), b"abc");
        assert!(!path.exists());

        manager.upload_start(start("resumed"), tx.clone()).await;
        assert!(matches!(rx.recv().await, Some(OutboundEvent::Message(msg))
            if msg.ty == Some(MessageType::FileUploadReady) && msg.offset == 3));
        // Late duplicate cancellation of the old generation cannot touch it.
        manager.cancel("first").await;
        manager
            .upload_chunk("resumed", 3, b"def".to_vec(), tx.clone())
            .await;
        assert!(matches!(
            rx.recv().await,
            Some(OutboundEvent::BinaryOffset { offset: 6, .. })
        ));
        manager.upload_end(start("resumed"), tx).await;
        assert!(matches!(rx.recv().await, Some(OutboundEvent::Message(msg))
            if msg.ty == Some(MessageType::FileTransferEnd) && msg.success));
        assert_eq!(fs::read(&path).await.unwrap(), b"abcdef");
        assert!(!part.exists());
    }

    #[tokio::test]
    async fn upload_resumes_after_pause_with_new_task_id() {
        check_upload_resume(false).await;
    }

    #[tokio::test]
    async fn upload_resumes_after_transport_disconnect() {
        check_upload_resume(true).await;
    }

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

#[cfg(test)]
mod concurrency_tests {
    use super::*;

    #[tokio::test]
    async fn directory_snapshot_refreshes_and_mutations_invalidate() {
        let dir = tempfile::tempdir().unwrap();
        fs::write(dir.path().join("a"), b"a").await.unwrap();
        fs::write(dir.path().join("b"), b"b").await.unwrap();
        let manager = FileManager::new();
        let (first, total) = manager.list_page(dir.path(), 0, 1).await.unwrap();
        assert_eq!(total, 2);
        assert_eq!(first[0].name, "a");
        let (second, _) = manager.list_page(dir.path(), 1, 1).await.unwrap();
        assert_eq!(second[0].name, "b");
        assert_eq!(manager.listings.lock().unwrap().len(), 1);
        manager.invalidate_listings();
        assert!(manager.listings.lock().unwrap().is_empty());
        fs::write(dir.path().join("c"), b"c").await.unwrap();
        let (_, total) = manager.list_page(dir.path(), 0, 1).await.unwrap();
        assert_eq!(total, 3);
    }

    #[tokio::test]
    async fn only_one_task_can_own_a_destination_and_cancel_releases_it() {
        let manager = FileManager::new();
        let dir = tempfile::tempdir().unwrap();
        let target = dir.path().join("file");
        let (a, b) = tokio::join!(
            manager.open_upload("a", target.clone(), 10),
            manager.open_upload("b", target.clone(), 10)
        );
        assert_ne!(a.is_ok(), b.is_ok());
        let owner = if a.is_ok() { "a" } else { "b" };
        manager.cancel(owner).await;
        manager.open_upload("next", target, 10).await.unwrap();
        manager.close_all().await;
    }
    #[tokio::test]
    #[cfg(unix)]
    async fn directory_alias_cannot_bypass_upload_ownership() {
        let manager = FileManager::new();
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir(dir.path().join("actual")).await.unwrap();
        std::os::unix::fs::symlink(dir.path().join("actual"), dir.path().join("alias")).unwrap();
        manager
            .open_upload("a", dir.path().join("actual/file"), 1)
            .await
            .unwrap();
        assert!(manager
            .open_upload("b", dir.path().join("alias/file"), 1)
            .await
            .is_err());
        manager.close_all().await;
    }
    #[test]
    fn no_replace_rename_preserves_destination() {
        let dir = tempfile::tempdir().unwrap();
        let a = dir.path().join("a");
        let b = dir.path().join("b");
        std::fs::write(&a, b"source").unwrap();
        std::fs::write(&b, b"keep").unwrap();
        assert!(crate::file_rename::no_replace(&a, &b).is_err());
        assert_eq!(std::fs::read(&b).unwrap(), b"keep");
        std::fs::remove_file(&b).unwrap();
        crate::file_rename::no_replace(&a, &b).unwrap();
        assert!(!a.exists());
        assert_eq!(std::fs::read(&b).unwrap(), b"source");
    }
}
