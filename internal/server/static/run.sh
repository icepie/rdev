#!/bin/sh
# rdev-client one-click runner
# Download → run, no install needed
# Compatible with: POSIX sh, bash, dash, ash, zsh, ksh, busybox sh
#
# Usage:
#   curl -sL http://SERVER:PORT/run.sh | sh -s -- ws://SERVER:PORT
#   curl -sL http://SERVER:PORT/run.sh | sh -s -- ws://SERVER:PORT --client rs
#   wget -qO- http://SERVER:PORT/run.sh | sh -s -- ws://SERVER:PORT

set -e

# ── Defaults ────────────────────────────────────────────────
RDEV_SERVER=""
RDEV_ID=""
RDEV_PASSWORD=""
RDEV_SHELL=""
RDEV_SSH_PORT=""
RDEV_VERSION=""
RDEV_CLIENT="go"
RDEV_REPO="icepie/rdev"

# CN GitHub mirrors (tried first, fallback to direct)
# Override with: RDEV_MIRRORS="mirror1 mirror2" sh run.sh ...
MIRRORS="${RDEV_MIRRORS:-gh.idayer.com gh.ddlc.top gh-proxy.com ghfast.top ghproxy.net ghproxy.cc gh-proxy.net ghproxy.cfd github.moeyy.xyz hub.gitmirror.com ghproxy.1888866.xyz ghproxy.sakuramoe.dev}"

# ── Parse arguments ─────────────────────────────────────────
while [ $# -gt 0 ]; do
    case "$1" in
        -s|--server)   RDEV_SERVER="$2"; shift 2 ;;
        -i|--id)       RDEV_ID="$2"; shift 2 ;;
        -p|--password) RDEV_PASSWORD="$2"; shift 2 ;;
        -S|--shell)    RDEV_SHELL="$2"; shift 2 ;;
        --ssh-port)    RDEV_SSH_PORT="$2"; shift 2 ;;
        -v|--version)  RDEV_VERSION="$2"; shift 2 ;;
        --client)      RDEV_CLIENT="$2"; shift 2 ;;
        --go)          RDEV_CLIENT="go"; shift ;;
        --rs)          RDEV_CLIENT="rs"; shift ;;
        --no-mirror)   MIRRORS=""; shift ;;
        -h|--help)
            echo "Usage: sh run.sh SERVER_URL [options]"
            echo ""
            echo "  Downloads a client to /tmp and runs it directly."
            echo "  Default client is compatible Go; use --client rs for performance Rust."
            echo "  No installation or root required."
            echo ""
            echo "Options:"
            echo "  -s, --server URL     Server WebSocket URL"
            echo "  -i, --id ID          Device ID (default: hostname)"
            echo "  -p, --password PW    Password for SSH auth"
            echo "  -S, --shell PATH     Shell path (e.g. /bin/bash)"
            echo "  --ssh-port PORT      Server SSH port hint (Go client only)"
            echo "  -v, --version VER    Client version (default: latest)"
            echo "  --client go|rs       Client flavor: compatible Go or performance Rust"
            echo "  --go, --rs           Shorthand for --client go|rs"
            echo "  --no-mirror          Skip CN mirrors, use github.com"
            echo ""
            echo "Examples:"
            echo "  curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:8080"
            echo "  curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:8080 -i my-pc -p secret"
            echo "  curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:8080 --client rs"
            exit 0 ;;
        ws://*|wss://*) RDEV_SERVER="$1"; shift ;;
        http://*|https://*) RDEV_SERVER="$1"; shift ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

case "$RDEV_CLIENT" in
    go|GO|Go) RDEV_CLIENT="go" ;;
    rs|RS|Rust|rust) RDEV_CLIENT="rs" ;;
    *) echo "Error: unsupported client: $RDEV_CLIENT (expected go or rs)" >&2; exit 1 ;;
esac

if [ -z "$RDEV_SERVER" ]; then
    echo "Error: server URL required" >&2
    echo "Usage: curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:PORT" >&2
    exit 1
fi

ANDROID_ENV=0
if [ "$(uname -o 2>/dev/null || true)" = "Android" ] || [ -n "${ANDROID_ROOT:-}" ]; then
    ANDROID_ENV=1
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

if [ "$ANDROID_ENV" != "1" ] && [ "$(id -u 2>/dev/null || echo 1)" != "0" ]; then
    if wait_elevation_key; then
        RDEV_ELEVATE=1
        echo "  Elevation requested; will start client with sudo/doas after download." >&2
    else
        echo "  Continuing in normal user mode." >&2
    fi
fi

# ── Detect OS / Arch ────────────────────────────────────────
OS="$(uname -s 2>/dev/null || echo unknown)"
ARCH="$(uname -m 2>/dev/null || echo unknown)"

case "$OS" in
    Linux*)
        if [ "$ANDROID_ENV" = "1" ]; then OS="android"; else OS="linux"; fi
        ;;
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

dl_stdout() {
    case "$DL_TOOL" in
        curl)         curl -fsSL --connect-timeout 8 --max-time 20 "$1" ;;
        wget)         wget -q --timeout=20 -O - "$1" ;;
        fetch)        fetch -qo - "$1" ;;
        busybox_wget) busybox wget -q -O - "$1" ;;
    esac
}

mirror_url() {
    echo "https://$1/$2"
}

server_http_base() {
    case "$RDEV_SERVER" in
        wss://*) base="https://${RDEV_SERVER#wss://}" ;;
        ws://*)  base="http://${RDEV_SERVER#ws://}" ;;
        http://*|https://*) base="$RDEV_SERVER" ;;
        *) return 1 ;;
    esac
    proto="${base%%://*}"
    rest="${base#*://}"
    host="${rest%%/*}"
    [ -n "$host" ] || return 1
    echo "$proto://$host"
}

release_proxy_url() {
    base="$(server_http_base 2>/dev/null || true)"
    [ -n "$base" ] || return 1
    asset="$1"
    tag="$TAG"
    [ -n "$tag" ] || tag="latest"
    echo "$base/download-release-proxy?asset=$asset&tag=$tag"
}

extract_latest_tag() {
    grep '"tag_name"' | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/'
}

latest_tag_with_fallback() {
    api="https://api.github.com/repos/${RDEV_REPO}/releases/latest"
    for m in $MIRRORS; do
        [ -z "$m" ] && continue
        tag="$(dl_stdout "$(mirror_url "$m" "$api")" 2>/dev/null | extract_latest_tag || true)"
        [ -n "$tag" ] && { echo "$tag"; return 0; }
    done
    tag="$(dl_stdout "$api" 2>/dev/null | extract_latest_tag || true)"
    [ -n "$tag" ] && echo "$tag"
}

# ── Determine version ──────────────────────────────────────
if [ -n "$RDEV_VERSION" ]; then
    case "$RDEV_VERSION" in v*) TAG="$RDEV_VERSION" ;; *) TAG="v${RDEV_VERSION}" ;; esac
else
    TAG="$(latest_tag_with_fallback)"
    [ -z "$TAG" ] && TAG="latest"
fi

release_url() {
    if [ "$TAG" = "latest" ]; then
        echo "https://github.com/${RDEV_REPO}/releases/latest/download/$1"
    else
        echo "https://github.com/${RDEV_REPO}/releases/download/${TAG}/$1"
    fi
}

download_with_fallback() {
    url="$1"
    out="$2"
    asset="$3"
    [ -n "$asset" ] || asset="${url##*/}"
    ok=0
    for m in $MIRRORS; do
        [ -z "$m" ] && continue
        echo "  Trying ${m}..." >&2
        if dl "$(mirror_url "$m" "$url")" "$out" 2>/dev/null && [ -s "$out" ]; then
            ok=1
            echo "  ok via ${m}" >&2
            break
        fi
        rm -f "$out" 2>/dev/null
    done
    if [ "$ok" = "0" ]; then
        echo "  Trying github.com..." >&2
        if dl "$url" "$out" && [ -s "$out" ]; then ok=1; echo "  ok via github.com" >&2; fi
    fi
    if [ "$ok" = "0" ]; then
        proxy_url="$(release_proxy_url "$asset" 2>/dev/null || true)"
        if [ -n "$proxy_url" ]; then
            echo "  Trying RDev server proxy..." >&2
            if dl "$proxy_url" "$out" && [ -s "$out" ]; then ok=1; echo "  ok via RDev server proxy" >&2; fi
        fi
    fi
    [ "$ok" = "1" ]
}

linux_rs_asset_suffix() {
    [ "$OS" = "linux" ] || { echo ""; return; }
    if [ -r /etc/os-release ] && grep -Eq '^ID=(arch|"arch")$' /etc/os-release 2>/dev/null; then
        echo ""
        return
    fi
    ver=""
    if command -v getconf >/dev/null 2>&1; then
        ver="$(getconf GNU_LIBC_VERSION 2>/dev/null | awk '{print $2}')"
    fi
    [ -n "$ver" ] || ver="$(ldd --version 2>/dev/null | sed -n '1s/.* //p')"
    case "$ver" in
        [0-9]*.[0-9]*) ;;
        *) echo "-debian11"; return ;;
    esac
    major=${ver%%.*}
    minor=${ver#*.}; minor=${minor%%.*}
    if [ "$major" -lt 2 ] 2>/dev/null || { [ "$major" -eq 2 ] 2>/dev/null && [ "$minor" -lt 28 ] 2>/dev/null; }; then
        if [ "$ARCH" = "amd64" ]; then echo "-centos7"; else echo "-centos8"; fi
    elif [ "$major" -eq 2 ] 2>/dev/null && [ "$minor" -lt 31 ] 2>/dev/null; then
        echo "-centos8"
    elif [ "$major" -eq 2 ] 2>/dev/null && [ "$minor" -lt 36 ] 2>/dev/null; then
        echo "-debian11"
    elif [ "$major" -eq 2 ] 2>/dev/null && [ "$minor" -lt 41 ] 2>/dev/null; then
        echo "-debian12"
    else
        echo ""
    fi
}

# ── Resolve asset and download ─────────────────────────────
TMPBASE="${TMPDIR:-/tmp}"
RUN_BIN=""
CLIENT_LABEL="rdev-client"

if [ "$RDEV_CLIENT" = "rs" ]; then
    CLIENT_LABEL="rdev-client-gpu"
    case "$OS/$ARCH" in
        linux/amd64|linux/arm64)
            ASSET="rdev-client-gpu-${OS}-${ARCH}$(linux_rs_asset_suffix).tar.gz"
            ARCHIVE="$TMPBASE/rdev-client-gpu-${TAG}-${OS}-${ARCH}-$$.tar.gz"
            ;;
        android/amd64|android/arm64)
            ASSET="rdev-client-gpu-${OS}-${ARCH}.tar.gz"
            ARCHIVE="$TMPBASE/rdev-client-gpu-${TAG}-${OS}-${ARCH}-$$.tar.gz"
            ;;
        darwin/amd64|darwin/arm64)
            ASSET="rdev-client-gpu-${OS}-${ARCH}.tar.gz"
            ARCHIVE="$TMPBASE/rdev-client-gpu-${TAG}-${OS}-${ARCH}-$$.tar.gz"
            ;;
        windows/amd64)
            ASSET="rdev-client-gpu-windows-amd64.zip"
            ARCHIVE="$TMPBASE/rdev-client-gpu-${TAG}-windows-${ARCH}-$$.zip"
            ;;
        *)
            echo "Error: performance Rust client is not published for ${OS}/${ARCH}" >&2
            exit 1
            ;;
    esac
    GH_URL="$(release_url "$ASSET")"
    echo "  Downloading ${CLIENT_LABEL} package (${OS}/${ARCH})..." >&2
    if ! download_with_fallback "$GH_URL" "$ARCHIVE" "$ASSET"; then
        echo "Error: download failed" >&2
        rm -f "$ARCHIVE" 2>/dev/null
        exit 1
    fi

    EXTRACT_DIR="$TMPBASE/rdev-client-gpu-${TAG}-${OS}-${ARCH}-$$"
    rm -rf "$EXTRACT_DIR" 2>/dev/null
    mkdir -p "$EXTRACT_DIR"
    case "$ASSET" in
        *.tar.gz)
            command -v tar >/dev/null 2>&1 || { echo "Error: tar is required for Rust client package" >&2; exit 1; }
            tar -xzf "$ARCHIVE" -C "$EXTRACT_DIR"
            RUN_BIN="$(find "$EXTRACT_DIR" -type f -name rdev-client-gpu | head -1)"
            ;;
        *.zip)
            command -v unzip >/dev/null 2>&1 || { echo "Error: unzip is required for Rust client package" >&2; exit 1; }
            unzip -q "$ARCHIVE" -d "$EXTRACT_DIR"
            RUN_BIN="$(find "$EXTRACT_DIR" -type f -name rdev-client-gpu.exe | head -1)"
            ;;
    esac
    [ -n "$RUN_BIN" ] || { echo "Error: rdev-client-gpu binary not found in package" >&2; exit 1; }
    chmod +x "$RUN_BIN" 2>/dev/null || true
else
    BINARY="rdev-client-${OS}-${ARCH}"
    [ "$OS" = "windows" ] && BINARY="${BINARY}.exe"
    GH_URL="$(release_url "$BINARY")"
    RUN_BIN="$TMPBASE/rdev-client-${TAG}-${OS}-${ARCH}-$$"
    echo "  Downloading rdev-client (${OS}/${ARCH})..." >&2
    if ! download_with_fallback "$GH_URL" "$RUN_BIN" "$BINARY"; then
        echo "Error: download failed" >&2
        rm -f "$RUN_BIN" 2>/dev/null
        exit 1
    fi
    chmod +x "$RUN_BIN"
fi

# ── Build args & run ───────────────────────────────────────
if [ "$RDEV_CLIENT" = "rs" ] && [ -z "$RDEV_ID" ]; then
    RDEV_ID="$(hostname 2>/dev/null || uname -n 2>/dev/null || echo rdev-client-gpu)"
fi

if [ "$OS" = "android" ] && [ -z "$RDEV_SHELL" ]; then
    if [ -x "${PREFIX:-}/bin/bash" ]; then
        RDEV_SHELL="${PREFIX}/bin/bash"
    elif [ -x "${PREFIX:-}/bin/sh" ]; then
        RDEV_SHELL="${PREFIX}/bin/sh"
    elif [ -x /system/bin/sh ]; then
        RDEV_SHELL="/system/bin/sh"
    fi
fi

if [ "$RDEV_CLIENT" = "rs" ]; then
    set -- -s "$RDEV_SERVER"
    [ -n "$RDEV_ID" ] && set -- "$@" -i "$RDEV_ID"
    [ -n "$RDEV_PASSWORD" ] && set -- "$@" -p "$RDEV_PASSWORD"
    [ -n "$RDEV_SHELL" ] && set -- "$@" --shell "$RDEV_SHELL"
else
    set -- -s "$RDEV_SERVER"
    [ -n "$RDEV_ID" ] && set -- "$@" -i "$RDEV_ID"
    [ -n "$RDEV_PASSWORD" ] && set -- "$@" -p "$RDEV_PASSWORD"
    [ -n "$RDEV_SHELL" ] && set -- "$@" -S "$RDEV_SHELL"
    [ -n "$RDEV_SSH_PORT" ] && set -- "$@" --ssh-port "$RDEV_SSH_PORT"
fi

echo "" >&2
echo "  Starting ${CLIENT_LABEL}..." >&2
printf '  %s' "$RUN_BIN" >&2
for arg in "$@"; do printf ' %s' "$arg" >&2; done
printf '\n\n' >&2

if [ "$RDEV_ELEVATE" = "1" ]; then
    if command -v sudo >/dev/null 2>&1; then
        exec sudo "$RUN_BIN" "$@"
    elif command -v doas >/dev/null 2>&1; then
        exec doas "$RUN_BIN" "$@"
    else
        echo "  sudo/doas not found; continuing in normal user mode." >&2
    fi
fi

exec "$RUN_BIN" "$@"
