use anyhow::{Context, Result};
use clap::Parser;
use futures_util::{SinkExt, StreamExt};
use rdev_client_gpu::{
    config::Args,
    desktop,
    fileput::FilePutManager,
    files::FileManager,
    forward::ForwardManager,
    identity::new_instance_id,
    protocol::{
        self, Message, MessageType, BIN_DATA, BIN_FILE_CHUNK, BIN_FILE_END, BIN_FILE_PUT,
        BIN_FILE_START, BIN_TCP_DATA,
    },
    session::{OutboundEvent, SessionManager},
};
use tokio::sync::mpsc;
use tokio_tungstenite::{connect_async, tungstenite::Message as WsMessage};
use tracing::{debug, info, warn};
use tracing_subscriber::EnvFilter;

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")),
        )
        .init();

    let args = Args::parse();
    if args.id.trim().is_empty() {
        anyhow::bail!("--id is required");
    }
    let instance_id = args.instance_id.clone().unwrap_or_else(new_instance_id);

    loop {
        match run_once(&args, &instance_id).await {
            Ok(()) => info!("connection closed"),
            Err(err) => warn!("connection failed: {err:#}"),
        }
        tokio::select! {
            _ = tokio::time::sleep(args.reconnect_delay) => {},
            _ = tokio::signal::ctrl_c() => {
                info!("shutdown requested");
                break;
            }
        }
    }
    Ok(())
}

async fn run_once(args: &Args, instance_id: &str) -> Result<()> {
    let ws_url = websocket_url(&args.server)?;
    info!("connecting to {ws_url} as {}", args.id);
    let (ws, _) = connect_async(ws_url).await.context("connect websocket")?;
    let (mut write, mut read) = ws.split();
    let (out_tx, mut out_rx) = mpsc::channel::<OutboundEvent>(4096);
    let sessions = SessionManager::new();
    let forwards = ForwardManager::new();
    let files = FileManager::new();
    let fileputs = FilePutManager::new();

    let register = Message {
        ty: Some(MessageType::Register),
        client_id: args.id.clone(),
        instance_id: instance_id.to_string(),
        password: args.password.clone(),
        desktop: Some(desktop::capabilities(!args.no_desktop)),
        ..Default::default()
    };
    write
        .send(WsMessage::Text(protocol::encode_message(&register)?))
        .await?;

    loop {
        tokio::select! {
            outbound = out_rx.recv() => {
                match outbound {
                    Some(OutboundEvent::Message(msg)) => {
                        write.send(WsMessage::Text(protocol::encode_message(&msg)?)).await?;
                    }
                    Some(OutboundEvent::Binary { typ, id, payload }) => {
                        write.send(WsMessage::Binary(protocol::encode_bin_frame(typ, &id, &payload)?)).await?;
                    }
                    Some(OutboundEvent::BinaryOffset { typ, id, offset, payload }) => {
                        write.send(WsMessage::Binary(protocol::encode_bin_frame_offset(typ, &id, offset, &payload)?)).await?;
                    }
                    None => break,
                }
            }
            inbound = read.next() => {
                match inbound {
                    Some(Ok(WsMessage::Text(text))) => handle_text(&text, args, &sessions, &forwards, &files, &out_tx).await?,
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
    Ok(())
}

async fn handle_text(
    text: &str,
    args: &Args,
    sessions: &SessionManager,
    forwards: &ForwardManager,
    files: &FileManager,
    out_tx: &mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    let msg = protocol::decode_message(text)?;
    match msg.ty {
        Some(MessageType::Register) => {
            if !msg.client_id.is_empty() && msg.client_id != args.id {
                info!(
                    "server assigned device ID {} for requested ID {}",
                    msg.client_id, args.id
                );
            } else {
                info!(
                    "registered as {}",
                    if msg.client_id.is_empty() {
                        &args.id
                    } else {
                        &msg.client_id
                    }
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
            warn!("desktop_start received but GPU desktop pipeline is not enabled yet");
            let _ = out_tx
                .send(OutboundEvent::Message(Message {
                    ty: Some(MessageType::DesktopReady),
                    session_id: msg.session_id,
                    error: "rdev-client-gpu desktop pipeline is not implemented yet".into(),
                    ..Default::default()
                }))
                .await;
        }
        other => debug!("ignored message type: {:?}", other),
    }
    Ok(())
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

fn websocket_url(server: &str) -> Result<String> {
    let trimmed = server.trim_end_matches('/');
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
    }
}
