#!/usr/bin/env bash
# Android SFTP smoke test.
#
# Runs the flow inside a uniquely named directory under RDEV_SFTP_REMOTE_DIR, so a
# wrong remote path fails the run instead of silently exercising the login
# directory. Needs sshpass and OpenSSH sftp.
set -euo pipefail

HOST=${RDEV_SFTP_HOST:-127.0.0.1}
PORT=${RDEV_SFTP_PORT:-2222}
DEVICE=${RDEV_SFTP_DEVICE:-PDA3109}
PASSWORD=${RDEV_SFTP_PASSWORD:-x}
REMOTE_DIR=${RDEV_SFTP_REMOTE_DIR:-.}
LIMIT=${RDEV_SFTP_TIMEOUT:-120}

case "$LIMIT" in
  '' | *[!0-9]*) echo "RDEV_SFTP_TIMEOUT must be a whole number of seconds" >&2; exit 2 ;;
esac

SMOKE_DIR="rdev-sftp-smoke-$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
TMP=${TMPDIR:-/tmp}/rdev-android-sftp-smoke-$$
mkdir -p "$TMP"

SFTP_OPTS=(
  -P "$PORT"
  -o PreferredAuthentications=password,keyboard-interactive
  -o PubkeyAuthentication=no
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null
  -o ConnectTimeout=20
)

remote_touched=
cleanup() {
  local status=$?
  trap - EXIT
  if [ -n "$remote_touched" ]; then
    # Path-scoped best effort: a failed cd cannot make cleanup target another directory.
    cat >"$TMP/cleanup.sftp" <<EOF
-rm "$REMOTE_DIR/$SMOKE_DIR/put.txt"
-rm "$REMOTE_DIR/$SMOKE_DIR/renamed.txt"
-rmdir "$REMOTE_DIR/$SMOKE_DIR"
quit
EOF
    timeout "$LIMIT" sshpass -p "$PASSWORD" sftp "${SFTP_OPTS[@]}" \
      -b "$TMP/cleanup.sftp" "$DEVICE@$HOST" >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP"
  exit "$status"
}
trap cleanup EXIT

LOCAL_PUT="$TMP/rdev-sftp-put.txt"
LOCAL_GET="$TMP/rdev-sftp-get.txt"
BATCH="$TMP/batch.sftp"
LOG="$TMP/sftp.log"
printf 'RDEV_SFTP_OK %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$LOCAL_PUT"

# sftp batch mode aborts on the first failing command, so an unusable configured
# directory fails the run here instead of falling back to the login directory.
cat >"$BATCH" <<EOF
cd "$REMOTE_DIR"
pwd
mkdir "$SMOKE_DIR"
cd "$SMOKE_DIR"
pwd
put "$LOCAL_PUT" put.txt
ls
get put.txt "$LOCAL_GET"
rename put.txt renamed.txt
ls
rm renamed.txt
cd ..
rmdir "$SMOKE_DIR"
quit
EOF

remote_touched=1
if ! timeout "$LIMIT" sshpass -p "$PASSWORD" sftp "${SFTP_OPTS[@]}" \
  -b "$BATCH" "$DEVICE@$HOST" >"$LOG" 2>&1; then
  echo "android sftp smoke failed: sftp did not finish within ${LIMIT}s or reported an error" >&2
  sed 's/^/  /' "$LOG" >&2 || true
  exit 1
fi
remote_touched=

mapfile -t PWD_LINES < <(awk -F'Remote working directory: ' 'NF > 1 { print $2 }' "$LOG")
if [ "${#PWD_LINES[@]}" -lt 2 ]; then
  echo "android sftp smoke failed: sftp did not report the remote working directory" >&2
  sed 's/^/  /' "$LOG" >&2 || true
  exit 1
fi

strip_trailing_slash() {
  local path=$1
  while [ "${#path}" -gt 1 ] && [ "${path%/}" != "$path" ]; do
    path=${path%/}
  done
  printf '%s' "$path"
}

REMOTE_BASE=$(strip_trailing_slash "${PWD_LINES[0]}")
REMOTE_INNER=$(strip_trailing_slash "${PWD_LINES[1]}")
CONFIGURED=$(strip_trailing_slash "${REMOTE_DIR#./}")

case "$REMOTE_BASE" in
  /) EXPECTED_INNER="/$SMOKE_DIR" ;;
  *) EXPECTED_INNER="$REMOTE_BASE/$SMOKE_DIR" ;;
esac

if [ -z "$REMOTE_BASE" ] || [ "$REMOTE_INNER" != "$EXPECTED_INNER" ]; then
  echo "android sftp smoke failed: ran in '$REMOTE_INNER' instead of '$EXPECTED_INNER'" >&2
  exit 1
fi

if [ "$CONFIGURED" != "." ]; then
  case "$REMOTE_BASE" in
    "$CONFIGURED" | */"$CONFIGURED") ;;
    *)
      echo "android sftp smoke failed: RDEV_SFTP_REMOTE_DIR='$REMOTE_DIR' resolved to '$REMOTE_BASE'" >&2
      exit 1
      ;;
  esac
fi

cmp "$LOCAL_PUT" "$LOCAL_GET"
echo "android sftp smoke ok: $DEVICE@$HOST:$PORT remote=$REMOTE_BASE dir=$SMOKE_DIR timeout=${LIMIT}s"
