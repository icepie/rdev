use std::borrow::Cow;
use std::collections::hash_map::DefaultHasher;
use std::hash::{Hash, Hasher};
use std::io::Cursor;
use std::path::PathBuf;
use std::sync::mpsc::{self, Receiver, Sender};
use std::thread::{self, JoinHandle};
use std::time::Duration;

use arboard::{Clipboard, ImageData};
use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use image::codecs::png::PngEncoder;
use image::{ColorType, ImageEncoder, ImageFormat};

use crate::protocol::{
    ClipboardCapabilities, ClipboardEvent, ClipboardItem, MessageOutbound, WeylusSender,
};

const MAX_CLIPBOARD_BYTES: usize = 16 * 1024 * 1024;
const FORMATS: &[&str] = &["text/plain", "text/html", "image/png", "text/uri-list"];

enum Command {
    Get(Sender<Result<ClipboardEvent, String>>),
    Set(ClipboardEvent, Sender<Result<ClipboardEvent, String>>),
    Stop,
}

pub struct ClipboardBridge {
    commands: Sender<Command>,
    thread: Option<JoinHandle<()>>,
    supported: bool,
}

impl ClipboardBridge {
    pub fn new<S>(mut sender: S) -> Self
    where
        S: WeylusSender + Send + 'static,
    {
        let (commands, receiver) = mpsc::channel();
        let (ready_sender, ready_receiver) = mpsc::channel();
        let thread = thread::spawn(move || clipboard_worker(&mut sender, receiver, ready_sender));
        let supported = ready_receiver.recv().unwrap_or(false);
        Self {
            commands,
            thread: Some(thread),
            supported,
        }
    }

    pub fn capabilities(&self) -> ClipboardCapabilities {
        ClipboardCapabilities {
            supported: self.supported,
            read: self.supported,
            write: self.supported,
            formats: FORMATS.iter().map(|format| (*format).to_string()).collect(),
            max_bytes: MAX_CLIPBOARD_BYTES,
        }
    }

    pub fn get(&self) -> Result<ClipboardEvent, String> {
        if !self.supported {
            return Err("clipboard is unavailable".to_string());
        }
        let (reply, receiver) = mpsc::channel();
        self.commands
            .send(Command::Get(reply))
            .map_err(|_| "clipboard worker stopped".to_string())?;
        receiver
            .recv()
            .map_err(|_| "clipboard worker stopped".to_string())?
    }

    pub fn set(&self, event: ClipboardEvent) -> Result<ClipboardEvent, String> {
        if !self.supported {
            return Err("clipboard is unavailable".to_string());
        }
        let (reply, receiver) = mpsc::channel();
        self.commands
            .send(Command::Set(event, reply))
            .map_err(|_| "clipboard worker stopped".to_string())?;
        receiver
            .recv()
            .map_err(|_| "clipboard worker stopped".to_string())?
    }
}

impl Drop for ClipboardBridge {
    fn drop(&mut self) {
        let _ = self.commands.send(Command::Stop);
        if let Some(thread) = self.thread.take() {
            let _ = thread.join();
        }
    }
}

fn clipboard_worker<S>(sender: &mut S, receiver: Receiver<Command>, ready: Sender<bool>)
where
    S: WeylusSender,
{
    let mut clipboard = match Clipboard::new() {
        Ok(clipboard) => {
            let _ = ready.send(true);
            clipboard
        }
        Err(_) => {
            let _ = ready.send(false);
            while !matches!(receiver.recv(), Ok(Command::Stop) | Err(_)) {}
            return;
        }
    };
    let mut last_fingerprint = None;

    loop {
        match receiver.recv_timeout(Duration::from_millis(500)) {
            Ok(Command::Get(reply)) => {
                let result = read_clipboard(&mut clipboard);
                if let Ok(event) = &result {
                    last_fingerprint = Some(fingerprint(event));
                }
                let _ = reply.send(result);
            }
            Ok(Command::Set(event, reply)) => {
                let result = write_clipboard(&mut clipboard, event);
                if let Ok(event) = &result {
                    last_fingerprint = Some(fingerprint(event));
                }
                let _ = reply.send(result);
            }
            Ok(Command::Stop) | Err(mpsc::RecvTimeoutError::Disconnected) => return,
            Err(mpsc::RecvTimeoutError::Timeout) => {
                if let Ok(event) = read_clipboard(&mut clipboard) {
                    let current = fingerprint(&event);
                    if last_fingerprint != Some(current) {
                        last_fingerprint = Some(current);
                        let _ = sender.send_message(MessageOutbound::ClipboardContent(event));
                    }
                }
            }
        }
    }
}

fn read_clipboard(clipboard: &mut Clipboard) -> Result<ClipboardEvent, String> {
    let mut items = Vec::new();
    if let Ok(text) = clipboard.get_text() {
        push_text_item(&mut items, "text/plain", text)?;
    }
    if let Ok(image) = clipboard.get_image() {
        let png = encode_png(&image)?;
        push_binary_item(&mut items, "image/png", &png)?;
    }
    if let Ok(files) = clipboard.get().file_list() {
        if !files.is_empty() {
            let uris = files
                .iter()
                .map(|path| path_to_file_uri(path))
                .collect::<Vec<_>>()
                .join("\r\n");
            push_text_item(&mut items, "text/uri-list", uris)?;
        }
    }
    if items.is_empty() {
        return Err("clipboard has no supported content".to_string());
    }
    Ok(ClipboardEvent {
        text: plain_text(&items).unwrap_or_default(),
        items,
    })
}

fn write_clipboard(
    clipboard: &mut Clipboard,
    mut event: ClipboardEvent,
) -> Result<ClipboardEvent, String> {
    normalize_event(&mut event)?;
    let plain = item(&event, "text/plain").map(decode_text).transpose()?;

    if let Some(html) = item(&event, "text/html") {
        let html = decode_text(html)?;
        clipboard
            .set_html(html, plain.clone())
            .map_err(|err| err.to_string())?;
    } else if let Some(image) = item(&event, "image/png") {
        let png = decode_binary(image)?;
        let image = image::load_from_memory_with_format(&png, ImageFormat::Png)
            .map_err(|err| format!("invalid image/png clipboard payload: {err}"))?
            .into_rgba8();
        let (width, height) = image.dimensions();
        clipboard
            .set_image(ImageData {
                width: width as usize,
                height: height as usize,
                bytes: Cow::Owned(image.into_raw()),
            })
            .map_err(|err| err.to_string())?;
    } else if let Some(uris) = item(&event, "text/uri-list") {
        let files = parse_file_list(&decode_text(uris)?)?;
        clipboard
            .set()
            .file_list(&files)
            .map_err(|err| err.to_string())?;
    } else if let Some(text) = plain {
        clipboard.set_text(text).map_err(|err| err.to_string())?;
    } else {
        return Err("clipboard payload has no supported format".to_string());
    }

    Ok(event)
}

fn normalize_event(event: &mut ClipboardEvent) -> Result<(), String> {
    if event.items.is_empty() && !event.text.is_empty() {
        event.items.push(ClipboardItem {
            mime: "text/plain".to_string(),
            data: BASE64.encode(event.text.as_bytes()),
        });
    }
    let mut total = 0usize;
    for item in &event.items {
        if !FORMATS.contains(&item.mime.as_str()) {
            return Err(format!("unsupported clipboard format: {}", item.mime));
        }
        let decoded = BASE64
            .decode(&item.data)
            .map_err(|err| format!("invalid base64 clipboard payload: {err}"))?;
        total = total
            .checked_add(decoded.len())
            .ok_or_else(|| "clipboard payload is too large".to_string())?;
        if total > MAX_CLIPBOARD_BYTES {
            return Err(format!(
                "clipboard payload exceeds {MAX_CLIPBOARD_BYTES} bytes"
            ));
        }
    }
    event.text = plain_text(&event.items).unwrap_or_default();
    Ok(())
}

fn item<'a>(event: &'a ClipboardEvent, mime: &str) -> Option<&'a ClipboardItem> {
    event.items.iter().find(|item| item.mime == mime)
}

fn plain_text(items: &[ClipboardItem]) -> Option<String> {
    items
        .iter()
        .find(|item| item.mime == "text/plain")
        .and_then(|item| decode_text(item).ok())
}

fn decode_text(item: &ClipboardItem) -> Result<String, String> {
    String::from_utf8(decode_binary(item)?)
        .map_err(|err| format!("{} clipboard payload is not UTF-8: {err}", item.mime))
}

fn decode_binary(item: &ClipboardItem) -> Result<Vec<u8>, String> {
    BASE64
        .decode(&item.data)
        .map_err(|err| format!("invalid base64 clipboard payload: {err}"))
}

fn push_text_item(items: &mut Vec<ClipboardItem>, mime: &str, text: String) -> Result<(), String> {
    push_binary_item(items, mime, text.as_bytes())
}

fn push_binary_item(
    items: &mut Vec<ClipboardItem>,
    mime: &str,
    bytes: &[u8],
) -> Result<(), String> {
    let existing = items.iter().try_fold(0usize, |sum, item| {
        BASE64
            .decode(&item.data)
            .map(|data| sum.saturating_add(data.len()))
            .map_err(|err| format!("invalid base64 clipboard payload: {err}"))
    })?;
    let total = existing
        .checked_add(bytes.len())
        .ok_or_else(|| "clipboard payload is too large".to_string())?;
    if total > MAX_CLIPBOARD_BYTES {
        return Err(format!(
            "clipboard payload exceeds {MAX_CLIPBOARD_BYTES} bytes"
        ));
    }
    items.push(ClipboardItem {
        mime: mime.to_string(),
        data: BASE64.encode(bytes),
    });
    Ok(())
}

fn encode_png(image: &ImageData<'_>) -> Result<Vec<u8>, String> {
    let width =
        u32::try_from(image.width).map_err(|_| "clipboard image is too wide".to_string())?;
    let height =
        u32::try_from(image.height).map_err(|_| "clipboard image is too tall".to_string())?;
    let mut png = Vec::new();
    PngEncoder::new(Cursor::new(&mut png))
        .write_image(&image.bytes, width, height, ColorType::Rgba8.into())
        .map_err(|err| err.to_string())?;
    Ok(png)
}

fn path_to_file_uri(path: &PathBuf) -> String {
    url::Url::from_file_path(path)
        .map(|url| url.to_string())
        .unwrap_or_else(|_| path.to_string_lossy().into_owned())
}

fn parse_file_list(value: &str) -> Result<Vec<PathBuf>, String> {
    let files = value
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty() && !line.starts_with('#'))
        .map(|line| {
            if let Ok(url) = url::Url::parse(line) {
                if url.scheme() != "file" {
                    return Err(format!(
                        "unsupported clipboard URI scheme: {}",
                        url.scheme()
                    ));
                }
                return url
                    .to_file_path()
                    .map_err(|_| format!("invalid file URI: {line}"));
            }
            Ok(PathBuf::from(line))
        })
        .collect::<Result<Vec<_>, _>>()?;
    if files.is_empty() {
        return Err("clipboard file list is empty".to_string());
    }
    Ok(files)
}

fn fingerprint(event: &ClipboardEvent) -> u64 {
    let mut hasher = DefaultHasher::new();
    event.hash(&mut hasher);
    hasher.finish()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalizes_legacy_text_payload() {
        let mut event = ClipboardEvent {
            items: vec![],
            text: "hello 世界".to_string(),
        };
        normalize_event(&mut event).unwrap();
        assert_eq!(event.items[0].mime, "text/plain");
        assert_eq!(decode_text(&event.items[0]).unwrap(), "hello 世界");
        assert_eq!(event.text, "hello 世界");
    }

    #[test]
    fn rejects_unsupported_and_oversized_payloads() {
        let mut unsupported = ClipboardEvent {
            items: vec![ClipboardItem {
                mime: "application/octet-stream".to_string(),
                data: BASE64.encode(b"x"),
            }],
            text: String::new(),
        };
        assert!(normalize_event(&mut unsupported)
            .unwrap_err()
            .contains("unsupported"));

        let mut oversized = ClipboardEvent {
            items: vec![ClipboardItem {
                mime: "image/png".to_string(),
                data: BASE64.encode(vec![0; MAX_CLIPBOARD_BYTES + 1]),
            }],
            text: String::new(),
        };
        assert!(normalize_event(&mut oversized)
            .unwrap_err()
            .contains("exceeds"));
    }

    #[test]
    fn parses_file_uri_lists() {
        let files = parse_file_list("# comment\r\nfile:///tmp/a.txt\r\n/tmp/b.txt\r\n").unwrap();
        assert_eq!(
            files,
            vec![PathBuf::from("/tmp/a.txt"), PathBuf::from("/tmp/b.txt")]
        );
    }
}
