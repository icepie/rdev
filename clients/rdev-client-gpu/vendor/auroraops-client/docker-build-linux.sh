#!/usr/bin/env bash
# Matrix builder for AuroraOps Agent across 信创 / mainstream Linux targets.
# Targets are mapped onto upstream-compatible base images (same glibc):
#
#   ubuntu2004  ubuntu:20.04        glibc 2.31  → Ubuntu ≥20.04          (.deb, X11 only)
#   ubuntu2204  ubuntu:22.04        glibc 2.35  → Ubuntu ≥22.04          (.deb, Wayland)
#   uos-v20     debian:11           glibc 2.31  → 统信 UOS V20 桌面       (.deb)
#   kylin-v10-v11 ubuntu:20.04      glibc 2.31  → 麒麟 V10/V11 桌面       (.deb)
#   nfschina-desktop debian:11      glibc 2.31  → 中科方德桌面            (.deb)
#   centos7     centos:7            glibc 2.17  → CentOS 7 系列           (.rpm, no Wayland)
#   centos8     rockylinux:8        glibc 2.28  → CentOS/RHEL ≥8 / Rocky/Alma (.rpm)
#
# Targets without GStreamer ≥1.16 (ubuntu2004, centos7) build without the
# 'pipewire' feature — Wayland screen capture is disabled, X11 still works.
# By default this builds the current native architecture only. CI runs amd64
# and arm64 on matching native runners. Use --use-qemu to allow local cross-arch
# emulation explicitly.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOST_UNAME="$(uname -m)"
case "$HOST_UNAME" in
  x86_64|amd64) DEFAULT_ARCHES="amd64" ;;
  aarch64|arm64) DEFAULT_ARCHES="arm64" ;;
  *) DEFAULT_ARCHES="amd64" ;;
esac
DOCKERFILE="docker/Dockerfile.linux-builder"
OUTPUT_DIR="${OUTPUT_DIR:-dist/linux-matrix}"
CARGO_REGISTRY="${CARGO_REGISTRY:-sparse+https://index.crates.io/}"
RUSTUP_DIST_SERVER="${RUSTUP_DIST_SERVER:-https://static.rust-lang.org}"
RUSTUP_UPDATE_ROOT="${RUSTUP_UPDATE_ROOT:-https://static.rust-lang.org/rustup}"
CARGO_BUILD_JOBS="${CARGO_BUILD_JOBS:-$(nproc)}"
NETWORK_MODE="${NETWORK_MODE:-host}"
PROXY_URL="${PROXY_URL:-}"
NO_CACHE="${NO_CACHE:-0}"
TARGETS_INPUT="${TARGETS:-all}"
ARCHES_INPUT="${ARCHES:-$DEFAULT_ARCHES}"
USE_QEMU="${USE_QEMU:-0}"

ALL_TARGETS=(ubuntu2004 ubuntu2204 uos-v20 kylin-v10-v11 nfschina-desktop centos7 centos8)

usage() {
  cat <<'EOF'
Usage: ./docker-build-linux.sh [options]

Options:
  --target LIST   Comma-separated targets, or "all".
                  Choices: ubuntu2004, ubuntu2204, uos-v20, kylin-v10-v11, nfschina-desktop, centos7, centos8, all
                  Default: all
  --arch LIST     Comma-separated archs. Choices: amd64, arm64. Default: current host arch
  --output DIR    Output directory. Default: dist/linux-matrix
  --proxy URL     http/https/all proxy passed to docker build & run
  --network MODE  Docker network mode for build & run. Default: host
  --no-cache      Pass --no-cache to docker build
  --use-qemu      Allow cross-arch emulation via binfmt/QEMU when host != target
  -h, --help      Show this help

Artifacts land in: <output>/<target>-<arch>/{auroraops-agent, *.deb|*.rpm, *.ldd.txt}
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --target) TARGETS_INPUT="$2"; shift 2 ;;
    --arch) ARCHES_INPUT="$2"; shift 2 ;;
    --output) OUTPUT_DIR="$2"; shift 2 ;;
    --proxy) PROXY_URL="$2"; shift 2 ;;
    --network) NETWORK_MODE="$2"; shift 2 ;;
    --no-cache) NO_CACHE=1; shift ;;
    --use-qemu) USE_QEMU=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

cd "$ROOT_DIR"
command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }

# --- resolve target list ---
if [ "$TARGETS_INPUT" = "all" ]; then
  TARGETS=("${ALL_TARGETS[@]}")
else
  IFS=',' read -r -a TARGETS <<< "$TARGETS_INPUT"
fi
IFS=',' read -r -a ARCHES <<< "$ARCHES_INPUT"

# Per-target base image + features + extra packages
target_config() {
  case "$1" in
    ubuntu2004)
      BASE_IMAGE="ubuntu:20.04"
      # No pipewire/gstreamer (1.16 available but keep X11-only for max compat)
      FEATURES=""
      EXTRA_PKGS_DEB=""
      EXTRA_PKGS_RPM=""
      ;;
    ubuntu2204)
      BASE_IMAGE="ubuntu:22.04"
      # Full Wayland + X11 support.
      FEATURES="pipewire"
      EXTRA_PKGS_DEB=""
      EXTRA_PKGS_RPM=""
      ;;
    uos-v20|nfschina-desktop)
      BASE_IMAGE="debian:11"
      # GStreamer 1.18 available.
      FEATURES="pipewire"
      EXTRA_PKGS_DEB=""
      EXTRA_PKGS_RPM=""
      ;;
    kylin-v10-v11)
      BASE_IMAGE="ubuntu:20.04"
      # Kylin V10 SP1 desktop is glibc 2.31 with GStreamer 1.16.x.
      FEATURES="pipewire"
      EXTRA_PKGS_DEB=""
      EXTRA_PKGS_RPM=""
      ;;
    centos8)
      BASE_IMAGE="rockylinux:8"
      FEATURES="pipewire"
      EXTRA_PKGS_DEB=""
      EXTRA_PKGS_RPM="epel-release"
      ;;
    centos7)
      BASE_IMAGE="centos:7"
      # No pipewire; X11-only for maximum compatibility.
      FEATURES=""
      EXTRA_PKGS_DEB=""
      EXTRA_PKGS_RPM=""
      ;;
    *)
      echo "Unknown target: $1" >&2; exit 2 ;;
  esac
}

arch_to_platform() {
  case "$1" in
    amd64|x86_64) echo "linux/amd64" ;;
    arm64|aarch64) echo "linux/arm64" ;;
    *) echo "Unknown arch: $1" >&2; exit 2 ;;
  esac
}

host_arch="$HOST_UNAME"
platform_to_arch() {
  case "$1" in
    linux/amd64) echo "x86_64" ;;
    linux/arm64) echo "aarch64" ;;
    *) echo "Unknown platform: $1" >&2; exit 2 ;;
  esac
}

ensure_arch_support() {
  local platform="$1"
  local target_arch
  target_arch="$(platform_to_arch "$platform")"
  if [ "$host_arch" = "$target_arch" ]; then
    return 0
  fi
  if [ "$USE_QEMU" != 1 ]; then
    echo "Cross-arch build disabled: host=$host_arch target=$target_arch. Re-run with --use-qemu to allow emulation." >&2
    exit 3
  fi
  if [ "$platform" = "linux/amd64" ]; then
    docker run --privileged --rm tonistiigi/binfmt --install amd64 >/dev/null 2>&1 || true
  fi
  if [ "$platform" = "linux/arm64" ]; then
    docker run --privileged --rm tonistiigi/binfmt --install arm64 >/dev/null 2>&1 || true
  fi
}

mkdir -p "$OUTPUT_DIR"

for target in "${TARGETS[@]}"; do
  target_config "$target"
  for arch in "${ARCHES[@]}"; do
    platform="$(arch_to_platform "$arch")"
    tag="auroraops-agent-builder:${target}-${arch}"
    target_id="${target}-${arch}"
    out_dir="$ROOT_DIR/$OUTPUT_DIR/$target_id"
    mkdir -p "$out_dir"

    echo "============================================================"
    echo " target=$target  arch=$arch  base=$BASE_IMAGE  features='${FEATURES}'"
    echo "============================================================"

    ensure_arch_support "$platform"

    build_args=(
      --platform "$platform"
      --build-arg "BASE_IMAGE=$BASE_IMAGE"
      --build-arg "TARGET_ID=$target_id"
      --build-arg "FEATURES=$FEATURES"
      --build-arg "EXTRA_PKGS_DEB=$EXTRA_PKGS_DEB"
      --build-arg "EXTRA_PKGS_RPM=$EXTRA_PKGS_RPM"
      --build-arg "RUSTUP_DIST_SERVER=$RUSTUP_DIST_SERVER"
      --build-arg "RUSTUP_UPDATE_ROOT=$RUSTUP_UPDATE_ROOT"
      --build-arg "CARGO_REGISTRY=$CARGO_REGISTRY"
    )
    [ "$NO_CACHE" = 1 ] && build_args+=(--no-cache)
    [ -n "$NETWORK_MODE" ] && build_args+=(--network "$NETWORK_MODE")
    if [ -n "$PROXY_URL" ]; then
      build_args+=(
        --build-arg "http_proxy=$PROXY_URL"
        --build-arg "https_proxy=$PROXY_URL"
        --build-arg "HTTP_PROXY=$PROXY_URL"
        --build-arg "HTTPS_PROXY=$PROXY_URL"
        --build-arg "all_proxy=$PROXY_URL"
        --build-arg "ALL_PROXY=$PROXY_URL"
      )
    fi

    docker build -f "$DOCKERFILE" -t "$tag" "${build_args[@]}" .

    run_args=(--rm --platform "$platform"
      -e "CARGO_BUILD_JOBS=$CARGO_BUILD_JOBS"
      -v "$out_dir:/output/$target_id")
    [ -n "$NETWORK_MODE" ] && run_args+=(--network "$NETWORK_MODE")
    if [ -n "$PROXY_URL" ]; then
      run_args+=(
        -e "http_proxy=$PROXY_URL" -e "https_proxy=$PROXY_URL"
        -e "HTTP_PROXY=$PROXY_URL" -e "HTTPS_PROXY=$PROXY_URL"
        -e "all_proxy=$PROXY_URL"  -e "ALL_PROXY=$PROXY_URL"
        -e "NO_PROXY=localhost,127.0.0.1,::1"
        -e "no_proxy=localhost,127.0.0.1,::1"
      )
    fi

    docker run "${run_args[@]}" "$tag"
  done
done

echo
echo "==> Artifacts under $OUTPUT_DIR"
find "$OUTPUT_DIR" -mindepth 2 -maxdepth 2 -type f -printf '  %p\n' | sort
