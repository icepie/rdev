#!/bin/sh
# rdev-client one-click runner
# Download → run, no install needed
# Compatible with: POSIX sh, bash, dash, ash, zsh, ksh, busybox sh
# Uses curl or wget; no OpenSSL or external verifier required.
# Downloads and cached payloads are not authenticated by this script.
# Obtain this bootstrap and packages from sources you trust.
#
# Usage:
#   curl -sL http://SERVER:PORT/run.sh | sh -s --
#   curl -sL http://SERVER:PORT/run.sh | sh -s -- --client rs
#   wget -qO- http://SERVER:PORT/run.sh | sh

set -e

# Downloaded from rdev: server defaults are filled by the server template.
# ── Defaults ────────────────────────────────────────────────
RDEV_SERVER='{{.Server}}'
# Raw repository copies still accept an explicit server argument.
case "$RDEV_SERVER" in '{'*) RDEV_SERVER="" ;; esac
RDEV_ID=""
RDEV_PASSWORD=""
RDEV_RANDOM_PASSWORD=0
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
        -P|--random-password) RDEV_RANDOM_PASSWORD=1; shift ;;
        -S|--shell)    RDEV_SHELL="$2"; shift 2 ;;
        --ssh-port)    RDEV_SSH_PORT="$2"; shift 2 ;;
        -v|--version)  RDEV_VERSION="$2"; shift 2 ;;
        --client)      RDEV_CLIENT="$2"; shift 2 ;;
        --go)          RDEV_CLIENT="go"; shift ;;
        --rs)          RDEV_CLIENT="rs"; shift ;;
        --no-mirror)   MIRRORS=""; shift ;;
        -h|--help)
            echo "Usage: sh run.sh [SERVER_URL] [options]"
            echo ""
            echo "  Downloads a client to /tmp and runs it directly."
            echo "  Default client is compatible Go; use --client rs for the performance Rust client."
            echo "  No installation or root required."
            echo ""
            echo "Options:"
            echo "  -s, --server URL     Server URL or comma-separated URL group"
            echo "  -i, --id ID          Device ID (default: short random ID)"
            echo "  -p, --password PW    Password for SSH auth"
            echo "  -P, --random-password Generate a 21-character alphanumeric password (ignored with -p)"
            echo "  -S, --shell PATH     Shell path (e.g. /bin/bash)"
            echo "  --ssh-port PORT      Server SSH port hint (Go client only)"
            echo "  -v, --version VER    Client version (default: latest)"
            echo "  --client go|rs       Client flavor: compatible Go or performance Rust"
            echo "  --go, --rs           Shorthand for --client go|rs"
            echo "  --no-mirror          Skip CN mirrors (server proxy is still tried first)"
            echo ""
            echo "Examples:"
            echo "  curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:8080"
            echo "  curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:8080 -i my-pc -p secret"
            echo "  curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:8080 --random-password"
            echo "  curl -sL http://SERVER/run.sh | sh -s -- ws://SERVER:8080 --client rs"
            exit 0 ;;
        ws://*|wss://*|tcp://*|kcp://*|udp://*) RDEV_SERVER="$1"; shift ;;
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

# ── Random password ─────────────────────────────────────────
# Generate a 21-character alphanumeric password from the system CSPRNG.
# An explicit -p/--password always wins.
if [ "$RDEV_RANDOM_PASSWORD" = "1" ] && [ -z "$RDEV_PASSWORD" ]; then
    RDEV_PASSWORD="$(LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom 2>/dev/null | head -c 21 || true)"
    if [ "${#RDEV_PASSWORD}" -ne 21 ]; then
        echo "Error: failed to generate a random password" >&2
        exit 1
    fi
    echo "  Password: $RDEV_PASSWORD" >&2
fi

ANDROID_ENV=0
if [ "$(uname -o 2>/dev/null || true)" = "Android" ] || [ -n "${ANDROID_ROOT:-}" ]; then
    ANDROID_ENV=1
fi

# ── Optional elevation prompt ───────────────────────────────
RDEV_ELEVATE=0

wait_elevation_key() {
    (: </dev/tty) 2>/dev/null || return 1
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

mirror_url() {
    echo "https://$1/$2"
}

server_http_base() {
    first_ws=""
    first_any=""
    old_ifs=$IFS
    IFS=','
    for endpoint in $RDEV_SERVER; do
        endpoint=$(printf '%s' "$endpoint" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
        [ -n "$endpoint" ] || continue
        [ -n "$first_any" ] || first_any="$endpoint"
        case "$endpoint" in
            wss://*|ws://*|http://*|https://*) first_ws="$endpoint"; break ;;
        esac
    done
    IFS=$old_ifs
    endpoint="${first_ws:-$first_any}"
    case "$endpoint" in
        wss://*) base="https://${endpoint#wss://}" ;;
        ws://*)  base="http://${endpoint#ws://}" ;;
        http://*|https://*) base="$endpoint" ;;
        tcp://*|kcp://*|udp://*) return 1 ;;
        *) return 1 ;;
    esac
    proto="${base%%://*}"
    rest="${base#*://}"
    host="${rest%%/*}"
    host="${host%%\?*}"
    host="${host%%#*}"
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

json_tag_value() {
    sed -n 's/.*"tag"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1
}

resolve_latest_tag() {
    if [ "$TAG" != "latest" ]; then
        RESOLVED_TAG="$TAG"
        return
    fi
    RESOLVED_TAG=""
    base="$(server_http_base 2>/dev/null || true)"
    tmp_latest="${TMPBASE:-${TMPDIR:-/tmp}}/rdev-latest-$$.json"
    if [ -n "$base" ]; then
        if dl "$base/api/release/latest" "$tmp_latest" 2>/dev/null && [ -s "$tmp_latest" ]; then
            RESOLVED_TAG="$(json_tag_value < "$tmp_latest")"
        fi
        rm -f "$tmp_latest" 2>/dev/null
    fi
    if [ -z "$RESOLVED_TAG" ]; then
        tmp_hdr="${TMPBASE:-${TMPDIR:-/tmp}}/rdev-latest-$$.hdr"
        if command -v curl >/dev/null 2>&1; then
            curl -fsSLI --connect-timeout 5 --max-time 12 "https://github.com/${RDEV_REPO}/releases/latest" > "$tmp_hdr" 2>/dev/null || true
            RESOLVED_TAG="$(sed -n 's|^[Ll]ocation: .*/tag/\(v[^[:space:]\r]*\).*|\1|p' "$tmp_hdr" | tail -1)"
        fi
        rm -f "$tmp_hdr" 2>/dev/null
    fi
    if [ -z "$RESOLVED_TAG" ]; then
        RESOLVED_TAG="latest"
        echo "  Latest tag could not be resolved; using cache key 'latest'." >&2
    fi
}

safe_name() {
    printf '%s' "$1" | sed 's/[^A-Za-z0-9_.-]/-/g'
}

cache_lock_acquire() {
    lock_dir="$1.lock"
    i=0
    while ! mkdir "$lock_dir" 2>/dev/null; do
        i=$((i + 1))
        [ "$i" -ge 50 ] && return 1
        sleep 0.1 2>/dev/null || sleep 1
    done
    CACHE_LOCK_DIR="$lock_dir"
    return 0
}

cache_lock_release() {
    [ -n "${CACHE_LOCK_DIR:-}" ] && rmdir "$CACHE_LOCK_DIR" 2>/dev/null || true
    CACHE_LOCK_DIR=""
}

is_html_file() {
    [ -f "$1" ] && head -c 256 "$1" 2>/dev/null | grep -qiE '^[[:space:]]*<(!doctype|html|head|body)'
}

is_binary_file() {
    [ -s "$1" ] && ! is_html_file "$1"
}

download_ok() {
    dl "$1" "$2" && is_binary_file "$2"
}

cache_complete() {
    [ -f "$1/.complete" ] && is_binary_file "$1/payload"
}

load_release() {
    CACHE_DIR="$CACHE_BASE/$1"
    PAYLOAD="$WORK_DIR/$2"
    if cache_complete "$CACHE_DIR" && cp "$CACHE_DIR/payload" "$PAYLOAD" && is_binary_file "$PAYLOAD"; then
        echo "  Using cached release (${RESOLVED_TAG})." >&2
        return
    fi
    rm -f "$PAYLOAD"
    download_with_fallback "$(release_url "$2")" "$PAYLOAD" "$2" || {
        rm -f "$PAYLOAD"
        return 1
    }
    if cache_lock_acquire "$CACHE_DIR"; then
        rm -rf "$CACHE_DIR.part"
        mkdir -m 700 "$CACHE_DIR.part"
        cp "$PAYLOAD" "$CACHE_DIR.part/payload"
        : > "$CACHE_DIR.part/.complete"
        rm -rf "$CACHE_DIR"
        mv "$CACHE_DIR.part" "$CACHE_DIR"
        cache_lock_release
    fi
}

# ── Determine version ──────────────────────────────────────
if [ -n "$RDEV_VERSION" ]; then
    case "$RDEV_VERSION" in v*) TAG="$RDEV_VERSION" ;; *) TAG="v${RDEV_VERSION}" ;; esac
else
    TAG="latest"
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
    proxy_url="$(release_proxy_url "$asset" 2>/dev/null || true)"
    if [ -n "$proxy_url" ]; then
        echo "  Trying RDev server proxy..." >&2
        if download_ok "$proxy_url" "$out"; then ok=1; echo "  ok via RDev server proxy" >&2; fi
        [ "$ok" = "1" ] || rm -f "$out" 2>/dev/null
    fi
    for m in $MIRRORS; do
        [ "$ok" = "1" ] && break
        [ -z "$m" ] && continue
        echo "  Trying ${m}..." >&2
        if download_ok "$(mirror_url "$m" "$url")" "$out"; then
            ok=1
            echo "  ok via ${m}" >&2
            break
        fi
        rm -f "$out" 2>/dev/null
    done
    if [ "$ok" = "0" ]; then
        echo "  Trying github.com..." >&2
        if download_ok "$url" "$out"; then ok=1; echo "  ok via github.com" >&2; fi
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

extract_zip() {
    zip_file="$1"
    dest_dir="$2"
    if command -v unzip >/dev/null 2>&1 && unzip -q "$zip_file" -d "$dest_dir" 2>/dev/null; then
        return 0
    fi
    # Windows 10+ ships bsdtar as tar.exe and it reads zip; GNU tar does not,
    # so probe by running it rather than by presence.
    for tar_bin in bsdtar tar; do
        command -v "$tar_bin" >/dev/null 2>&1 || continue
        if (cd "$dest_dir" && "$tar_bin" -xf "$zip_file") 2>/dev/null; then
            return 0
        fi
    done
    for zip_bin in 7z 7za 7zz; do
        command -v "$zip_bin" >/dev/null 2>&1 || continue
        if "$zip_bin" x -y -o"$dest_dir" "$zip_file" >/dev/null 2>&1; then
            return 0
        fi
    done
    # Last resort for Windows shells (Git Bash / busybox) with no archiver.
    for ps_bin in powershell.exe pwsh.exe powershell pwsh; do
        command -v "$ps_bin" >/dev/null 2>&1 || continue
        ps_zip="$zip_file"
        ps_dest="$dest_dir"
        if command -v cygpath >/dev/null 2>&1; then
            ps_zip="$(cygpath -w "$zip_file" 2>/dev/null || echo "$zip_file")"
            ps_dest="$(cygpath -w "$dest_dir" 2>/dev/null || echo "$dest_dir")"
        fi
        if "$ps_bin" -NoProfile -NonInteractive -Command "Add-Type -AssemblyName System.IO.Compression.FileSystem; [System.IO.Compression.ZipFile]::ExtractToDirectory('$ps_zip','$ps_dest')" >/dev/null 2>&1; then
            return 0
        fi
    done
    return 1
}

# ── Resolve asset and download ─────────────────────────────
umask 077
# chmod succeeds on noexec mounts too; execute a harmless probe before downloads.
# Keep the complete runtime in the chosen directory (including Rust sidecars).
WORK_DIR=""
PROBE_SHELL=/bin/sh
if [ "$OS" = "android" ]; then
    PROBE_SHELL="$(command -v sh)"
fi
for TMPBASE in "${TMPDIR:-/tmp}" "${HOME:+$HOME/.cache/rdev}" /var/tmp /tmp; do
    [ -n "$TMPBASE" ] || continue
    mkdir -p "$TMPBASE" 2>/dev/null || continue
    WORK_DIR="$(mktemp -d "$TMPBASE/rdev-run.XXXXXXXX" 2>/dev/null)" || continue
    if printf '#!%s\nexit 0\n' "$PROBE_SHELL" > "$WORK_DIR/exec-probe" &&
        chmod 700 "$WORK_DIR/exec-probe" &&
        ("$WORK_DIR/exec-probe") 2>/dev/null; then
        rm -f "$WORK_DIR/exec-probe"
        break
    fi
    echo "  Cannot execute in $TMPBASE; trying another directory (check noexec/security policy)." >&2
    rm -rf "$WORK_DIR"
    WORK_DIR=""
done
if [ -z "$WORK_DIR" ]; then
    echo "Error: no writable, executable temporary directory. Set TMPDIR to an executable filesystem directory and retry." >&2
    exit 1
fi
trap 'cache_lock_release; rm -rf "$WORK_DIR"' 0
CACHE_BASE="$TMPBASE/rdev-cache"
mkdir -p "$CACHE_BASE"
resolve_latest_tag
TAG="$RESOLVED_TAG"
SAFE_TAG="$(safe_name "$RESOLVED_TAG")"
RUN_BIN=""
CLIENT_LABEL="rdev-client"

if [ "$RDEV_CLIENT" = "rs" ]; then
    CLIENT_LABEL="rdev-client-rs"
    case "$OS/$ARCH" in
        linux/amd64|linux/arm64)
            ASSET="rdev-client-rs-${OS}-${ARCH}$(linux_rs_asset_suffix).tar.gz"
            ;;
        android/amd64|android/arm64|android/armv7|android/386)
            ASSET_ARCH="$ARCH"
            [ "$ARCH" = "386" ] && ASSET_ARCH="x86"
            ASSET="rdev-client-rs-${OS}-${ASSET_ARCH}.tar.gz"
            ;;
        darwin/amd64|darwin/arm64)
            ASSET="rdev-client-rs-${OS}-${ARCH}.tar.gz"
            ;;
        windows/amd64|windows/arm64)
            ASSET="rdev-client-rs-windows-${ARCH}.zip"
            ;;
        *)
            echo "Error: performance Rust client is not published for ${OS}/${ARCH}" >&2
            exit 1
            ;;
    esac
    CACHE_KEY="rs-${SAFE_TAG}-${OS}-${ARCH}-$(safe_name "$ASSET")"
    if ! load_release "$CACHE_KEY" "$ASSET"; then
        # Releases up to v0.2.151 named this package rdev-client-gpu-*.
        ASSET="rdev-client-gpu-${ASSET#rdev-client-rs-}"
        CACHE_KEY="rs-${SAFE_TAG}-${OS}-${ARCH}-$(safe_name "$ASSET")"
        load_release "$CACHE_KEY" "$ASSET" || {
            echo "Error: no source supplied a usable release package; check the server address, version, network or mirror settings." >&2
            exit 1
        }
    fi
    ARCHIVE="$PAYLOAD"
    EXTRACT_DIR="$WORK_DIR/runtime"
    mkdir "$EXTRACT_DIR"
    case "$ASSET" in
        *.tar.gz)
            command -v tar >/dev/null 2>&1 || { echo "Error: tar is required for Rust client package" >&2; exit 1; }
            tar -xzf "$ARCHIVE" -C "$EXTRACT_DIR"
            RUN_BIN="$(find "$EXTRACT_DIR" -type f \( -name rdev-client-rs -o -name rdev-client-gpu \) | head -1)"
            ;;
        *.zip)
            extract_zip "$ARCHIVE" "$EXTRACT_DIR" || { echo "Error: need unzip, bsdtar, 7z, or PowerShell to extract Rust client package" >&2; exit 1; }
            RUN_BIN="$(find "$EXTRACT_DIR" -type f \( -name rdev-client-rs.exe -o -name rdev-client-gpu.exe \) | head -1)"
            ;;
    esac
    [ -n "$RUN_BIN" ] || { echo "Error: Rust client binary not found in package" >&2; exit 1; }
    chmod +x "$RUN_BIN" 2>/dev/null || true
    # Execute the freshly extracted package in place, preserving every runtime
    # sidecar. Cached extracted files are never trusted or executed.
else
    ASSET_ARCH="$ARCH"
    if [ "$OS" = "android" ]; then
        case "$ARCH" in
            amd64|arm64|armv7) ;;
            386) ASSET_ARCH="x86" ;;
            *) echo "Error: compatible Go client is not published for ${OS}/${ARCH}" >&2; exit 1 ;;
        esac
    fi
    BINARY="rdev-client-${OS}-${ASSET_ARCH}"
    [ "$OS" = "windows" ] && BINARY="${BINARY}.exe"
    CACHE_KEY="go-${SAFE_TAG}-${OS}-${ASSET_ARCH}-$(safe_name "$BINARY")"
    load_release "$CACHE_KEY" "$BINARY" || {
        echo "Error: no source supplied a usable release package; check the server address, version, network or mirror settings." >&2
        exit 1
    }
    RUN_BIN="$PAYLOAD"
    chmod +x "$RUN_BIN"
fi

# ── Build args & run ───────────────────────────────────────

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
        sudo "$RUN_BIN" "$@"
        exit $?
    elif command -v doas >/dev/null 2>&1; then
        doas "$RUN_BIN" "$@"
        exit $?
    else
        echo "  sudo/doas not found; continuing in normal user mode." >&2
    fi
fi

"$RUN_BIN" "$@"
