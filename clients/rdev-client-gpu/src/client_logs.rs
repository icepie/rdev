use std::collections::VecDeque;
use std::sync::{Arc, Mutex, OnceLock};
use std::time::{SystemTime, UNIX_EPOCH};

use tokio::sync::mpsc;
use tracing::{Event, Subscriber};
use tracing_subscriber::layer::{Context, Layer};

use crate::protocol::{LogEntry, Message, MessageType};
use crate::session::OutboundEvent;

static COLLECTOR: OnceLock<Arc<ClientLogCollector>> = OnceLock::new();

pub fn global() -> Arc<ClientLogCollector> {
    COLLECTOR
        .get_or_init(|| Arc::new(ClientLogCollector::new()))
        .clone()
}

pub struct ClientLogLayer;

impl<S> Layer<S> for ClientLogLayer
where
    S: Subscriber,
{
    fn on_event(&self, event: &Event<'_>, _ctx: Context<'_, S>) {
        let meta = event.metadata();
        let mut visitor = LogVisitor::default();
        event.record(&mut visitor);
        let msg = sanitize(&visitor.message.unwrap_or_else(|| meta.name().to_string()));
        global().record(LogEntry {
            ts: now_ts(),
            level: meta.level().as_str().to_ascii_lowercase(),
            target: meta.target().to_string(),
            module: meta.module_path().unwrap_or(meta.target()).to_string(),
            message: msg,
        });
    }
}

#[derive(Default)]
struct LogVisitor {
    message: Option<String>,
}
impl tracing::field::Visit for LogVisitor {
    fn record_debug(&mut self, field: &tracing::field::Field, value: &dyn std::fmt::Debug) {
        if field.name() == "message" {
            self.message = Some(format!("{value:?}"));
        }
    }
    fn record_str(&mut self, field: &tracing::field::Field, value: &str) {
        if field.name() == "message" {
            self.message = Some(value.to_string());
        }
    }
}

pub struct ClientLogCollector {
    inner: Mutex<Inner>,
}
struct Inner {
    ring: VecDeque<LogEntry>,
    enabled: bool,
    level: String,
    max_line_bytes: usize,
    tx: Option<mpsc::Sender<OutboundEvent>>,
}

impl ClientLogCollector {
    fn new() -> Self {
        Self {
            inner: Mutex::new(Inner {
                ring: VecDeque::with_capacity(1000),
                enabled: false,
                level: "info".into(),
                max_line_bytes: 8192,
                tx: None,
            }),
        }
    }
    pub fn attach_sender(&self, tx: mpsc::Sender<OutboundEvent>) {
        self.inner.lock().unwrap().tx = Some(tx);
    }
    pub fn detach_sender(&self) {
        self.inner.lock().unwrap().tx = None;
    }
    pub fn configure(&self, msg: &Message) {
        let mut inner = self.inner.lock().unwrap();
        inner.enabled = msg.log_enabled;
        inner.level = normalize_level(&msg.log_level);
        if msg.max_line_bytes > 0 {
            inner.max_line_bytes = msg.max_line_bytes as usize;
        }
        let snapshot: Vec<_> = inner.ring.iter().cloned().collect();
        let tx = inner.tx.clone();
        let enabled = inner.enabled;
        let level = inner.level.clone();
        drop(inner);
        if enabled {
            send_batch(
                tx,
                snapshot
                    .into_iter()
                    .filter(|e| level_allowed(&e.level, &level))
                    .collect(),
            );
        }
    }
    fn record(&self, mut entry: LogEntry) {
        let mut inner = self.inner.lock().unwrap();
        if entry.message.len() > inner.max_line_bytes {
            entry.message.truncate(inner.max_line_bytes);
        }
        inner.ring.push_back(entry.clone());
        while inner.ring.len() > 1000 {
            inner.ring.pop_front();
        }
        let tx = inner.tx.clone();
        let enabled = inner.enabled;
        let level = inner.level.clone();
        drop(inner);
        if enabled && level_allowed(&entry.level, &level) {
            send_batch(tx, vec![entry]);
        }
    }
}

fn send_batch(tx: Option<mpsc::Sender<OutboundEvent>>, logs: Vec<LogEntry>) {
    if logs.is_empty() {
        return;
    }
    if let Some(tx) = tx {
        let _ = tx.try_send(OutboundEvent::Message(Message {
            ty: Some(MessageType::LogBatch),
            logs,
            ..Default::default()
        }));
    }
}

fn normalize_level(level: &str) -> String {
    match level.trim().to_ascii_lowercase().as_str() {
        "trace" | "debug" | "info" | "warn" | "error" => level.trim().to_ascii_lowercase(),
        _ => "info".into(),
    }
}
fn rank(level: &str) -> u8 {
    match normalize_level(level).as_str() {
        "trace" => 0,
        "debug" => 1,
        "info" => 2,
        "warn" => 3,
        "error" => 4,
        _ => 2,
    }
}
fn level_allowed(level: &str, min: &str) -> bool {
    rank(level) >= rank(min)
}
fn now_ts() -> String {
    let d = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    format!("{}.{:09}Z", d.as_secs(), d.subsec_nanos())
}
fn sanitize(value: &str) -> String {
    let mut out = value.replace('\0', "");
    for key in [
        "password",
        "token",
        "authorization",
        "cookie",
        "secret",
        "key",
    ] {
        out = redact_key(&out, key);
    }
    out
}
fn redact_key(input: &str, key: &str) -> String {
    input
        .split_whitespace()
        .map(|part| {
            if part.to_ascii_lowercase().contains(key) {
                format!("{key}=[redacted]")
            } else {
                part.to_string()
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}


#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn level_filtering_matches_severity_order() {
        assert!(level_allowed("error", "info"));
        assert!(level_allowed("warn", "debug"));
        assert!(!level_allowed("debug", "info"));
        assert!(!level_allowed("trace", "warn"));
    }

    #[test]
    fn sanitize_redacts_secret_like_tokens() {
        let got = sanitize("connect token=abc password=hunter2 authorization=Bearer cookie=x secret=y key=z");
        assert!(got.contains("[redacted]"));
        assert!(!got.contains("abc"));
        assert!(!got.contains("hunter2"));
        assert!(!got.contains("Bearer"));
    }

    #[test]
    fn normalize_unknown_level_to_info() {
        assert_eq!(normalize_level("DEBUG"), "debug");
        assert_eq!(normalize_level("verbose"), "info");
    }
}
