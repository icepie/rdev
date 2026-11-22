//! Terminal RTC carries raw PTY bytes on the reliable ordered rdev-terminal
//! channel. Control, resize and fallback stay on the device control connection.
//! A fallback fence disables direct input before acknowledging fallback_ready;
//! already submitted RTC bytes are never replayed (their delivery is uncertain).
use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex,
    },
    time::Duration,
};

use crate::{
    protocol::{Message, MessageType},
    session::{OutboundEvent, SessionManager},
};
use anyhow::{anyhow, Result};
use bytes::Bytes;
use serde_json::Value;
use tokio::sync::mpsc;
use webrtc::{
    api::APIBuilder,
    data_channel::{data_channel_state::RTCDataChannelState, RTCDataChannel},
    ice_transport::{ice_candidate::RTCIceCandidateInit, ice_server::RTCIceServer},
    peer_connection::{
        configuration::RTCConfiguration,
        peer_connection_state::RTCPeerConnectionState,
        sdp::{sdp_type::RTCSdpType, session_description::RTCSessionDescription},
        RTCPeerConnection,
    },
};

const MAX_SIGNAL: usize = 512 * 1024;
const MAX_INPUT: usize = 16 * 1024;
const HIGH_WATER: usize = 512 * 1024;

#[derive(Clone)]
pub struct TerminalRtcManager {
    state: Arc<Mutex<HashMap<String, Arc<TerminalSession>>>>,
    stopped: Arc<AtomicBool>,
    sessions: SessionManager,
}
struct TerminalSession {
    id: String,
    out: mpsc::Sender<OutboundEvent>,
    peer: Mutex<Option<Arc<RTCPeerConnection>>>,
    channel: Mutex<Option<Arc<RTCDataChannel>>>,
    candidates: tokio::sync::Mutex<Candidates>,
    input: tokio::sync::Mutex<()>,
    opened: AtomicBool,
    bound: AtomicBool,
    disabled: AtomicBool,
}
struct Candidates {
    ready: bool,
    pending: Vec<RTCIceCandidateInit>,
}

impl TerminalRtcManager {
    pub fn new(sessions: SessionManager) -> Self {
        Self {
            state: Arc::new(Mutex::new(HashMap::new())),
            stopped: Arc::new(AtomicBool::new(false)),
            sessions,
        }
    }

    pub fn close_all(&self) {
        self.stopped.store(true, Ordering::Release);
        let sessions = std::mem::take(&mut *self.state.lock().unwrap());
        for (_, session) in sessions {
            session.disable();
        }
        self.sessions.close_all_nowait();
    }

    pub fn remove(&self, id: &str) {
        let session = self.state.lock().unwrap().remove(id);
        if let Some(session) = session {
            session.disable();
        }
    }

    pub fn bind_session(&self, id: &str) {
        if let Some(session) = self.state.lock().unwrap().get(id) {
            session.bound.store(true, Ordering::Release);
        }
    }

    pub fn signal(&self, msg: Message, out: mpsc::Sender<OutboundEvent>) {
        if msg.session_id.is_empty() || self.stopped.load(Ordering::Acquire) {
            return;
        }
        let id = msg.session_id.clone();
        if msg.rtc_type == "close" {
            self.remove(&id);
            return;
        }
        if msg.rtc_type == "fallback" {
            let session = self.state.lock().unwrap().get(&id).cloned();
            if let Some(session) = &session {
                session.disable();
            }
            // Never block the control loop on its own outbound queue. The
            // fence still orders accepted RTC input before subsequent WS input.
            tokio::spawn(async move {
                if let Some(session) = session {
                    let _input = session.input.lock().await;
                    send_signal(&out, &id, "fallback_ready", Value::Null).await;
                } else {
                    send_signal(&out, &id, "fallback_ready", Value::Null).await;
                }
            });
            return;
        }
        if msg.rtc_type == "offer" {
            if !msg.rtc_data.is_object() || msg.rtc_data.to_string().len() > MAX_SIGNAL {
                tokio::spawn(async move {
                    send_signal(&out, &id, "close", Value::Null).await;
                });
                return;
            }
            let session = Arc::new(TerminalSession {
                id: id.clone(),
                out,
                peer: Mutex::new(None),
                channel: Mutex::new(None),
                candidates: tokio::sync::Mutex::new(Candidates {
                    ready: false,
                    pending: Vec::new(),
                }),
                input: tokio::sync::Mutex::new(()),
                opened: AtomicBool::new(false),
                bound: AtomicBool::new(false),
                disabled: AtomicBool::new(false),
            });
            {
                let mut state = self.state.lock().unwrap();
                if state.contains_key(&id) || self.stopped.load(Ordering::Acquire) {
                    return;
                }
                state.insert(id, session.clone());
            }
            let sessions = self.sessions.clone();
            tokio::spawn(async move {
                let result =
                    tokio::time::timeout(Duration::from_secs(15), session.negotiate(msg, sessions))
                        .await;
                if !matches!(result, Ok(Ok(()))) {
                    session.fail();
                }
            });
            return;
        }
        if msg.rtc_type == "candidate" {
            let session = self.state.lock().unwrap().get(&id).cloned();
            let Some(session) = session else { return };
            if session.disabled.load(Ordering::Acquire) {
                return;
            }
            if msg.rtc_data.to_string().len() > 16 * 1024 {
                session.fail();
                return;
            }
            let Ok(candidate) = serde_json::from_value::<RTCIceCandidateInit>(msg.rtc_data) else {
                session.fail();
                return;
            };
            tokio::spawn(async move {
                let mut candidates = session.candidates.lock().await;
                if !candidates.ready {
                    if candidates.pending.len() >= 128 {
                        session.fail();
                    } else {
                        candidates.pending.push(candidate);
                    }
                    return;
                }
                let peer = session.peer.lock().unwrap().clone();
                if let Some(peer) = peer {
                    if peer.add_ice_candidate(candidate).await.is_err() {
                        session.fail();
                    }
                }
            });
        }
    }

    /// True means consumed by RTC, including a failed send with uncertain
    /// delivery. Replaying that payload over WS could duplicate terminal output.
    pub async fn send_output(&self, id: &str, payload: &mut Vec<u8>) -> bool {
        let session = self.state.lock().unwrap().get(id).cloned();
        let Some(session) = session else { return false };
        if !session.bound.load(Ordering::Acquire) || session.disabled.load(Ordering::Acquire) {
            return false;
        }
        let channel = session.channel.lock().unwrap().clone();
        let Some(channel) = channel else {
            session.fail();
            return false;
        };
        if channel.ready_state() != RTCDataChannelState::Open
            || channel.buffered_amount().await > HIGH_WATER
        {
            session.fail();
            return false;
        }
        let mut bytes = Bytes::from(std::mem::take(payload));
        while !bytes.is_empty() {
            let chunk = bytes.split_to(bytes.len().min(MAX_INPUT));
            if !matches!(
                tokio::time::timeout(Duration::from_secs(2), channel.send(&chunk)).await,
                Ok(Ok(_))
            ) {
                session.fail();
                break;
            }
        }
        true
    }
}

impl TerminalSession {
    fn disable(&self) {
        self.disabled.store(true, Ordering::Release);
        let peer = self.peer.lock().unwrap().take();
        self.channel.lock().unwrap().take();
        if let Some(peer) = peer {
            tokio::spawn(async move {
                let _ = peer.close().await;
            });
        }
    }

    fn fail(&self) {
        if self.disabled.swap(true, Ordering::AcqRel) {
            return;
        }
        self.disable();
        let out = self.out.clone();
        let id = self.id.clone();
        tokio::spawn(async move {
            send_signal(&out, &id, "close", Value::Null).await;
        });
    }

    async fn negotiate(self: &Arc<Self>, msg: Message, sessions: SessionManager) -> Result<()> {
        let offer: RTCSessionDescription = serde_json::from_value(msg.rtc_data)?;
        if offer.sdp_type != RTCSdpType::Offer {
            return Err(anyhow!("not a terminal RTC offer"));
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
                    urls: vec!["stun:stun.l.google.com:19302".into()],
                    ..Default::default()
                }]
            });
        let peer = Arc::new(
            APIBuilder::new()
                .build()
                .new_peer_connection(RTCConfiguration {
                    ice_servers,
                    ..Default::default()
                })
                .await?,
        );
        let canceled = {
            let mut slot = self.peer.lock().unwrap();
            if self.disabled.load(Ordering::Acquire) {
                true
            } else {
                *slot = Some(peer.clone());
                false
            }
        };
        if canceled {
            let _ = peer.close().await;
            return Ok(());
        }

        let weak = Arc::downgrade(self);
        peer.on_ice_candidate(Box::new(move |candidate| {
            let weak = weak.clone();
            Box::pin(async move {
                let Some(session) = weak.upgrade() else {
                    return;
                };
                if session.disabled.load(Ordering::Acquire) {
                    return;
                }
                if let Some(candidate) = candidate {
                    if let Ok(candidate) = candidate.to_json() {
                        if let Ok(value) = serde_json::to_value(candidate) {
                            send_signal(&session.out, &session.id, "candidate", value).await;
                        }
                    }
                }
            })
        }));
        let weak = Arc::downgrade(self);
        peer.on_peer_connection_state_change(Box::new(move |state| {
            let weak = weak.clone();
            Box::pin(async move {
                if matches!(
                    state,
                    RTCPeerConnectionState::Failed
                        | RTCPeerConnectionState::Closed
                        | RTCPeerConnectionState::Disconnected
                ) {
                    if let Some(session) = weak.upgrade() {
                        session.fail();
                    }
                }
            })
        }));
        let weak = Arc::downgrade(self);
        peer.on_data_channel(Box::new(move |channel| {
            let weak = weak.clone();
            let sessions = sessions.clone();
            Box::pin(async move {
                let Some(session) = weak.upgrade() else {
                    let _ = channel.close().await;
                    return;
                };
                let accepted = {
                    let mut slot = session.channel.lock().unwrap();
                    if channel.label() != "rdev-terminal"
                        || !channel.ordered()
                        || channel.max_retransmits().is_some()
                        || channel.max_packet_lifetime().is_some()
                        || slot.is_some()
                        || session.disabled.load(Ordering::Acquire)
                    {
                        false
                    } else {
                        *slot = Some(channel.clone());
                        true
                    }
                };
                if !accepted {
                    let _ = channel.close().await;
                    session.fail();
                    return;
                }
                let input_weak = weak.clone();
                channel.on_message(Box::new(move |message| {
                    let weak = input_weak.clone();
                    let sessions = sessions.clone();
                    Box::pin(async move {
                        let Some(session) = weak.upgrade() else {
                            return;
                        };
                        let _input = session.input.lock().await;
                        if session.disabled.load(Ordering::Acquire) {
                            return;
                        }
                        if message.is_string
                            || message.data.len() > MAX_INPUT
                            || !session.bound.load(Ordering::Acquire)
                        {
                            session.fail();
                            return;
                        }
                        if !matches!(
                            tokio::time::timeout(
                                Duration::from_secs(2),
                                sessions.send_data_reliable(&session.id, message.data.to_vec())
                            )
                            .await,
                            Ok(Ok(()))
                        ) {
                            session.fail();
                        }
                    })
                }));
                let close_weak = weak.clone();
                channel.on_close(Box::new(move || {
                    let weak = close_weak.clone();
                    Box::pin(async move {
                        if let Some(session) = weak.upgrade() {
                            session.fail();
                        }
                    })
                }));
                channel.on_open(Box::new(move || {
                    Box::pin(async move {
                        let Some(session) = weak.upgrade() else {
                            return;
                        };
                        if !session.disabled.load(Ordering::Acquire)
                            && !session.opened.swap(true, Ordering::AcqRel)
                        {
                            send_signal(&session.out, &session.id, "ready", Value::Null).await;
                        }
                    })
                }));
            })
        }));
        peer.set_remote_description(offer).await?;
        {
            let mut candidates = self.candidates.lock().await;
            for candidate in candidates.pending.drain(..) {
                peer.add_ice_candidate(candidate).await?;
            }
            candidates.ready = true;
        }
        let answer = peer.create_answer(None).await?;
        peer.set_local_description(answer).await?;
        if self.disabled.load(Ordering::Acquire) {
            return Ok(());
        }
        let local = peer
            .local_description()
            .await
            .ok_or_else(|| anyhow!("missing terminal RTC answer"))?;
        send_signal(&self.out, &self.id, "answer", serde_json::to_value(local)?).await;
        let weak = Arc::downgrade(self);
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_secs(15)).await;
            if let Some(session) = weak.upgrade() {
                if !session.bound.load(Ordering::Acquire) {
                    session.fail();
                }
            }
        });
        Ok(())
    }
}

async fn send_signal(out: &mpsc::Sender<OutboundEvent>, id: &str, kind: &str, data: Value) {
    let _ = out
        .send(OutboundEvent::Message(Message {
            ty: Some(MessageType::TerminalRTCSignal),
            session_id: id.into(),
            rtc_type: kind.into(),
            rtc_data: data,
            ..Default::default()
        }))
        .await;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn fallback_fences_accepted_input_without_blocking_the_control_queue() {
        let manager = TerminalRtcManager::new(SessionManager::new());
        let (out, mut received) = mpsc::channel(1);
        let session = Arc::new(TerminalSession {
            id: "shell".into(),
            out: out.clone(),
            peer: Mutex::new(None),
            channel: Mutex::new(None),
            candidates: tokio::sync::Mutex::new(Candidates {
                ready: false,
                pending: Vec::new(),
            }),
            input: tokio::sync::Mutex::new(()),
            opened: AtomicBool::new(true),
            bound: AtomicBool::new(true),
            disabled: AtomicBool::new(false),
        });
        manager
            .state
            .lock()
            .unwrap()
            .insert("shell".into(), session.clone());
        let input = session.input.lock().await;
        out.send(OutboundEvent::Message(Message::default()))
            .await
            .unwrap();
        manager.signal(
            Message {
                session_id: "shell".into(),
                rtc_type: "fallback".into(),
                ..Default::default()
            },
            out,
        );
        assert!(session.disabled.load(Ordering::Acquire));
        assert!(session.bound.load(Ordering::Acquire));
        received.recv().await.unwrap();
        tokio::task::yield_now().await;
        assert!(matches!(
            received.try_recv(),
            Err(mpsc::error::TryRecvError::Empty)
        ));
        drop(input);
        let ack = tokio::time::timeout(Duration::from_secs(1), received.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(
            matches!(ack, OutboundEvent::Message(msg) if msg.session_id == "shell" && msg.rtc_type == "fallback_ready")
        );
    }

    #[tokio::test]
    async fn lost_peer_still_acknowledges_relay_recovery() {
        let manager = TerminalRtcManager::new(SessionManager::new());
        let (out, mut received) = mpsc::channel(1);
        manager.signal(
            Message {
                session_id: "shell".into(),
                rtc_type: "fallback".into(),
                ..Default::default()
            },
            out,
        );
        let ack = tokio::time::timeout(Duration::from_secs(1), received.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(
            matches!(ack, OutboundEvent::Message(msg) if msg.session_id == "shell" && msg.rtc_type == "fallback_ready")
        );
    }
}
