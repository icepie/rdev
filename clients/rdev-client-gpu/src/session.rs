use anyhow::{anyhow, Context, Result};
use std::{
    collections::HashMap,
    io::{Read, Write},
    process::Stdio,
    sync::{Arc, Mutex},
    thread,
};
use tokio::{
    io::{AsyncRead, AsyncReadExt, AsyncWriteExt},
    process::Command,
    sync::mpsc,
};
use tracing::warn;

use crate::protocol::{Message, MessageType, BIN_DATA, BIN_STDERR};

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

    pipe_async_reader(session_id.clone(), BIN_DATA, &outbound, stdout);
    pipe_async_reader(session_id.clone(), BIN_STDERR, &outbound, stderr);

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
    });
}

async fn run_sftp(
    msg: Message,
    rx: mpsc::Receiver<SessionInput>,
    outbound: mpsc::Sender<OutboundEvent>,
) -> Result<()> {
    let server = find_sftp_server().ok_or_else(|| {
        anyhow!("sftp subsystem requires sftp-server in PATH or a common OpenSSH path")
    })?;
    let mut command = Message {
        command: server,
        ..msg
    };
    command.subsystem.clear();
    run_exec(command, None, rx, outbound).await
}

async fn run_pty(
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

fn find_sftp_server() -> Option<String> {
    let mut candidates = Vec::new();
    if cfg!(windows) {
        candidates.push("sftp-server.exe".to_string());
    } else {
        candidates.extend(
            [
                "/usr/lib/openssh/sftp-server",
                "/usr/lib/ssh/sftp-server",
                "/usr/libexec/openssh/sftp-server",
                "sftp-server",
            ]
            .iter()
            .map(|s| s.to_string()),
        );
    }
    candidates.into_iter().find(|path| which_like(path))
}

fn which_like(path: &str) -> bool {
    if path.contains(std::path::MAIN_SEPARATOR) {
        return std::path::Path::new(path).is_file();
    }
    std::env::var_os("PATH")
        .and_then(|paths| std::env::split_paths(&paths).find(|dir| dir.join(path).is_file()))
        .is_some()
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
