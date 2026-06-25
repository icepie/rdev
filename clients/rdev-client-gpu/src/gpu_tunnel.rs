use std::{
    collections::HashMap,
    net::SocketAddr,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use anyhow::{anyhow, Context, Result};
use futures_util::{SinkExt, StreamExt};
use serde::Deserialize;
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::{
        mpsc::{self, error::TrySendError},
        watch,
    },
    task::JoinHandle,
};
use tokio_tungstenite::{connect_async, tungstenite::Message as WsMessage};
use tracing::{debug, info, warn};
use url::Url;

use crate::config::Args;

const FRAME_OPEN: u8 = 1;
const FRAME_DATA: u8 = 2;
const FRAME_CLOSE: u8 = 3;
const CHUNK_SIZE: usize = 64 * 1024;
const TUNNEL_CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const TUNNEL_PING_PERIOD: Duration = Duration::from_secs(25);
const TUNNEL_READ_WAIT: Duration = Duration::from_secs(75);
const TUNNEL_OUTBOUND_QUEUE: usize = 256;

#[derive(Debug, Clone)]
struct ReconnectBackoff {
    min: Duration,
    max: Duration,
    current: Duration,
}

impl ReconnectBackoff {
    fn new(min: Duration, max: Duration) -> Self {
        let min = if min.is_zero() {
            Duration::from_secs(1)
        } else {
            min
        };
        let max = if max < min { min } else { max };
        Self {
            min,
            max,
            current: min,
        }
    }

    fn reset(&mut self) {
        self.current = self.min;
    }

    fn next(&mut self) -> Duration {
        let base = self.current;
        self.current = self.current.saturating_mul(2).min(self.max);
        jitter(base)
    }
}

fn jitter(base: Duration) -> Duration {
    let millis = base.as_millis();
    if millis < 5 {
        return base;
    }
    let spread = (millis / 5).max(1);
    let seed = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    let offset = seed % (spread * 2 + 1);
    Duration::from_millis((millis - spread + offset) as u64)
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct TunnelOpen {
    stream_id: u64,
}

#[derive(Debug)]
struct TunnelFrame {
    typ: u8,
    stream_id: u64,
    payload: Vec<u8>,
}

pub fn spawn(args: Args, instance_id: String, local_desktop_ready: bool) -> Option<JoinHandle<()>> {
    if args.no_desktop || args.no_gpu_desktop_tunnel || !local_desktop_ready {
        return None;
    }
    Some(tokio::spawn(async move {
        let mut backoff = ReconnectBackoff::new(args.reconnect_delay, Duration::from_secs(30));
        loop {
            if let Err(err) = run_once(&args, &instance_id).await {
                warn!("gpu desktop tunnel disconnected: {err:#}");
            } else {
                backoff.reset();
            }
            tokio::time::sleep(backoff.next()).await;
        }
    }))
}

pub fn spawn_supervisor(
    args: Args,
    instance_id: String,
    mut device_rx: watch::Receiver<Option<String>>,
    local_desktop_ready: bool,
) -> Option<JoinHandle<()>> {
    if args.no_desktop || args.no_gpu_desktop_tunnel || !local_desktop_ready {
        return None;
    }
    Some(tokio::spawn(async move {
        let mut current_device: Option<String> = None;
        let mut worker: Option<JoinHandle<()>> = None;
        loop {
            let next_device = device_rx.borrow().clone();
            if next_device != current_device {
                current_device = next_device.clone();
                if let Some(handle) = worker.take() {
                    handle.abort();
                }
                if let Some(device_id) = next_device {
                    let mut tunnel_args = args.clone();
                    tunnel_args.id = device_id;
                    worker = spawn(tunnel_args, instance_id.clone(), local_desktop_ready);
                }
            }
            if device_rx.changed().await.is_err() {
                break;
            }
        }
        if let Some(handle) = worker.take() {
            handle.abort();
        }
    }))
}

async fn run_once(args: &Args, instance_id: &str) -> Result<()> {
    let tunnel_url = tunnel_url(args, instance_id)?;
    let local_addr: SocketAddr = args
        .gpu_desktop_local
        .parse()
        .with_context(|| format!("invalid --gpu-desktop-local {}", args.gpu_desktop_local))?;
    info!("connecting gpu desktop tunnel to {tunnel_url}; local={local_addr}");
    let (ws, _) = tokio::time::timeout(TUNNEL_CONNECT_TIMEOUT, connect_async(tunnel_url.as_str()))
        .await
        .context("gpu desktop tunnel websocket connect timed out")?
        .context("connect gpu desktop tunnel websocket")?;
    let (mut ws_write, mut ws_read) = ws.split();
    let (out_tx, mut out_rx) = mpsc::channel::<TunnelFrame>(TUNNEL_OUTBOUND_QUEUE);
    let mut streams: HashMap<u64, mpsc::Sender<Vec<u8>>> = HashMap::new();
    let mut ping_interval = tokio::time::interval(TUNNEL_PING_PERIOD);
    let mut last_control = Instant::now();

    loop {
        tokio::select! {
            outbound = out_rx.recv() => {
                let Some(frame) = outbound else { break; };
                ws_write.send(WsMessage::Binary(encode_frame(frame.typ, frame.stream_id, &frame.payload).into())).await?;
            }
            _ = ping_interval.tick() => {
                if last_control.elapsed() > TUNNEL_READ_WAIT {
                    return Err(anyhow!("gpu desktop tunnel pong timeout"));
                }
                ws_write.send(WsMessage::Ping(b"rdev".to_vec().into())).await?;
            }
            inbound = ws_read.next() => {
                match inbound {
                    Some(Ok(WsMessage::Binary(raw))) => {
                        last_control = Instant::now();
                        let frame = decode_frame(&raw)?;
                        match frame.typ {
                            FRAME_OPEN => {
                                let open = serde_json::from_slice::<TunnelOpen>(&frame.payload).ok();
                                let stream_id = open.map(|open| open.stream_id).unwrap_or(frame.stream_id);
                                let (stream_tx, stream_rx) = mpsc::channel::<Vec<u8>>(128);
                                streams.insert(stream_id, stream_tx);
                                tokio::spawn(proxy_stream(stream_id, local_addr, stream_rx, out_tx.clone()));
                            }
                            FRAME_DATA => {
                                if let Some(stream) = streams.get(&frame.stream_id) {
                                    let _ = stream.send(frame.payload).await;
                                }
                            }
                            FRAME_CLOSE => {
                                streams.remove(&frame.stream_id);
                            }
                            other => debug!("ignored gpu desktop tunnel frame type={other}"),
                        }
                    }
                    Some(Ok(WsMessage::Ping(payload))) => {
                        last_control = Instant::now();
                        ws_write.send(WsMessage::Pong(payload)).await?;
                    }
                    Some(Ok(WsMessage::Pong(_))) => {
                        last_control = Instant::now();
                    }
                    Some(Ok(WsMessage::Close(frame))) => return Err(anyhow!("gpu desktop tunnel closed by server: {frame:?}")),
                    Some(Ok(WsMessage::Text(_))) | Some(Ok(WsMessage::Frame(_))) => {}
                    Some(Err(err)) => return Err(err.into()),
                    None => return Err(anyhow!("gpu desktop tunnel websocket ended")),
                }
            }
        }
    }
    Ok(())
}

async fn proxy_stream(
    stream_id: u64,
    local_addr: SocketAddr,
    mut inbound: mpsc::Receiver<Vec<u8>>,
    outbound: mpsc::Sender<TunnelFrame>,
) {
    let upstream = match TcpStream::connect(local_addr).await {
        Ok(stream) => stream,
        Err(err) => {
            warn!("gpu desktop local stream {stream_id} connect failed: {err}");
            let _ = outbound
                .send(TunnelFrame {
                    typ: FRAME_CLOSE,
                    stream_id,
                    payload: Vec::new(),
                })
                .await;
            return;
        }
    };
    let (mut reader, mut writer) = upstream.into_split();
    let reader_outbound = outbound.clone();
    let reader_task = tokio::spawn(async move {
        let mut buffer = vec![0_u8; CHUNK_SIZE];
        loop {
            match reader.read(&mut buffer).await {
                Ok(0) => break,
                Ok(n) => {
                    match reader_outbound.try_send(TunnelFrame {
                        typ: FRAME_DATA,
                        stream_id,
                        payload: buffer[..n].to_vec(),
                    }) {
                        Ok(()) => {}
                        Err(TrySendError::Full(_)) => {
                            warn!("gpu desktop tunnel stream {stream_id} outbound queue full; closing stream");
                            break;
                        }
                        Err(TrySendError::Closed(_)) => break,
                    }
                }
                Err(err) => {
                    debug!("gpu desktop local stream {stream_id} read failed: {err}");
                    break;
                }
            }
        }
        let _ = reader_outbound
            .send(TunnelFrame {
                typ: FRAME_CLOSE,
                stream_id,
                payload: Vec::new(),
            })
            .await;
    });

    while let Some(data) = inbound.recv().await {
        if let Err(err) = writer.write_all(&data).await {
            debug!("gpu desktop local stream {stream_id} write failed: {err}");
            break;
        }
    }
    let _ = writer.shutdown().await;
    reader_task.abort();
    let _ = outbound
        .send(TunnelFrame {
            typ: FRAME_CLOSE,
            stream_id,
            payload: Vec::new(),
        })
        .await;
}

fn encode_frame(typ: u8, stream_id: u64, payload: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(9 + payload.len());
    out.push(typ);
    out.extend_from_slice(&stream_id.to_be_bytes());
    out.extend_from_slice(payload);
    out
}

fn decode_frame(raw: &[u8]) -> Result<TunnelFrame> {
    if raw.len() < 9 {
        return Err(anyhow!("gpu desktop tunnel frame too short"));
    }
    let mut id = [0_u8; 8];
    id.copy_from_slice(&raw[1..9]);
    Ok(TunnelFrame {
        typ: raw[0],
        stream_id: u64::from_be_bytes(id),
        payload: raw[9..].to_vec(),
    })
}

fn tunnel_url(args: &Args, instance_id: &str) -> Result<Url> {
    let mut url = Url::parse(args.server.trim_end_matches('/'))?;
    match url.scheme() {
        "http" => url
            .set_scheme("ws")
            .map_err(|_| anyhow!("invalid tunnel scheme"))?,
        "https" => url
            .set_scheme("wss")
            .map_err(|_| anyhow!("invalid tunnel scheme"))?,
        "ws" | "wss" => {}
        other => return Err(anyhow!("unsupported server URL scheme {other}")),
    }
    url.set_path("/gpu-desktop-tunnel");
    url.query_pairs_mut()
        .clear()
        .append_pair("device", &args.id)
        .append_pair("instanceId", instance_id)
        .append_pair("password", &args.password);
    Ok(url)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tunnel_frame_roundtrip() {
        let encoded = encode_frame(FRAME_DATA, 42, b"hello");
        let decoded = decode_frame(&encoded).unwrap();
        assert_eq!(decoded.typ, FRAME_DATA);
        assert_eq!(decoded.stream_id, 42);
        assert_eq!(decoded.payload, b"hello");
    }

    #[test]
    fn reconnect_backoff_caps_and_resets() {
        let mut backoff = ReconnectBackoff::new(Duration::from_secs(1), Duration::from_secs(4));
        let first = backoff.next();
        assert!(first >= Duration::from_millis(800) && first <= Duration::from_millis(1200));
        let second = backoff.next();
        assert!(second >= Duration::from_millis(1600) && second <= Duration::from_millis(2400));
        let capped = backoff.next();
        assert!(capped >= Duration::from_millis(3200) && capped <= Duration::from_millis(4800));
        backoff.reset();
        let reset = backoff.next();
        assert!(reset >= Duration::from_millis(800) && reset <= Duration::from_millis(1200));
    }
}
