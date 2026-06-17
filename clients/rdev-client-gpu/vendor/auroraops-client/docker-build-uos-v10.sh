#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCKERFILE="${DOCKERFILE:-docker/Dockerfile.uos-v10}"
BASE_IMAGE="${BASE_IMAGE:-macrosan/kylin:v10-sp3-2403}"
IMAGE_NAME="${IMAGE_NAME:-auroraops-agent-uos-v10-builder}"
OUTPUT_DIR="${OUTPUT_DIR:-dist/uos-v10}"
APT_SOURCE_URL="${APT_SOURCE_URL:-http://archive.debian.org/debian}"
APT_SECURITY_URL="${APT_SECURITY_URL:-http://archive.debian.org/debian-security}"
CARGO_REGISTRY="${CARGO_REGISTRY:-sparse+https://mirrors.ustc.edu.cn/crates.io-index/}"
RUSTUP_DIST_SERVER="${RUSTUP_DIST_SERVER:-https://mirrors.ustc.edu.cn/rust-static}"
RUSTUP_UPDATE_ROOT="${RUSTUP_UPDATE_ROOT:-https://mirrors.ustc.edu.cn/rust-static/rustup}"
CARGO_BUILD_JOBS="${CARGO_BUILD_JOBS:-$(nproc)}"
PLATFORM="${PLATFORM:-}"
NO_CACHE="${NO_CACHE:-0}"
NETWORK_MODE="${NETWORK_MODE:-host}"
PROXY_URL="${PROXY_URL:-}"

usage() {
  cat <<'EOF'
Usage: ./docker-build-uos-v10.sh [options]

Build AuroraOps Agent in a Kylin/UOS-compatible container and export the
binary plus a deb/rpm package into dist/uos-v10.

Options:
  --base IMAGE       Base image. Default: macrosan/kylin:v10-sp3-2403
  --platform VALUE   Docker platform, for example linux/amd64 or linux/arm64
  --output DIR       Output directory. Default: dist/uos-v10
  --network VALUE    Docker network mode. Default: host
  --proxy URL        Proxy for docker build/run, for example http://127.0.0.1:12333
  --no-cache         Build Docker image without cache
  -h, --help         Show this help

Environment:
  APT_SOURCE_URL     Debian fallback archive URL. Default: http://archive.debian.org/debian
  APT_SECURITY_URL   Debian fallback security archive URL. Default: http://archive.debian.org/debian-security
  CARGO_REGISTRY     Cargo sparse registry mirror. Default: USTC
  CARGO_BUILD_JOBS   Rust build parallelism. Default: nproc
  NETWORK_MODE       Docker network mode. Default: host
  PROXY_URL          Proxy URL passed as http_proxy/https_proxy/all_proxy

Examples:
  ./docker-build-uos-v10.sh
  ./docker-build-uos-v10.sh --platform linux/amd64
  ./docker-build-uos-v10.sh --proxy http://127.0.0.1:12333
  ./docker-build-uos-v10.sh --base debian:buster
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base)
      BASE_IMAGE="$2"
      shift 2
      ;;
    --platform)
      PLATFORM="$2"
      shift 2
      ;;
    --output)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --network)
      NETWORK_MODE="$2"
      shift 2
      ;;
    --proxy)
      PROXY_URL="$2"
      shift 2
      ;;
    --no-cache)
      NO_CACHE=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

cd "$ROOT_DIR"

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is required." >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"

build_args=(
  --build-arg "BASE_IMAGE=$BASE_IMAGE"
  --build-arg "APT_SOURCE_URL=$APT_SOURCE_URL"
  --build-arg "APT_SECURITY_URL=$APT_SECURITY_URL"
  --build-arg "RUSTUP_DIST_SERVER=$RUSTUP_DIST_SERVER"
  --build-arg "RUSTUP_UPDATE_ROOT=$RUSTUP_UPDATE_ROOT"
  --build-arg "CARGO_REGISTRY=$CARGO_REGISTRY"
)
if [ -n "$PLATFORM" ]; then
  build_args+=(--platform "$PLATFORM")
fi
if [ "$NO_CACHE" = 1 ]; then
  build_args+=(--no-cache)
fi
if [ -n "$NETWORK_MODE" ]; then
  build_args+=(--network "$NETWORK_MODE")
fi
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

echo "==> Building image: $IMAGE_NAME"
echo "    Dockerfile: $DOCKERFILE"
echo "    Base image: $BASE_IMAGE"
echo "    Output dir: $OUTPUT_DIR"
echo "    Network: $NETWORK_MODE"
if [ -n "$PROXY_URL" ]; then
  echo "    Proxy: $PROXY_URL"
fi
docker build -f "$DOCKERFILE" -t "$IMAGE_NAME" "${build_args[@]}" .

run_args=(--rm -e "CARGO_BUILD_JOBS=$CARGO_BUILD_JOBS" -v "$ROOT_DIR/$OUTPUT_DIR:/output")
if [ -n "$PLATFORM" ]; then
  run_args+=(--platform "$PLATFORM")
fi
if [ -n "$NETWORK_MODE" ]; then
  run_args+=(--network "$NETWORK_MODE")
fi
if [ -n "$PROXY_URL" ]; then
  run_args+=(
    -e "http_proxy=$PROXY_URL"
    -e "https_proxy=$PROXY_URL"
    -e "HTTP_PROXY=$PROXY_URL"
    -e "HTTPS_PROXY=$PROXY_URL"
    -e "all_proxy=$PROXY_URL"
    -e "ALL_PROXY=$PROXY_URL"
    -e "NO_PROXY=localhost,127.0.0.1,::1"
    -e "no_proxy=localhost,127.0.0.1,::1"
  )
fi

echo "==> Building package in container"
docker run "${run_args[@]}" "$IMAGE_NAME"

echo "==> Artifacts"
find "$OUTPUT_DIR" -maxdepth 1 -type f -printf '  %p\n' | sort
