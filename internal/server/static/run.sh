#!/bin/sh
# rdev-client one-click runner
# Download → run, no install needed
# Compatible with: POSIX sh, bash, dash, ash, zsh, ksh, busybox sh
#
# Usage:
#   curl -sL http://SERVER:PORT/run.sh | sh -s -- ws://SERVER:PORT
#   wget -qO- http://SERVER:PORT/run.sh | sh -s -- ws://SERVER:PORT

set -e

# ── Defaults ────────────────────────────────────────────────
RDEV_SERVER=""
RDEV_ID=""
RDEV_PASSWORD=""
RDEV_SHELL=""
RDEV_SSH_PORT=""
RDEV_VERSION=""
RDEV_REPO="icepie/rdev"

# CN GitHub mirrors (tried first, fallback to direct)
MIRRORS="gh.llkk.cc gh.idayer.com gh.ddlc.top gh-proxy.com ghfast.top ghproxy.net"

# ── Parse arguments ─────────────────────────────────────────
while [ $# -gt 0 ]; do
    case "$1" in
        -s|--server)   RDEV_SERVER="$2"; shift 2 ;;
        -i|--id)       RDEV_ID="$2"; shift 2 ;;
        -p|--password) RDEV_PASSWORD="$2"; shift 2 ;;
        -S|--shell)    RDEV_SHELL="$2"; shift 2 ;;
        --ssh-port)    RDEV_SSH_PORT="$2"; shift 2 ;;
        -v|--version)  RDEV_VERSION="$2"; shift 2 ;;
        --no-mirror)   MIRRORS=""; shift ;;
        -h|--help)
            echo "Usage: sh run.sh SERVER_URL [options]"
            echo ""
            echo "  Downloads rdev-client to /tmp and runs it directly."
            echo "  No installation or root required."
            echo ""
            echo "Options:"
            echo "  -s, --server URL     Server WebSocket URL"
            echo "  -i, --id ID          Device ID (default: hostname)"
            echo "  -p, --password PW   Password for SSH auth"
            echo "  -S, --shell PATH    Shell path (e.g. /bin/bash)"
            echo "  --ssh-port PORT      Server SSH port (for hint display)"
            echo "  -v, --version VER    Client version (default: latest)"
            echo "  --no-mirror          Skip CN mirrors, use github.com"
            echo ""
            echo "Examples:"
            echo "  curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:8080"
            echo "  curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:8080 -i my-pc -p secret"
            exit 0 ;;
        ws://*|wss://*) RDEV_SERVER="$1"; shift ;;
        http://*|https://*) RDEV_SERVER="$1"; shift ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

if [ -z "$RDEV_SERVER" ]; then
    echo "Error: server URL required" >&2
    echo "Usage: curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:PORT" >&2
    exit 1
fi

# ── Optional elevation prompt ───────────────────────────────
RDEV_ELEVATE=0

wait_elevation_key() {
    [ -r /dev/tty ] || return 1
    printf '%s' "  Not running as root. Press any key within 3 seconds to run elevated; waiting continues normal mode... " >/dev/tty
    old_stty="$(stty -g </dev/tty 2>/dev/null || true)"
    if [ -n "$old_stty" ]; then
        stty raw -echo min 0 time 30 </dev/tty 2>/dev/null || true
        key_file="${TMPDIR:-/tmp}/rdev-key-$$"
        dd bs=1 count=1 of="$key_file" 2>/dev/null </dev/tty || true
        stty "$old_stty" </dev/tty 2>/dev/null || true
        printf '\n' >/dev/tty
        if [ -s "$key_file" ]; then
            rm -f "$key_file" 2>/dev/null
            return 0
        fi
        rm -f "$key_file" 2>/dev/null
        return 1
    fi
    if (IFS= read -r -t 0 _ </dev/tty) 2>/dev/null; then
        if IFS= read -r -t 3 _ </dev/tty; then
            printf '\n' >/dev/tty
            return 0
        fi
        printf '\n' >/dev/tty
    fi
    return 1
}

if [ "$(id -u 2>/dev/null || echo 1)" != "0" ]; then
    if wait_elevation_key; then
        RDEV_ELEVATE=1
        echo "  Elevation requested; will start rdev-client with sudo/doas after download." >&2
    else
        echo "  Continuing in normal user mode." >&2
    fi
fi

# ── Detect OS / Arch ────────────────────────────────────────
OS="$(uname -s 2>/dev/null || echo unknown)"
ARCH="$(uname -m 2>/dev/null || echo unknown)"

case "$OS" in
    Linux*)   OS="linux" ;;
    Darwin*)  OS="darwin" ;;
    FreeBSD*) OS="freebsd" ;;
    OpenBSD*) OS="openbsd" ;;
    NetBSD*)  OS="netbsd" ;;
    MINGW*|MSYS*|CYGWIN*) OS="windows" ;;
    *)        echo "Error: unsupported OS: $OS" >&2; exit 1 ;;
esac

case "$ARCH" in
    x86_64|amd64|x64)       ARCH="amd64" ;;
    aarch64|arm64|armv8l)    ARCH="arm64" ;;
    armv7l|armv7|armhf)      ARCH="armv7" ;;
    armv6l|armv6)            ARCH="armv6" ;;
    i386|i686|x86)           ARCH="386" ;;
    *) echo "Error: unsupported arch: $ARCH" >&2; exit 1 ;;
esac

# ── Detect download tool ────────────────────────────────────
DL_TOOL=""
if command -v curl >/dev/null 2>&1; then DL_TOOL="curl"
elif command -v wget >/dev/null 2>&1; then DL_TOOL="wget"
elif command -v fetch >/dev/null 2>&1; then DL_TOOL="fetch"
elif command -v busybox >/dev/null 2>&1 && busybox --list 2>/dev/null | grep -q wget; then DL_TOOL="busybox_wget"
else echo "Error: need curl, wget, or fetch" >&2; exit 1
fi

dl() {
    case "$DL_TOOL" in
        curl)         curl -fsSL --connect-timeout 10 --max-time 120 "$1" -o "$2" ;;
        wget)         wget -q --timeout=120 -O "$2" "$1" ;;
        fetch)        fetch -o "$2" "$1" ;;
        busybox_wget) busybox wget -O "$2" "$1" ;;
    esac
}

# ── Determine version & URL ────────────────────────────────
if [ -n "$RDEV_VERSION" ]; then
    TAG="v${RDEV_VERSION}"
else
    TAG=""
    if [ "$DL_TOOL" = "curl" ]; then
        TAG="$(curl -fsSL --connect-timeout 5 --max-time 10 \
            "https://api.github.com/repos/${RDEV_REPO}/releases/latest" 2>/dev/null \
            | grep '"tag_name"' | head -1 \
            | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')"
    fi
    [ -z "$TAG" ] && TAG="latest"
fi

BINARY="rdev-client-${OS}-${ARCH}"
[ "$OS" = "windows" ] && BINARY="${BINARY}.exe"

if [ "$TAG" = "latest" ]; then
    GH_URL="https://github.com/${RDEV_REPO}/releases/latest/download/${BINARY}"
else
    GH_URL="https://github.com/${RDEV_REPO}/releases/download/${TAG}/${BINARY}"
fi

# ── Download to /tmp (mirror → github fallback) ───────────
TMPFILE="${TMPDIR:-/tmp}/rdev-client-$$"

echo "  Downloading rdev-client (${OS}/${ARCH})..." >&2

OK=0
for M in $MIRRORS; do
    [ -z "$M" ] && continue
    echo "  Trying ${M}..." >&2
    if dl "https://${M}/${GH_URL}" "$TMPFILE" 2>/dev/null && [ -s "$TMPFILE" ]; then
        OK=1; echo "  ok via ${M}" >&2; break
    fi
    rm -f "$TMPFILE" 2>/dev/null
done

if [ "$OK" = "0" ]; then
    echo "  Trying github.com..." >&2
    if dl "$GH_URL" "$TMPFILE"; then OK=1; echo "  ok via github.com" >&2; fi
fi

if [ "$OK" = "0" ] || [ ! -s "$TMPFILE" ]; then
    echo "Error: download failed" >&2; rm -f "$TMPFILE" 2>/dev/null; exit 1
fi

chmod +x "$TMPFILE"

# ── Build args & run ───────────────────────────────────────
ARGS="-s $RDEV_SERVER"
[ -n "$RDEV_ID" ] && ARGS="$ARGS -i $RDEV_ID"
[ -n "$RDEV_PASSWORD" ] && ARGS="$ARGS -p $RDEV_PASSWORD"
[ -n "$RDEV_SHELL" ] && ARGS="$ARGS -S $RDEV_SHELL"
[ -n "$RDEV_SSH_PORT" ] && ARGS="$ARGS --ssh-port $RDEV_SSH_PORT"

echo "" >&2
echo "  Starting rdev-client..." >&2
echo "  $TMPFILE $ARGS" >&2
echo "" >&2

if [ "$RDEV_ELEVATE" = "1" ]; then
    if command -v sudo >/dev/null 2>&1; then
        exec sudo "$TMPFILE" $ARGS
    elif command -v doas >/dev/null 2>&1; then
        exec doas "$TMPFILE" $ARGS
    else
        echo "  sudo/doas not found; continuing in normal user mode." >&2
    fi
fi

exec "$TMPFILE" $ARGS
