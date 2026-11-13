use std::{
    collections::{HashMap, HashSet},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex, Weak,
    },
    time::Duration,
};

use crate::{
    protocol::{Message, MessageType},
    session::OutboundEvent,
};
use anyhow::{anyhow, Result};
use bytes::Bytes;
use futures_util::{SinkExt, StreamExt};
use serde_json::Value;
use tokio::{sync::mpsc, task::JoinHandle};
use tokio_tungstenite::{connect_async, tungstenite::Message as WsMessage};
use webrtc::{
    api::{media_engine::MediaEngine, APIBuilder},
    data_channel::{data_channel_state::RTCDataChannelState, RTCDataChannel},
    ice_transport::ice_candidate::RTCIceCandidateInit,
    ice_transport::ice_server::RTCIceServer,
    peer_connection::{
        configuration::RTCConfiguration, sdp::session_description::RTCSessionDescription,
        RTCPeerConnection,
    },
};

const MAX_SIGNAL_BYTES: usize = 512 * 1024;
const MAX_BROWSER_TEXT: usize = 64 * 1024;
const MAX_LOGICAL_FRAME: usize = 16 * 1024 * 1024;
const FRAME_CHUNK: usize = 16 * 1024 - 13;
const BRIDGE_QUEUE: usize = 2;
const MAX_PENDING_CANDIDATES: usize = 128;
const DATA_CHANNEL_HIGH_WATER: usize = 512 * 1024;
const FRAME_MAGIC: &[u8; 4] = b"RDG1";

#[derive(Clone)]
pub struct DesktopRtcManager {
    state: Arc<Mutex<RtcManagerState>>,
    closed: Arc<AtomicBool>,
}
struct RtcManagerState {
    sessions: HashMap<String, Arc<RtcSession>>,
    negotiating: HashSet<String>,
    early_candidates: HashMap<String, Vec<RTCIceCandidateInit>>,
}
struct RtcSession {
    peer: Arc<RTCPeerConnection>,
    candidates: Mutex<RemoteCandidates>,
    bridge: Mutex<Option<JoinHandle<()>>>,
    closed: AtomicBool,
}
struct RemoteCandidates {
    ready: bool,
    pending: Vec<RTCIceCandidateInit>,
}

impl Default for DesktopRtcManager {
    fn default() -> Self {
        Self::new()
    }
}

impl DesktopRtcManager {
    pub fn new() -> Self {
        Self {
            state: Arc::new(Mutex::new(RtcManagerState {
                sessions: HashMap::new(),
                negotiating: HashSet::new(),
                early_candidates: HashMap::new(),
            })),
            closed: Arc::new(AtomicBool::new(false)),
        }
    }
    pub fn close_all(&self) {
        self.closed.store(true, Ordering::Release);
        let sessions = {
            let mut state = self.state.lock().unwrap();
            state.negotiating.clear();
            state.early_candidates.clear();
            std::mem::take(&mut state.sessions)
        };
        for (_, session) in sessions {
            session.close();
        }
    }
    pub fn signal(&self, msg: Message, local_addr: String, out: mpsc::Sender<OutboundEvent>) {
        if msg.session_id.is_empty() || self.closed.load(Ordering::Acquire) {
            return;
        }
        let id = msg.session_id.clone();
        match msg.rtc_type.as_str() {
            "close" => self.remove(&id),
            "offer" => {
                if msg.rtc_data.is_null() || msg.rtc_data.to_string().len() > MAX_SIGNAL_BYTES {
                    return;
                }
                let inserted = {
                    let mut state = self.state.lock().unwrap();
                    !self.closed.load(Ordering::Acquire)
                        && !state.sessions.contains_key(&id)
                        && state.negotiating.insert(id.clone())
                };
                if !inserted {
                    return;
                }
                let manager = self.clone();
                tokio::spawn(async move {
                    if let Err(err) = manager.accept_offer(msg, local_addr, out).await {
                        manager.remove(&id);
                        tracing::warn!("desktop RTC negotiation failed: {err:#}");
                    }
                });
            }
            "candidate" => {
                if msg.rtc_data.is_null() || msg.rtc_data.to_string().len() > 16 * 1024 {
                    return;
                }
                let Ok(candidate) = serde_json::from_value::<RTCIceCandidateInit>(msg.rtc_data)
                else {
                    return;
                };
                let (session, queue_early) = {
                    let mut state = self.state.lock().unwrap();
                    let session = state.sessions.get(&id).cloned();
                    let queue_early = session.is_none() && state.negotiating.contains(&id);
                    if queue_early {
                        let early = state.early_candidates.entry(id.clone()).or_default();
                        if early.len() < MAX_PENDING_CANDIDATES {
                            early.push(candidate.clone());
                        }
                    }
                    (session, queue_early)
                };
                if !queue_early {
                    if let Some(session) = session {
                        let manager = self.clone();
                        tokio::spawn(async move {
                            manager.add_candidate(id, session, candidate).await;
                        });
                    }
                }
            }
            _ => {}
        }
    }
    fn remove(&self, id: &str) {
        let session = {
            let mut state = self.state.lock().unwrap();
            state.early_candidates.remove(id);
            state.negotiating.remove(id);
            state.sessions.remove(id)
        };
        if let Some(session) = session {
            session.close();
        }
    }
    async fn accept_offer(
        &self,
        msg: Message,
        local_addr: String,
        out: mpsc::Sender<OutboundEvent>,
    ) -> Result<()> {
        let offer: RTCSessionDescription = serde_json::from_value(msg.rtc_data)?;
        if offer.sdp_type != webrtc::peer_connection::sdp::sdp_type::RTCSdpType::Offer {
            return Err(anyhow!("RTC signal is not an offer"));
        }
        let peer = Arc::new(
            APIBuilder::new()
                .with_media_engine(MediaEngine::default())
                .build()
                .new_peer_connection(RTCConfiguration {
                    ice_servers: vec![RTCIceServer {
                        urls: vec!["stun:stun.l.google.com:19302".to_owned()],
                        ..Default::default()
                    }],
                    ..Default::default()
                })
                .await?,
        );
        let session = Arc::new(RtcSession {
            peer: peer.clone(),
            candidates: Mutex::new(RemoteCandidates {
                ready: false,
                pending: Vec::new(),
            }),
            bridge: Mutex::new(None),
            closed: AtomicBool::new(false),
        });
        let inserted = {
            let mut state = self.state.lock().unwrap();
            if self.closed.load(Ordering::Acquire)
                || !state.negotiating.remove(&msg.session_id)
                || state.sessions.contains_key(&msg.session_id)
            {
                false
            } else {
                session.candidates.lock().unwrap().pending = state
                    .early_candidates
                    .remove(&msg.session_id)
                    .unwrap_or_default();
                state
                    .sessions
                    .insert(msg.session_id.clone(), session.clone());
                true
            }
        };
        if !inserted {
            let _ = peer.close().await;
            return Ok(());
        }
        let candidate_out = out.clone();
        let candidate_id = msg.session_id.clone();
        peer.on_ice_candidate(Box::new(move |candidate| {
            let out = candidate_out.clone();
            let id = candidate_id.clone();
            Box::pin(async move {
                if let Some(candidate) = candidate {
                    if let Ok(candidate) = candidate.to_json() {
                        if let Ok(data) = serde_json::to_value(candidate) {
                            send_signal(&out, id, "candidate", data).await;
                        }
                    }
                }
            })
        }));
        let state_weak = Arc::downgrade(&self.state);
        let session_id = msg.session_id.clone();
        peer.on_peer_connection_state_change(Box::new(move |state| {
            let state_weak = state_weak.clone(); let id = session_id.clone();
            Box::pin(async move {
                if state == webrtc::peer_connection::peer_connection_state::RTCPeerConnectionState::Failed || state == webrtc::peer_connection::peer_connection_state::RTCPeerConnectionState::Closed {
                    if let Some(state) = state_weak.upgrade() {
                        let session = { let mut state = state.lock().unwrap(); state.early_candidates.remove(&id); state.negotiating.remove(&id); state.sessions.remove(&id) };
                        if let Some(session) = session { session.close(); }
                    }
                }
            })
        }));
        let addr = local_addr.clone();
        let session_weak = Arc::downgrade(&session);
        peer.on_data_channel(Box::new(move |channel| {
            let addr = addr.clone();
            let session = session_weak.clone();
            Box::pin(async move {
                if channel.label() != "rdev-desktop" {
                    let _ = channel.close().await;
                    return;
                }
                let (local_tx, local_rx) = mpsc::channel::<WsMessage>(BRIDGE_QUEUE);
                let channel_weak = Arc::downgrade(&channel);
                let tx = local_tx.clone();
                channel.on_message(Box::new(move |message| {
                    let channel = channel_weak.clone();
                    let tx = tx.clone();
                    Box::pin(async move {
                        if !message.is_string
                            || message.data.len() > MAX_BROWSER_TEXT
                            || !safe_browser_text(&message.data)
                            || tx
                                .try_send(WsMessage::Text(
                                    String::from_utf8_lossy(&message.data).into_owned().into(),
                                ))
                                .is_err()
                        {
                            if let Some(channel) = channel.upgrade() {
                                let _ = channel.close().await;
                            }
                        }
                    })
                }));
                let close_tx = local_tx.clone();
                channel.on_close(Box::new(move || {
                    let tx = close_tx.clone();
                    Box::pin(async move {
                        let _ = tx.try_send(WsMessage::Close(None));
                    })
                }));
                let channel_weak: Weak<RTCDataChannel> = Arc::downgrade(&channel);
                channel.on_open(Box::new(move || {
                    let channel = channel_weak.clone();
                    let addr = addr.clone();
                    let session = session.clone();
                    Box::pin(async move {
                        if let (Some(channel), Some(session)) =
                            (channel.upgrade(), session.upgrade())
                        {
                            let mut bridge = session.bridge.lock().unwrap();
                            if !session.closed.load(Ordering::Acquire) && bridge.is_none() {
                                *bridge = Some(tokio::spawn(start_bridge(channel, addr, local_rx)));
                            }
                        }
                    })
                }));
            })
        }));
        peer.set_remote_description(offer).await?;
        let pending = {
            let mut candidates = session.candidates.lock().unwrap();
            candidates.ready = true;
            std::mem::take(&mut candidates.pending)
        };
        for candidate in pending {
            peer.add_ice_candidate(candidate).await?;
        }
        let answer = peer.create_answer(None).await?;
        peer.set_local_description(answer).await?;
        let still_active = {
            let state = self.state.lock().unwrap();
            !self.closed.load(Ordering::Acquire) && state.sessions.contains_key(&msg.session_id)
        };
        if !still_active {
            return Ok(());
        }
        let local = peer
            .local_description()
            .await
            .ok_or_else(|| anyhow!("missing local RTC description"))?;
        send_signal(&out, msg.session_id, "answer", serde_json::to_value(local)?).await;
        Ok(())
    }
    async fn add_candidate(
        &self,
        id: String,
        session: Arc<RtcSession>,
        candidate: RTCIceCandidateInit,
    ) {
        let ready = {
            let mut candidates = session.candidates.lock().unwrap();
            if !candidates.ready {
                if candidates.pending.len() < MAX_PENDING_CANDIDATES {
                    candidates.pending.push(candidate.clone());
                }
                return;
            }
            true
        };
        if ready && session.peer.add_ice_candidate(candidate).await.is_err() {
            self.remove(&id);
        }
    }
}
impl RtcSession {
    fn close(&self) {
        self.closed.store(true, Ordering::Release);
        if let Some(task) = self.bridge.lock().unwrap().take() {
            task.abort();
        }
        let peer = self.peer.clone();
        tokio::spawn(async move {
            let _ = peer.close().await;
        });
    }
}
async fn send_signal(
    tx: &mpsc::Sender<OutboundEvent>,
    session_id: String,
    kind: &str,
    data: Value,
) {
    if serde_json::to_vec(&data).is_ok_and(|raw| raw.len() <= MAX_SIGNAL_BYTES) {
        let _ = tx
            .send(OutboundEvent::Message(Message {
                ty: Some(MessageType::DesktopRTCSignal),
                session_id,
                rtc_type: kind.to_owned(),
                rtc_data: data,
                ..Default::default()
            }))
            .await;
    }
}
fn safe_browser_text(raw: &[u8]) -> bool {
    let Ok(value) = serde_json::from_slice::<Value>(raw) else {
        return false;
    };
    if let Some(command) = value.as_str() {
        return matches!(
            command,
            "GetCapturableList" | "PauseVideo" | "ResumeVideo" | "RestartVideo"
        );
    }
    let Some(object) = value.as_object() else {
        return false;
    };
    if object.len() != 1 {
        return false;
    }
    let Some(config) = object.get("Config").and_then(Value::as_object) else {
        return false;
    };
    const ALLOWED: &[&str] = &[
        "capturable_id",
        "capture_cursor",
        "max_width",
        "max_height",
        "client_name",
        "frame_rate",
        "encoder",
    ];
    config.keys().all(|key| ALLOWED.contains(&key.as_str()))
}
async fn start_bridge(
    channel: Arc<RTCDataChannel>,
    local_addr: String,
    mut to_local_rx: mpsc::Receiver<WsMessage>,
) {
    let Ok(addr) = local_addr.parse::<std::net::SocketAddr>() else {
        tracing::warn!("invalid local desktop websocket address");
        return;
    };
    let (socket, _) = match connect_async(format!("ws://{addr}/ws")).await {
        Ok(value) => value,
        Err(err) => {
            tracing::warn!("connecting local desktop websocket: {err}");
            let _ = channel.close().await;
            return;
        }
    };
    let (mut sink, mut stream) = socket.split();
    let (to_peer_tx, mut to_peer_rx) = mpsc::channel::<(bool, Bytes)>(2);
    let sender = channel.clone();
    let mut outbound = tokio::task::JoinSet::new();
    outbound.spawn(async move {
        while let Some((is_text, data)) = to_peer_rx.recv().await {
            if data.len() > MAX_LOGICAL_FRAME || sender.ready_state() != RTCDataChannelState::Open {
                break;
            }
            let mut offset = 0;
            while offset < data.len() {
                while sender.buffered_amount().await > DATA_CHANNEL_HIGH_WATER {
                    if sender.ready_state() != RTCDataChannelState::Open {
                        return;
                    }
                    tokio::time::sleep(Duration::from_millis(5)).await;
                }
                let end = (offset + FRAME_CHUNK).min(data.len());
                let mut wire = Vec::with_capacity(13 + end - offset);
                wire.extend_from_slice(FRAME_MAGIC);
                wire.push(u8::from(is_text));
                wire.extend_from_slice(&(data.len() as u32).to_be_bytes());
                wire.extend_from_slice(&(offset as u32).to_be_bytes());
                wire.extend_from_slice(&data[offset..end]);
                if sender.send(&Bytes::from(wire)).await.is_err() {
                    return;
                }
                offset = end;
            }
        }
    });
    loop {
        tokio::select! {
            _ = outbound.join_next() => break,
            local = stream.next() => match local {
                Some(Ok(WsMessage::Text(text))) => { if text.len() > MAX_LOGICAL_FRAME || to_peer_tx.send((true, Bytes::copy_from_slice(text.as_bytes()))).await.is_err() { break; } }
                Some(Ok(WsMessage::Binary(data))) => { if data.len() > MAX_LOGICAL_FRAME || to_peer_tx.send((false, data)).await.is_err() { break; } }
                Some(Ok(WsMessage::Ping(data))) => { if sink.send(WsMessage::Pong(data)).await.is_err() { break; } }
                Some(Ok(WsMessage::Close(_))) | None | Some(Err(_)) => break,
                _ => {}
            },
            frame = to_local_rx.recv() => match frame { Some(frame) => if sink.send(frame).await.is_err() { break; }, None => break }
        }
    }
    let _ = sink.send(WsMessage::Close(None)).await;
    let _ = channel.close().await;
}
