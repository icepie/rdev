use anyhow::{anyhow, Result};
use serde::{Deserialize, Serialize};

pub const BIN_DATA: u8 = 0x01;
pub const BIN_STDERR: u8 = 0x02;
pub const BIN_TCP_DATA: u8 = 0x03;
pub const BIN_FILE_PUT: u8 = 0x04;
pub const BIN_FILE_START: u8 = 0x05;
pub const BIN_FILE_CHUNK: u8 = 0x06;
pub const BIN_FILE_END: u8 = 0x07;
pub const BIN_FILE_ACK: u8 = 0x08;
pub const BIN_FILE_UPLOAD_CHUNK: u8 = 0x20;
pub const BIN_FILE_UPLOAD_ACK: u8 = 0x21;
pub const BIN_FILE_DOWNLOAD_CHUNK: u8 = 0x22;
pub const BIN_FILE_TRANSFER_END: u8 = 0x23;
pub const BIN_FILE_TRANSFER_CANCEL: u8 = 0x24;
pub const BIN_DESKTOP_VIDEO_CONFIG: u8 = 0x31;
pub const BIN_DESKTOP_VIDEO_CHUNK: u8 = 0x32;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum MessageType {
    #[serde(rename = "register")]
    Register,
    #[serde(rename = "new_session")]
    NewSession,
    #[serde(rename = "stdin_close")]
    StdinClose,
    #[serde(rename = "close")]
    Close,
    #[serde(rename = "resize")]
    Resize,
    #[serde(rename = "exit_code")]
    ExitCode,
    #[serde(rename = "tcp_connect")]
    TcpConnect,
    #[serde(rename = "tcp_open")]
    TcpOpen,
    #[serde(rename = "tcp_fail")]
    TcpFail,
    #[serde(rename = "tcp_close")]
    TcpClose,
    #[serde(rename = "tcp_listen")]
    TcpListen,
    #[serde(rename = "tcp_listen_ok")]
    TcpListenOk,
    #[serde(rename = "tcp_accept")]
    TcpAccept,
    #[serde(rename = "file_list")]
    FileList,
    #[serde(rename = "file_list_result")]
    FileListResult,
    #[serde(rename = "file_upload_start")]
    FileUploadStart,
    #[serde(rename = "file_upload_ready")]
    FileUploadReady,
    #[serde(rename = "file_upload_end")]
    FileUploadEnd,
    #[serde(rename = "file_download_start")]
    FileDownloadStart,
    #[serde(rename = "file_transfer_end")]
    FileTransferEnd,
    #[serde(rename = "file_transfer_error")]
    FileTransferError,
    #[serde(rename = "file_transfer_cancel")]
    FileTransferCancel,
    #[serde(rename = "file_result")]
    FileResult,
    #[serde(rename = "desktop_start")]
    DesktopStart,
    #[serde(rename = "desktop_ready")]
    DesktopReady,
    #[serde(rename = "desktop_close")]
    DesktopClose,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct Message {
    #[serde(rename = "type")]
    pub ty: Option<MessageType>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub client_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub session_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub instance_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub subsystem: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub command: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub pty: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub env: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub term: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub rows: u16,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub cols: u16,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub password: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub ssh_port: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub http_host: String,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub exit_code: i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub forward_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub listen_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub host: String,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub port: i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub source_addr: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub file_path: String,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub file_mode: i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub request_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub task_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub parent_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub home_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    #[serde(default, skip_serializing_if = "is_zero_i64")]
    pub size: i64,
    #[serde(default, skip_serializing_if = "is_zero_i64")]
    pub offset: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub mod_time: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub success: bool,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub truncated: bool,
    #[serde(default, rename = "entries", skip_serializing_if = "Vec::is_empty")]
    pub file_entries: Vec<FileEntry>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub desktop: Option<DesktopCapabilities>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct DesktopCapabilities {
    pub platform: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub display_server: String,
    pub supported: bool,
    pub view_only: bool,
    pub input: bool,
    pub clipboard: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub backends: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub input_backends: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<DesktopSource>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub video_codecs: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub encoder_backends: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub reason: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct DesktopSource {
    pub id: String,
    pub label: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub kind: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub backend: String,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub width: i32,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub height: i32,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub primary: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct FileEntry {
    pub name: String,
    pub path: String,
    pub is_dir: bool,
    pub size: i64,
    pub mod_time: String,
}

pub fn encode_message(message: &Message) -> Result<String> {
    Ok(serde_json::to_string(message)?)
}

pub fn decode_message(text: &str) -> Result<Message> {
    Ok(serde_json::from_str(text)?)
}

pub fn encode_bin_frame(typ: u8, id: &str, payload: &[u8]) -> Result<Vec<u8>> {
    let id_bytes = id.as_bytes();
    if id_bytes.len() > u8::MAX as usize {
        return Err(anyhow!("binary frame id too long: {}", id_bytes.len()));
    }
    let mut out = Vec::with_capacity(2 + id_bytes.len() + payload.len());
    out.push(typ);
    out.push(id_bytes.len() as u8);
    out.extend_from_slice(id_bytes);
    out.extend_from_slice(payload);
    Ok(out)
}

pub fn decode_bin_frame(raw: &[u8]) -> Result<(u8, String, Vec<u8>)> {
    if raw.len() < 2 {
        return Err(anyhow!("binary frame too short"));
    }
    let typ = raw[0];
    let id_len = raw[1] as usize;
    if raw.len() < 2 + id_len {
        return Err(anyhow!("binary frame id truncated"));
    }
    let id = String::from_utf8(raw[2..2 + id_len].to_vec())?;
    Ok((typ, id, raw[2 + id_len..].to_vec()))
}

pub fn encode_bin_frame_offset(typ: u8, id: &str, offset: i64, payload: &[u8]) -> Result<Vec<u8>> {
    let mut body = Vec::with_capacity(8 + payload.len());
    body.extend_from_slice(&(offset as u64).to_be_bytes());
    body.extend_from_slice(payload);
    encode_bin_frame(typ, id, &body)
}

pub fn decode_bin_frame_offset(raw: &[u8]) -> Result<(u8, String, i64, Vec<u8>)> {
    let (typ, id, payload) = decode_bin_frame(raw)?;
    if payload.len() < 8 {
        return Err(anyhow!("binary offset frame too short"));
    }
    let mut bytes = [0u8; 8];
    bytes.copy_from_slice(&payload[..8]);
    Ok((
        typ,
        id,
        u64::from_be_bytes(bytes) as i64,
        payload[8..].to_vec(),
    ))
}

pub fn encode_bin_file_put(id: &str, path: &str, mode: i32, file_data: &[u8]) -> Result<Vec<u8>> {
    let mut payload = Vec::with_capacity(2 + path.len() + 4 + file_data.len());
    append_bin_file_header(&mut payload, path, mode)?;
    payload.extend_from_slice(file_data);
    encode_bin_frame(BIN_FILE_PUT, id, &payload)
}

pub fn encode_bin_file_start(id: &str, path: &str, mode: i32) -> Result<Vec<u8>> {
    let mut payload = Vec::with_capacity(2 + path.len() + 4);
    append_bin_file_header(&mut payload, path, mode)?;
    encode_bin_frame(BIN_FILE_START, id, &payload)
}

pub fn decode_bin_file_put(payload: &[u8]) -> Result<(String, i32, Vec<u8>)> {
    if payload.len() < 2 {
        return Err(anyhow!("binary file payload too short"));
    }
    let path_len = u16::from_be_bytes([payload[0], payload[1]]) as usize;
    if payload.len() < 2 + path_len + 4 {
        return Err(anyhow!("binary file header truncated"));
    }
    let path = String::from_utf8(payload[2..2 + path_len].to_vec())?;
    let mode_pos = 2 + path_len;
    let mode = u32::from_be_bytes([
        payload[mode_pos],
        payload[mode_pos + 1],
        payload[mode_pos + 2],
        payload[mode_pos + 3],
    ]) as i32;
    Ok((path, mode, payload[mode_pos + 4..].to_vec()))
}

fn append_bin_file_header(out: &mut Vec<u8>, path: &str, mode: i32) -> Result<()> {
    let path_bytes = path.as_bytes();
    if path_bytes.len() > u16::MAX as usize {
        return Err(anyhow!("binary file path too long: {}", path_bytes.len()));
    }
    out.extend_from_slice(&(path_bytes.len() as u16).to_be_bytes());
    out.extend_from_slice(path_bytes);
    out.extend_from_slice(&(mode as u32).to_be_bytes());
    Ok(())
}

fn is_zero(v: &u16) -> bool {
    *v == 0
}
fn is_zero_i32(v: &i32) -> bool {
    *v == 0
}
fn is_zero_i64(v: &i64) -> bool {
    *v == 0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn binary_frame_roundtrip() {
        let frame = encode_bin_frame(BIN_DATA, "session", b"hello").unwrap();
        let (typ, id, payload) = decode_bin_frame(&frame).unwrap();
        assert_eq!(typ, BIN_DATA);
        assert_eq!(id, "session");
        assert_eq!(payload, b"hello");
    }

    #[test]
    fn binary_offset_frame_roundtrip() {
        let frame = encode_bin_frame_offset(BIN_FILE_DOWNLOAD_CHUNK, "task", 42, b"chunk").unwrap();
        let (typ, id, offset, payload) = decode_bin_frame_offset(&frame).unwrap();
        assert_eq!(typ, BIN_FILE_DOWNLOAD_CHUNK);
        assert_eq!(id, "task");
        assert_eq!(offset, 42);
        assert_eq!(payload, b"chunk");
    }

    #[test]
    fn binary_offset_frame_rejects_short_payload() {
        let frame = encode_bin_frame(BIN_FILE_DOWNLOAD_CHUNK, "task", b"short").unwrap();
        assert!(decode_bin_frame_offset(&frame).is_err());
    }

    #[test]
    fn file_list_result_uses_rdev_entries_field() {
        let msg = Message {
            ty: Some(MessageType::FileListResult),
            request_id: "req".into(),
            file_entries: vec![FileEntry {
                name: "file.txt".into(),
                path: "/tmp/file.txt".into(),
                is_dir: false,
                size: 5,
                mod_time: "2026-06-17T00:00:00+00:00".into(),
            }],
            ..Default::default()
        };
        let text = encode_message(&msg).unwrap();
        assert!(text.contains("\"entries\":"));
        assert!(!text.contains("fileEntries"));
    }

    #[test]
    fn register_message_uses_rdev_field_names() {
        let msg = Message {
            ty: Some(MessageType::Register),
            client_id: "dev".into(),
            instance_id: "inst".into(),
            ..Default::default()
        };
        let text = encode_message(&msg).unwrap();
        assert!(text.contains("\"clientId\":\"dev\""));
        assert!(text.contains("\"instanceId\":\"inst\""));
    }
}
