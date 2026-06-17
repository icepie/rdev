use anyhow::{anyhow, Result};
use std::{
    collections::HashMap,
    path::PathBuf,
    sync::{Arc, Mutex},
};
use tokio::{
    fs::{self, File},
    io::AsyncWriteExt,
    sync::mpsc,
};

use crate::{
    protocol::{
        decode_bin_file_put, Message, MessageType, BIN_FILE_ACK, BIN_FILE_CHUNK, BIN_FILE_END,
        BIN_FILE_PUT, BIN_FILE_START,
    },
    session::OutboundEvent,
};

#[derive(Debug)]
struct StreamState {
    path: String,
    file: File,
}

#[derive(Clone, Default)]
pub struct FilePutManager {
    streams: Arc<Mutex<HashMap<String, StreamState>>>,
}

impl FilePutManager {
    pub fn new() -> Self {
        Self::default()
    }

    pub async fn handle_frame(
        &self,
        typ: u8,
        id: String,
        payload: Vec<u8>,
        outbound: mpsc::Sender<OutboundEvent>,
    ) {
        match typ {
            BIN_FILE_PUT => self.handle_put(&id, &payload, outbound).await,
            BIN_FILE_START => self.handle_start(&id, &payload, outbound).await,
            BIN_FILE_CHUNK => self.handle_chunk(&id, payload, outbound).await,
            BIN_FILE_END => self.handle_end(&id, outbound).await,
            _ => {}
        }
    }

    pub async fn close_all(&self) {
        let streams: Vec<_> = self
            .streams
            .lock()
            .unwrap()
            .drain()
            .map(|(_, state)| state)
            .collect();
        for mut state in streams {
            let _ = state.file.flush().await;
        }
    }

    async fn handle_put(&self, id: &str, payload: &[u8], outbound: mpsc::Sender<OutboundEvent>) {
        let result = async {
            let (path, mode, data) = decode_bin_file_put(payload)?;
            write_file_bytes(&path, mode, &data).await?;
            Ok::<_, anyhow::Error>(path)
        }
        .await;
        send_result(&outbound, id, result).await;
    }

    async fn handle_start(&self, id: &str, payload: &[u8], outbound: mpsc::Sender<OutboundEvent>) {
        let result = async {
            let (path, mode, _) = decode_bin_file_put(payload)?;
            let file = open_file_for_write(&path, mode).await?;
            let old = self.streams.lock().unwrap().remove(id);
            if let Some(mut old) = old {
                let _ = old.file.flush().await;
            }
            self.streams.lock().unwrap().insert(
                id.to_string(),
                StreamState {
                    path: path.clone(),
                    file,
                },
            );
            Ok::<_, anyhow::Error>(path)
        }
        .await;
        match result {
            Ok(_) => {}
            Err(err) => send_result(&outbound, id, Err(err)).await,
        }
    }

    async fn handle_chunk(&self, id: &str, data: Vec<u8>, outbound: mpsc::Sender<OutboundEvent>) {
        let state = self.streams.lock().unwrap().remove(id);
        let Some(mut state) = state else {
            send_result(&outbound, id, Err(anyhow!("file stream not found"))).await;
            return;
        };
        let result = state.file.write_all(&data).await;
        match result {
            Ok(()) => {
                self.streams.lock().unwrap().insert(id.to_string(), state);
                let _ = outbound
                    .send(OutboundEvent::Binary {
                        typ: BIN_FILE_ACK,
                        id: id.to_string(),
                        payload: Vec::new(),
                    })
                    .await;
            }
            Err(err) => send_result(&outbound, id, Err(anyhow!("{}: {err}", state.path))).await,
        }
    }

    async fn handle_end(&self, id: &str, outbound: mpsc::Sender<OutboundEvent>) {
        let state = self.streams.lock().unwrap().remove(id);
        let Some(mut state) = state else {
            send_result(&outbound, id, Err(anyhow!("file stream not found"))).await;
            return;
        };
        let path = state.path.clone();
        let result = async {
            state.file.flush().await?;
            state.file.sync_all().await?;
            Ok::<_, anyhow::Error>(path)
        }
        .await;
        send_result(&outbound, id, result).await;
    }
}

async fn write_file_bytes(path: &str, mode: i32, data: &[u8]) -> Result<()> {
    let mut file = open_file_for_write(path, mode).await?;
    file.write_all(data).await?;
    file.flush().await?;
    file.sync_all().await?;
    Ok(())
}

async fn open_file_for_write(path: &str, mode: i32) -> Result<File> {
    let path = PathBuf::from(path);
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).await?;
    }
    let file = File::create(&path).await?;
    set_file_mode(&path, mode).await?;
    Ok(file)
}

#[cfg(unix)]
async fn set_file_mode(path: &PathBuf, mode: i32) -> Result<()> {
    if mode <= 0 {
        return Ok(());
    }
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, std::fs::Permissions::from_mode(mode as u32)).await?;
    Ok(())
}

#[cfg(not(unix))]
async fn set_file_mode(_path: &PathBuf, _mode: i32) -> Result<()> {
    Ok(())
}

async fn send_result(outbound: &mpsc::Sender<OutboundEvent>, id: &str, result: Result<String>) {
    let (file_path, success, error) = match result {
        Ok(path) => (path, true, String::new()),
        Err(err) => (String::new(), false, err.to_string()),
    };
    let _ = outbound
        .send(OutboundEvent::Message(Message {
            ty: Some(MessageType::FileResult),
            session_id: id.to_string(),
            file_path,
            success,
            error,
            ..Default::default()
        }))
        .await;
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::{decode_bin_frame, encode_bin_file_put};

    #[test]
    fn decodes_file_put_payload() {
        let frame = encode_bin_file_put("id", "/tmp/a.txt", 0o644, b"hello").unwrap();
        let (typ, id, payload) = decode_bin_frame(&frame).unwrap();
        let (path, mode, data) = decode_bin_file_put(&payload).unwrap();
        assert_eq!(typ, BIN_FILE_PUT);
        assert_eq!(id, "id");
        assert_eq!(path, "/tmp/a.txt");
        assert_eq!(mode, 0o644);
        assert_eq!(data, b"hello");
    }
}
