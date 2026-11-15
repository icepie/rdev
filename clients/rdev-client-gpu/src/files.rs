use anyhow::{anyhow, Result};
use sha2::{Digest, Sha256};
use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc, Mutex, LazyLock,
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
    file_rtc::FileRtcManager,
    protocol::{
        self, FileEntry, Message, MessageType, BIN_FILE_DOWNLOAD_CHUNK, BIN_FILE_TRANSFER_END,
        BIN_FILE_UPLOAD_ACK,
    },
    session::OutboundEvent,
};

#[derive(Debug)]
struct UploadState {
    path: PathBuf,
    part_path: PathBuf,
    meta_path: PathBuf,
    file: File,
    size: i64,
    offset: i64,
    sha256: String,
    rtc_closed: Option<tokio::sync::watch::Receiver<bool>>,
}

#[derive(Clone, Debug)]
struct DownloadFingerprint {
    size: u64,
    version: String,
    sha256: String,
    at: Instant,
}

const MAX_TRANSFER_SIZE: i64 = 8 * 1024 * 1024 * 1024;
const MAX_CHUNK_SIZE: usize = 16 * 1024 * 1024;
const FINGERPRINT_TTL: Duration = Duration::from_secs(30);
#[cfg(unix)]
fn metadata_version(meta: &std::fs::Metadata) -> String {
    use std::os::unix::fs::MetadataExt;
    format!("{}:{}:{}:{}:{}:{}", meta.dev(), meta.ino(), meta.ctime(), meta.ctime_nsec(), meta.len(), meta.mtime_nsec())
}

// Non-Unix std metadata has no portable inode or change-time API; use size and
// modification time as the strongest stable version information available.
#[cfg(not(unix))]
fn metadata_version(meta: &std::fs::Metadata) -> String {
    format!("{}:{:?}", meta.len(), meta.modified().ok())
}

#[derive(Clone)]
pub struct FileManager {
    uploads: Arc<Mutex<HashMap<String, UploadState>>>,
    upload_gate: Arc<tokio::sync::Mutex<()>>,
    downloads: Arc<Mutex<HashMap<String, DownloadHandle>>>,
    listings: Arc<Mutex<HashMap<PathBuf, DirectorySnapshot>>>,
    listing_generation: Arc<AtomicU64>,
    rtc: Arc<Mutex<Option<crate::file_rtc::FileRtcManager>>>,
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
    generation: Arc<()>,
}

impl Default for FileManager {
    fn default() -> Self {
        Self::new()
    }
}

impl FileManager {
    fn invalidate_listings(&self) {
        let mut cache = self.listings.lock().unwrap();
        self.listing_generation.fetch_add(1, Ordering::Relaxed);
        cache.clear();
    }

    pub fn new() -> Self {
        Self {
            uploads: Arc::new(Mutex::new(HashMap::new())),
            upload_gate: Arc::new(tokio::sync::Mutex::new(())),
            downloads: Arc::new(Mutex::new(HashMap::new())),
            listings: Arc::new(Mutex::new(HashMap::new())),
            listing_generation: Arc::new(AtomicU64::new(0)),
            rtc: Arc::new(Mutex::new(None)),
        }
    }
    pub fn attach_rtc(&self, rtc: crate::file_rtc::FileRtcManager) {
        *self.rtc.lock().unwrap() = Some(rtc);
    }
    fn rtc(&self) -> Option<crate::file_rtc::FileRtcManager> {
        self.rtc.lock().unwrap().clone()
    }
    fn upload_part_for(&self, task_id: &str) -> Option<PathBuf> {
        self.uploads.lock().unwrap().get(task_id).map(|state| state.part_path.clone())
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
        if msg.rtc_type == "offer" {
            let rtc = self.rtc();
            let result = rtc
                .as_ref()
                .ok_or_else(|| anyhow!("file RTC unavailable"))
                .and_then(|rtc| rtc.start(msg.clone(), outbound.clone()));
            match result {
                Ok(task) => {
                    let files = self.clone();
                    tokio::spawn(async move {
                        files
                            .upload_start_rtc(msg, outbound, rtc.unwrap(), task)
                            .await;
                    });
                }
                Err(err) => {
                    send_transfer_error(&outbound, &task_id, &upload_target(&msg), err).await
                }
            }
            return;
        }
        let path = upload_target(&msg);
        let result = self
            .open_upload(&task_id, path.clone(), msg.size, &msg.sha256, msg.restart, None)
            .await;
        match result {
            Ok(offset) => {
                let partial = self.upload_part_for(&task_id).unwrap_or_else(|| part_path_for(&path));
                let prefix_sha256 = match hash_prefix(&partial, offset).await {
                    Ok(hash) => hash,
                    Err(err) => { self.cancel(&task_id).await; send_transfer_error(&outbound, &task_id, &path, err).await; return; }
                };
                let _ = outbound
                    .send(OutboundEvent::Message(Message {
                        ty: Some(MessageType::FileUploadReady), task_id, path: path.to_string_lossy().to_string(),
                        offset, size: msg.size, sha256: msg.sha256, prefix_sha256, ..Default::default()
                    }))
                    .await;
            }
            Err(err) => send_transfer_error(&outbound, &task_id, &path, err).await,
        }
    }

    async fn upload_start_rtc(
        &self,
        msg: Message,
        outbound: mpsc::Sender<OutboundEvent>,
        rtc: FileRtcManager,
        task: crate::file_rtc::FileRtcTask,
    ) {
        let crate::file_rtc::FileRtcTask {
            open,
            mut events,
            mut closed,
        } = task;
        let operation = async {
            open.await
                .map_err(|_| anyhow!("file RTC negotiation closed"))?;
            let path = upload_target(&msg);
            let offset = self
                .open_upload(&msg.task_id, path.clone(), msg.size, &msg.sha256, msg.restart, Some(&rtc))
                .await?;
            let partial = self.upload_part_for(&msg.task_id).ok_or_else(|| anyhow!("upload state disappeared"))?;
            let prefix_sha256 = hash_prefix(&partial, offset).await?;
            let _ = outbound
                .send(OutboundEvent::Message(Message {
                    ty: Some(MessageType::FileUploadReady), task_id: msg.task_id.clone(),
                    path: path.to_string_lossy().to_string(), offset, size: msg.size,
                    sha256: msg.sha256.clone(), prefix_sha256, ..Default::default()
                }))
                .await;
            while let Some(event) = events.recv().await {
                match event {
                    crate::file_rtc::FileRtcEvent::Binary(raw) => {
                        match protocol::decode_bin_frame_offset(&raw) {
                            Ok((typ, id, at, data))
                                if typ == crate::protocol::BIN_FILE_UPLOAD_CHUNK
                                    && (id.is_empty() || id == msg.task_id) =>
                            {
                                self.upload_chunk_inner(
                                    &msg.task_id,
                                    at,
                                    data,
                                    outbound.clone(),
                                    Some(rtc.clone()),
                                )
                                .await
                            }
                            _ => {
                                rtc.remove(&msg.task_id);
                                break;
                            }
                        }
                    }
                }
            }
            Ok::<_, anyhow::Error>(())
        };
        let result = tokio::select! {
            biased;
            _ = closed.wait_for(|closed| *closed) => Ok(()),
            result = operation => result,
        };
        rtc.remove_if_current(&msg.task_id, &closed);
        // Serialize with open/write/finalize, including a cancel during file open.
        let gate = self.upload_gate.lock().await;
        let upload = {
            let mut uploads = self.uploads.lock().unwrap();
            if uploads
                .get(&msg.task_id)
                .and_then(|up| up.rtc_closed.as_ref())
                .is_some_and(|owner| owner.same_channel(&closed))
            {
                uploads.remove(&msg.task_id)
            } else {
                None
            }
        };
        if let Some(mut upload) = upload {
            let _ = upload.file.flush().await;
        }
        drop(gate);
        if let Err(err) = result {
            send_transfer_error(&outbound, &msg.task_id, &upload_target(&msg), err).await;
        }
    }

    pub async fn upload_chunk(
        &self,
        task_id: &str,
        offset: i64,
        data: Vec<u8>,
        outbound: mpsc::Sender<OutboundEvent>,
    ) {
        self.upload_chunk_inner(task_id, offset, data, outbound, None)
            .await;
    }
    async fn upload_chunk_inner(
        &self,
        task_id: &str,
        offset: i64,
        data: Vec<u8>,
        outbound: mpsc::Sender<OutboundEvent>,
        rtc: Option<FileRtcManager>,
    ) {
        if data.is_empty() || data.len() > MAX_CHUNK_SIZE {
            send_transfer_error(&outbound, task_id, Path::new(""), anyhow!("invalid upload chunk size")).await;
            return;
        }
        let _gate = self.upload_gate.lock().await;
        if rtc.as_ref().is_some_and(|rtc| !rtc.active(task_id)) {
            return;
        }
        let state = self.uploads.lock().unwrap().remove(task_id);
        let Some(mut state) = state else {
            drop(_gate);
            if let Some(rtc) = rtc.as_ref() {
                rtc.remove(task_id);
            }
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
            if offset != state.offset { return Err(anyhow!("unexpected offset {offset}, want {}", state.offset)); }
            let next = state.offset.checked_add(data.len() as i64).ok_or_else(|| anyhow!("upload size overflow"))?;
            if next > state.size { return Err(anyhow!("upload exceeds declared size")); }
            state.file.write_all(&data).await?;
            state.offset = next;
            Ok::<_, anyhow::Error>(state.offset)
        }.await;
        match result {
            Ok(next) => {
                self.uploads
                    .lock()
                    .unwrap()
                    .insert(task_id.to_string(), state);
                drop(_gate);
                if let Some(rtc) = rtc {
                    if rtc.acknowledge(task_id, next).is_err() {
                        rtc.remove(task_id);
                    }
                } else {
                    let _ = outbound
                        .send(OutboundEvent::BinaryOffset {
                            typ: BIN_FILE_UPLOAD_ACK,
                            id: task_id.to_string(),
                            offset: next,
                            payload: Vec::new(),
                        })
                        .await;
                }
            }
            Err(err) => {
                let path = state.path.clone();
                drop(state);
                drop(_gate);
                if let Some(rtc) = rtc {
                    rtc.remove(task_id);
                }
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
            if state
                .rtc_closed
                .as_ref()
                .is_some_and(|closed| *closed.borrow())
            {
                return Err(anyhow!("file RTC task canceled"));
            }
            state.file.flush().await?;
            state.file.sync_all().await?;
            drop(state.file);
            if state.offset != state.size {
                return Err(anyhow!("size mismatch: wrote {} of {}", state.offset, state.size));
            }
            let actual = hash_file(&state.part_path).await?;
            if actual != state.sha256 {
                let _ = fs::remove_file(&state.meta_path).await;
                return Err(anyhow!("upload SHA-256 mismatch"));
            }
            if state.rtc_closed.as_ref().is_some_and(|closed| *closed.borrow()) {
                return Err(anyhow!("file RTC task canceled"));
            }
            fs::rename(&state.part_path, &state.path).await?;
            let _ = fs::remove_file(&state.meta_path).await;
            Ok::<_, anyhow::Error>(())
        }
        .await;
        self.invalidate_listings();
        drop(_gate);
        match result {
            Ok(()) => {
                if let Some(rtc) = self.rtc() {
                    rtc.completed(&task_id);
                }
                let _ = outbound
                    .send(OutboundEvent::Message(Message {
                        ty: Some(MessageType::FileTransferEnd),
                        task_id,
                        path: state.path.to_string_lossy().to_string(),
                        size: state.offset,
                        success: true,
                        sha256: state.sha256.clone(),
                        ..Default::default()
                    }))
                    .await;
            }
            Err(err) => {
                if let Some(rtc) = self.rtc() {
                    rtc.remove(&task_id);
                }
                send_transfer_error(&outbound, &task_id, &state.path, err).await;
            }
        }
    }

    pub async fn download_start(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let task_id = msg.task_id.clone();
        if task_id.is_empty() || msg.path.is_empty() {
            return;
        }
        self.cancel(&task_id).await;
        let path = msg.path.clone();
        let rtc = if msg.rtc_type == "offer" {
            self.rtc()
        } else {
            None
        };
        let task = if let Some(rtc) = rtc.as_ref() {
            match rtc.start(msg.clone(), outbound.clone()) {
                Ok(task) => Some(task),
                Err(err) => {
                    send_transfer_error(&outbound, &task_id, Path::new(&path), err).await;
                    return;
                }
            }
        } else {
            None
        };
        let (start, started) = tokio::sync::oneshot::channel();
        let files = self.clone();
        let id = task_id.clone();
        let output = outbound.clone();
        let generation = Arc::new(());
        let current = generation.clone();
        let handle = tokio::spawn(async move {
            let _ = started.await;
            if let (Some(rtc), Some(task)) = (rtc, task) {
                download_rtc_wait(msg, output, rtc, task).await;
            } else {
                download_task(msg, output, None).await;
            }
            let mut downloads = files.downloads.lock().unwrap();
            if downloads
                .get(&id)
                .is_some_and(|entry| Arc::ptr_eq(&entry.generation, &current))
            {
                downloads.remove(&id);
            }
        });
        self.downloads.lock().unwrap().insert(
            task_id,
            DownloadHandle {
                handle,
                outbound,
                path,
                generation,
            },
        );
        let _ = start.send(());
    }

    pub async fn cancel(&self, task_id: &str) {
        if task_id.is_empty() {
            return;
        }
        if let Some(rtc) = self.rtc() {
            rtc.remove(task_id);
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
        if let Some(rtc) = self.rtc() {
            rtc.close_all();
        }
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

    async fn open_upload(
        &self,
        task_id: &str,
        path: PathBuf,
        size: i64,
        expected_sha256: &str,
        restart: bool,
        rtc: Option<&FileRtcManager>,
    ) -> Result<i64> {
        let _gate = self.upload_gate.lock().await;
        let rtc_closed = rtc.map(|rtc| rtc.cancellation(task_id)).transpose()?;
        if rtc.is_some_and(|rtc| !rtc.active(task_id)) { return Err(anyhow!("file RTC task canceled")); }
        if !(0..=MAX_TRANSFER_SIZE).contains(&size) { return Err(anyhow!("invalid upload size")); }
        let expected_sha256 = validate_hash(expected_sha256)?;
        let absolute = std::path::absolute(&path)?;
        let parent = absolute.parent().ok_or_else(|| anyhow!("missing parent"))?;
        fs::create_dir_all(parent).await?;
        let path = fs::canonicalize(parent).await?.join(absolute.file_name().ok_or_else(|| anyhow!("missing filename"))?);
        let key = |p: &Path| if cfg!(windows) { p.to_string_lossy().to_lowercase() } else { p.to_string_lossy().into_owned() };
        {
            let uploads = self.uploads.lock().unwrap();
            if uploads.contains_key(task_id) || uploads.values().any(|up| key(&up.path) == key(&path)) {
                return Err(anyhow!("upload already active for this task or destination"));
            }
        }
        let part_path = PathBuf::from(format!("{}.rdevpart", path.to_string_lossy()));
        let meta_path = PathBuf::from(format!("{}.meta", part_path.to_string_lossy()));
        let mut file = OpenOptions::new().create(true).truncate(false).read(true).write(true).open(&part_path).await?;
        let mut offset = file.metadata().await?.len();
        let binding = match fs::metadata(&meta_path).await {
            Ok(meta) if meta.len() <= 4096 => fs::read(&meta_path).await.ok().and_then(|raw| serde_json::from_slice::<Sidecar>(&raw).ok()),
            _ => None,
        };
        let resume = !restart && !expected_sha256.is_empty() && binding.as_ref().is_some_and(|b| b.sha256 == expected_sha256 && b.size == size) && offset <= size as u64;
        if !resume { file.set_len(0).await?; offset = 0; }
        if !expected_sha256.is_empty() {
            write_sidecar(&meta_path, &Sidecar { sha256: expected_sha256.clone(), size }).await?;
        } else { let _ = fs::remove_file(&meta_path).await; }
        file.seek(std::io::SeekFrom::Start(offset)).await?;
        if rtc_closed.as_ref().is_some_and(|closed| *closed.borrow()) { return Err(anyhow!("file RTC task canceled")); }
        self.uploads.lock().unwrap().insert(task_id.to_string(), UploadState {
            path, part_path, meta_path, file, size, offset: offset as i64, sha256: expected_sha256, rtc_closed,
        });
        Ok(offset as i64)
    }
}
#[derive(serde::Serialize, serde::Deserialize)]
struct Sidecar { sha256: String, size: i64 }

fn to_hex(bytes: &[u8]) -> String { bytes.iter().map(|b| format!("{b:02x}")).collect() }
fn part_path_for(path: &Path) -> PathBuf { PathBuf::from(format!("{}.rdevpart", path.to_string_lossy())) }

fn validate_hash(value: &str) -> Result<String> {
    if value.len() != 64 || !value.bytes().all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase()) {
        return Err(anyhow!("sha256 must be 64 lowercase hexadecimal characters"));
    }
    Ok(value.to_owned())
}

async fn hash_file(path: &Path) -> Result<String> {
    let mut file = File::open(path).await?;
    let mut hash = Sha256::new();
    let mut buffer = [0u8; 64 * 1024];
    loop { let n = file.read(&mut buffer).await?; if n == 0 { break; } hash.update(&buffer[..n]); }
    Ok(to_hex(&hash.finalize()))
}

async fn hash_prefix(path: &Path, length: i64) -> Result<String> {
    let mut file = File::open(path).await?;
    let mut remaining = u64::try_from(length).map_err(|_| anyhow!("invalid prefix length"))?;
    let mut hash = Sha256::new();
    let mut buffer = [0u8; 64 * 1024];
    while remaining > 0 {
        let capacity = buffer.len().min(remaining as usize);
        let n = file.read(&mut buffer[..capacity]).await?;
        if n == 0 { return Err(anyhow!("partial file shorter than resume offset")); }
        hash.update(&buffer[..n]); remaining -= n as u64;
    }
    Ok(to_hex(&hash.finalize()))
}

static DOWNLOAD_FINGERPRINTS: LazyLock<Mutex<HashMap<PathBuf, DownloadFingerprint>>> = LazyLock::new(|| Mutex::new(HashMap::new()));

async fn hash_open_file(file: &mut File) -> Result<String> {
    let saved = file.seek(std::io::SeekFrom::Current(0)).await?;
    file.seek(std::io::SeekFrom::Start(0)).await?;
    let mut hash = Sha256::new();
    let mut buffer = [0u8; 64 * 1024];
    loop { let n = file.read(&mut buffer).await?; if n == 0 { break; } hash.update(&buffer[..n]); }
    file.seek(std::io::SeekFrom::Start(saved)).await?;
    Ok(to_hex(&hash.finalize()))
}

async fn download_fingerprint(path: &Path, file: &mut File, meta: &std::fs::Metadata) -> Result<String> {
    let version = metadata_version(meta);
    let cache = &*DOWNLOAD_FINGERPRINTS;
    if let Some(cached) = cache.lock().unwrap().get(path) {
        if cached.size == meta.len() && cached.version == version && cached.at.elapsed() < FINGERPRINT_TTL {
            return Ok(cached.sha256.clone());
        }
    }
    let sha256 = hash_open_file(file).await?;
    let after = file.metadata().await?;
    if metadata_version(&after) != version { return Err(anyhow!("download source changed while hashing")); }
    {
        let mut entries = cache.lock().unwrap();
        entries.retain(|_, value| value.at.elapsed() < FINGERPRINT_TTL);
        while entries.len() >= 64 {
            let oldest = entries.iter().min_by_key(|(_, value)| value.at).map(|(path, _)| path.clone());
            if let Some(path) = oldest { entries.remove(&path); } else { break; }
        }
        entries.insert(path.to_path_buf(), DownloadFingerprint { size: meta.len(), version, sha256: sha256.clone(), at: Instant::now() });
    }
    Ok(sha256)
}
async fn write_sidecar(path: &Path, sidecar: &Sidecar) -> Result<()> {
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let temp = path.with_extension(format!("meta.tmp.{}.{}", std::process::id(), NEXT.fetch_add(1, Ordering::Relaxed)));
    let mut file = OpenOptions::new().write(true).create_new(true).open(&temp).await?;
    file.write_all(&serde_json::to_vec(sidecar)?).await?;
    file.sync_all().await?;
    drop(file);
    if let Err(error) = fs::rename(&temp, path).await { let _ = fs::remove_file(&temp).await; return Err(error.into()); }
    Ok(())
}

async fn download_task(
    msg: Message,
    outbound: mpsc::Sender<OutboundEvent>,
    rtc: Option<FileRtcManager>,
) {
    download_task_inner(msg, outbound, rtc, None).await;
}

async fn download_task_inner(
    msg: Message,
    outbound: mpsc::Sender<OutboundEvent>,
    rtc: Option<FileRtcManager>,
    mut range_events: Option<&mut mpsc::Receiver<crate::file_rtc::FileRtcEvent>>,
) {
    let task_id = msg.task_id.clone();
    let path = PathBuf::from(&msg.path);
    let result = async {
        let mut file = File::open(&path).await?;
        let meta = file.metadata().await?;
        if meta.is_dir() {
            return Err(anyhow!("cannot download directory"));
        }
        let size = i64::try_from(meta.len()).map_err(|_| anyhow!("file is too large"))?;
        if msg.length < 0 || (msg.length > 0 && (msg.offset < 0 || msg.offset > size)) {
            return Err(anyhow!("invalid download range"));
        }
        let source_sha256 = download_fingerprint(&path, &mut file, &meta).await?;
        if !msg.sha256.is_empty() && msg.sha256 != source_sha256 {
            return Err(anyhow!("download source SHA-256 changed"));
        }
        if msg.length > MAX_TRANSFER_SIZE { return Err(anyhow!("download range too large")); }
        let mut offset = msg.offset.clamp(0, size);
        let length = if msg.length > 0 {
            (size - offset).min(msg.length)
        } else {
            size - offset
        };
        let mut segment_end = offset + length;
        file.seek(std::io::SeekFrom::Start(offset as u64)).await?;
        outbound
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
                length,
                mod_time: system_time_rfc3339(meta.modified().ok()),
                sha256: source_sha256.clone(),
                ..Default::default()
            }))
            .await
            .map_err(|_| anyhow!("transfer transport closed"))?;
        let mut buf = vec![0u8; crate::file_rtc::FILE_RTC_MAX_PAYLOAD];
        loop {
            let remaining = if msg.length > 0 {
                segment_end - offset
            } else {
                buf.len() as i64
            };
            if remaining == 0 {
                if offset < size && range_events.is_some() {
                    let current = file.metadata().await?;
                    if metadata_version(&current) != metadata_version(&meta) {
                        DOWNLOAD_FINGERPRINTS.lock().unwrap().remove(&path);
                        return Err(anyhow!("download source changed during transfer"));
                    }
                    let rtc = rtc.as_ref().ok_or_else(|| anyhow!("missing range RTC"))?;
                    // Arm before publishing the boundary, so an immediate ACK
                    // is accepted. The transport rejects ACKs at all other times.
                    rtc.wait_for_range_ack(&task_id, offset)?;
                    rtc.send_end(&task_id, offset).await?;
                    wait_download_range_ack(range_events.as_deref_mut().unwrap(), rtc, &task_id)
                        .await?;
                    segment_end = offset + (size - offset).min(msg.length);
                    continue;
                }
                break;
            }
            let capacity = buf.len().min(remaining as usize);
            let n = file.read(&mut buf[..capacity]).await?;
            if n == 0 {
                if msg.length > 0 && offset != segment_end {
                    return Err(anyhow!("file changed during range download"));
                }
                break;
            }
            if let Some(rtc) = rtc.as_ref() {
                rtc.send_offset(&task_id, BIN_FILE_DOWNLOAD_CHUNK, offset, &buf[..n])
                    .await?;
            } else {
                outbound
                    .send(OutboundEvent::BinaryOffset {
                        typ: BIN_FILE_DOWNLOAD_CHUNK,
                        id: task_id.clone(),
                        offset,
                        payload: buf[..n].to_vec(),
                    })
                    .await
                    .map_err(|_| anyhow!("transfer transport closed"))?;
            }
            offset += n as i64;
        }
        let current = file.metadata().await?;
        if metadata_version(&current) != metadata_version(&meta) {
            DOWNLOAD_FINGERPRINTS.lock().unwrap().remove(&path);
            return Err(anyhow!("download source changed during transfer"));
        }
        if let Some(rtc) = rtc.as_ref() {
            rtc.send_end(&task_id, offset).await?;
            rtc.drain(&task_id).await?;
            rtc.completed(&task_id);
        } else {
            outbound
                .send(OutboundEvent::BinaryOffset {
                    typ: BIN_FILE_TRANSFER_END,
                    id: task_id.clone(),
                    offset,
                    payload: Vec::new(),
                })
                .await
                .map_err(|_| anyhow!("transfer transport closed"))?;
        }
        outbound
            .send(OutboundEvent::Message(Message {
                ty: Some(MessageType::FileTransferEnd),
                task_id: task_id.clone(),
                path: path.to_string_lossy().to_string(),
                size: meta.len() as i64,
                offset,
                success: true,
                sha256: source_sha256,
                ..Default::default()
            }))
            .await
            .ok();
        Ok::<_, anyhow::Error>(())
    }
    .await;
    if let Err(err) = result {
        if let Some(rtc) = rtc {
            rtc.remove(&task_id);
        }
        send_transfer_error(&outbound, &task_id, &path, err).await;
    }
}

async fn wait_download_range_ack(
    events: &mut mpsc::Receiver<crate::file_rtc::FileRtcEvent>,
    rtc: &FileRtcManager,
    task_id: &str,
) -> Result<()> {
    let mut closed = rtc.cancellation(task_id)?;
    tokio::select! {
        biased;
        _ = closed.wait_for(|closed| *closed) => Err(anyhow!("file RTC closed")),
        event = events.recv() => {
            event.ok_or_else(|| anyhow!("file RTC closed"))?;
            // The session already checked exact framing, offset, and ACK phase.
            rtc.resume_range(task_id)
        }
    }
}

async fn download_rtc_wait(
    msg: Message,
    outbound: mpsc::Sender<OutboundEvent>,
    rtc: FileRtcManager,
    task: crate::file_rtc::FileRtcTask,
) {
    let crate::file_rtc::FileRtcTask {
        open,
        mut events,
        mut closed,
    } = task;
    let id = msg.task_id.clone();
    if crate::file_rtc::range_ack_enabled(&msg) {
        let operation = async {
            if open.await.is_ok() {
                download_task_inner(msg, outbound, Some(rtc.clone()), Some(&mut events)).await;
            } else {
                rtc.remove(&id);
            }
        };
        tokio::select! {
            biased;
            _ = closed.wait_for(|closed| *closed) => {},
            _ = operation => {},
        }
        return;
    }
    let operation = async {
        if open.await.is_ok() {
            download_task(msg, outbound, Some(rtc.clone())).await;
        } else {
            rtc.remove(&id);
        }
    };
    tokio::select! {
        biased;
        _ = closed.wait_for(|closed| *closed) => {},
        _ = operation => {},
        // Download peers may not submit upload frames.
        _ = events.recv() => { rtc.remove(&id); }
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
        let digest = "bef57ec7f53a6d40beb640a780a639c83bc29ac8a9816f1fc6c5c6dcd93c4721";
        let start = |id: &str| Message {
            task_id: id.into(),
            path: path.to_string_lossy().into_owned(),
            size: 6,
            sha256: digest.to_owned(),
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
    async fn changed_upload_source_resets_old_partial_before_resume() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("changed.bin");
        let part = part_path_for(&path);
        let meta = PathBuf::from(format!("{}.meta", part.to_string_lossy()));
        let old_hash = to_hex(&Sha256::digest(b"OLD!"));
        let new_hash = to_hex(&Sha256::digest(b"bbbbbbbb"));
        fs::write(&part, b"OLD!").await.unwrap();
        write_sidecar(&meta, &Sidecar { sha256: old_hash, size: 4 }).await.unwrap();

        let manager = FileManager::new();
        let (tx, mut rx) = mpsc::channel(8);
        manager.upload_start(Message { task_id: "new-source".into(), path: path.to_string_lossy().into_owned(), size: 8, sha256: new_hash.clone(), ..Default::default() }, tx.clone()).await;
        let Some(OutboundEvent::Message(ready)) = rx.recv().await else { panic!("missing ready") };
        assert_eq!(ready.offset, 0);
        assert_eq!(ready.sha256, new_hash);
        assert_eq!(ready.prefix_sha256, to_hex(&Sha256::digest(b"")));
        manager.upload_chunk("new-source", 0, b"bbbbbbbb".to_vec(), tx.clone()).await;
        let _ = rx.recv().await;
        manager.upload_end(Message { task_id: "new-source".into(), ..Default::default() }, tx.clone()).await;
        let Some(OutboundEvent::Message(end)) = rx.recv().await else { panic!("missing end") };
        assert!(end.success, "{}", end.error);
        assert_eq!(end.sha256, new_hash);
        assert_eq!(fs::read(path).await.unwrap(), b"bbbbbbbb");
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
            manager.open_upload("a", target.clone(), 10, "0000000000000000000000000000000000000000000000000000000000000000", false, None),
            manager.open_upload("b", target.clone(), 10, "0000000000000000000000000000000000000000000000000000000000000000", false, None)
        );
        assert_ne!(a.is_ok(), b.is_ok());
        let owner = if a.is_ok() { "a" } else { "b" };
        manager.cancel(owner).await;
        manager.open_upload("next", target, 10, "0000000000000000000000000000000000000000000000000000000000000000", false, None).await.unwrap();
        manager.close_all().await;
    }
    #[tokio::test]
    async fn canceled_direct_start_does_not_create_partial_file() {
        let manager = FileManager::new();
        let rtc = FileRtcManager::new();
        manager.attach_rtc(rtc.clone());
        let dir = tempfile::tempdir().unwrap();
        let target = dir.path().join("canceled");
        manager.cancel("canceled-task").await;
        let result = manager
            .open_upload("canceled-task", target, 10, "0000000000000000000000000000000000000000000000000000000000000000", false, Some(&rtc))
            .await;
        assert!(result.is_err());
        assert_eq!(std::fs::read_dir(dir.path()).unwrap().count(), 0);
    }

    #[tokio::test]
    #[cfg(unix)]
    async fn directory_alias_cannot_bypass_upload_ownership() {
        let manager = FileManager::new();
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir(dir.path().join("actual")).await.unwrap();
        std::os::unix::fs::symlink(dir.path().join("actual"), dir.path().join("alias")).unwrap();
        manager
            .open_upload("a", dir.path().join("actual/file"), 1, "0000000000000000000000000000000000000000000000000000000000000000", false, None)
            .await
            .unwrap();
        assert!(manager
            .open_upload("b", dir.path().join("alias/file"), 1, "0000000000000000000000000000000000000000000000000000000000000000", false, None)
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

    #[tokio::test]
    async fn ranged_download_rejects_changed_source_hash() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("changed-range");
        fs::write(&path, b"original").await.unwrap();
        let old_hash = hash_file(&path).await.unwrap();
        fs::write(&path, b"different source").await.unwrap();
        let (tx, mut rx) = mpsc::channel(4);
        download_task(Message { task_id: "changed-range".into(), path: path.to_string_lossy().into_owned(), offset: 0, length: 4, sha256: old_hash, ..Default::default() }, tx, None).await;
        let Some(OutboundEvent::Message(error)) = rx.recv().await else { panic!("missing source-change error") };
        assert_eq!(error.ty, Some(MessageType::FileTransferError));
        assert_eq!(error.error, "download source SHA-256 changed");
        assert!(rx.recv().await.is_none());
    }

    #[tokio::test]
    async fn download_fingerprint_cache_is_bounded() {
        let dir = tempfile::tempdir().unwrap();
        for index in 0..70 {
            let path = dir.path().join(format!("fingerprint-{index}"));
            fs::write(&path, [index as u8]).await.unwrap();
            let mut file = File::open(&path).await.unwrap();
            let meta = file.metadata().await.unwrap();
            download_fingerprint(&path, &mut file, &meta).await.unwrap();
        }
        assert!(DOWNLOAD_FINGERPRINTS.lock().unwrap().len() <= 64);
    }

    #[tokio::test]
    async fn ranged_download_bounds_metadata_payload_and_terminal_offset() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("ranges");
        let data = vec![42u8; 40_000];
        fs::write(&path, &data).await.unwrap();
        for (offset, requested, expected) in
            [(123, 20_000, 20_000), (39_990, 500, 10), (40_000, 1, 0)]
        {
            let (tx, mut rx) = mpsc::channel(16);
            download_task(
                Message {
                    task_id: "range".to_owned(),
                    path: path.to_string_lossy().into_owned(),
                    offset,
                    length: requested,
                    ..Default::default()
                },
                tx,
                None,
            )
            .await;
            let Some(OutboundEvent::Message(start)) = rx.recv().await else {
                panic!("missing start")
            };
            assert_eq!(start.ty, Some(MessageType::FileDownloadStart));
            assert_eq!(
                (start.size, start.offset, start.length),
                (40_000, offset, expected)
            );
            let mut next = offset;
            let mut received = Vec::new();
            let mut ended = false;
            while let Some(event) = rx.recv().await {
                match event {
                    OutboundEvent::BinaryOffset {
                        typ,
                        offset: at,
                        payload,
                        ..
                    } => {
                        assert_eq!(at, next);
                        assert!(!ended);
                        if typ == BIN_FILE_DOWNLOAD_CHUNK {
                            next += payload.len() as i64;
                            received.extend(payload);
                        } else {
                            assert_eq!(typ, BIN_FILE_TRANSFER_END);
                            assert!(payload.is_empty());
                            ended = true;
                        }
                    }
                    OutboundEvent::Message(end) => {
                        assert!(ended);
                        assert_eq!(end.ty, Some(MessageType::FileTransferEnd));
                        assert!(end.success);
                        assert_eq!(end.offset, offset + expected);
                    }
                    _ => panic!("unexpected transfer event"),
                }
            }
            assert!(ended);
            assert_eq!(
                received,
                data[offset as usize..(offset + expected) as usize]
            );
        }
    }

    #[tokio::test]
    async fn invalid_download_ranges_never_emit_data() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("ranges");
        fs::write(&path, b"abc").await.unwrap();
        for (offset, length) in [(-1, 1), (4, 1), (0, -1)] {
            let (tx, mut rx) = mpsc::channel(4);
            download_task(
                Message {
                    task_id: "range".to_owned(),
                    path: path.to_string_lossy().into_owned(),
                    offset,
                    length,
                    ..Default::default()
                },
                tx,
                None,
            )
            .await;
            let Some(OutboundEvent::Message(error)) = rx.recv().await else {
                panic!("missing error")
            };
            assert_eq!(error.ty, Some(MessageType::FileTransferError));
            assert_eq!(error.error, "invalid download range");
            assert!(rx.recv().await.is_none());
        }
    }

    #[tokio::test]
    async fn canceled_rtc_waiter_cannot_open_after_gate_is_released() {
        let manager = FileManager::new();
        let rtc = FileRtcManager::new();
        manager.attach_rtc(rtc.clone());
        let dir = tempfile::tempdir().unwrap();
        let target = dir.path().join("canceled");
        let gate = manager.upload_gate.lock().await;
        let mut pending = Box::pin(manager.open_upload("old", target.clone(), 1, "0000000000000000000000000000000000000000000000000000000000000000", false, Some(&rtc)));
        assert!(
            tokio::time::timeout(Duration::from_millis(10), &mut pending)
                .await
                .is_err()
        );
        rtc.remove("old");
        drop(gate);
        assert!(pending.await.is_err());
        assert!(!target.exists());
        assert!(!dir.path().join("canceled.rdevpart").exists());
        assert!(manager.uploads.lock().unwrap().is_empty());
    }
}
