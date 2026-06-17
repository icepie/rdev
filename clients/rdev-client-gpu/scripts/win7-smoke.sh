#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
HOST=${RDEV_GPU_WIN7_HOST:?set RDEV_GPU_WIN7_HOST, for example 192.168.1.166}
USER=${RDEV_GPU_WIN7_USER:-Administrator}
PORT=${RDEV_GPU_WIN7_PORT:-2222}
PASSWORD=${RDEV_GPU_WIN7_PASSWORD:?set RDEV_GPU_WIN7_PASSWORD}
REMOTE_DIR=${RDEV_GPU_WIN7_REMOTE_DIR:-C:/Windows/Temp/rdev-gpu-win7}
REMOTE_DIR_WIN=${REMOTE_DIR//\//\\}
DEVICE_ID=${RDEV_GPU_WIN7_DEVICE_ID:-win7-rgpu-smoke}
DEVICE_PASSWORD=${RDEV_GPU_WIN7_DEVICE_PASSWORD:-smoke}
BASE=${RDEV_GPU_WIN7_SMOKE_DIR:-/data/tmp/rdev-client-gpu-win7-smoke}
SERVER_HOST=${RDEV_GPU_WIN7_SERVER_HOST:-}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

free_port() {
  python3 - <<'PY'
import socket
sock = socket.socket()
sock.bind(('0.0.0.0', 0))
print(sock.getsockname()[1])
sock.close()
PY
}

local_source_ip() {
  if [ -n "$SERVER_HOST" ]; then
    printf '%s\n' "$SERVER_HOST"
    return
  fi
  ip route get "$HOST" 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}'
}

strip_ansi() {
  python3 -c 'import re,sys; data=sys.stdin.buffer.read(); data=re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", data); sys.stdout.write(data.decode(errors="ignore").strip())'
}

require_cmd go
require_cmd cargo
require_cmd curl
require_cmd ssh
require_cmd scp
require_cmd sshpass
require_cmd python3
require_cmd ip

SERVER_HOST=$(local_source_ip)
if [ -z "$SERVER_HOST" ]; then
  echo "failed to detect local source IP; set RDEV_GPU_WIN7_SERVER_HOST" >&2
  exit 1
fi

HTTP_PORT=${RDEV_GPU_WIN7_HTTP_PORT:-$(free_port)}
SSH_PORT=${RDEV_GPU_WIN7_SSH_PORT:-$(free_port)}
SERVER_BIN="$BASE/rdev-server"
SERVER_LOG="$BASE/server.log"
CLIENT_LOG="$BASE/client.log.remote"
KNOWN_HOSTS="$BASE/known_hosts"
REMOTE="$USER@$HOST"
SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=12 -p "$PORT")
CLIENT_SSH_OPTS=(
  -o ConnectTimeout=5
  -o PreferredAuthentications=password
  -o PubkeyAuthentication=no
  -o NumberOfPasswordPrompts=1
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile="$KNOWN_HOSTS"
  -p "$SSH_PORT"
  "$DEVICE_ID@127.0.0.1"
)

rm -rf "$BASE"
mkdir -p "$BASE/data"

cleanup() {
  kill "${REMOTE_SSH_PID:-}" "${SERVER_PID:-}" 2>/dev/null || true
  wait "${REMOTE_SSH_PID:-}" "${SERVER_PID:-}" 2>/dev/null || true
  sshpass -p "$PASSWORD" ssh "${SSH_OPTS[@]}" "$REMOTE" "C:\\Windows\\System32\\taskkill.exe /F /IM rdev-client-gpu.exe" >/dev/null 2>&1 || true
}
trap cleanup EXIT

make -C "$ROOT" rust-client-gpu-win7-package
go build -o "$SERVER_BIN" "$ROOT/cmd/rdev-server"

sshpass -p "$PASSWORD" ssh "${SSH_OPTS[@]}" "$REMOTE" "C:\\Windows\\System32\\cmd.exe /c \"if not exist $REMOTE_DIR_WIN mkdir $REMOTE_DIR_WIN & del /q $REMOTE_DIR_WIN\\* 2>nul\"" >/dev/null
sshpass -p "$PASSWORD" scp -P "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$ROOT"/clients/rdev-client-gpu/target/win7-dist/* "$REMOTE:$REMOTE_DIR/" >/dev/null

"$SERVER_BIN" --http "0.0.0.0:$HTTP_PORT" --ssh "127.0.0.1:$SSH_PORT" --data "$BASE/data" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

sshpass -p "$PASSWORD" ssh "${SSH_OPTS[@]}" "$REMOTE" \
  "C:\\Windows\\System32\\cmd.exe /c \"cd /d $REMOTE_DIR_WIN && set RUST_LOG=info && $REMOTE_DIR_WIN\\rdev-client-gpu.exe --server ws://$SERVER_HOST:$HTTP_PORT --id $DEVICE_ID --password $DEVICE_PASSWORD --no-desktop --reconnect-delay 1s > $REMOTE_DIR_WIN\\client.log 2>&1\"" \
  >/dev/null 2>&1 &
REMOTE_SSH_PID=$!

for i in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$HTTP_PORT/api/clients" 2>/dev/null | grep -q "$DEVICE_ID"; then
    break
  fi
  sleep 0.25
  if [ "$i" = 100 ]; then
    echo "Win7 Rust client did not register" >&2
    tail -100 "$SERVER_LOG" >&2 || true
    sshpass -p "$PASSWORD" ssh "${SSH_OPTS[@]}" "$REMOTE" "C:\\Windows\\System32\\cmd.exe /c type $REMOTE_DIR_WIN\\client.log" >&2 || true
    exit 1
  fi
done

EXEC_OUT=$(timeout 25 sshpass -p "$DEVICE_PASSWORD" ssh "${CLIENT_SSH_OPTS[@]}" 'echo win7-exec-ok' | tr -d '\r' | strip_ansi)
if [ "$EXEC_OUT" != "win7-exec-ok" ]; then
  echo "bad Win7 exec output: $EXEC_OUT" >&2
  exit 1
fi

PTY_OUT=$(timeout 25 sshpass -p "$DEVICE_PASSWORD" ssh -tt "${CLIENT_SSH_OPTS[@]}" 'echo win7-pty-ok' 2>/dev/null | tr -d '\r' | strip_ansi)
if [ "$PTY_OUT" != "win7-pty-ok" ]; then
  echo "bad Win7 PTY output: $PTY_OUT" >&2
  exit 1
fi

LONG_PTY_OUT="$BASE/long-pty.out"
(timeout 25 sshpass -p "$DEVICE_PASSWORD" ssh -tt "${CLIENT_SSH_OPTS[@]}" 'C:\Windows\System32\ping.exe -n 5 127.0.0.1 > nul & echo win7-winpty-agent-ok' >"$LONG_PTY_OUT" 2>/dev/null) &
LONG_PTY_PID=$!
sleep 1
if ! sshpass -p "$PASSWORD" ssh "${SSH_OPTS[@]}" "$REMOTE" "C:\\Windows\\System32\\tasklist.exe" 2>/dev/null | grep -qi 'winpty-agent.exe'; then
  echo "WinPTY agent was not observed during Win7 PTY session" >&2
  wait "$LONG_PTY_PID" 2>/dev/null || true
  exit 1
fi
wait "$LONG_PTY_PID"
LONG_PTY_TEXT=$(tr -d '\r' <"$LONG_PTY_OUT" | strip_ansi)
if ! grep -q 'win7-winpty-agent-ok' <<<"$LONG_PTY_TEXT"; then
  echo "bad Win7 long PTY output: $LONG_PTY_TEXT" >&2
  exit 1
fi

sshpass -p "$PASSWORD" ssh "${SSH_OPTS[@]}" "$REMOTE" "C:\\Windows\\System32\\cmd.exe /c type $REMOTE_DIR_WIN\\client.log" >"$CLIENT_LOG" 2>/dev/null || true
if grep -q "WinPTY terminal start failed" "$CLIENT_LOG"; then
  echo "WinPTY failed and pipe fallback was used" >&2
  tail -80 "$CLIENT_LOG" >&2
  exit 1
fi

echo "rdev-client-gpu Win7 smoke ok"
