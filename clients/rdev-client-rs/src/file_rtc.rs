use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex, Weak,
    },
    time::Duration,
};

use anyhow::{anyhow, Result};
use bytes::Bytes;
use serde_json::Value;
use tokio::{
    sync::{mpsc, oneshot, watch},
    time::Instant,
};
use webrtc::{
    api::{media_engine::MediaEngine, APIBuilder},
    data_channel::{data_channel_state::RTCDataChannelState, RTCDataChannel},
    ice_transport::{ice_candidate::RTCIceCandidateInit, ice_server::RTCIceServer},
    peer_connection::{
        configuration::RTCConfiguration, peer_connection_state::RTCPeerConnectionState,
        sdp::session_description::RTCSessionDescription, RTCPeerConnection,
    },
};

use crate::{
    protocol::{self, Message, MessageType, BIN_FILE_TRANSFER_END, BIN_FILE_UPLOAD_ACK},
    session::OutboundEvent,
};

pub const FILE_RTC_MAX_PAYLOAD: usize = 16 * 1024;
const FRAME_HEADER: usize = 2 + 8;
const FILE_RTC_MAX_FRAME: usize = FILE_RTC_MAX_PAYLOAD + FRAME_HEADER;
const MAX_SIGNAL_BYTES: usize = 512 * 1024;
const MAX_CANDIDATES: usize = 128;
const DATA_CHANNEL_HIGH_WATER: usize = 512 * 1024;
const NEGOTIATION_TIMEOUT: Duration = Duration::from_secs(5);
const NO_PROGRESS_TIMEOUT: Duration = Duration::from_secs(15);
const TERMINAL_TIMEOUT: Duration = Duration::from_secs(30);
const DRAIN_TIMEOUT: Duration = Duration::from_millis(1500);

// Negotiated committed-range ACKs leave legacy cancellation (0x24) unchanged.
const BIN_FILE_DOWNLOAD_ACK: u8 = 0x25;

pub(crate) fn range_ack_enabled(msg: &Message) -> bool {
    msg.ty == Some(MessageType::FileDownloadStart)
        && msg.length > 0
        && msg.rtc_data.get("rangeAck").and_then(Value::as_bool) == Some(true)
}

#[derive(Debug)]
pub enum FileRtcEvent {
    Binary(Vec<u8>),
}

pub struct FileRtcTask {
    pub open: oneshot::Receiver<()>,
    pub events: mpsc::Receiver<FileRtcEvent>,
    pub closed: watch::Receiver<bool>,
}

#[derive(Clone, Default)]
pub struct FileRtcManager {
    state: Arc<Mutex<State>>,
    closed: Arc<AtomicBool>,
}
#[derive(Default)]
struct State {
    sessions: HashMap<String, Arc<Session>>,
}
struct Session {
    id: String,
    manager: Weak<Mutex<State>>,
    peer: Mutex<Option<Arc<RTCPeerConnection>>>,
    channel: Mutex<Option<Arc<RTCDataChannel>>>,
    candidates: Mutex<RemoteCandidates>,
    events: Mutex<Option<mpsc::Sender<FileRtcEvent>>>,
    opened: Mutex<Option<oneshot::Sender<()>>>,
    closed: AtomicBool,
    // The watch value is sticky cancellation; version changes also wake buffer waiters.
    wake: watch::Sender<bool>,
    ack: watch::Sender<Option<i64>>,
    range_ack: bool,
    send_gate: tokio::sync::Mutex<()>,
    lifecycle: Mutex<Lifecycle>,
}
struct Lifecycle {
    opened: bool,
    progress: Instant,
    terminal: Option<Instant>,
    range_wait: Option<Instant>,
    range_end: Option<i64>,
}
impl Lifecycle {
    fn expired(&self, now: Instant) -> bool {
        let (since, timeout) = match (self.terminal, self.range_wait) {
            (Some(at), _) | (_, Some(at)) => (at, TERMINAL_TIMEOUT),
            _ if self.opened => (self.progress, NO_PROGRESS_TIMEOUT),
            _ => (self.progress, NEGOTIATION_TIMEOUT),
        };
        now.duration_since(since) >= timeout
    }
}
#[derive(Default)]
struct RemoteCandidates {
    ready: bool,
    received: usize,
    pending: Vec<RTCIceCandidateInit>,
}
impl RemoteCandidates {
    fn admit(&mut self) -> bool {
        if self.received >= MAX_CANDIDATES {
            return false;
        }
        self.received += 1;
        true
    }
}

impl FileRtcManager {
    pub fn new() -> Self {
        Self::default()
    }
    pub fn close_all(&self) {
        self.closed.store(true, Ordering::Release);
        let sessions = std::mem::take(&mut self.state.lock().unwrap().sessions);
        for (_, session) in sessions {
            session.close();
        }
    }
    /// Starts only from a server-authorized file start. Frames are task-bound.
    pub fn start(&self, msg: Message, out: mpsc::Sender<OutboundEvent>) -> Result<FileRtcTask> {
        if msg.task_id.is_empty()
            || msg.rtc_type != "offer"
            || msg.rtc_data.is_null()
            || serde_json::to_vec(&msg.rtc_data)?.len() > MAX_SIGNAL_BYTES
        {
            return Err(anyhow!("invalid file RTC offer"));
        }
        let (open_tx, open_rx) = oneshot::channel();
        let range_ack = range_ack_enabled(&msg);
        let (event_tx, event_rx) = mpsc::channel(if range_ack { 1 } else { 32 });
        let (wake, closed) = watch::channel(false);
        let (ack, mut acknowledgments) = watch::channel(None);
        let session = Arc::new(Session {
            id: msg.task_id.clone(),
            manager: Arc::downgrade(&self.state),
            peer: Mutex::new(None),
            channel: Mutex::new(None),
            candidates: Mutex::new(RemoteCandidates::default()),
            events: Mutex::new(Some(event_tx)),
            opened: Mutex::new(Some(open_tx)),
            closed: AtomicBool::new(false),
            wake,
            ack,
            range_ack,
            send_gate: tokio::sync::Mutex::new(()),
            lifecycle: Mutex::new(Lifecycle {
                opened: false,
                progress: Instant::now(),
                terminal: None,
                range_wait: None,
                range_end: None,
            }),
        });
        {
            let mut state = self.state.lock().unwrap();
            if self.closed.load(Ordering::Acquire)
                || state.sessions.contains_key(&msg.task_id)
                || state.sessions.len() >= 32
            {
                return Err(anyhow!("file RTC task already active"));
            }
            state.sessions.insert(msg.task_id.clone(), session.clone());
        }
        let watchdog = session.clone();
        tokio::spawn(async move { watchdog.watchdog().await });
        let sending = session.clone();
        tokio::spawn(async move {
            let mut canceled = sending.wake.subscribe();
            loop {
                tokio::select! {
                    biased;
                    _ = async { let _ = canceled.wait_for(|closed| *closed).await; } => break,
                    result = acknowledgments.changed() => {
                        if result.is_err() { break; }
                        let offset = *acknowledgments.borrow_and_update();
                        if let Some(offset) = offset {
                            let result = match protocol::encode_bin_frame_offset(BIN_FILE_UPLOAD_ACK, "", offset, &[]) {
                                Ok(raw) => sending.send(raw).await,
                                Err(err) => Err(err),
                            };
                            if result.is_err() {
                                sending.close();
                                break;
                            }
                        }
                    }
                }
            }
        });
        tokio::spawn(async move {
            let mut canceled = session.wake.subscribe();
            tokio::select! {
                biased;
                _ = canceled.wait_for(|closed| *closed) => {},
                result = session.accept_offer(msg, out) => {
                    if let Err(err) = result {
                        tracing::warn!("file RTC negotiation failed: {err:#}");
                        session.close();
                    }
                }
            }
        });
        Ok(FileRtcTask {
            open: open_rx,
            events: event_rx,
            closed,
        })
    }
    pub fn signal(&self, msg: Message) {
        let Ok(session) = self.session(&msg.task_id) else {
            return;
        };
        match msg.rtc_type.as_str() {
            "candidate" => {
                if msg.rtc_data.is_null()
                    || serde_json::to_vec(&msg.rtc_data).map_or(true, |v| v.len() > 8 * 1024)
                {
                    session.close();
                    return;
                }
                let Ok(candidate) = serde_json::from_value::<RTCIceCandidateInit>(msg.rtc_data)
                else {
                    session.close();
                    return;
                };
                {
                    let mut candidates = session.candidates.lock().unwrap();
                    if session.closed.load(Ordering::Acquire) {
                        return;
                    }
                    if !candidates.admit() {
                        drop(candidates);
                        session.close();
                        return;
                    }
                    if !candidates.ready {
                        candidates.pending.push(candidate);
                        return;
                    }
                }
                let peer = session.peer.lock().unwrap().clone();
                if let Some(peer) = peer {
                    tokio::spawn(async move {
                        let mut canceled = session.wake.subscribe();
                        tokio::select! {
                            biased;
                            _ = canceled.wait_for(|closed| *closed) => {},
                            result = peer.add_ice_candidate(candidate) => {
                                if result.is_err() { session.close(); }
                            }
                        }
                    });
                }
            }
            "close" => session.close(),
            _ => {}
        }
    }
    pub async fn send_offset(
        &self,
        task_id: &str,
        typ: u8,
        offset: i64,
        payload: &[u8],
    ) -> Result<()> {
        if payload.len() > FILE_RTC_MAX_PAYLOAD {
            return Err(anyhow!("file RTC payload exceeds 16KiB"));
        }
        self.send_raw(
            task_id,
            protocol::encode_bin_frame_offset(typ, "", offset, payload)?,
        )
        .await
    }
    pub async fn send_end(&self, task_id: &str, offset: i64) -> Result<()> {
        self.send_offset(task_id, BIN_FILE_TRANSFER_END, offset, &[])
            .await
    }
    pub(crate) fn wait_for_range_ack(&self, task_id: &str, offset: i64) -> Result<()> {
        let session = self.session(task_id)?;
        let mut lifecycle = session.lifecycle.lock().unwrap();
        if !session.range_ack || lifecycle.range_wait.is_some() {
            return Err(anyhow!("unexpected download range boundary"));
        }
        lifecycle.range_end = Some(offset);
        lifecycle.range_wait = Some(Instant::now());
        Ok(())
    }
    pub(crate) fn resume_range(&self, task_id: &str) -> Result<()> {
        let session = self.session(task_id)?;
        let mut lifecycle = session.lifecycle.lock().unwrap();
        if lifecycle.range_wait.is_none() || lifecycle.range_end.is_some() {
            return Err(anyhow!("download range has not been acknowledged"));
        }
        lifecycle.range_wait = None;
        lifecycle.progress = Instant::now();
        Ok(())
    }
    pub fn completed(&self, task_id: &str) {
        if let Ok(session) = self.session(task_id) {
            session
                .lifecycle
                .lock()
                .unwrap()
                .terminal
                .get_or_insert_with(Instant::now);
        }
    }
    pub fn cancellation(&self, task_id: &str) -> Result<watch::Receiver<bool>> {
        Ok(self.session(task_id)?.wake.subscribe())
    }
    pub fn remove_if_current(&self, task_id: &str, closed: &watch::Receiver<bool>) {
        let session = self
            .state
            .lock()
            .unwrap()
            .sessions
            .get(task_id)
            .filter(|session| session.wake.subscribe().same_channel(closed))
            .cloned();
        if let Some(session) = session {
            session.close();
        }
    }
    /// Cumulative ACKs may coalesce, but payload frames never do. Keeping the
    /// writer independent lets a full send buffer coexist with pipelined input.
    pub fn acknowledge(&self, task_id: &str, offset: i64) -> Result<()> {
        let session = self.session(task_id)?;
        if session.closed.load(Ordering::Acquire) {
            return Err(anyhow!("file RTC closed"));
        }
        session.progress();
        session.ack.send_replace(Some(offset));
        Ok(())
    }
    pub async fn drain(&self, task_id: &str) -> Result<()> {
        let session = self.session(task_id)?;
        let mut canceled = session.wake.subscribe();
        let result = tokio::select! {
            biased;
            _ = canceled.wait_for(|closed| *closed) => Err(anyhow!("file RTC closed")),
            result = tokio::time::timeout(DRAIN_TIMEOUT, async {
                let _gate = session.send_gate.lock().await;
                let channel = session.channel()?;
                channel.set_buffered_amount_low_threshold(0).await;
                let result = session.wait_buffer(&channel, 0).await;
                channel.set_buffered_amount_low_threshold(DATA_CHANNEL_HIGH_WATER / 2).await;
                result
            }) => result.unwrap_or_else(|_| Err(anyhow!("file RTC drain timeout"))),
        };
        if result.is_err() {
            session.close();
        }
        result
    }
    pub async fn send_raw(&self, task_id: &str, raw: Vec<u8>) -> Result<()> {
        if raw.len() > FILE_RTC_MAX_FRAME {
            return Err(anyhow!("file RTC frame exceeds 16KiB payload"));
        }
        let session = self.session(task_id)?;
        session.send(raw).await
    }
    fn session(&self, id: &str) -> Result<Arc<Session>> {
        self.state
            .lock()
            .unwrap()
            .sessions
            .get(id)
            .cloned()
            .ok_or_else(|| anyhow!("file RTC task not found"))
    }
    pub fn remove(&self, id: &str) {
        let session = self.state.lock().unwrap().sessions.remove(id);
        if let Some(session) = session {
            session.close();
        }
    }
    pub fn active(&self, id: &str) -> bool {
        !self.closed.load(Ordering::Acquire)
            && self
                .session(id)
                .is_ok_and(|s| !s.closed.load(Ordering::Acquire))
    }
}

impl Session {
    async fn send(&self, raw: Vec<u8>) -> Result<()> {
        let mut canceled = self.wake.subscribe();
        let result = tokio::select! {
            biased;
            _ = canceled.wait_for(|closed| *closed) => Err(anyhow!("file RTC closed")),
            result = async {
                let _gate = self.send_gate.lock().await;
                let channel = self.channel()?;
                self.wait_buffer(&channel, DATA_CHANNEL_HIGH_WATER - raw.len()).await?;
                channel.send(&Bytes::from(raw)).await.map_err(|e| anyhow!("file RTC send: {e}"))?;
                self.progress();
                Ok(())
            } => result,
        };
        if result.is_err() {
            self.close();
        }
        result
    }
    fn channel(&self) -> Result<Arc<RTCDataChannel>> {
        self.channel
            .lock()
            .unwrap()
            .clone()
            .ok_or_else(|| anyhow!("file RTC not open"))
    }
    fn progress(&self) {
        let mut lifecycle = self.lifecycle.lock().unwrap();
        if lifecycle.opened {
            lifecycle.progress = Instant::now();
        }
    }
    async fn wait_buffer(&self, channel: &RTCDataChannel, limit: usize) -> Result<()> {
        let mut wake = self.wake.subscribe();
        loop {
            if self.closed.load(Ordering::Acquire)
                || channel.ready_state() != RTCDataChannelState::Open
            {
                return Err(anyhow!("file RTC closed"));
            }
            if channel.buffered_amount().await <= limit {
                return Ok(());
            }
            wake.changed()
                .await
                .map_err(|_| anyhow!("file RTC closed"))?;
        }
    }
    async fn watchdog(self: Arc<Self>) {
        let mut canceled = self.wake.subscribe();
        let mut tick = tokio::time::interval(Duration::from_secs(1));
        loop {
            tokio::select! {
                biased;
                _ = canceled.wait_for(|closed| *closed) => return,
                _ = tick.tick() => {
                    if self.lifecycle.lock().unwrap().expired(Instant::now()) {
                        self.close();
                        return;
                    }
                }
            }
        }
    }
    async fn accept_offer(
        self: &Arc<Self>,
        msg: Message,
        out: mpsc::Sender<OutboundEvent>,
    ) -> Result<()> {
        let offer: RTCSessionDescription = serde_json::from_value(msg.rtc_data)?;
        if offer.sdp_type != webrtc::peer_connection::sdp::sdp_type::RTCSdpType::Offer {
            return Err(anyhow!("file RTC signal is not an offer"));
        }
        let ice_servers = msg
            .ice_servers
            .map(|servers| {
                servers
                    .into_iter()
                    .map(|server| RTCIceServer {
                        urls: server.urls,
                        username: server.username,
                        credential: server.credential,
                    })
                    .collect()
            })
            .unwrap_or_else(|| {
                vec![RTCIceServer {
                    urls: vec!["stun:stun.l.google.com:19302".to_owned()],
                    ..Default::default()
                }]
            });
        let peer = Arc::new(
            APIBuilder::new()
                .with_media_engine(MediaEngine::default())
                .build()
                .new_peer_connection(RTCConfiguration {
                    ice_servers,
                    ..Default::default()
                })
                .await?,
        );
        {
            let mut slot = self.peer.lock().unwrap();
            if self.closed.load(Ordering::Acquire) {
                drop(slot);
                tokio::spawn(async move {
                    let _ = peer.close().await;
                });
                return Err(anyhow!("file RTC offer expired"));
            }
            *slot = Some(peer.clone());
        }
        let weak = Arc::downgrade(self);
        peer.on_peer_connection_state_change(Box::new(move |state| {
            let weak = weak.clone();
            Box::pin(async move {
                if matches!(
                    state,
                    RTCPeerConnectionState::Failed | RTCPeerConnectionState::Closed
                ) {
                    if let Some(session) = weak.upgrade() {
                        session.close();
                    }
                }
            })
        }));
        let weak = Arc::downgrade(self);
        let out_candidate = out.clone();
        peer.on_ice_candidate(Box::new(move |candidate| {
            let weak = weak.clone();
            let out = out_candidate.clone();
            Box::pin(async move {
                if let (Some(session), Some(candidate)) = (weak.upgrade(), candidate) {
                    if let Ok(candidate) = candidate.to_json() {
                        if let Ok(data) = serde_json::to_value(candidate) {
                            session.signal(&out, "candidate", data).await;
                        }
                    }
                }
            })
        }));
        let weak = Arc::downgrade(self);
        peer.on_data_channel(Box::new(move |channel| {
            let weak = weak.clone();
            Box::pin(async move {
                let Some(session) = weak.upgrade() else {
                    return;
                };
                let accepted = {
                    let mut slot = session.channel.lock().unwrap();
                    if session.closed.load(Ordering::Acquire)
                        || slot.is_some()
                        || channel.label() != "rdev-file"
                        || !channel.ordered()
                        || channel.max_retransmits().is_some()
                        || channel.max_packet_lifetime().is_some()
                    {
                        false
                    } else {
                        *slot = Some(channel.clone());
                        true
                    }
                };
                if !accepted {
                    session.close();
                    tokio::spawn(async move {
                        let _ = channel.close().await;
                    });
                    return;
                }
                let messages = weak.clone();
                channel.on_message(Box::new(move |message| {
                    let weak = messages.clone();
                    Box::pin(async move {
                        let Some(session) = weak.upgrade() else {
                            return;
                        };
                        if message.is_string || message.data.len() > FILE_RTC_MAX_FRAME {
                            session.close();
                            return;
                        }
                        session.receive(message.data.to_vec()).await;
                    })
                }));
                let opened = weak.clone();
                channel.on_open(Box::new(move || {
                    let weak = opened.clone();
                    Box::pin(async move {
                        if let Some(session) = weak.upgrade() {
                            // Accepted channels install their underlying SCTP channel only
                            // at open; pre-open low-buffer callbacks are not forwarded.
                            if let Ok(channel) = session.channel() {
                                channel
                                    .set_buffered_amount_low_threshold(DATA_CHANNEL_HIGH_WATER / 2)
                                    .await;
                                let low = Arc::downgrade(&session);
                                channel
                                    .on_buffered_amount_low(Box::new(move || {
                                        let weak = low.clone();
                                        Box::pin(async move {
                                            if let Some(session) = weak.upgrade() {
                                                session.wake.send_modify(|_| {});
                                            }
                                        })
                                    }))
                                    .await;
                            }
                            let mut lifecycle = session.lifecycle.lock().unwrap();
                            if session.closed.load(Ordering::Acquire) {
                                return;
                            }
                            lifecycle.opened = true;
                            lifecycle.progress = Instant::now();
                            if let Some(tx) = session.opened.lock().unwrap().take() {
                                let _ = tx.send(());
                            }
                        }
                    })
                }));
                channel.on_close(Box::new(move || {
                    let weak = weak.clone();
                    Box::pin(async move {
                        if let Some(session) = weak.upgrade() {
                            session.close();
                        }
                    })
                }));
            })
        }));
        peer.set_remote_description(offer).await?;
        let pending = {
            let mut candidates = self.candidates.lock().unwrap();
            candidates.ready = true;
            std::mem::take(&mut candidates.pending)
        };
        for candidate in pending {
            peer.add_ice_candidate(candidate).await?;
        }
        let answer = peer.create_answer(None).await?;
        peer.set_local_description(answer).await?;
        let local = peer
            .local_description()
            .await
            .ok_or_else(|| anyhow!("missing file RTC answer"))?;
        let mut answer = serde_json::to_value(local)?;
        if self.range_ack {
            answer["rangeAck"] = Value::Bool(true);
        }
        self.signal(&out, "answer", answer).await;
        Ok(())
    }
    async fn signal(&self, out: &mpsc::Sender<OutboundEvent>, kind: &str, data: Value) {
        if serde_json::to_vec(&data).is_ok_and(|v| v.len() <= MAX_SIGNAL_BYTES) {
            let mut canceled = self.wake.subscribe();
            tokio::select! {
                biased;
                _ = canceled.wait_for(|closed| *closed) => {},
                _ = out.send(OutboundEvent::Message(Message {
                    ty: Some(MessageType::FileRTCSignal), task_id: self.id.clone(),
                    rtc_type: kind.to_owned(), rtc_data: data, ..Default::default()
                })) => {},
            }
        }
    }
    async fn receive(&self, raw: Vec<u8>) {
        if self.range_ack {
            // Validate without allocating decoded task IDs or payloads. Only the
            // exact committed boundary can advance the already-open file.
            let valid = {
                let mut lifecycle = self.lifecycle.lock().unwrap();
                if raw.len() == FRAME_HEADER
                    && raw[0] == BIN_FILE_DOWNLOAD_ACK
                    && raw[1] == 0
                    && lifecycle.range_end
                        == Some(i64::from_be_bytes(raw[2..10].try_into().unwrap()))
                {
                    lifecycle.range_end = None;
                    true
                } else {
                    false
                }
            };
            if !valid {
                self.close();
                return;
            }
            let tx = self.events.lock().unwrap().clone();
            if tx.is_none_or(|tx| tx.try_send(FileRtcEvent::Binary(raw)).is_err()) {
                self.close();
            }
            return;
        }
        let tx = self.events.lock().unwrap().clone();
        if let Some(tx) = tx {
            let mut canceled = self.wake.subscribe();
            // Backpressure the reader rather than dropping a pipelined upload.
            tokio::select! {
                biased;
                _ = canceled.wait_for(|closed| *closed) => {},
                result = tx.send(FileRtcEvent::Binary(raw)) => {
                    if result.is_err() { self.close(); }
                }
            }
        }
    }
    fn close(&self) {
        if self.closed.swap(true, Ordering::AcqRel) {
            return;
        }
        self.wake.send_replace(true);
        self.opened.lock().unwrap().take();
        self.events.lock().unwrap().take();
        self.channel.lock().unwrap().take();
        self.candidates.lock().unwrap().pending.clear();
        if let Some(manager) = self.manager.upgrade() {
            let mut state = manager.lock().unwrap();
            if state
                .sessions
                .get(&self.id)
                .is_some_and(|s| std::ptr::eq(s.as_ref(), self))
            {
                state.sessions.remove(&self.id);
            }
        }
        let peer = self.peer.lock().unwrap().take();
        if let Some(peer) = peer {
            tokio::spawn(async move {
                let _ = peer.close().await;
            });
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn session(manager: &FileRtcManager, id: &str) -> (Arc<Session>, FileRtcTask) {
        session_with_range_ack(manager, id, false)
    }

    fn session_with_range_ack(
        manager: &FileRtcManager,
        id: &str,
        range_ack: bool,
    ) -> (Arc<Session>, FileRtcTask) {
        let (opened, open) = oneshot::channel();
        let (events, rx) = mpsc::channel(if range_ack { 1 } else { 2 });
        let (wake, closed) = watch::channel(false);
        let (ack, _) = watch::channel(None);
        let session = Arc::new(Session {
            id: id.to_owned(),
            manager: Arc::downgrade(&manager.state),
            peer: Mutex::new(None),
            channel: Mutex::new(None),
            candidates: Mutex::new(RemoteCandidates::default()),
            events: Mutex::new(Some(events)),
            opened: Mutex::new(Some(opened)),
            closed: AtomicBool::new(false),
            wake,
            ack,
            range_ack,
            send_gate: tokio::sync::Mutex::new(()),
            lifecycle: Mutex::new(Lifecycle {
                opened: false,
                progress: Instant::now(),
                terminal: None,
                range_wait: None,
                range_end: None,
            }),
        });
        manager
            .state
            .lock()
            .unwrap()
            .sessions
            .insert(id.to_owned(), session.clone());
        (
            session,
            FileRtcTask {
                open,
                events: rx,
                closed,
            },
        )
    }

    #[test]
    fn range_ack_requires_explicit_bounded_download_offer() {
        let mut msg = Message {
            ty: Some(MessageType::FileDownloadStart),
            length: 16,
            rtc_data: serde_json::json!({"rangeAck": true}),
            ..Default::default()
        };
        assert!(range_ack_enabled(&msg));
        msg.length = 0;
        assert!(!range_ack_enabled(&msg));
        msg.length = -1;
        assert!(!range_ack_enabled(&msg));
        msg.length = 16;
        msg.ty = Some(MessageType::FileUploadStart);
        assert!(!range_ack_enabled(&msg));
        msg.ty = Some(MessageType::FileDownloadStart);
        for value in [
            Value::Null,
            Value::Bool(false),
            Value::String("true".into()),
        ] {
            msg.rtc_data = serde_json::json!({"rangeAck": value});
            assert!(!range_ack_enabled(&msg));
        }
    }

    #[tokio::test]
    async fn range_ack_advances_only_one_exact_boundary() {
        let manager = FileRtcManager::new();
        let (session, mut task) = session_with_range_ack(&manager, "download", true);
        assert_eq!(task.events.max_capacity(), 1);
        for offset in [16, 32, 35] {
            manager.wait_for_range_ack("download", offset).unwrap();
            assert!(manager.resume_range("download").is_err());
            session
                .receive(
                    protocol::encode_bin_frame_offset(BIN_FILE_DOWNLOAD_ACK, "", offset, &[])
                        .unwrap(),
                )
                .await;
            assert!(task.events.recv().await.is_some());
            manager.resume_range("download").unwrap();
            let life = session.lifecycle.lock().unwrap();
            assert!(life.range_wait.is_none());
            assert!(life.range_end.is_none());
            assert!(life.terminal.is_none());
        }
        // A repeated ACK after consumption must not authorize another segment.
        session
            .receive(protocol::encode_bin_frame_offset(BIN_FILE_DOWNLOAD_ACK, "", 35, &[]).unwrap())
            .await;
        assert!(!manager.active("download"));
    }

    #[tokio::test]
    async fn malformed_early_and_duplicate_range_acks_close_session() {
        for (typ, id, offset, payload, armed, duplicate) in [
            (BIN_FILE_DOWNLOAD_ACK, "", 16, &[][..], false, false),
            (BIN_FILE_UPLOAD_ACK, "", 16, &[][..], true, false),
            (BIN_FILE_DOWNLOAD_ACK, "download", 16, &[][..], true, false),
            (BIN_FILE_DOWNLOAD_ACK, "", 16, &b"x"[..], true, false),
            (BIN_FILE_DOWNLOAD_ACK, "", 15, &[][..], true, false),
            (BIN_FILE_DOWNLOAD_ACK, "", 17, &[][..], true, false),
            (BIN_FILE_DOWNLOAD_ACK, "", 16, &[][..], true, true),
        ] {
            let manager = FileRtcManager::new();
            let (session, task) = session_with_range_ack(&manager, "download", true);
            if armed {
                manager.wait_for_range_ack("download", 16).unwrap();
            }
            if duplicate {
                session
                    .receive(protocol::encode_bin_frame_offset(typ, id, offset, payload).unwrap())
                    .await;
            }
            session
                .receive(protocol::encode_bin_frame_offset(typ, id, offset, payload).unwrap())
                .await;
            assert!(!manager.active("download"));
            assert!(*task.closed.borrow());
            assert!(task.events.len() <= 1);
        }
    }

    #[tokio::test]
    async fn cancel_unblocks_range_wait_and_disk_wait_gets_terminal_deadline() {
        let manager = FileRtcManager::new();
        let (session, mut task) = session_with_range_ack(&manager, "download", true);
        manager.wait_for_range_ack("download", 16).unwrap();
        {
            let mut life = session.lifecycle.lock().unwrap();
            life.opened = true;
            let start = life.range_wait.unwrap();
            assert!(!life.expired(start + NO_PROGRESS_TIMEOUT));
            assert!(life.expired(start + TERMINAL_TIMEOUT));
        }
        manager.remove("download");
        tokio::time::timeout(Duration::from_secs(1), async {
            task.closed.wait_for(|closed| *closed).await.unwrap();
            assert!(task.events.recv().await.is_none());
        })
        .await
        .unwrap();
        assert!(manager.resume_range("download").is_err());
    }

    #[test]
    fn phase_deadlines_do_not_extend_terminal_retention() {
        let start = Instant::now();
        let mut life = Lifecycle {
            opened: false,
            progress: start,
            terminal: None,
            range_wait: None,
            range_end: None,
        };
        assert!(!life.expired(start + NEGOTIATION_TIMEOUT - Duration::from_millis(1)));
        assert!(life.expired(start + NEGOTIATION_TIMEOUT));
        life.opened = true;
        assert!(!life.expired(start + NEGOTIATION_TIMEOUT));
        assert!(life.expired(start + NO_PROGRESS_TIMEOUT));
        life.terminal = Some(start);
        life.progress = start + Duration::from_secs(25);
        assert!(!life.expired(start + Duration::from_secs(29)));
        assert!(life.expired(start + TERMINAL_TIMEOUT));
    }

    #[test]
    fn candidate_cap_survives_pending_queue_drain() {
        let mut candidates = RemoteCandidates::default();
        for _ in 0..64 {
            assert!(candidates.admit());
        }
        candidates.pending.clear();
        candidates.ready = true;
        for _ in 64..MAX_CANDIDATES {
            assert!(candidates.admit());
        }
        assert!(!candidates.admit());
        assert_eq!(candidates.received, MAX_CANDIDATES);
    }

    #[tokio::test]
    async fn watchdog_closes_every_phase_without_transfer_activity() {
        for (opened, terminal, range_wait, elapsed) in [
            (false, false, false, NEGOTIATION_TIMEOUT),
            (true, false, false, NO_PROGRESS_TIMEOUT),
            (true, true, false, TERMINAL_TIMEOUT),
            (true, false, true, TERMINAL_TIMEOUT),
        ] {
            let manager = FileRtcManager::new();
            let (session, mut task) = session(&manager, "task");
            let at = Instant::now() - elapsed;
            *session.lifecycle.lock().unwrap() = Lifecycle {
                opened,
                progress: at,
                terminal: terminal.then_some(at),
                range_wait: range_wait.then_some(at),
                range_end: None,
            };
            tokio::time::timeout(Duration::from_secs(1), session.watchdog())
                .await
                .unwrap();
            assert!(!manager.active("task"));
            assert!(*task.closed.borrow());
            assert!(task.open.await.is_err());
            assert!(task.events.recv().await.is_none());
        }
    }

    #[tokio::test]
    async fn full_receive_queue_preserves_order_and_cancel_wakes_all_waiters() {
        let manager = FileRtcManager::new();
        let (session, mut task) = session(&manager, "upload");
        session.receive(vec![1]).await;
        session.receive(vec![2]).await;
        let mut blocked = Box::pin(session.receive(vec![3]));
        assert!(
            tokio::time::timeout(Duration::from_millis(10), &mut blocked)
                .await
                .is_err()
        );
        let Some(FileRtcEvent::Binary(first)) = task.events.recv().await else {
            panic!("missing frame")
        };
        assert_eq!(first, vec![1]);
        blocked.await;
        for expected in [2, 3] {
            let Some(FileRtcEvent::Binary(raw)) = task.events.recv().await else {
                panic!("missing frame")
            };
            assert_eq!(raw, vec![expected]);
        }
        session.receive(vec![4]).await;
        session.receive(vec![5]).await;
        let mut blocked = Box::pin(session.receive(vec![6]));
        assert!(
            tokio::time::timeout(Duration::from_millis(10), &mut blocked)
                .await
                .is_err()
        );
        let mut wake_a = session.wake.subscribe();
        let mut wake_b = session.wake.subscribe();
        session.close();
        tokio::time::timeout(Duration::from_secs(1), async {
            blocked.await;
            wake_a.wait_for(|closed| *closed).await.unwrap();
            wake_b.wait_for(|closed| *closed).await.unwrap();
        })
        .await
        .unwrap();
        assert!(task.open.await.is_err());
        assert!(manager.state.lock().unwrap().sessions.is_empty());
        assert!(session.events.lock().unwrap().is_none());
        assert!(session.channel.lock().unwrap().is_none());
    }

    #[tokio::test]
    async fn old_cleanup_cannot_remove_replacement_and_ack_is_cumulative() {
        let manager = FileRtcManager::new();
        let (old, task) = session(&manager, "upload");
        let (new, _) = session(&manager, "upload");
        old.close();
        manager.remove_if_current("upload", &task.closed);
        assert!(Arc::ptr_eq(&manager.session("upload").unwrap(), &new));
        let ack = new.ack.subscribe();
        for offset in 1..1000 {
            manager.acknowledge("upload", offset).unwrap();
        }
        assert_eq!(*ack.borrow(), Some(999));
        manager.close_all();
        assert!(!manager.active("upload"));
    }
}
