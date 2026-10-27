use std::{
    collections::HashMap,
    net::SocketAddr,
    sync::{Arc, Mutex},
    time::Duration,
};
use anyhow::{Context, Result};
use futures_util::{SinkExt, StreamExt};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
    sync::mpsc,
};
use tokio_tungstenite::tungstenite::Message as WsMessage;
use tracing::debug;

use crate::protocol::{Message, MessageType, BIN_TCP_DATA};
use crate::session::OutboundEvent;
use crate::ws_redirect::{connect_async_follow_redirects, WsStream};

const TUNNEL_CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const TUNNEL_WRITE_TIMEOUT: Duration = Duration::from_secs(10);
const TUNNEL_READ_BUF: usize = 64 * 1024;

#[derive(Debug)]
pub enum ForwardInput {
    Data(Vec<u8>),
    Close,
}

#[derive(Clone, Default)]
pub struct ForwardManager {
    connections: Arc<Mutex<HashMap<String, mpsc::Sender<ForwardInput>>>>,
    listeners: Arc<Mutex<HashMap<String, tokio::task::JoinHandle<()>>>>,
}

impl ForwardManager {
    pub fn new() -> Self {
        Self::default()
    }

    pub async fn connect(
        &self,
        msg: Message,
        outbound: mpsc::Sender<OutboundEvent>,
        tunnel_base: Option<String>,
    ) {
        let forward_id = msg.forward_id.clone();
        let addr = format!("{}:{}", msg.host, msg.port);
        match TcpStream::connect(&addr).await {
            Ok(stream) => {
                // Fast path: the server offered a one-shot dial-back path, so
                // carry this stream on a dedicated raw WebSocket instead of
                // framed control-channel forwarding.
                let tunnel_path = msg.tunnel.as_deref().filter(|path| !path.is_empty());
                if let (Some(path), Some(base)) = (tunnel_path, tunnel_base.as_deref()) {
                    match dial_tcp_tunnel(base, path).await {
                        Ok(ws) => {
                            debug!("TCP forward {forward_id} using dedicated tunnel websocket");
                            let (tx, rx) = mpsc::channel(1024);
                            self.connections
                                .lock()
                                .unwrap()
                                .insert(forward_id.clone(), tx);
                            self.spawn_tunnel_connection(forward_id, stream, ws, rx);
                            return;
                        }
                        Err(err) => {
                            debug!(
                                "TCP tunnel dial-back failed for {forward_id}: {err:#}; falling back to framed forwarding"
                            );
                        }
                    }
                }
                let (tx, rx) = mpsc::channel(1024);
                self.connections
                    .lock()
                    .unwrap()
                    .insert(forward_id.clone(), tx);
                let _ = outbound
                    .send(OutboundEvent::Message(Message {
                        ty: Some(MessageType::TcpOpen),
                        forward_id: forward_id.clone(),
                        ..Default::default()
                    }))
                    .await;
                self.spawn_connection(forward_id, stream, rx, outbound)
                    .await;
            }
            Err(err) => {
                let _ = outbound
                    .send(OutboundEvent::Message(Message {
                        ty: Some(MessageType::TcpFail),
                        forward_id,
                        error: err.to_string(),
                        ..Default::default()
                    }))
                    .await;
            }
        }
    }

    pub async fn listen(&self, msg: Message, outbound: mpsc::Sender<OutboundEvent>) {
        let listen_id = msg.listen_id.clone();
        let bind = format!(
            "{}:{}",
            if msg.host.is_empty() {
                "127.0.0.1"
            } else {
                &msg.host
            },
            msg.port
        );
        match TcpListener::bind(&bind).await {
            Ok(listener) => {
                let port = listener
                    .local_addr()
                    .map(|a| a.port())
                    .unwrap_or(msg.port as u16) as i32;
                let _ = outbound
                    .send(OutboundEvent::Message(Message {
                        ty: Some(MessageType::TcpListenOk),
                        listen_id: listen_id.clone(),
                        port,
                        ..Default::default()
                    }))
                    .await;
                let manager = self.clone();
                let outbound_clone = outbound.clone();
                let listen_id_for_task = listen_id.clone();
                let handle = tokio::spawn(async move {
                    loop {
                        match listener.accept().await {
                            Ok((stream, addr)) => {
                                manager
                                    .accept_connection(
                                        &listen_id_for_task,
                                        stream,
                                        addr,
                                        outbound_clone.clone(),
                                    )
                                    .await
                            }
                            Err(err) => {
                                debug!("TCP listener {listen_id_for_task} closed: {err}");
                                break;
                            }
                        }
                    }
                });
                if let Some(old) = self.listeners.lock().unwrap().insert(listen_id, handle) {
                    old.abort();
                }
            }
            Err(err) => {
                let _ = outbound
                    .send(OutboundEvent::Message(Message {
                        ty: Some(MessageType::TcpListenOk),
                        listen_id,
                        error: err.to_string(),
                        ..Default::default()
                    }))
                    .await;
            }
        }
    }

    pub async fn open(&self, forward_id: &str) {
        debug!("TCP forward opened by server: {forward_id}");
    }

    pub fn send_data_nowait(&self, forward_id: &str, data: Vec<u8>) {
        let tx = self.connections.lock().unwrap().get(forward_id).cloned();
        if let Some(tx) = tx {
            let _ = tx.try_send(ForwardInput::Data(data));
        }
    }

    pub async fn send_data(&self, forward_id: &str, data: Vec<u8>) {
        self.send_data_nowait(forward_id, data);
    }

    pub async fn close_forward(&self, forward_id: &str) {
        let tx = self.connections.lock().unwrap().remove(forward_id);
        if let Some(tx) = tx {
            let _ = tx.send(ForwardInput::Close).await;
        }
    }

    pub async fn close_listener(&self, listen_id: &str) {
        if let Some(handle) = self.listeners.lock().unwrap().remove(listen_id) {
            handle.abort();
        }
    }

    pub async fn close_all(&self) {
        let conns: Vec<_> = self
            .connections
            .lock()
            .unwrap()
            .drain()
            .map(|(_, tx)| tx)
            .collect();
        for tx in conns {
            let _ = tx.send(ForwardInput::Close).await;
        }
        let listeners: Vec<_> = self
            .listeners
            .lock()
            .unwrap()
            .drain()
            .map(|(_, h)| h)
            .collect();
        for handle in listeners {
            handle.abort();
        }
    }

    async fn accept_connection(
        &self,
        listen_id: &str,
        stream: TcpStream,
        addr: SocketAddr,
        outbound: mpsc::Sender<OutboundEvent>,
    ) {
        let forward_id = new_forward_id();
        let (tx, rx) = mpsc::channel(1024);
        self.connections
            .lock()
            .unwrap()
            .insert(forward_id.clone(), tx);
        let _ = outbound
            .send(OutboundEvent::Message(Message {
                ty: Some(MessageType::TcpAccept),
                listen_id: listen_id.to_string(),
                forward_id: forward_id.clone(),
                source_addr: addr.to_string(),
                ..Default::default()
            }))
            .await;
        self.spawn_connection(forward_id, stream, rx, outbound)
            .await;
    }

    async fn spawn_connection(
        &self,
        forward_id: String,
        stream: TcpStream,
        mut rx: mpsc::Receiver<ForwardInput>,
        outbound: mpsc::Sender<OutboundEvent>,
    ) {
        let manager = self.clone();
        tokio::spawn(async move {
            let (mut reader, mut writer) = stream.into_split();
            let (done_tx, mut done_rx) = mpsc::channel::<()>(1);
            let read_id = forward_id.clone();
            let read_out = outbound.clone();
            let reader_done = done_tx.clone();
            let reader_task = tokio::spawn(async move {
                let mut buf = vec![0u8; 32 * 1024];
                loop {
                    match reader.read(&mut buf).await {
                        Ok(0) => break,
                        Ok(n) => {
                            if read_out
                                .send(OutboundEvent::Binary {
                                    typ: BIN_TCP_DATA,
                                    id: read_id.clone(),
                                    payload: buf[..n].to_vec(),
                                })
                                .await
                                .is_err()
                            {
                                break;
                            }
                        }
                        Err(_) => break,
                    }
                }
                let _ = reader_done.send(()).await;
            });
            loop {
                tokio::select! {
                    input = rx.recv() => {
                        match input {
                            Some(ForwardInput::Data(data)) => {
                                if writer.write_all(&data).await.is_err() {
                                    break;
                                }
                            }
                            Some(ForwardInput::Close) | None => break,
                        }
                    }
                    _ = done_rx.recv() => break,
                }
            }
            reader_task.abort();
            manager.connections.lock().unwrap().remove(&forward_id);
            let _ = outbound
                .send(OutboundEvent::Message(Message {
                    ty: Some(MessageType::TcpClose),
                    forward_id,
                    ..Default::default()
                }))
                .await;
        });
    }

    /// Splice a target TCP stream with a dedicated dial-back WebSocket:
    /// binary frames are raw stream bytes and WS close means EOF. Either
    /// side closing (or a server tcp_close through the registry) tears down
    /// both ends. The WebSocket itself is the open/close handshake, so no
    /// tcp_open/tcp_close control messages are sent on this path.
    fn spawn_tunnel_connection(
        &self,
        forward_id: String,
        stream: TcpStream,
        ws: WsStream,
        mut rx: mpsc::Receiver<ForwardInput>,
    ) {
        let manager = self.clone();
        tokio::spawn(async move {
            let (mut ws_write, mut ws_read) = ws.split();
            let (mut tcp_reader, mut tcp_writer) = stream.into_split();
            let (data_tx, mut data_rx) = mpsc::channel::<Vec<u8>>(64);
            let (done_tx, mut done_rx) = mpsc::channel::<()>(1);
            let reader_task = tokio::spawn(async move {
                let mut buf = vec![0u8; TUNNEL_READ_BUF];
                loop {
                    match tcp_reader.read(&mut buf).await {
                        Ok(0) => break,
                        Ok(n) => {
                            if data_tx.send(buf[..n].to_vec()).await.is_err() {
                                break;
                            }
                        }
                        Err(_) => break,
                    }
                }
                let _ = done_tx.send(()).await;
            });
            loop {
                tokio::select! {
                    input = rx.recv() => {
                        match input {
                            Some(ForwardInput::Data(data)) => {
                                if tcp_writer.write_all(&data).await.is_err() {
                                    break;
                                }
                            }
                            Some(ForwardInput::Close) | None => break,
                        }
                    }
                    data = data_rx.recv() => {
                        match data {
                            Some(data) => {
                                let send = ws_write.send(WsMessage::Binary(data.into()));
                                if !matches!(tokio::time::timeout(TUNNEL_WRITE_TIMEOUT, send).await, Ok(Ok(()))) {
                                    break;
                                }
                            }
                            None => break,
                        }
                    }
                    inbound = ws_read.next() => {
                        match inbound {
                            Some(Ok(WsMessage::Binary(data))) => {
                                if tcp_writer.write_all(&data).await.is_err() {
                                    break;
                                }
                            }
                            Some(Ok(WsMessage::Ping(data))) => {
                                if ws_write.send(WsMessage::Pong(data)).await.is_err() {
                                    break;
                                }
                            }
                            Some(Ok(WsMessage::Pong(_))) => {}
                            Some(Ok(WsMessage::Close(_))) | None => break,
                            Some(Ok(_)) => {}
                            Some(Err(_)) => break,
                        }
                    }
                    _ = done_rx.recv() => break,
                }
            }
            let _ = tokio::time::timeout(
                TUNNEL_WRITE_TIMEOUT,
                ws_write.send(WsMessage::Close(None)),
            )
            .await;
            reader_task.abort();
            manager.connections.lock().unwrap().remove(&forward_id);
        });
    }
}

/// Dial the one-shot tunnel WebSocket offered in tcp_connect. Compression
/// stays disabled (tokio-tungstenite is built without deflate features).
async fn dial_tcp_tunnel(base: &str, path: &str) -> Result<WsStream> {
    let url = format!("{}{}", base.trim_end_matches('/'), path);
    let (ws, _) = tokio::time::timeout(
        TUNNEL_CONNECT_TIMEOUT,
        connect_async_follow_redirects(&url, 5),
    )
    .await
    .context("tcp tunnel websocket connect timed out")?
    .context("connect tcp tunnel websocket")?;
    Ok(ws)
}

fn new_forward_id() -> String {
    use rand::RngExt;
    use std::fmt::Write;

    let mut bytes = [0u8; 8];
    rand::rng().fill(&mut bytes);
    let mut hex = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        let _ = write!(hex, "{byte:02x}");
    }
    format!("rgpu-{hex}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn generated_forward_ids_are_prefixed() {
        assert!(new_forward_id().starts_with("rgpu-"));
    }
}
