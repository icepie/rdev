use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc,
};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result};
use clap::Parser;
use futures_util::{SinkExt, StreamExt};
use rdev_client_gpu::{
    config::Args,
    desktop, desktop_env,
    fileput::FilePutManager,
    files::FileManager,
    forward::ForwardManager,
    gpu_tunnel,
    identity::new_instance_id,
    protocol::{
        self, Message, MessageType, BIN_DATA, BIN_FILE_CHUNK, BIN_FILE_END, BIN_FILE_PUT,
        BIN_FILE_START, BIN_TCP_DATA,
    },
    rdev_desktop_service,
    session::{OutboundEvent, SessionManager},
};
use tokio::sync::mpsc;
use tokio_tungstenite::{connect_async, tungstenite::Message as WsMessage};
use tracing::{debug, info, warn};
use tracing_subscriber::EnvFilter;

struct ClientRuntime<'a> {
    args: &'a Args,
    instance_id: &'a str,
    server_host: &'a str,
    desktop_enabled: bool,
    gpu_tunnel_started: Arc<AtomicBool>,
    connect_printed: Arc<AtomicBool>,
}

#[tokio::main]
async fn main() -> Result<()> {
    install_default_tls_provider();

    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")),
        )
        .init();

    let mut args = Args::parse();
    if args.id.trim().is_empty() {
        args.id = default_device_id();
    }
    args.server = normalize_server_url(&args.server);
    print_startup_summary(&args);

    desktop_env::prepare(&mut args);
    let instance_id = args.instance_id.clone().unwrap_or_else(new_instance_id);
    let rdev_desktop_service = rdev_desktop_service::start(&args);
    let desktop_enabled = rdev_desktop_service.is_some();
    if let Some(service) = rdev_desktop_service.as_ref() {
        args.gpu_desktop_local = service.bind_addr().to_string();
    }
    let server_host = parse_ws_host(&args.server);
    let gpu_tunnel_started = Arc::new(AtomicBool::new(false));
    let connect_printed = Arc::new(AtomicBool::new(false));
    let _rdev_desktop_service = rdev_desktop_service;
    let mut reconnect_backoff =
        ReconnectBackoff::new(args.reconnect_delay, Duration::from_secs(30));

    loop {
        match run_once(
            &args,
            &instance_id,
            &server_host,
            desktop_enabled,
            gpu_tunnel_started.clone(),
            connect_printed.clone(),
        )
        .await
        {
            Ok(registered) => {
                if registered {
                    reconnect_backoff.reset();
                }
                info!("connection closed");
            }
            Err(err) => warn!("connection failed: {err:#}"),
        }
        let delay = reconnect_backoff.next();
        info!("reconnecting in {:?}", delay);
        tokio::select! {
            _ = tokio::time::sleep(delay) => {},
            _ = tokio::signal::ctrl_c() => {
                info!("shutdown requested");
                break;
            }
        }
    }
    Ok(())
}

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

async fn run_once(
    args: &Args,
    instance_id: &str,
    server_host: &str,
    desktop_enabled: bool,
    gpu_tunnel_started: Arc<AtomicBool>,
    connect_printed: Arc<AtomicBool>,
) -> Result<bool> {
    let ws_url = websocket_url(&args.server)?;
    info!("connecting to {ws_url} as {}", args.id);
    let (ws, _) = connect_async(ws_url).await.context("connect websocket")?;
    let (mut write, mut read) = ws.split();
    let (out_tx, mut out_rx) = mpsc::channel::<OutboundEvent>(4096);
    let sessions = SessionManager::new();
    let forwards = ForwardManager::new();
    let files = FileManager::new();
    let fileputs = FilePutManager::new();
    let mut registered = false;

    let register = Message {
        ty: Some(MessageType::Register),
        client_id: args.id.clone(),
        instance_id: instance_id.to_string(),
        client_version: format!("rs/{}", env!("CARGO_PKG_VERSION")),
        password: args.password.clone(),
        desktop: Some(desktop::capabilities(!args.no_desktop && desktop_enabled)),
        ..Default::default()
    };
    write
        .send(WsMessage::Text(protocol::encode_message(&register)?.into()))
        .await?;

    loop {
        tokio::select! {
            outbound = out_rx.recv() => {
                match outbound {
                    Some(OutboundEvent::Message(msg)) => {
                        write.send(WsMessage::Text(protocol::encode_message(&msg)?.into())).await?;
                    }
                    Some(OutboundEvent::Binary { typ, id, payload }) => {
                        write.send(WsMessage::Binary(protocol::encode_bin_frame(typ, &id, &payload)?.into())).await?;
                    }
                    Some(OutboundEvent::BinaryOffset { typ, id, offset, payload }) => {
                        write.send(WsMessage::Binary(protocol::encode_bin_frame_offset(typ, &id, offset, &payload)?.into())).await?;
                    }
                    None => break,
                }
            }
            inbound = read.next() => {
                match inbound {
                    Some(Ok(WsMessage::Text(text))) => {
                        let runtime = ClientRuntime {
                            args,
                            instance_id,
                            server_host,
                            desktop_enabled,
                            gpu_tunnel_started: gpu_tunnel_started.clone(),
                            connect_printed: connect_printed.clone(),
                        };
                        if handle_text(&text, runtime, &sessions, &forwards, &files, &out_tx).await? {
                            registered = true;
                        }
                    },
                    Some(Ok(WsMessage::Binary(raw))) => handle_binary(&raw, &sessions, &forwards, &files, &fileputs, &out_tx).await?,
                    Some(Ok(WsMessage::Close(frame))) => {
                        info!("websocket closed by server: {:?}", frame);
                        break;
                    }
                    Some(Ok(WsMessage::Ping(data))) => write.send(WsMessage::Pong(data)).await?,
                    Some(Ok(WsMessage::Pong(_))) => {}
                    Some(Ok(WsMessage::Frame(_))) => {}
                    Some(Err(err)) => return Err(err.into()),
                    None => break,
                }
            }
        }
    }
    sessions.close_all().await;
    forwards.close_all().await;
    files.close_all().await;
    fileputs.close_all().await;
    Ok(registered)
}

async fn handle_text(
    text: &str,
    runtime: ClientRuntime<'_>,
    sessions: &SessionManager,
    forwards: &ForwardManager,
    files: &FileManager,
    out_tx: &mpsc::Sender<OutboundEvent>,
) -> Result<bool> {
    let args = runtime.args;
    let msg = protocol::decode_message(text)?;
    let is_register = matches!(msg.ty, Some(MessageType::Register));
    match msg.ty {
        Some(MessageType::Register) => {
            let registered_id = if msg.client_id.is_empty() {
                args.id.as_str()
            } else {
                msg.client_id.as_str()
            };
            if !msg.client_id.is_empty() && msg.client_id != args.id {
                info!(
                    "server assigned device ID {} for requested ID {}",
                    msg.client_id, args.id
                );
            } else {
                info!("registered as {registered_id}");
            }
            if !runtime.connect_printed.swap(true, Ordering::SeqCst) {
                print_connection_hints(args, runtime.server_host, registered_id, &msg.ssh_port);
            }
            if runtime.desktop_enabled && !runtime.gpu_tunnel_started.swap(true, Ordering::SeqCst) {
                let mut tunnel_args = args.clone();
                tunnel_args.id = registered_id.to_string();
                gpu_tunnel::spawn(
                    tunnel_args,
                    runtime.instance_id.to_string(),
                    runtime.desktop_enabled,
                );
            }
        }
        Some(MessageType::NewSession) => sessions.start(msg, args.shell.clone(), out_tx.clone())?,
        Some(MessageType::StdinClose) => sessions.stdin_close(&msg.session_id).await,
        Some(MessageType::Resize) => sessions.resize(&msg.session_id, msg.rows, msg.cols).await,
        Some(MessageType::Close) => sessions.close(&msg.session_id).await,
        Some(MessageType::TcpConnect) => forwards.connect(msg, out_tx.clone()).await,
        Some(MessageType::TcpListen) => forwards.listen(msg, out_tx.clone()).await,
        Some(MessageType::TcpOpen) => forwards.open(&msg.forward_id).await,
        Some(MessageType::TcpClose) => {
            if !msg.listen_id.is_empty() {
                forwards.close_listener(&msg.listen_id).await;
            } else {
                forwards.close_forward(&msg.forward_id).await;
            }
        }
        Some(MessageType::FileList) => files.list(msg, out_tx.clone()).await,
        Some(MessageType::FileUploadStart) => files.upload_start(msg, out_tx.clone()).await,
        Some(MessageType::FileUploadEnd) => files.upload_end(msg, out_tx.clone()).await,
        Some(MessageType::FileDownloadStart) => files.download_start(msg, out_tx.clone()).await,
        Some(MessageType::FileTransferCancel) => files.cancel(&msg.task_id).await,
        Some(MessageType::DesktopStart) => {
            let error = if runtime.desktop_enabled {
                info!("desktop_start received; embedded GPU desktop is served through the GPU desktop tunnel");
                "embedded GPU desktop is available through the GPU desktop tunnel; refresh the device list if the browser did not switch automatically"
            } else {
                warn!("desktop_start received but embedded GPU desktop service is not active");
                "embedded GPU desktop service is not active"
            };
            let _ = out_tx
                .send(OutboundEvent::Message(Message {
                    ty: Some(MessageType::DesktopReady),
                    session_id: msg.session_id,
                    error: error.into(),
                    desktop: Some(desktop::capabilities(runtime.desktop_enabled)),
                    ..Default::default()
                }))
                .await;
        }
        other => debug!("ignored message type: {:?}", other),
    }
    Ok(is_register)
}

async fn handle_binary(
    raw: &[u8],
    sessions: &SessionManager,
    forwards: &ForwardManager,
    files: &FileManager,
    fileputs: &FilePutManager,
    out_tx: &mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    let (typ, id, payload) = protocol::decode_bin_frame(raw)?;
    match typ {
        BIN_DATA => sessions.send_data(&id, payload).await,
        BIN_TCP_DATA => forwards.send_data(&id, payload).await,
        protocol::BIN_FILE_UPLOAD_CHUNK => {
            let (_, task_id, offset, data) = protocol::decode_bin_frame_offset(raw)?;
            files
                .upload_chunk(&task_id, offset, data, out_tx.clone())
                .await;
        }
        protocol::BIN_FILE_TRANSFER_CANCEL => files.cancel(&id).await,
        BIN_FILE_PUT | BIN_FILE_START | BIN_FILE_CHUNK | BIN_FILE_END => {
            fileputs
                .handle_frame(typ, id, payload, out_tx.clone())
                .await;
        }
        other => debug!("ignored binary frame type {other:#x} id={id}"),
    }
    Ok(())
}

#[cfg(not(windows))]
fn install_default_tls_provider() {
    let _ = rustls::crypto::ring::default_provider().install_default();
}

#[cfg(windows)]
fn install_default_tls_provider() {}

fn default_device_id() -> String {
    std::env::var("RDEV_ID")
        .ok()
        .filter(|value| !value.trim().is_empty())
        .or_else(|| std::env::var("HOSTNAME").ok())
        .or_else(|| std::env::var("COMPUTERNAME").ok())
        .filter(|value| !value.trim().is_empty())
        .unwrap_or_else(|| "rdev-client-gpu".to_string())
}

fn print_startup_summary(args: &Args) {
    println!();
    println!("  ╔═══════════════════════════════════════════╗");
    println!("  ║         RDev Remote Debug Client          ║");
    println!("  ╠═══════════════════════════════════════════╣");
    println!("  ║  Server:  {:<31}  ║", args.server);
    println!("  ║  ID:      {:<31}  ║", args.id);
    if let Some(shell) = args.shell.as_deref().filter(|value| !value.is_empty()) {
        println!("  ║  Shell:   {shell:<31}  ║");
    }
    let auth_mode = if args.password.is_empty() {
        "open (no password)"
    } else {
        "password"
    };
    println!("  ║  Auth:    {auth_mode:<31}  ║");
    if !args.password.is_empty() {
        println!("  ║  Pass:    {:<31}  ║", args.password);
    }
    println!("  ╚═══════════════════════════════════════════╝");
    println!();
}

fn print_connection_hints(args: &Args, server_host: &str, registered_id: &str, ssh_port: &str) {
    let ssh_port = if ssh_port.is_empty() {
        "2222"
    } else {
        ssh_port
    };

    println!("  ── How to Connect ─────────────────────────────");
    println!("  SSH:      ssh {registered_id}@{server_host} -p {ssh_port}");
    if args.password.is_empty() {
        println!("  Password: <none> (open mode)");
    } else {
        println!("  Password: {}", args.password);
        println!(
            "            sshpass -p '{}' ssh {registered_id}@{server_host} -p {ssh_port}",
            args.password
        );
    }
    println!("  SFTP:     sftp -P {ssh_port} {registered_id}@{server_host}");
    println!("  SCP:      scp -P {ssh_port} file {registered_id}@{server_host}:~/");
    println!("  Dashboard: http://{server_host}");
    println!("  ────────────────────────────────────────────────");
    println!();
}

fn normalize_server_url(server: &str) -> String {
    let mut value = server.trim().to_string();
    if value.starts_with("wss:///") {
        value = format!("wss://{}", value["wss://".len()..].trim_start_matches('/'));
    } else if value.starts_with("ws:///") {
        value = format!("ws://{}", value["ws://".len()..].trim_start_matches('/'));
    } else if value.starts_with("https:///") {
        value = format!(
            "https://{}",
            value["https://".len()..].trim_start_matches('/')
        );
    } else if value.starts_with("http:///") {
        value = format!(
            "http://{}",
            value["http://".len()..].trim_start_matches('/')
        );
    } else if !value.starts_with("ws://")
        && !value.starts_with("wss://")
        && !value.starts_with("http://")
        && !value.starts_with("https://")
    {
        value = format!("ws://{value}");
    }
    value
}

fn parse_ws_host(ws_url: &str) -> String {
    let mut value = ws_url
        .strip_prefix("ws://")
        .or_else(|| ws_url.strip_prefix("wss://"))
        .or_else(|| ws_url.strip_prefix("http://"))
        .or_else(|| ws_url.strip_prefix("https://"))
        .unwrap_or(ws_url);
    if let Some((host, _path)) = value.split_once('/') {
        value = host;
    }
    if let Ok(addr) = value.parse::<std::net::SocketAddr>() {
        return addr.ip().to_string();
    }
    if let Some((host, port)) = value.rsplit_once(':') {
        if !host.contains(':') && port.parse::<u16>().is_ok() {
            return host.to_string();
        }
    }
    value.to_string()
}

fn websocket_url(server: &str) -> Result<String> {
    let normalized = normalize_server_url(server);
    let trimmed = normalized.trim_end_matches('/');
    let mut url = if trimmed.ends_with("/ws") {
        trimmed.to_string()
    } else {
        format!("{trimmed}/ws")
    };
    if url.starts_with("http://") {
        url.replace_range(0..4, "ws");
    } else if url.starts_with("https://") {
        url.replace_range(0..5, "wss");
    }
    Ok(url)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn builds_websocket_urls() {
        assert_eq!(
            websocket_url("http://host:8080").unwrap(),
            "ws://host:8080/ws"
        );
        assert_eq!(websocket_url("https://host/r").unwrap(), "wss://host/r/ws");
        assert_eq!(websocket_url("ws://host/ws").unwrap(), "ws://host/ws");
        assert_eq!(
            websocket_url("1.2.3.4:8080").unwrap(),
            "ws://1.2.3.4:8080/ws"
        );
        assert_eq!(
            websocket_url("ws:///1.2.3.4:8080").unwrap(),
            "ws://1.2.3.4:8080/ws"
        );
    }

    #[test]
    fn parses_connection_hint_hosts() {
        assert_eq!(parse_ws_host("ws://1.2.3.4:8080"), "1.2.3.4");
        assert_eq!(
            parse_ws_host("wss://rdev.example.com/ws"),
            "rdev.example.com"
        );
        assert_eq!(
            parse_ws_host("ws://rdev.example.com:8080/path"),
            "rdev.example.com"
        );
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
