#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
BASE=${RDEV_GPU_SMOKE_DIR:-/tmp/rdev-client-gpu-smoke}
PASSWORD=${RDEV_GPU_SMOKE_PASSWORD:-smoke}
DEVICE_ID=${RDEV_GPU_SMOKE_ID:-rgpu-smoke}

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(('127.0.0.1', 0))
print(s.getsockname()[1])
s.close()
PY
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd go
require_cmd cargo
require_cmd curl
require_cmd ssh
require_cmd scp
require_cmd sshpass
require_cmd rsync
require_cmd python3

HTTP_PORT=${RDEV_GPU_SMOKE_HTTP_PORT:-$(free_port)}
SSH_PORT=${RDEV_GPU_SMOKE_SSH_PORT:-$(free_port)}
WEB_PORT=${RDEV_GPU_SMOKE_WEB_PORT:-$(free_port)}
FWD_PORT=${RDEV_GPU_SMOKE_FWD_PORT:-$(free_port)}
DATA="$BASE/data"
SERVER_LOG="$BASE/server.log"
CLIENT_LOG="$BASE/client.log"
KNOWN_HOSTS="$BASE/known_hosts"
SERVER_BIN="$BASE/rdev-server"
CLIENT_BIN="$ROOT/clients/rdev-client-gpu/target/release/rdev-client-gpu"

rm -rf "$BASE"
mkdir -p "$DATA"

go build -o "$SERVER_BIN" "$ROOT/cmd/rdev-server"
cargo build --release --manifest-path "$ROOT/clients/rdev-client-gpu/Cargo.toml"

"$SERVER_BIN" --http "127.0.0.1:$HTTP_PORT" --ssh "127.0.0.1:$SSH_PORT" --data "$DATA" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!
"$CLIENT_BIN" --server "ws://127.0.0.1:$HTTP_PORT" --id "$DEVICE_ID" --password "$PASSWORD" --no-desktop >"$CLIENT_LOG" 2>&1 &
CLIENT_PID=$!
HTTPD_PID=""
FWD_SSH_PID=""

cleanup() {
  if [ -n "$FWD_SSH_PID" ]; then
    kill "$FWD_SSH_PID" 2>/dev/null || true
    wait "$FWD_SSH_PID" 2>/dev/null || true
  fi
  if [ -n "$HTTPD_PID" ]; then
    kill "$HTTPD_PID" 2>/dev/null || true
    wait "$HTTPD_PID" 2>/dev/null || true
  fi
  kill "$CLIENT_PID" "$SERVER_PID" 2>/dev/null || true
  wait "$CLIENT_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
}
trap cleanup EXIT

for i in $(seq 1 80); do
  if curl -fsS "http://127.0.0.1:$HTTP_PORT/api/clients" 2>/dev/null | grep -q "$DEVICE_ID"; then
    break
  fi
  sleep 0.25
  if [ "$i" = 80 ]; then
    echo "client did not register" >&2
    tail -100 "$SERVER_LOG" >&2 || true
    tail -100 "$CLIENT_LOG" >&2 || true
    exit 1
  fi
done

SSH_OPTS=(
  -o ConnectTimeout=5
  -o PreferredAuthentications=password
  -o PubkeyAuthentication=no
  -o NumberOfPasswordPrompts=1
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile="$KNOWN_HOSTS"
  -p "$SSH_PORT"
  "$DEVICE_ID@127.0.0.1"
)

OUT=$(timeout 20 sshpass -p "$PASSWORD" ssh "${SSH_OPTS[@]}" 'printf rgpu-exec-ok')
[ "$OUT" = "rgpu-exec-ok" ] || {
  echo "bad exec output: $OUT" >&2
  exit 1
}

PTY_OUT=$(timeout 20 sshpass -p "$PASSWORD" ssh "${SSH_OPTS[@]}" -tt 'printf rgpu-pty-ok' 2>/dev/null | tr -d '\r')
[ "$PTY_OUT" = "rgpu-pty-ok" ] || {
  echo "bad pty output: $PTY_OUT" >&2
  exit 1
}

mkdir -p "$BASE/rsync-src"
printf 'hello-rsync' >"$BASE/rsync-src/file.txt"
timeout 30 sshpass -p "$PASSWORD" rsync -az --delete \
  -e "ssh -o ConnectTimeout=5 -o PreferredAuthentications=password -o PubkeyAuthentication=no -o NumberOfPasswordPrompts=1 -o StrictHostKeyChecking=no -o UserKnownHostsFile=$KNOWN_HOSTS -p $SSH_PORT" \
  "$BASE/rsync-src/" "$DEVICE_ID@127.0.0.1:$BASE/rsync-dst/"
[ "$(cat "$BASE/rsync-dst/file.txt")" = "hello-rsync" ] || {
  echo "rsync content mismatch" >&2
  exit 1
}

printf 'hello-scp' >"$BASE/scp-src.txt"
timeout 30 sshpass -p "$PASSWORD" scp -O \
  -P "$SSH_PORT" \
  -o BatchMode=no \
  -o ConnectTimeout=5 \
  -o PreferredAuthentications=password \
  -o PubkeyAuthentication=no \
  -o NumberOfPasswordPrompts=1 \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile="$KNOWN_HOSTS" \
  "$BASE/scp-src.txt" "$DEVICE_ID@127.0.0.1:$BASE/scp-remote.txt"
timeout 30 sshpass -p "$PASSWORD" scp -O \
  -P "$SSH_PORT" \
  -o BatchMode=no \
  -o ConnectTimeout=5 \
  -o PreferredAuthentications=password \
  -o PubkeyAuthentication=no \
  -o NumberOfPasswordPrompts=1 \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile="$KNOWN_HOSTS" \
  "$DEVICE_ID@127.0.0.1:$BASE/scp-remote.txt" "$BASE/scp-downloaded.txt"
[ "$(cat "$BASE/scp-downloaded.txt")" = "hello-scp" ] || {
  echo "scp content mismatch" >&2
  exit 1
}

HTTP_PORT="$HTTP_PORT" DEVICE_ID="$DEVICE_ID" PASSWORD="$PASSWORD" BASE="$BASE" python3 - <<'PY'
import base64
import hashlib
import json
import os
import secrets
import socket
import struct
import time
from pathlib import Path

BIN_FILE_UPLOAD_CHUNK = 0x20
BIN_FILE_DOWNLOAD_CHUNK = 0x22
BIN_FILE_TRANSFER_END = 0x23

host = "127.0.0.1"
port = int(os.environ["HTTP_PORT"])
device_id = os.environ["DEVICE_ID"]
password = os.environ["PASSWORD"]
base = Path(os.environ["BASE"])
remote_dir = base / "file-manager"
remote_file = remote_dir / "upload.txt"
remote_dir.mkdir(parents=True, exist_ok=True)

sock = socket.create_connection((host, port), timeout=5)
sock.settimeout(5)
key = base64.b64encode(secrets.token_bytes(16)).decode()
request = (
    f"GET /files HTTP/1.1\r\n"
    f"Host: {host}:{port}\r\n"
    "Upgrade: websocket\r\n"
    "Connection: Upgrade\r\n"
    f"Sec-WebSocket-Key: {key}\r\n"
    "Sec-WebSocket-Version: 13\r\n\r\n"
)
sock.sendall(request.encode())
header = b""
while b"\r\n\r\n" not in header:
    header += sock.recv(4096)
if not header.startswith(b"HTTP/1.1 101"):
    raise SystemExit(f"websocket upgrade failed: {header[:120]!r}")
accept = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
if f"Sec-WebSocket-Accept: {accept}".lower() not in header.decode(errors="ignore").lower():
    raise SystemExit("websocket accept mismatch")

def send_frame(opcode, payload):
    if isinstance(payload, str):
        payload = payload.encode()
    mask = secrets.token_bytes(4)
    length = len(payload)
    if length < 126:
        prefix = bytes([0x80 | opcode, 0x80 | length])
    elif length <= 0xFFFF:
        prefix = bytes([0x80 | opcode, 0x80 | 126]) + struct.pack("!H", length)
    else:
        prefix = bytes([0x80 | opcode, 0x80 | 127]) + struct.pack("!Q", length)
    masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
    sock.sendall(prefix + mask + masked)

def recv_frame():
    first = sock.recv(2)
    if len(first) < 2:
        raise EOFError("websocket closed")
    opcode = first[0] & 0x0F
    length = first[1] & 0x7F
    if length == 126:
        length = struct.unpack("!H", sock.recv(2))[0]
    elif length == 127:
        length = struct.unpack("!Q", sock.recv(8))[0]
    masked = bool(first[1] & 0x80)
    mask = sock.recv(4) if masked else b""
    payload = b""
    while len(payload) < length:
        chunk = sock.recv(length - len(payload))
        if not chunk:
            raise EOFError("websocket payload truncated")
        payload += chunk
    if masked:
        payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
    if opcode == 0x9:
        send_frame(0xA, payload)
        return recv_frame()
    if opcode == 0x8:
        raise EOFError("websocket close frame")
    return opcode, payload

def send_json(obj):
    send_frame(0x1, json.dumps(obj, separators=(",", ":")))

def recv_json(op=None, task_id=None, request_id=None):
    deadline = time.time() + 10
    while time.time() < deadline:
        opcode, payload = recv_frame()
        if opcode != 0x1:
            continue
        msg = json.loads(payload.decode())
        if op and msg.get("op") != op:
            continue
        if task_id and msg.get("taskId") != task_id:
            continue
        if request_id and msg.get("requestId") != request_id:
            continue
        return msg
    raise TimeoutError(f"timed out waiting for op={op} task={task_id} request={request_id}")

def encode_offset_frame(typ, task_id, offset, payload=b""):
    tid = task_id.encode()
    return bytes([typ, len(tid)]) + tid + struct.pack("!Q", offset) + payload

send_json({"op": "auth", "deviceId": device_id, "password": password})
assert recv_json("auth_ok")["deviceId"] == device_id

request_id = "list-smoke"
send_json({"op": "list", "deviceId": device_id, "requestId": request_id, "path": str(base / "rsync-src")})
listing = recv_json("list_result", request_id=request_id)
if "file.txt" not in {entry.get("name") for entry in listing.get("entries", [])}:
    raise SystemExit(f"file list did not include file.txt: {listing}")

upload_id = "upload-smoke"
content = b"hello-file-manager"
send_json({
    "op": "upload_start",
    "deviceId": device_id,
    "taskId": upload_id,
    "parentPath": str(remote_dir),
    "name": remote_file.name,
    "size": len(content),
})
ready = recv_json("upload_ready", task_id=upload_id)
if ready.get("offset", 0) != 0:
    raise SystemExit(f"unexpected upload offset: {ready}")
send_frame(0x2, encode_offset_frame(BIN_FILE_UPLOAD_CHUNK, upload_id, 0, content))
send_json({"op": "upload_end", "taskId": upload_id, "path": ready.get("path"), "size": len(content)})
end = recv_json("transfer_end", task_id=upload_id)
if not end.get("success") or remote_file.read_bytes() != content:
    raise SystemExit(f"upload failed: {end}")

download_id = "download-smoke"
send_json({"op": "download_start", "deviceId": device_id, "taskId": download_id, "path": str(remote_file)})
start = recv_json("download_start", task_id=download_id)
if start.get("size") != len(content):
    raise SystemExit(f"bad download metadata: {start}")
downloaded = bytearray()
while True:
    opcode, payload = recv_frame()
    if opcode != 0x2 or len(payload) < 10:
        continue
    typ = payload[0]
    id_len = payload[1]
    tid = payload[2:2 + id_len].decode()
    if tid != download_id:
        continue
    offset = struct.unpack("!Q", payload[2 + id_len:10 + id_len])[0]
    body = payload[10 + id_len:]
    if typ == BIN_FILE_DOWNLOAD_CHUNK:
        if offset != len(downloaded):
            raise SystemExit(f"download offset mismatch: got {offset}, want {len(downloaded)}")
        downloaded.extend(body)
    elif typ == BIN_FILE_TRANSFER_END:
        if offset != len(content) or bytes(downloaded) != content:
            raise SystemExit("download content mismatch")
        break

send_frame(0x8, b"")
sock.close()
PY

if command -v sftp >/dev/null 2>&1; then
  printf 'hello-sftp' >"$BASE/sftp-src.txt"
  timeout 30 sh -c 'printf "%s\n" "put $1 $2" "get $2 $3" bye | sshpass -p "$4" sftp \
    -P "$5" \
    -o BatchMode=no \
    -o ConnectTimeout=5 \
    -o PreferredAuthentications=password \
    -o PubkeyAuthentication=no \
    -o NumberOfPasswordPrompts=1 \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile="$6" \
    "$7"' sh \
    "$BASE/sftp-src.txt" \
    "$BASE/sftp-remote.txt" \
    "$BASE/sftp-downloaded.txt" \
    "$PASSWORD" \
    "$SSH_PORT" \
    "$KNOWN_HOSTS" \
    "$DEVICE_ID@127.0.0.1"
  [ "$(cat "$BASE/sftp-downloaded.txt")" = "hello-sftp" ] || {
    echo "sftp content mismatch" >&2
    exit 1
  }
else
  echo "sftp smoke skipped: sftp client not found"
fi

python3 -m http.server "$WEB_PORT" --bind 127.0.0.1 --directory "$BASE" >"$BASE/http.log" 2>&1 &
HTTPD_PID=$!
sshpass -p "$PASSWORD" ssh -N -L "127.0.0.1:$FWD_PORT:127.0.0.1:$WEB_PORT" "${SSH_OPTS[@]}" >"$BASE/forward.log" 2>&1 &
FWD_SSH_PID=$!
for _ in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:$FWD_PORT/rsync-src/file.txt" 2>/dev/null | grep -q hello-rsync; then
    echo "rdev-client-gpu smoke ok"
    exit 0
  fi
  sleep 0.25
done

echo "port forward smoke failed" >&2
tail -100 "$SERVER_LOG" >&2 || true
tail -100 "$CLIENT_LOG" >&2 || true
exit 1
