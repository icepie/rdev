#!/bin/sh
# rdev-client install & launch script
# Compatible with: POSIX sh, bash, dash, ash, zsh, ksh, busybox sh
# Usage:
#   Direct:     sh install.sh ws://SERVER:PORT [-i DEVICE_ID] [-p PASSWORD]
#   Curl pipe:  curl -sL http://SERVER:PORT/install.sh | sh -s -- ws://SERVER:PORT
#   Wget pipe:  wget -qO- http://SERVER:PORT/install.sh | sh -s -- ws://SERVER:PORT

set -e

# ── Defaults ────────────────────────────────────────────────
RDEV_VERSION=""
RDEV_SERVER=""
RDEV_ID=""
RDEV_PASSWORD=""
RDEV_SHELL=""
RDEV_SSH_PORT=""
RDEV_INSTALL_DIR="/usr/local/bin"
RDEV_REPO="icepie/rdev"
RDEV_BASE_URL="https://github.com/${RDEV_REPO}/releases"

# ── Parse arguments ─────────────────────────────────────────
# First non-flag argument is the server URL (for pipe usage)
while [ $# -gt 0 ]; do
    case "$1" in
        -s|--server)   RDEV_SERVER="$2"; shift 2 ;;
        -i|--id)       RDEV_ID="$2"; shift 2 ;;
        -p|--password) RDEV_PASSWORD="$2"; shift 2 ;;
        -S|--shell)    RDEV_SHELL="$2"; shift 2 ;;
        --ssh-port)    RDEV_SSH_PORT="$2"; shift 2 ;;
        -v|--version)  RDEV_VERSION="$2"; shift 2 ;;
        -d|--dir)      RDEV_INSTALL_DIR="$2"; shift 2 ;;
        -h|--help)
            echo "Usage: sh install.sh [SERVER_URL] [options]"
            echo ""
            echo "Options:"
            echo "  -s, --server URL     Server WebSocket URL (e.g. ws://1.2.3.4:8080)"
            echo "  -i, --id ID          Device ID (default: hostname)"
            echo "  -p, --password PW   Password for SSH auth"
            echo "  -S, --shell PATH    Shell path (e.g. /bin/bash)"
            echo "  --ssh-port PORT      Server SSH port (for hint display, default: 2222)"
            echo "  -v, --version VER    Client version to download (default: latest)"
            echo "  -d, --dir DIR        Install directory (default: /usr/local/bin)"
            echo ""
            echo "Examples:"
            echo "  sh install.sh ws://192.168.1.100:8080 -i my-pc -p secret"
            echo "  sh install.sh -s ws://10.0.0.1:8080 --ssh-port 2222"
            echo "  curl -sL http://SERVER:8080/install.sh | sh -s -- ws://SERVER:8080"
            exit 0 ;;
        ws://*|wss://*) RDEV_SERVER="$1"; shift ;;
        http://*|https://*) RDEV_SERVER="$1"; shift ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

if [ -z "$RDEV_SERVER" ]; then
    echo "Error: server URL required" >&2
    echo "Usage: sh install.sh ws://SERVER:PORT [-i ID] [-p PASSWORD]" >&2
    exit 1
fi

# ── Detect OS ───────────────────────────────────────────────
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
    *) echo "Error: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

# ── Detect download tool ────────────────────────────────────
DL_TOOL=""
if command -v curl >/dev/null 2>&1; then
    DL_TOOL="curl"
elif command -v wget >/dev/null 2>&1; then
    DL_TOOL="wget"
elif command -v fetch >/dev/null 2>&1; then
    DL_TOOL="fetch"
elif command -v busybox >/dev/null 2>&1 && busybox --list 2>/dev/null | grep -q wget; then
    DL_TOOL="busybox_wget"
else
    echo "Error: need curl, wget, or fetch to download" >&2
    exit 1
fi

download() {
    # $1 = URL, $2 = output file
    case "$DL_TOOL" in
        curl)          curl -fsSL "$1" -o "$2" ;;
        wget)          wget -qO "$2" "$1" ;;
        fetch)         fetch -o "$2" "$1" ;;
        busybox_wget)  busybox wget -O "$2" "$1" ;;
    esac
}

# ── Determine version & URL ────────────────────────────────
if [ -n "$RDEV_VERSION" ]; then
    TAG="v${RDEV_VERSION}"
else
    # Get latest tag via GitHub API (fallback to 'latest' redirect)
    TAG=""
    if command -v curl >/dev/null 2>&1; then
        TAG="$(curl -fsSL "https://api.github.com/repos/${RDEV_REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')"
    fi
    if [ -z "$TAG" ]; then
        TAG="latest"
    fi
fi

BINARY="rdev-client-${OS}-${ARCH}"
if [ "$OS" = "windows" ]; then
    BINARY="${BINARY}.exe"
fi

if [ "$TAG" = "latest" ]; then
    DL_URL="${RDEV_BASE_URL}/latest/download/${BINARY}"
else
    DL_URL="${RDEV_BASE_URL}/download/${TAG}/${BINARY}"
fi

# ── Download ────────────────────────────────────────────────
# Use a temp directory that works everywhere
TMPDIR="${TMPDIR:-/tmp}"
TMPFILE="${TMPDIR}/rdev-client-download-$$"

echo "  Downloading rdev-client (${OS}/${ARCH})..."
download "$DL_URL" "$TMPFILE"

# Verify download succeeded
if [ ! -s "$TMPFILE" ]; then
    echo "Error: download failed or empty file" >&2
    rm -f "$TMPFILE" 2>/dev/null
    exit 1
fi

# ── Install ─────────────────────────────────────────────────
INSTALL_PATH="${RDEV_INSTALL_DIR}/rdev-client"
if [ "$OS" = "windows" ]; then
    INSTALL_PATH="${INSTALL_PATH}.exe"
fi

# Try install (needs root for /usr/local/bin)
NEED_SUDO=0
if [ -w "$(dirname "$INSTALL_PATH")" ] 2>/dev/null; then
    NEED_SUDO=0
else
    NEED_SUDO=1
fi

INSTALL_CMD="cp"
if [ "$NEED_SUDO" = "1" ]; then
    if command -v sudo >/dev/null 2>&1; then
        INSTALL_CMD="sudo cp"
    elif command -v doas >/dev/null 2>&1; then
        INSTALL_CMD="doas cp"
    elif [ "$(id -u 2>/dev/null)" = "0" ]; then
        INSTALL_CMD="cp"
    else
        # Fallback: install to ~/.local/bin
        INSTALL_PATH="${HOME}/.local/bin/rdev-client"
        mkdir -p "${HOME}/.local/bin" 2>/dev/null
        NEED_SUDO=0
        INSTALL_CMD="cp"
    fi
fi

$INSTALL_CMD "$TMPFILE" "$INSTALL_PATH"
chmod +x "$INSTALL_PATH" 2>/dev/null || $INSTALL_CMD chmod +x "$INSTALL_PATH"
rm -f "$TMPFILE"

echo "  Installed: $INSTALL_PATH"

# Add to PATH if not already
case ":${PATH}:" in
    *":$(dirname "$INSTALL_PATH"):"*) ;;
    *)
        echo "  Add to PATH: export PATH=\"\$(dirname $INSTALL_PATH):\$PATH\""
        ;;
esac

# ── Launch ──────────────────────────────────────────────────
LAUNCH_ARGS=""
if [ -n "$RDEV_ID" ]; then       LAUNCH_ARGS="$LAUNCH_ARGS -i $RDEV_ID"; fi
if [ -n "$RDEV_PASSWORD" ]; then  LAUNCH_ARGS="$LAUNCH_ARGS -p $RDEV_PASSWORD"; fi
if [ -n "$RDEV_SHELL" ]; then     LAUNCH_ARGS="$LAUNCH_ARGS -S $RDEV_SHELL"; fi
if [ -n "$RDEV_SSH_PORT" ]; then  LAUNCH_ARGS="$LAUNCH_ARGS --ssh-port $RDEV_SSH_PORT"; fi

echo ""
echo "  Starting rdev-client..."
echo "  $INSTALL_PATH -s $RDEV_SERVER $LAUNCH_ARGS"
echo ""

exec "$INSTALL_PATH" -s "$RDEV_SERVER" $LAUNCH_ARGS
