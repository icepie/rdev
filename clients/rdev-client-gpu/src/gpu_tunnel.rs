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
};
use tokio_kcp::{KcpConfig, KcpNoDelayConfig, KcpStream};
use tokio_tungstenite::tungstenite::{
    error::ProtocolError as WsProtocolError, protocol::frame::coding::CloseCode, Error as WsError,
    Message as WsMessage,
};
use tracing::{debug, info, warn};
use url::Url;

use crate::config::Args;
use crate::module_runtime::spawn_tokio_thread;
use crate::protocol::{self, Message, MessageType};
use crate::stream_frame;
use crate::ws_redirect::connect_async_follow_redirects;

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

pub struct TunnelWorker {
    stop_tx: watch::Sender<bool>,
    _thread: std::thread::JoinHandle<()>,
}

impl TunnelWorker {
    fn stop(self) {
        let _ = self.stop_tx.send(true);
        drop(self._thread);
    }
}

pub fn spawn(args: Args, instance_id: String, local_desktop_ready: bool) -> Option<TunnelWorker> {
    if args.no_desktop || args.no_gpu_desktop_tunnel || !local_desktop_ready {
        return None;
    }
    let (stop_tx, mut stop_rx) = watch::channel(false);
    let thread = spawn_tokio_thread("rdev-gpu-tunnel", async move {
        let mut backoff = ReconnectBackoff::new(args.reconnect_delay, Duration::from_secs(30));
        loop {
            if *stop_rx.borrow() {
                break;
            }
            tokio::select! {
                result = run_once(&args, &instance_id) => {
                    match result {
                        Ok(()) => backoff.reset(),
                        Err(err) if is_expected_disconnect(&err) => {
                            info!("gpu desktop tunnel disconnected; reconnecting");
                        }
                        Err(err) => warn!("gpu desktop tunnel failed: {err:#}"),
                    }
                }
                _ = stop_rx.changed() => break,
            }
            let delay = backoff.next();
            tokio::select! {
                _ = tokio::time::sleep(delay) => {}
                _ = stop_rx.changed() => break,
            }
        }
    });
    Some(TunnelWorker {
        stop_tx,
        _thread: thread,
    })
}

pub fn spawn_supervisor(
    args: Args,
    instance_id: String,
    mut device_rx: watch::Receiver<Option<String>>,
    local_desktop_ready: bool,
) -> Option<std::thread::JoinHandle<()>> {
    if args.no_desktop || args.no_gpu_desktop_tunnel || !local_desktop_ready {
        return None;
    }
    Some(spawn_tokio_thread(
        "rdev-gpu-tunnel-supervisor",
        async move {
            let mut current_device: Option<String> = None;
            let mut worker: Option<TunnelWorker> = None;
            loop {
                let next_device = device_rx.borrow().clone();
                if next_device != current_device {
                    current_device = next_device.clone();
                    if let Some(handle) = worker.take() {
                        handle.stop();
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
                handle.stop();
            }
        },
    ))
}

async fn run_once(args: &Args, instance_id: &str) -> Result<()> {
    let endpoints = server_endpoints(&args.server);
    let mut tried_stream = false;
    let mut last_stream_error: Option<anyhow::Error> = None;
    for endpoint in &endpoints {
        if is_stream_endpoint(endpoint) {
            tried_stream = true;
            match run_once_stream_tunnel(args, instance_id, endpoint).await {
                Ok(()) => return Ok(()),
                Err(err) => {
                    warn!("gpu desktop stream tunnel failed for {endpoint}: {err:#}");
                    last_stream_error = Some(err);
                }
            }
        }
    }
    let Some(ws_endpoint) = endpoints.iter().find(|endpoint| is_ws_endpoint(endpoint)) else {
        if tried_stream {
            return Err(last_stream_error
                .unwrap_or_else(|| anyhow!("all gpu desktop stream tunnel endpoints failed")));
        }
        return Err(anyhow!(
            "no usable gpu desktop tunnel endpoint in {}",
            args.server
        ));
    };
    let tunnel_url = tunnel_url_for_endpoint(args, instance_id, ws_endpoint)?;
    let local_addr: SocketAddr = args
        .gpu_desktop_local
        .parse()
        .with_context(|| format!("invalid --gpu-desktop-local {}", args.gpu_desktop_local))?;
    info!(
        "connecting gpu desktop tunnel to {}://{}{}; local={local_addr}",
        tunnel_url.scheme(),
        tunnel_url.host_str().unwrap_or("unknown"),
        tunnel_url.path()
    );
    let (ws, _) = tokio::time::timeout(
        TUNNEL_CONNECT_TIMEOUT,
        connect_async_follow_redirects(tunnel_url.as_str(), 5),
    )
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
                    Some(Ok(WsMessage::Close(frame))) => {
                        if frame.as_ref().is_some_and(|frame| !is_normal_close_code(frame.code)) {
                            return Err(anyhow!("gpu desktop tunnel closed by server: {frame:?}"));
                        }
                        return Ok(());
                    }
                    Some(Ok(WsMessage::Text(_))) | Some(Ok(WsMessage::Frame(_))) => {}
                    Some(Err(err)) if is_expected_ws_disconnect(&err) => return Ok(()),
                    Some(Err(err)) => return Err(err.into()),
                    None => return Ok(()),
                }
            }
        }
    }
    Ok(())
}

trait AsyncReadWrite: tokio::io::AsyncRead + tokio::io::AsyncWrite {}
impl<T: tokio::io::AsyncRead + tokio::io::AsyncWrite + ?Sized> AsyncReadWrite for T {}

async fn run_once_stream_tunnel(args: &Args, instance_id: &str, endpoint: &str) -> Result<()> {
    let local_addr: SocketAddr = args
        .gpu_desktop_local
        .parse()
        .with_context(|| format!("invalid --gpu-desktop-local {}", args.gpu_desktop_local))?;
    let addr = endpoint_addr(endpoint);
    info!("connecting gpu desktop tcp tunnel to {endpoint}; local={local_addr}");
    let stream: Box<dyn AsyncReadWrite + Unpin + Send> =
        if endpoint.starts_with("kcp://") || endpoint.starts_with("udp://") {
            Box::new(KcpStream::connect(&kcp_config(), addr.parse()?).await?)
        } else {
            Box::new(
                TcpStream::connect(addr)
                    .await
                    .context("connect gpu desktop tcp tunnel")?,
            )
        };
    let (mut reader, mut writer) = tokio::io::split(stream);
    let hello = Message {
        ty: Some(MessageType::GpuDesktopTunnel),
        client_id: args.id.clone(),
        instance_id: instance_id.to_string(),
        password: args.password.clone(),
        ..Default::default()
    };
    stream_frame::write_frame(
        &mut writer,
        stream_frame::KIND_JSON,
        protocol::encode_message(&hello)?.as_bytes(),
    )
    .await?;
    let (out_tx, mut out_rx) = mpsc::channel::<TunnelFrame>(TUNNEL_OUTBOUND_QUEUE);
    let mut streams: HashMap<u64, mpsc::Sender<Vec<u8>>> = HashMap::new();
    let mut ping_interval = tokio::time::interval(TUNNEL_PING_PERIOD);
    let mut last_control = Instant::now();
    loop {
        tokio::select! {
            outbound = out_rx.recv() => {
                let Some(frame) = outbound else { break; };
                stream_frame::write_frame(&mut writer, stream_frame::KIND_BINARY, &encode_frame(frame.typ, frame.stream_id, &frame.payload)).await?;
            }
            _ = ping_interval.tick() => {
                if last_control.elapsed() > TUNNEL_READ_WAIT {
                    return Err(anyhow!("gpu desktop tcp tunnel pong timeout"));
                }
                stream_frame::write_frame(&mut writer, stream_frame::KIND_PING, b"gpu").await?;
            }
            inbound = stream_frame::read_frame(&mut reader) => {
                let frame = inbound?;
                last_control = Instant::now();
                match frame.kind {
                    stream_frame::KIND_BINARY => {
                        let Some(frame) = decode_frame_or_warn(&frame.payload, endpoint) else {
                            continue;
                        };
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
                            other => debug!("ignored gpu desktop tcp tunnel frame type={other}"),
                        }
                    }
                    stream_frame::KIND_PING => stream_frame::write_frame(&mut writer, stream_frame::KIND_PONG, &frame.payload).await?,
                    stream_frame::KIND_PONG => {}
                    stream_frame::KIND_CLOSE => return Err(anyhow!("gpu desktop tcp tunnel closed by server")),
                    _ => {}
                }
            }
        }
    }
    Ok(())
}

fn server_endpoints(server: &str) -> Vec<String> {
    server
        .split(',')
        .map(|part| part.trim())
        .filter(|part| !part.is_empty())
        .map(|part| {
            if part.contains("://") {
                part.to_string()
            } else {
                format!("tcp://{part}")
            }
        })
        .collect()
}

fn endpoint_addr(endpoint: &str) -> String {
    let mut value = endpoint
        .trim()
        .trim_start_matches("tcp://")
        .trim_start_matches("kcp://")
        .trim_start_matches("udp://")
        .to_string();
    if let Some((addr, _)) = value.split_once('/') {
        value = addr.to_string();
    }
    value
}

fn is_stream_endpoint(endpoint: &str) -> bool {
    endpoint.starts_with("tcp://")
        || endpoint.starts_with("kcp://")
        || endpoint.starts_with("udp://")
}

fn is_ws_endpoint(endpoint: &str) -> bool {
    endpoint.starts_with("ws://")
        || endpoint.starts_with("wss://")
        || endpoint.starts_with("http://")
        || endpoint.starts_with("https://")
}

fn kcp_config() -> KcpConfig {
    KcpConfig {
        mtu: 1200,
        nodelay: KcpNoDelayConfig {
            nodelay: true,
            interval: 20,
            resend: 2,
            nc: true,
        },
        wnd_size: (256, 256),
        stream: true,
        ..Default::default()
    }
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
        return Err(anyhow!(
            "gpu desktop tunnel frame too short: {} bytes",
            raw.len()
        ));
    }
    let mut id = [0_u8; 8];
    id.copy_from_slice(&raw[1..9]);
    Ok(TunnelFrame {
        typ: raw[0],
        stream_id: u64::from_be_bytes(id),
        payload: raw[9..].to_vec(),
    })
}

fn decode_frame_or_warn(raw: &[u8], endpoint: &str) -> Option<TunnelFrame> {
    match decode_frame(raw) {
        Ok(frame) => Some(frame),
        Err(err) => {
            warn!(
                "ignored invalid gpu desktop tcp tunnel frame from {endpoint}: {err:#}; bytes={} head={}",
                raw.len(),
                hex_head(raw)
            );
            None
        }
    }
}

fn hex_head(raw: &[u8]) -> String {
    raw.iter()
        .take(16)
        .map(|b| format!("{b:02x}"))
        .collect::<Vec<_>>()
        .join(" ")
}

fn is_normal_close_code(code: CloseCode) -> bool {
    matches!(
        code,
        CloseCode::Normal | CloseCode::Away | CloseCode::Restart | CloseCode::Again
    )
}

fn is_expected_ws_disconnect(err: &WsError) -> bool {
    match err {
        WsError::ConnectionClosed | WsError::AlreadyClosed => true,
        WsError::Protocol(WsProtocolError::ResetWithoutClosingHandshake) => true,
        WsError::Io(err) => matches!(
            err.kind(),
            std::io::ErrorKind::UnexpectedEof
                | std::io::ErrorKind::ConnectionReset
                | std::io::ErrorKind::ConnectionAborted
                | std::io::ErrorKind::BrokenPipe
                | std::io::ErrorKind::NotConnected
        ),
        _ => false,
    }
}

fn is_expected_disconnect(err: &anyhow::Error) -> bool {
    err.chain().any(|cause| {
        cause
            .downcast_ref::<WsError>()
            .is_some_and(is_expected_ws_disconnect)
            || cause.downcast_ref::<std::io::Error>().is_some_and(|err| {
                matches!(
                    err.kind(),
                    std::io::ErrorKind::UnexpectedEof
                        | std::io::ErrorKind::ConnectionReset
                        | std::io::ErrorKind::ConnectionAborted
                        | std::io::ErrorKind::BrokenPipe
                        | std::io::ErrorKind::NotConnected
                )
            })
    })
}

fn tunnel_url_for_endpoint(args: &Args, instance_id: &str, endpoint: &str) -> Result<Url> {
    let mut url = Url::parse(endpoint.trim_end_matches('/'))?;
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

    fn test_args() -> Args {
        Args {
            version: false,
            server: "tcp://127.0.0.1:8081,ws://127.0.0.1:8080".to_string(),
            id: "dev".to_string(),
            password: String::new(),
            shell: None,
            instance_id: None,
            reconnect_delay: Duration::from_secs(1),
            no_auto_update: true,
            auto_update: false,
            update_interval: Duration::from_secs(60),
            no_desktop: false,
            gpu_desktop_local: "127.0.0.1:1701".to_string(),
            no_gpu_desktop_tunnel: false,
            #[cfg(target_os = "linux")]
            gpu_desktop_wayland: false,
            #[cfg(target_os = "linux")]
            gpu_desktop_kms: false,
            #[cfg(target_os = "linux")]
            gpu_desktop_kms_device: None,
            #[cfg(target_os = "linux")]
            gpu_desktop_nvfbc: false,
            #[cfg(target_os = "linux")]
            gpu_desktop_vaapi: false,
            #[cfg(any(target_os = "linux", target_os = "windows"))]
            gpu_desktop_nvenc: false,
            #[cfg(target_os = "linux")]
            gpu_desktop_vulkan_video: false,
            #[cfg(target_os = "macos")]
            gpu_desktop_videotoolbox: false,
            #[cfg(target_os = "windows")]
            gpu_desktop_mediafoundation: false,
            #[cfg(target_os = "windows")]
            gpu_desktop_windows_capture_source: "auto".to_string(),
        }
    }

    #[test]
    fn tunnel_frame_roundtrip() {
        let encoded = encode_frame(FRAME_DATA, 42, b"hello");
        let decoded = decode_frame(&encoded).unwrap();
        assert_eq!(decoded.typ, FRAME_DATA);
        assert_eq!(decoded.stream_id, 42);
        assert_eq!(decoded.payload, b"hello");
    }

    #[test]
    fn stream_endpoint_classification() {
        assert!(is_stream_endpoint("tcp://host:8481"));
        assert!(is_stream_endpoint("kcp://host:8482"));
        assert!(is_ws_endpoint("ws://host/ws"));
        assert!(is_ws_endpoint("https://host"));
        assert!(!is_ws_endpoint("tcp://host:8481"));
    }

    #[test]
    fn tunnel_url_uses_explicit_ws_endpoint_from_multi_server() {
        let args = Args {
            server: "tcp://host:8481,ws://host:8480".to_string(),
            id: "dev1".to_string(),
            password: "pw".to_string(),
            ..test_args()
        };
        let url = tunnel_url_for_endpoint(&args, "inst", "ws://host:8480").unwrap();
        assert_eq!(url.scheme(), "ws");
        assert_eq!(url.path(), "/gpu-desktop-tunnel");
        assert!(url.as_str().contains("device=dev1"));
        assert!(url.as_str().contains("instanceId=inst"));
    }

    #[test]
    fn short_tunnel_frames_are_ignored_by_stream_tunnel() {
        assert!(decode_frame_or_warn(&[], "tcp://host:8481").is_none());
        assert!(decode_frame_or_warn(&[1, 2, 3], "tcp://host:8481").is_none());
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

    #[test]
    fn common_websocket_disconnects_are_expected() {
        assert!(is_expected_ws_disconnect(&WsError::ConnectionClosed));
        assert!(is_expected_ws_disconnect(&WsError::Protocol(
            WsProtocolError::ResetWithoutClosingHandshake,
        )));
        assert!(is_expected_ws_disconnect(&WsError::Io(
            std::io::Error::from(std::io::ErrorKind::BrokenPipe),
        )));
        assert!(!is_expected_ws_disconnect(&WsError::Io(
            std::io::Error::from(std::io::ErrorKind::PermissionDenied),
        )));
    }

    #[test]
    fn websocket_close_codes_preserve_abnormal_failures() {
        assert!(is_normal_close_code(CloseCode::Normal));
        assert!(is_normal_close_code(CloseCode::Restart));
        assert!(!is_normal_close_code(CloseCode::Protocol));
        assert!(!is_normal_close_code(CloseCode::Error));
    }
}
