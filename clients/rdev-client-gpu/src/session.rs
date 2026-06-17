use anyhow::{anyhow, Context, Result};
#[cfg(not(windows))]
use std::io::Read;
use std::io::Write;
#[cfg(not(windows))]
use std::thread;
use std::{
    collections::HashMap,
    process::Stdio,
    sync::{Arc, Mutex},
};
use tokio::{
    io::{AsyncRead, AsyncReadExt, AsyncWriteExt},
    process::Command,
    sync::mpsc,
};
use tracing::warn;

use crate::{
    protocol::{Message, MessageType, BIN_DATA, BIN_STDERR},
    sftp::run_sftp_session,
};

#[derive(Debug, Clone)]
#[allow(clippy::large_enum_variant)]
pub enum OutboundEvent {
    Message(Message),
    Binary {
        typ: u8,
        id: String,
        payload: Vec<u8>,
    },
    BinaryOffset {
        typ: u8,
        id: String,
        offset: i64,
        payload: Vec<u8>,
    },
}

#[derive(Debug)]
pub enum SessionInput {
    Data(Vec<u8>),
    StdinClose,
    Resize { rows: u16, cols: u16 },
    Close,
}

#[derive(Clone, Default)]
pub struct SessionManager {
    sessions: Arc<Mutex<HashMap<String, mpsc::Sender<SessionInput>>>>,
}

impl SessionManager {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn start(
        &self,
        msg: Message,
        shell: Option<String>,
        outbound: mpsc::Sender<OutboundEvent>,
    ) -> Result<()> {
        let session_id = msg.session_id.clone();
        if session_id.is_empty() {
            return Err(anyhow!("new_session missing sessionId"));
        }
        let (tx, rx) = mpsc::channel(1024);
        self.sessions.lock().unwrap().insert(session_id.clone(), tx);
        let manager = self.clone();
        tokio::spawn(async move {
            let result = if msg.subsystem == "sftp" {
                run_sftp(msg, rx, outbound.clone()).await
            } else if msg.pty {
                run_pty(msg, shell, rx, outbound.clone()).await
            } else {
                run_exec(msg, shell, rx, outbound.clone()).await
            };
            if let Err(err) = result {
                warn!("session {session_id} failed: {err:#}");
                let _ = outbound
                    .send(OutboundEvent::Binary {
                        typ: BIN_STDERR,
                        id: session_id.clone(),
                        payload: format!("rdev-client-gpu: {err:#}\n").into_bytes(),
                    })
                    .await;
                send_close(&outbound, &session_id).await;
            }
            manager.remove(&session_id);
        });
        Ok(())
    }

    pub async fn send_data(&self, session_id: &str, data: Vec<u8>) {
        let tx = self.sessions.lock().unwrap().get(session_id).cloned();
        if let Some(tx) = tx {
            let _ = tx.send(SessionInput::Data(data)).await;
        }
    }

    pub async fn stdin_close(&self, session_id: &str) {
        let tx = self.sessions.lock().unwrap().get(session_id).cloned();
        if let Some(tx) = tx {
            let _ = tx.send(SessionInput::StdinClose).await;
        }
    }

    pub async fn resize(&self, session_id: &str, rows: u16, cols: u16) {
        let tx = self.sessions.lock().unwrap().get(session_id).cloned();
        if let Some(tx) = tx {
            let _ = tx.send(SessionInput::Resize { rows, cols }).await;
        }
    }

    pub async fn close(&self, session_id: &str) {
        let tx = self.sessions.lock().unwrap().remove(session_id);
        if let Some(tx) = tx {
            let _ = tx.send(SessionInput::Close).await;
        }
    }

    pub async fn close_all(&self) {
        let sessions: Vec<_> = self
            .sessions
            .lock()
            .unwrap()
            .drain()
            .map(|(_, tx)| tx)
            .collect();
        for tx in sessions {
            let _ = tx.send(SessionInput::Close).await;
        }
    }

    fn remove(&self, session_id: &str) {
        self.sessions.lock().unwrap().remove(session_id);
    }
}

async fn run_exec(
    msg: Message,
    shell: Option<String>,
    mut rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    let session_id = msg.session_id.clone();
    let mut cmd = shell_command(shell, &msg.command);
    cmd.envs(parse_env(&msg.env));
    cmd.stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = cmd
        .spawn()
        .with_context(|| format!("spawn command {:?}", msg.command))?;
    let mut stdin = Some(child.stdin.take().context("child stdin unavailable")?);
    let stdout = child.stdout.take().context("child stdout unavailable")?;
    let stderr = child.stderr.take().context("child stderr unavailable")?;
    let decode_terminal_output = cfg!(windows) && msg.pty;

    pipe_async_reader(
        session_id.clone(),
        BIN_DATA,
        &outbound,
        stdout,
        decode_terminal_output,
    );
    pipe_async_reader(
        session_id.clone(),
        BIN_STDERR,
        &outbound,
        stderr,
        decode_terminal_output,
    );

    let mut tick = tokio::time::interval(std::time::Duration::from_millis(100));
    loop {
        tokio::select! {
            _ = tick.tick() => {
                if let Some(status) = child.try_wait()? {
                    drop(stdin.take());
                    send_exit_and_close(&outbound, &session_id, status.code().unwrap_or(-1)).await;
                    return Ok(());
                }
            }
            input = rx.recv() => {
                match input {
                    Some(SessionInput::Data(data)) => {
                        let write_failed = if let Some(input) = stdin.as_mut() {
                            input.write_all(&data).await.is_err()
                        } else {
                            false
                        };
                        if write_failed {
                            drop(stdin.take());
                        }
                    }
                    Some(SessionInput::StdinClose) => {
                        drop(stdin.take());
                    }
                    Some(SessionInput::Resize { .. }) => {}
                    Some(SessionInput::Close) => {
                        let _ = child.kill().await;
                        drop(stdin.take());
                        let status = child.wait().await?;
                        send_exit_and_close(&outbound, &session_id, status.code().unwrap_or(-1)).await;
                        return Ok(());
                    }
                    None => {
                        drop(stdin.take());
                    }
                }
            }
        }
    }
}

fn pipe_async_reader<R>(
    session_id: String,
    typ: u8,
    outbound: &mpsc::Sender<OutboundEvent>,
    mut reader: R,
    decode_terminal_output: bool,
) where
    R: AsyncRead + Unpin + Send + 'static,
{
    let outbound = outbound.clone();
    tokio::spawn(async move {
        let mut buf = vec![0u8; 32 * 1024];
        loop {
            match reader.read(&mut buf).await {
                Ok(0) => break,
                Ok(n) => {
                    if outbound
                        .send(OutboundEvent::Binary {
                            typ,
                            id: session_id.clone(),
                            payload: terminal_output_payload(&buf[..n], decode_terminal_output),
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
    });
}

fn terminal_output_payload(bytes: &[u8], decode_terminal_output: bool) -> Vec<u8> {
    if !decode_terminal_output {
        return bytes.to_vec();
    }
    decode_terminal_output_bytes(bytes).into_bytes()
}

fn decode_terminal_output_bytes(bytes: &[u8]) -> String {
    if let Ok(output) = std::str::from_utf8(bytes) {
        return output.to_string();
    }
    decode_windows_oem_output(bytes)
}

#[cfg(windows)]
fn decode_windows_oem_output(bytes: &[u8]) -> String {
    use windows_sys::Win32::Globalization::{GetOEMCP, MultiByteToWideChar, MB_ERR_INVALID_CHARS};

    if bytes.is_empty() {
        return String::new();
    }
    unsafe {
        let code_page = GetOEMCP();
        let src_len = bytes.len().min(i32::MAX as usize) as i32;
        let src = bytes.as_ptr();
        let mut flags = MB_ERR_INVALID_CHARS;
        let mut wide_len =
            MultiByteToWideChar(code_page, flags, src, src_len, std::ptr::null_mut(), 0);
        if wide_len <= 0 {
            flags = 0;
            wide_len = MultiByteToWideChar(code_page, flags, src, src_len, std::ptr::null_mut(), 0);
        }
        if wide_len <= 0 {
            return String::from_utf8_lossy(bytes).to_string();
        }
        let mut wide = vec![0u16; wide_len as usize];
        let written =
            MultiByteToWideChar(code_page, flags, src, src_len, wide.as_mut_ptr(), wide_len);
        if written <= 0 {
            return String::from_utf8_lossy(bytes).to_string();
        }
        String::from_utf16_lossy(&wide[..written as usize])
    }
}

#[cfg(not(windows))]
fn decode_windows_oem_output(bytes: &[u8]) -> String {
    String::from_utf8_lossy(bytes).to_string()
}

async fn run_sftp(
    msg: Message,
    rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    let session_id = msg.session_id.clone();
    run_sftp_session(msg.session_id, rx, outbound.clone()).await?;
    send_exit_and_close(&outbound, &session_id, 0).await;
    Ok(())
}

async fn run_pty(
    msg: Message,
    shell: Option<String>,
    rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    run_pty_platform(msg, shell, rx, outbound).await
}

#[cfg(windows)]
async fn run_pty_platform(
    msg: Message,
    shell: Option<String>,
    rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    let shell_for_fallback = shell.clone();
    if crate::winpty::is_legacy_windows() {
        match run_winpty_pty(msg.clone(), shell, rx, outbound.clone()).await {
            Ok(()) => Ok(()),
            Err((msg, rx, err)) => {
                warn!("WinPTY terminal start failed, falling back to pipe shell: {err:#}");
                run_exec(msg, shell_for_fallback, rx, outbound).await
            }
        }
    } else {
        match run_conpty_pty(msg.clone(), shell, rx, outbound.clone()).await {
            Ok(()) => Ok(()),
            Err((msg, rx, err)) => {
                warn!("ConPTY terminal start failed, falling back to pipe shell: {err:#}");
                run_exec(msg, shell_for_fallback, rx, outbound).await
            }
        }
    }
}

#[cfg(windows)]
async fn run_conpty_pty(
    msg: Message,
    shell: Option<String>,
    mut rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> std::result::Result<(), (Message, mpsc::Receiver<SessionInput>, anyhow::Error)> {
    use portable_pty::{native_pty_system, CommandBuilder, PtySize};

    let session_id = msg.session_id.clone();
    let result = (|| -> Result<_> {
        let pty_system = native_pty_system();
        let pair = pty_system.openpty(PtySize {
            rows: nonzero(msg.rows, 24),
            cols: nonzero(msg.cols, 80),
            pixel_width: 0,
            pixel_height: 0,
        })?;
        let mut builder = if msg.command.is_empty() {
            CommandBuilder::new(default_shell(shell))
        } else {
            let mut b = CommandBuilder::new(default_shell(shell));
            b.arg("/c");
            b.arg(&msg.command);
            b
        };
        for (k, v) in parse_env(&msg.env) {
            builder.env(k, v);
        }
        if !msg.term.is_empty() {
            builder.env("TERM", msg.term.clone());
        }

        let child = pair.slave.spawn_command(builder)?;
        drop(pair.slave);
        let reader = pair.master.try_clone_reader()?;
        let writer = pair.master.take_writer()?;
        Ok((pair.master, child, writer, reader))
    })();

    let (master, mut child, mut writer, reader) = match result {
        Ok(parts) => parts,
        Err(err) => return Err((msg, rx, err)),
    };
    tracing::info!("Windows PTY started via ConPTY/portable-pty");
    pipe_blocking_reader(session_id.clone(), BIN_DATA, &outbound, reader);

    let mut tick = tokio::time::interval(std::time::Duration::from_millis(100));
    loop {
        tokio::select! {
            _ = tick.tick() => {
                match child.try_wait() {
                    Ok(Some(status)) => {
                        send_exit_and_close(&outbound, &session_id, status.exit_code() as i32).await;
                        return Ok(());
                    }
                    Ok(None) => {}
                    Err(err) => {
                        warn!("ConPTY exit status failed: {err:#}");
                        send_exit_and_close(&outbound, &session_id, -1).await;
                        return Ok(());
                    }
                }
            }
            input = rx.recv() => {
                match input {
                    Some(SessionInput::Data(data)) => {
                        let _ = writer.write_all(&data);
                        let _ = writer.flush();
                    }
                    Some(SessionInput::StdinClose) => {}
                    Some(SessionInput::Close) => {
                        let _ = child.kill();
                        send_exit_and_close(&outbound, &session_id, -1).await;
                        return Ok(());
                    }
                    Some(SessionInput::Resize { rows, cols }) => {
                        let _ = master.resize(PtySize {
                            rows: nonzero(rows, 24),
                            cols: nonzero(cols, 80),
                            pixel_width: 0,
                            pixel_height: 0,
                        });
                    }
                    None => return Ok(()),
                }
            }
        }
    }
}

#[cfg(windows)]
async fn run_winpty_pty(
    msg: Message,
    shell: Option<String>,
    mut rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> std::result::Result<(), (Message, mpsc::Receiver<SessionInput>, anyhow::Error)> {
    let session_id = msg.session_id.clone();
    let shell = default_shell(shell);
    let command = if msg.command.is_empty() {
        None
    } else {
        Some(msg.command.clone())
    };
    let pty = match crate::winpty::spawn(
        &shell,
        command.as_deref(),
        nonzero(msg.cols, 80) as i32,
        nonzero(msg.rows, 24) as i32,
    ) {
        Ok(pty) => pty,
        Err(err) => return Err((msg, rx, err)),
    };
    tracing::info!("Windows PTY started via winpty");

    let pty = Arc::new(pty);
    let output = match pty.take_output() {
        Some(output) => output,
        None => return Err((msg, rx, anyhow!("winpty output pipe unavailable"))),
    };
    pipe_blocking_reader(session_id.clone(), BIN_DATA, &outbound, output);

    let mut tick = tokio::time::interval(std::time::Duration::from_millis(100));
    loop {
        tokio::select! {
            _ = tick.tick() => {
                match pty.exit_status() {
                    Ok(Some(code)) => {
                        send_exit_and_close(&outbound, &session_id, code).await;
                        return Ok(());
                    }
                    Ok(None) => {}
                    Err(err) => {
                        warn!("winpty exit status failed: {err:#}");
                        send_exit_and_close(&outbound, &session_id, -1).await;
                        return Ok(());
                    }
                }
            }
            input = rx.recv() => {
                match input {
                    Some(SessionInput::Data(data)) => {
                        let _ = pty.write(&data);
                    }
                    Some(SessionInput::StdinClose) => {}
                    Some(SessionInput::Close) => {
                        pty.terminate();
                        send_exit_and_close(&outbound, &session_id, -1).await;
                        return Ok(());
                    }
                    Some(SessionInput::Resize { rows, cols }) => {
                        let _ = pty.resize(nonzero(cols, 80) as i32, nonzero(rows, 24) as i32);
                    }
                    None => return Ok(()),
                }
            }
        }
    }
}

#[cfg(windows)]
fn pipe_blocking_reader<R>(
    session_id: String,
    typ: u8,
    outbound: &mpsc::Sender<OutboundEvent>,
    mut reader: R,
) where
    R: std::io::Read + Send + 'static,
{
    let outbound = outbound.clone();
    std::thread::spawn(move || {
        let mut buf = vec![0u8; 32 * 1024];
        loop {
            match reader.read(&mut buf) {
                Ok(0) => break,
                Ok(n) => {
                    if outbound
                        .blocking_send(OutboundEvent::Binary {
                            typ,
                            id: session_id.clone(),
                            payload: buf[..n].to_vec(),
                        })
                        .is_err()
                    {
                        break;
                    }
                }
                Err(_) => break,
            }
        }
    });
}

#[cfg(not(windows))]
async fn run_pty_platform(
    msg: Message,
    shell: Option<String>,
    mut rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    let session_id = msg.session_id.clone();
    let (control_tx, control_rx) = std::sync::mpsc::channel::<SessionInput>();
    let (done_tx, done_rx) = tokio::sync::oneshot::channel::<i32>();
    let outbound_for_thread = outbound.clone();

    thread::spawn(move || {
        let code =
            run_pty_blocking(msg, shell, control_rx, outbound_for_thread).unwrap_or_else(|err| {
                warn!("pty session failed: {err:#}");
                -1
            });
        let _ = done_tx.send(code);
    });

    let mut done_rx = done_rx;
    loop {
        tokio::select! {
            biased;
            code = &mut done_rx => {
                send_exit_and_close(&outbound, &session_id, code.unwrap_or(-1)).await;
                break;
            }
            input = rx.recv() => {
                match input {
                    Some(input) => {
                        let is_close = matches!(input, SessionInput::Close);
                        let _ = control_tx.send(input);
                        if is_close { break; }
                    }
                    None => break,
                }
            }
        }
    }
    Ok(())
}

#[cfg(not(windows))]
fn run_pty_blocking(
    msg: Message,
    shell: Option<String>,
    control_rx: std::sync::mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> Result<i32> {
    use portable_pty::{native_pty_system, CommandBuilder, PtySize};
    let pty_system = native_pty_system();
    let pair = pty_system.openpty(PtySize {
        rows: nonzero(msg.rows, 24),
        cols: nonzero(msg.cols, 80),
        pixel_width: 0,
        pixel_height: 0,
    })?;
    let mut builder = if msg.command.is_empty() {
        CommandBuilder::new(default_shell(shell))
    } else {
        let mut b = CommandBuilder::new(default_shell(shell));
        if cfg!(windows) {
            b.arg("/c");
        } else {
            b.arg("-c");
        }
        b.arg(&msg.command);
        b
    };
    for (k, v) in parse_env(&msg.env) {
        builder.env(k, v);
    }
    if !msg.term.is_empty() {
        builder.env("TERM", msg.term);
    }

    let mut child = pair.slave.spawn_command(builder)?;
    drop(pair.slave);
    let mut reader = pair.master.try_clone_reader()?;
    let mut writer = pair.master.take_writer()?;
    let master = pair.master;
    let session_id = msg.session_id.clone();
    let outbound_read = outbound.clone();
    thread::spawn(move || {
        let mut buf = vec![0u8; 32 * 1024];
        loop {
            match reader.read(&mut buf) {
                Ok(0) => break,
                Ok(n) => {
                    if outbound_read
                        .blocking_send(OutboundEvent::Binary {
                            typ: BIN_DATA,
                            id: session_id.clone(),
                            payload: buf[..n].to_vec(),
                        })
                        .is_err()
                    {
                        break;
                    }
                }
                Err(_) => break,
            }
        }
    });

    use std::sync::mpsc::RecvTimeoutError;
    loop {
        match control_rx.recv_timeout(std::time::Duration::from_millis(50)) {
            Ok(SessionInput::Data(data)) => {
                let _ = writer.write_all(&data);
                let _ = writer.flush();
            }
            Ok(SessionInput::StdinClose) => break,
            Ok(SessionInput::Close) => {
                let _ = child.kill();
                break;
            }
            Ok(SessionInput::Resize { rows, cols }) => {
                let _ = master.resize(PtySize {
                    rows: nonzero(rows, 24),
                    cols: nonzero(cols, 80),
                    pixel_width: 0,
                    pixel_height: 0,
                });
            }
            Err(RecvTimeoutError::Timeout) => {
                if let Some(status) = child.try_wait()? {
                    return Ok(status.exit_code() as i32);
                }
            }
            Err(RecvTimeoutError::Disconnected) => break,
        }
    }
    Ok(child.wait()?.exit_code() as i32)
}

fn shell_command(shell: Option<String>, command: &str) -> Command {
    let shell = default_shell(shell);
    let mut cmd = Command::new(shell);
    if !command.is_empty() {
        if cfg!(windows) {
            cmd.arg("/c");
        } else {
            cmd.arg("-c");
        }
        cmd.arg(command);
    }
    cmd
}

fn default_shell(shell: Option<String>) -> String {
    if let Some(shell) = shell.filter(|s| !s.is_empty()) {
        return shell;
    }
    std::env::var(if cfg!(windows) { "COMSPEC" } else { "SHELL" }).unwrap_or_else(|_| {
        if cfg!(windows) {
            "cmd.exe".into()
        } else {
            "/bin/sh".into()
        }
    })
}

fn parse_env(env: &[String]) -> Vec<(String, String)> {
    env.iter()
        .filter_map(|item| {
            item.split_once('=')
                .map(|(k, v)| (k.to_string(), v.to_string()))
        })
        .collect()
}

fn nonzero(value: u16, fallback: u16) -> u16 {
    if value == 0 {
        fallback
    } else {
        value
    }
}

async fn send_exit_and_close(
    outbound: &mpsc::Sender<OutboundEvent>,
    session_id: &str,
    exit_code: i32,
) {
    let _ = outbound
        .send(OutboundEvent::Message(Message {
            ty: Some(MessageType::ExitCode),
            session_id: session_id.to_string(),
            exit_code,
            ..Default::default()
        }))
        .await;
    send_close(outbound, session_id).await;
}

async fn send_close(outbound: &mpsc::Sender<OutboundEvent>, session_id: &str) {
    let _ = outbound
        .send(OutboundEvent::Message(Message {
            ty: Some(MessageType::Close),
            session_id: session_id.to_string(),
            ..Default::default()
        }))
        .await;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_env_pairs() {
        assert_eq!(
            parse_env(&["A=1".into(), "BAD".into(), "B=two=2".into()]),
            vec![("A".into(), "1".into()), ("B".into(), "two=2".into())]
        );
    }

    #[test]
    fn default_shell_is_non_empty() {
        assert!(!default_shell(None).is_empty());
    }
}
