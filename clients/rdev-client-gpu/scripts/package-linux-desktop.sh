#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
CRATE_DIR="$ROOT/clients/rdev-client-gpu"
DISTRO=${1:-${RDEV_GPU_LINUX_DISTRO:-linux}}
TARGET=${RDEV_GPU_TARGET:-x86_64-unknown-linux-gnu}
ARCH=${RDEV_GPU_ARCH:-$(uname -m)}
ASSET=${RDEV_GPU_ASSET:-rdev-client-gpu-${DISTRO}-${ARCH}-desktop}
DIST_DIR=${RDEV_GPU_DIST:-$CRATE_DIR/dist/$ASSET}
RUST_TOOLCHAIN=${RDEV_GPU_RUST_TOOLCHAIN:-1.85.1}

install_debian_deps() {
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends \
    ca-certificates curl git build-essential pkg-config nasm yasm python3 make \
    libx11-dev libxext-dev libxrandr-dev libxfixes-dev libxcomposite-dev libxi-dev \
    libdrm-dev libepoxy-dev libdbus-1-dev
  rm -rf /var/lib/apt/lists/*
}

install_rhel_deps() {
  if [ -f /etc/centos-release ] && grep -q ' 7\.' /etc/centos-release; then
    sed -i 's|^mirrorlist=|#mirrorlist=|g; s|^#baseurl=http://mirror.centos.org/centos/$releasever|baseurl=http://vault.centos.org/7.9.2009|g' /etc/yum.repos.d/CentOS-*.repo || true
    yum install -y centos-release-scl epel-release || true
    yum install -y devtoolset-10-gcc devtoolset-10-gcc-c++ devtoolset-10-binutils || true
  else
    yum install -y epel-release || true
  fi
  yum install -y \
    ca-certificates curl git make pkgconfig nasm yasm python3 \
    gcc gcc-c++ clang \
    libX11-devel libXext-devel libXrandr-devel libXfixes-devel libXcomposite-devel libXi-devel \
    libdrm-devel libepoxy-devel dbus-devel
  yum clean all || true
}

install_alpine_deps() {
  apk add --no-cache \
    bash ca-certificates curl git build-base pkgconf nasm yasm python3 make \
    libx11-dev libxext-dev libxrandr-dev libxfixes-dev libxcomposite-dev libxi-dev \
    libdrm-dev libepoxy-dev dbus-dev
}

install_deps() {
  if command -v apt-get >/dev/null 2>&1; then
    install_debian_deps
  elif command -v yum >/dev/null 2>&1; then
    install_rhel_deps
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y epel-release || true
    dnf install -y \
      ca-certificates curl git make pkgconf-pkg-config nasm yasm python3 \
      gcc gcc-c++ clang \
      libX11-devel libXext-devel libXrandr-devel libXfixes-devel libXcomposite-devel libXi-devel \
      libdrm-devel libepoxy-devel dbus-devel
    dnf clean all || true
  elif command -v apk >/dev/null 2>&1; then
    install_alpine_deps
  else
    echo "unsupported package manager" >&2
    exit 1
  fi
}

ensure_rust() {
  if command -v cargo >/dev/null 2>&1 && command -v rustc >/dev/null 2>&1; then
    rustup target add "$TARGET" >/dev/null 2>&1 || true
    return
  fi
  curl -fSL --retry 5 --retry-delay 5 https://sh.rustup.rs -o /tmp/rustup-init.sh
  sh /tmp/rustup-init.sh -y --profile minimal --default-toolchain "$RUST_TOOLCHAIN" --target "$TARGET"
  # shellcheck source=/dev/null
  . "${CARGO_HOME:-$HOME/.cargo}/env"
}

activate_legacy_toolchain() {
  if [ -f /opt/rh/devtoolset-10/enable ]; then
    # shellcheck source=/dev/null
    . /opt/rh/devtoolset-10/enable
  elif [ -f /opt/rh/devtoolset-9/enable ]; then
    # shellcheck source=/dev/null
    . /opt/rh/devtoolset-9/enable
  fi
}

if [ "${RDEV_GPU_INSTALL_DEPS:-auto}" != "n" ]; then
  if [ "$(id -u)" -eq 0 ]; then
    install_deps
  else
    echo "Skipping system dependency installation because the current user is not root." >&2
    echo "Install X11/DRM/DBus/pkg-config/nasm/yasm build dependencies first, or run in the CI container matrix." >&2
  fi
fi
ensure_rust
activate_legacy_toolchain

export CARGO_NET_RETRY=${CARGO_NET_RETRY:-5}
export ENABLE_VAAPI=${ENABLE_VAAPI:-n}
export ENABLE_NVENC=${ENABLE_NVENC:-n}
export ENABLE_VULKAN_VIDEO=${ENABLE_VULKAN_VIDEO:-n}
export TMPDIR=${TMPDIR:-/tmp}

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

cargo build \
  --release \
  --manifest-path "$CRATE_DIR/Cargo.toml" \
  --features embedded-rdev-desktop \
  --target "$TARGET"

cp "$CRATE_DIR/target/$TARGET/release/rdev-client-gpu" "$DIST_DIR/rdev-client-gpu"
chmod +x "$DIST_DIR/rdev-client-gpu"
"$DIST_DIR/rdev-client-gpu" --version > "$DIST_DIR/VERSION.txt"
{
  echo "asset=$ASSET"
  echo "target=$TARGET"
  echo "distro=$DISTRO"
  echo "arch=$ARCH"
  if command -v ldd >/dev/null 2>&1; then
    echo
    echo "ldd:"
    ldd "$DIST_DIR/rdev-client-gpu" || true
  fi
} > "$DIST_DIR/BUILD_INFO.txt"

echo "$DIST_DIR"
