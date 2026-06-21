#!/usr/bin/env bash
set -euo pipefail

HOST=${RDEV_SFTP_HOST:-127.0.0.1}
PORT=${RDEV_SFTP_PORT:-2222}
DEVICE=${RDEV_SFTP_DEVICE:-PDA3109}
PASSWORD=${RDEV_SFTP_PASSWORD:-x}
REMOTE_DIR=${RDEV_SFTP_REMOTE_DIR:-.}
TMP=${TMPDIR:-/tmp}/rdev-android-sftp-smoke-$$
mkdir -p "$TMP"
trap 'rm -rf "$TMP"' EXIT

LOCAL_PUT="$TMP/rdev-sftp-put.txt"
LOCAL_GET="$TMP/rdev-sftp-get.txt"
BATCH="$TMP/batch.sftp"
printf 'RDEV_SFTP_OK %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$LOCAL_PUT"

cat >"$BATCH" <<EOF
pwd
ls
mkdir rdev-sftp-smoke
cd rdev-sftp-smoke
put $LOCAL_PUT put.txt
ls
get put.txt $LOCAL_GET
rename put.txt renamed.txt
ls
rm renamed.txt
cd ..
rmdir rdev-sftp-smoke
quit
EOF

sshpass -p "$PASSWORD" sftp \
  -P "$PORT" \
  -o PreferredAuthentications=password,keyboard-interactive \
  -o PubkeyAuthentication=no \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null \
  -o ConnectTimeout=20 \
  -b "$BATCH" \
  "$DEVICE@$HOST"

cmp "$LOCAL_PUT" "$LOCAL_GET"
echo "android sftp smoke ok: $DEVICE@$HOST:$PORT $REMOTE_DIR"
