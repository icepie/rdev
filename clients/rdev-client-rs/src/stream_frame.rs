use anyhow::{anyhow, Result};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};

pub const KIND_JSON: u8 = 1;
pub const KIND_BINARY: u8 = 2;
pub const KIND_PING: u8 = 3;
pub const KIND_PONG: u8 = 4;
pub const KIND_CLOSE: u8 = 5;
const MAX_FRAME: usize = 16 * 1024 * 1024;

#[derive(Debug)]
pub struct Frame {
    pub kind: u8,
    pub payload: Vec<u8>,
}

pub async fn read_frame<R>(reader: &mut R) -> Result<Frame>
where
    R: AsyncRead + Unpin,
{
    let mut header = [0u8; 5];
    reader.read_exact(&mut header).await?;
    let len = u32::from_be_bytes([header[1], header[2], header[3], header[4]]) as usize;
    if len > MAX_FRAME {
        return Err(anyhow!("stream frame too large: {len}"));
    }
    let mut payload = vec![0u8; len];
    if len > 0 {
        reader.read_exact(&mut payload).await?;
    }
    Ok(Frame {
        kind: header[0],
        payload,
    })
}

pub async fn write_frame<W>(writer: &mut W, kind: u8, payload: &[u8]) -> Result<()>
where
    W: AsyncWrite + Unpin,
{
    if payload.len() > MAX_FRAME {
        return Err(anyhow!("stream frame too large: {}", payload.len()));
    }
    let mut header = [0u8; 5];
    header[0] = kind;
    header[1..].copy_from_slice(&(payload.len() as u32).to_be_bytes());
    writer.write_all(&header).await?;
    if !payload.is_empty() {
        writer.write_all(payload).await?;
    }
    writer.flush().await?;
    Ok(())
}
