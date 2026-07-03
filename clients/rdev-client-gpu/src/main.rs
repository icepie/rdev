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
    module_runtime::{spawn_runtime, ModuleRuntime},
    protocol::{
        self, Message, MessageType, BIN_DATA, BIN_FILE_CHUNK, BIN_FILE_END, BIN_FILE_PUT,
        BIN_FILE_START, BIN_TCP_DATA,
    },
    rdev_desktop_service,
    session::{OutboundEvent, SessionManager},
    stream_frame, updater, version,
    ws_redirect::connect_async_follow_redirects,
};
use tokio::io::{AsyncRead, AsyncWrite, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::mpsc;
use tokio::sync::watch;
use tokio_kcp::{KcpConfig, KcpNoDelayConfig, KcpStream};
use tokio_tungstenite::tungstenite::Message as WsMessage;
use tracing::{debug, info, warn};
use tracing_subscriber::EnvFilter;
use url::Url;

trait AsyncReadWrite: AsyncRead + AsyncWrite {}
impl<T: AsyncRead + AsyncWrite + ?Sized> AsyncReadWrite for T {}

struct ClientRuntime<'a> {
    args: &'a Args,
    server_host: &'a str,
    desktop_enabled: bool,
    gpu_tunnel_device_tx: &'a watch::Sender<Option<String>>,
    connect_printed: Arc<AtomicBool>,
}

#[derive(Clone)]
struct ModuleRuntimes {
    forwards: ModuleRuntime,
    files: ModuleRuntime,
    fileputs: ModuleRuntime,
}

impl ModuleRuntimes {
    fn new() -> Self {
        Self {
            forwards: spawn_runtime("rdev-forward-runtime"),
            files: spawn_runtime("rdev-file-runtime"),
            fileputs: spawn_runtime("rdev-fileput-runtime"),
        }
    }
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
    if args.version {
        println!("{}", version::VERSION);
        return Ok(());
    }
    if args.id.trim().is_empty() {
        args.id = default_device_id();
    }
    args.server = normalize_server_url(&args.server);
    print_startup_summary(&args);
    updater::start(updater::Config {
        version: version::VERSION,
        enabled: args.auto_update && !args.no_auto_update,
        interval: args.update_interval,
    });

    desktop_env::prepare(&mut args);
    let instance_id = args.instance_id.clone().unwrap_or_else(new_instance_id);
    let rdev_desktop_service = rdev_desktop_service::start(&args);
    let desktop_enabled = rdev_desktop_service.is_some();
    if let Some(service) = rdev_desktop_service.as_ref() {
        args.gpu_desktop_local = service.bind_addr().to_string();
    }
    let server_host = parse_ws_host(&args.server);
    let connect_printed = Arc::new(AtomicBool::new(false));
    let (gpu_tunnel_device_tx, gpu_tunnel_device_rx) = watch::channel::<Option<String>>(None);
    let _gpu_tunnel_supervisor = gpu_tunnel::spawn_supervisor(
        args.clone(),
        instance_id.clone(),
        gpu_tunnel_device_rx,
        desktop_enabled,
    );
    let _rdev_desktop_service = rdev_desktop_service;
    let modules = ModuleRuntimes::new();
    let mut reconnect_backoff =
        ReconnectBackoff::new(args.reconnect_delay, Duration::from_secs(30));

    loop {
        match run_once_any(
            &args,
            &instance_id,
            &server_host,
            desktop_enabled,
            &gpu_tunnel_device_tx,
            &modules,
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
    gpu_tunnel_device_tx: &watch::Sender<Option<String>>,
    modules: &ModuleRuntimes,
    connect_printed: Arc<AtomicBool>,
) -> Result<bool> {
    let ws_url = websocket_url(&args.server)?;
    info!("connecting to {ws_url} as {}", args.id);
    let (ws, _) = connect_async_follow_redirects(&ws_url, 5)
        .await
        .context("connect websocket")?;
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
        client_version: version::client_version(),
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
                            server_host,
                            desktop_enabled,
                            gpu_tunnel_device_tx,
                            connect_printed: connect_printed.clone(),
                        };
                        if handle_text(&text, runtime, modules, &sessions, &forwards, &files, &out_tx).await? {
                            registered = true;
                        }
                    },
                    Some(Ok(WsMessage::Binary(raw))) => handle_binary(&raw, modules, &sessions, &forwards, &files, &fileputs, &out_tx).await?,
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

async fn run_once_any(
    args: &Args,
    instance_id: &str,
    server_host: &str,
    desktop_enabled: bool,
    gpu_tunnel_device_tx: &watch::Sender<Option<String>>,
    modules: &ModuleRuntimes,
    connect_printed: Arc<AtomicBool>,
) -> Result<bool> {
    let mut last_err: Option<anyhow::Error> = None;
    for endpoint in server_endpoints(&args.server) {
        match endpoint_kind(&endpoint) {
            "tcp" => match run_once_tcp(
                args,
                instance_id,
                server_host,
                desktop_enabled,
                gpu_tunnel_device_tx,
                modules,
                connect_printed.clone(),
                &endpoint,
            )
            .await
            {
                Ok(v) => return Ok(v),
                Err(err) => last_err = Some(err),
            },
            "ws" => match run_once(
                args,
                instance_id,
                server_host,
                desktop_enabled,
                gpu_tunnel_device_tx,
                modules,
                connect_printed.clone(),
            )
            .await
            {
                Ok(v) => return Ok(v),
                Err(err) => last_err = Some(err),
            },
            "kcp" => match run_once_stream(
                args,
                instance_id,
                server_host,
                desktop_enabled,
                gpu_tunnel_device_tx,
                modules,
                connect_printed.clone(),
                &endpoint,
                true,
            )
            .await
            {
                Ok(v) => return Ok(v),
                Err(err) => last_err = Some(err),
            },
            other => last_err = Some(anyhow::anyhow!("unsupported endpoint type {other}")),
        }
    }
    Err(last_err.unwrap_or_else(|| anyhow::anyhow!("no server endpoints")))
}

#[allow(clippy::too_many_arguments)]
async fn run_once_tcp(
    args: &Args,
    instance_id: &str,
    server_host: &str,
    desktop_enabled: bool,
    gpu_tunnel_device_tx: &watch::Sender<Option<String>>,
    modules: &ModuleRuntimes,
    connect_printed: Arc<AtomicBool>,
    endpoint: &str,
) -> Result<bool> {
    run_once_stream(
        args,
        instance_id,
        server_host,
        desktop_enabled,
        gpu_tunnel_device_tx,
        modules,
        connect_printed,
        endpoint,
        false,
    )
    .await
}

#[allow(clippy::too_many_arguments)]
async fn run_once_stream(
    args: &Args,
    instance_id: &str,
    server_host: &str,
    desktop_enabled: bool,
    gpu_tunnel_device_tx: &watch::Sender<Option<String>>,
    modules: &ModuleRuntimes,
    connect_printed: Arc<AtomicBool>,
    endpoint: &str,
    kcp: bool,
) -> Result<bool> {
    let addr = endpoint_addr(endpoint)?;
    info!("connecting to {endpoint} as {}", args.id);
    let stream: Box<dyn AsyncReadWrite + Unpin + Send> = if kcp {
        let socket_addr = addr.parse()?;
        Box::new(KcpStream::connect(&kcp_config(), socket_addr).await?)
    } else {
        Box::new(TcpStream::connect(addr).await.context("connect tcp")?)
    };
    let (mut read_half, mut write_half) = tokio::io::split(stream);
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
        client_version: version::client_version(),
        password: args.password.clone(),
        desktop: Some(desktop::capabilities(!args.no_desktop && desktop_enabled)),
        ..Default::default()
    };
    stream_frame::write_frame(
        &mut write_half,
        stream_frame::KIND_JSON,
        protocol::encode_message(&register)?.as_bytes(),
    )
    .await?;
    let mut ping_interval = tokio::time::interval(Duration::from_secs(25));
    loop {
        tokio::select! {
            outbound = out_rx.recv() => {
                match outbound {
                    Some(OutboundEvent::Message(msg)) => {
                        stream_frame::write_frame(&mut write_half, stream_frame::KIND_JSON, protocol::encode_message(&msg)?.as_bytes()).await?;
                    }
                    Some(OutboundEvent::Binary { typ, id, payload }) => {
                        let frame = protocol::encode_bin_frame(typ, &id, &payload)?;
                        stream_frame::write_frame(&mut write_half, stream_frame::KIND_BINARY, &frame).await?;
                    }
                    Some(OutboundEvent::BinaryOffset { typ, id, offset, payload }) => {
                        let frame = protocol::encode_bin_frame_offset(typ, &id, offset, &payload)?;
                        stream_frame::write_frame(&mut write_half, stream_frame::KIND_BINARY, &frame).await?;
                    }
                    None => break,
                }
            }
            _ = ping_interval.tick() => {
                stream_frame::write_frame(&mut write_half, stream_frame::KIND_PING, b"rdev").await?;
            }
            inbound = stream_frame::read_frame(&mut read_half) => {
                let frame = inbound?;
                match frame.kind {
                    stream_frame::KIND_JSON => {
                        let text = String::from_utf8(frame.payload)?;
                        let runtime = ClientRuntime { args, server_host, desktop_enabled, gpu_tunnel_device_tx, connect_printed: connect_printed.clone() };
                        if handle_text(&text, runtime, modules, &sessions, &forwards, &files, &out_tx).await? {
                            registered = true;
                        }
                    }
                    stream_frame::KIND_BINARY => handle_binary(&frame.payload, modules, &sessions, &forwards, &files, &fileputs, &out_tx).await?,
                    stream_frame::KIND_PING => stream_frame::write_frame(&mut write_half, stream_frame::KIND_PONG, &frame.payload).await?,
                    stream_frame::KIND_CLOSE => break,
                    _ => {}
                }
            }
        }
    }
    let _ = write_half.shutdown().await;
    sessions.close_all().await;
    forwards.close_all().await;
    files.close_all().await;
    fileputs.close_all().await;
    Ok(registered)
}

async fn handle_text(
    text: &str,
    runtime: ClientRuntime<'_>,
    modules: &ModuleRuntimes,
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
            if runtime.desktop_enabled {
                let _ = runtime
                    .gpu_tunnel_device_tx
                    .send(Some(registered_id.to_string()));
            }
        }
        Some(MessageType::NewSession) => sessions.start(msg, args.shell.clone(), out_tx.clone())?,
        Some(MessageType::StdinClose) => sessions.stdin_close(&msg.session_id).await,
        Some(MessageType::Resize) => sessions.resize(&msg.session_id, msg.rows, msg.cols).await,
        Some(MessageType::Close) => sessions.close(&msg.session_id).await,
        Some(MessageType::TcpConnect) => {
            let forwards = forwards.clone();
            let out_tx = out_tx.clone();
            modules.forwards.spawn(async move {
                forwards.connect(msg, out_tx).await;
            });
        }
        Some(MessageType::TcpListen) => {
            let forwards = forwards.clone();
            let out_tx = out_tx.clone();
            modules.forwards.spawn(async move {
                forwards.listen(msg, out_tx).await;
            });
        }
        Some(MessageType::TcpOpen) => forwards.open(&msg.forward_id).await,
        Some(MessageType::TcpClose) => {
            if !msg.listen_id.is_empty() {
                forwards.close_listener(&msg.listen_id).await;
            } else {
                forwards.close_forward(&msg.forward_id).await;
            }
        }
        Some(MessageType::FileList) => {
            let files = files.clone();
            let out_tx = out_tx.clone();
            modules.files.spawn(async move {
                files.list(msg, out_tx).await;
            });
        }
        Some(MessageType::FileUploadStart) => {
            let files = files.clone();
            let out_tx = out_tx.clone();
            modules.files.spawn(async move {
                files.upload_start(msg, out_tx).await;
            });
        }
        Some(MessageType::FileUploadEnd) => {
            let files = files.clone();
            let out_tx = out_tx.clone();
            modules.files.spawn(async move {
                files.upload_end(msg, out_tx).await;
            });
        }
        Some(MessageType::FileDownloadStart) => {
            let files = files.clone();
            let out_tx = out_tx.clone();
            modules.files.spawn(async move {
                files.download_start(msg, out_tx).await;
            });
        }
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
    modules: &ModuleRuntimes,
    sessions: &SessionManager,
    forwards: &ForwardManager,
    files: &FileManager,
    fileputs: &FilePutManager,
    out_tx: &mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    let (typ, id, payload) = protocol::decode_bin_frame(raw)?;
    match typ {
        BIN_DATA => sessions.send_data_nowait(&id, payload),
        BIN_TCP_DATA => forwards.send_data_nowait(&id, payload),
        protocol::BIN_FILE_UPLOAD_CHUNK => {
            let (_, task_id, offset, data) = protocol::decode_bin_frame_offset(raw)?;
            let files = files.clone();
            let out_tx = out_tx.clone();
            modules.files.spawn(async move {
                files.upload_chunk(&task_id, offset, data, out_tx).await;
            });
        }
        protocol::BIN_FILE_TRANSFER_CANCEL => files.cancel(&id).await,
        BIN_FILE_PUT | BIN_FILE_START | BIN_FILE_CHUNK | BIN_FILE_END => {
            let fileputs = fileputs.clone();
            let out_tx = out_tx.clone();
            modules.fileputs.spawn(async move {
                fileputs.handle_frame(typ, id, payload, out_tx).await;
            });
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
    server
        .split(',')
        .map(normalize_server_endpoint)
        .collect::<Vec<_>>()
        .join(",")
}

fn normalize_server_endpoint(server: &str) -> String {
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
    } else if value.starts_with("tcp:///") {
        value = format!("tcp://{}", value["tcp://".len()..].trim_start_matches('/'));
    } else if value.starts_with("kcp:///") {
        value = format!("kcp://{}", value["kcp://".len()..].trim_start_matches('/'));
    } else if value.starts_with("udp:///") {
        value = format!("udp://{}", value["udp://".len()..].trim_start_matches('/'));
    } else if !value.starts_with("ws://")
        && !value.starts_with("wss://")
        && !value.starts_with("http://")
        && !value.starts_with("https://")
        && !value.starts_with("tcp://")
        && !value.starts_with("kcp://")
        && !value.starts_with("udp://")
    {
        value = format!("tcp://{value}");
    }
    value
}

fn parse_ws_host(server_url: &str) -> String {
    let first = server_url.split(',').next().unwrap_or(server_url);
    let mut value = first
        .strip_prefix("ws://")
        .or_else(|| first.strip_prefix("wss://"))
        .or_else(|| first.strip_prefix("http://"))
        .or_else(|| first.strip_prefix("https://"))
        .or_else(|| first.strip_prefix("tcp://"))
        .or_else(|| first.strip_prefix("kcp://"))
        .or_else(|| first.strip_prefix("udp://"))
        .unwrap_or(first);
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
    let normalized = server_endpoints(server)
        .into_iter()
        .find(|endpoint| endpoint_kind(endpoint) == "ws")
        .unwrap_or_else(|| {
            derive_ws_endpoint(
                server_endpoints(server)
                    .first()
                    .map(String::as_str)
                    .unwrap_or(server),
            )
        });
    let mut parsed = Url::parse(&normalized)?;
    let path = parsed.path().trim_end_matches('/').to_string();
    if path.is_empty() || path == "/" {
        parsed.set_path("/ws");
    } else if !path.ends_with("/ws") {
        parsed.set_path(&format!("{path}/ws"));
    }
    match parsed.scheme() {
        "http" => parsed
            .set_scheme("ws")
            .map_err(|_| anyhow::anyhow!("invalid websocket scheme"))?,
        "https" => parsed
            .set_scheme("wss")
            .map_err(|_| anyhow::anyhow!("invalid websocket scheme"))?,
        "ws" | "wss" => {}
        other => return Err(anyhow::anyhow!("unsupported websocket scheme: {other}")),
    }
    Ok(parsed.to_string())
}

fn server_endpoints(server: &str) -> Vec<String> {
    server
        .split(',')
        .map(normalize_server_endpoint)
        .filter(|value| !value.trim().is_empty())
        .collect()
}

fn endpoint_kind(endpoint: &str) -> &'static str {
    if endpoint.starts_with("tcp://") {
        "tcp"
    } else if endpoint.starts_with("ws://")
        || endpoint.starts_with("wss://")
        || endpoint.starts_with("http://")
        || endpoint.starts_with("https://")
    {
        "ws"
    } else if endpoint.starts_with("kcp://") || endpoint.starts_with("udp://") {
        "kcp"
    } else {
        "tcp"
    }
}

fn endpoint_addr(endpoint: &str) -> Result<String> {
    let mut value = endpoint
        .trim()
        .trim_start_matches("tcp://")
        .trim_start_matches("kcp://")
        .trim_start_matches("udp://")
        .to_string();
    if let Some((addr, _)) = value.split_once('/') {
        value = addr.to_string();
    }
    Ok(value)
}

fn derive_ws_endpoint(endpoint: &str) -> String {
    let addr = endpoint_addr(endpoint).unwrap_or_else(|_| endpoint.to_string());
    let host = addr.rsplit_once(':').map(|(host, _)| host).unwrap_or(&addr);
    format!("ws://{host}:8080")
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
