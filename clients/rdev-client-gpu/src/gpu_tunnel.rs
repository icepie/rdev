use std::{collections::HashMap, net::SocketAddr, time::Duration};

use anyhow::{anyhow, Context, Result};
use futures_util::{SinkExt, StreamExt};
use serde::Deserialize;
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::mpsc,
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

pub fn spawn(args: Args, instance_id: String, local_desktop_ready: bool) {
    if args.no_desktop || args.no_gpu_desktop_tunnel || !local_desktop_ready {
        return;
    }
    tokio::spawn(async move {
        loop {
            if let Err(err) = run_once(&args, &instance_id).await {
                warn!("gpu desktop tunnel disconnected: {err:#}");
            }
            tokio::time::sleep(args.reconnect_delay.max(Duration::from_secs(1))).await;
        }
    });
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
    let (out_tx, mut out_rx) = mpsc::channel::<TunnelFrame>(1024);
    let mut streams: HashMap<u64, mpsc::Sender<Vec<u8>>> = HashMap::new();

    loop {
        tokio::select! {
            outbound = out_rx.recv() => {
                let Some(frame) = outbound else { break; };
                ws_write.send(WsMessage::Binary(encode_frame(frame.typ, frame.stream_id, &frame.payload).into())).await?;
            }
            inbound = ws_read.next() => {
                match inbound {
                    Some(Ok(WsMessage::Binary(raw))) => {
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
                    Some(Ok(WsMessage::Ping(payload))) => ws_write.send(WsMessage::Pong(payload)).await?,
                    Some(Ok(WsMessage::Pong(_))) => {}
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
                    if reader_outbound
                        .send(TunnelFrame {
                            typ: FRAME_DATA,
                            stream_id,
                            payload: buffer[..n].to_vec(),
                        })
                        .await
                        .is_err()
                    {
                        break;
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
}
