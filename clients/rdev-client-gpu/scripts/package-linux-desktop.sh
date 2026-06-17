#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
CRATE_DIR="$ROOT/clients/rdev-client-gpu"
DISTRO=${1:-${RDEV_GPU_LINUX_DISTRO:-linux}}
TARGET=${RDEV_GPU_TARGET:-x86_64-unknown-linux-gnu}
ARCH=${RDEV_GPU_ARCH:-$(uname -m)}
ASSET=${RDEV_GPU_ASSET:-rdev-client-gpu-${DISTRO}-${ARCH}}
DIST_DIR=${RDEV_GPU_DIST:-$CRATE_DIR/dist/$ASSET}
FEATURES=${RDEV_GPU_FEATURES:-embedded-rdev-desktop}
RUST_TOOLCHAIN=${RDEV_GPU_RUST_TOOLCHAIN:-stable}
PROXY=${RDEV_GPU_PROXY:-${HTTPS_PROXY:-${https_proxy:-}}}
APT_MIRROR=${RDEV_GPU_APT_MIRROR:-}
CARGO_REGISTRY=${RDEV_GPU_CARGO_REGISTRY:-}
RUSTUP_DIST_SERVER=${RDEV_GPU_RUSTUP_DIST_SERVER:-}
RUSTUP_UPDATE_ROOT=${RDEV_GPU_RUSTUP_UPDATE_ROOT:-}

apply_proxy() {
  [ -n "$PROXY" ] || return 0
  export http_proxy="$PROXY"
  export https_proxy="$PROXY"
  export all_proxy="$PROXY"
  export HTTP_PROXY="$PROXY"
  export HTTPS_PROXY="$PROXY"
  export ALL_PROXY="$PROXY"
  export CARGO_HTTP_PROXY="$PROXY"
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0=http.proxy
  export GIT_CONFIG_VALUE_0="$PROXY"
  echo "Using build proxy: $PROXY" >&2
}

install_debian_deps() {
  export DEBIAN_FRONTEND=noninteractive
  if [ -n "$APT_MIRROR" ] && [ -d /etc/apt ]; then
    . /etc/os-release
    codename=${VERSION_CODENAME:-}
    if [ -n "$codename" ] && [ "${ID:-}" = "ubuntu" ]; then
      cat > /etc/apt/sources.list <<EOF
deb $APT_MIRROR $codename main restricted universe multiverse
deb $APT_MIRROR $codename-updates main restricted universe multiverse
deb $APT_MIRROR $codename-backports main restricted universe multiverse
deb $APT_MIRROR $codename-security main restricted universe multiverse
EOF
    fi
  fi
  pipewire_deps=""
  case ",$FEATURES," in
    *embedded-rdev-desktop-wayland*|*rdev-desktop/pipewire*|*,pipewire,*)
      pipewire_deps="libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev gstreamer1.0-pipewire gstreamer1.0-plugins-base gstreamer1.0-plugins-good pipewire xdg-desktop-portal"
      ;;
  esac
  apt-get update
  apt-get install -y --no-install-recommends \
    ca-certificates curl git build-essential pkg-config nasm yasm python3 make \
    autoconf automake libtool \
    libx11-dev libx11-xcb-dev libxext-dev libxrandr-dev libxfixes-dev libxcomposite-dev libxi-dev libxtst-dev libxv-dev \
    libxcb1-dev libxcb-dri3-dev libdrm-dev libepoxy-dev libdbus-1-dev \
    $pipewire_deps
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
    autoconf automake libtool \
    gcc gcc-c++ clang \
    libX11-devel libxcb-devel libXext-devel libXrandr-devel libXfixes-devel libXcomposite-devel libXi-devel libXtst-devel libXv-devel \
    libdrm-devel libepoxy-devel dbus-devel
  yum clean all || true
}

install_alpine_deps() {
  apk add --no-cache \
    bash ca-certificates curl git build-base pkgconf nasm yasm python3 make \
    autoconf automake libtool \
    libx11-dev libxcb-dev libxext-dev libxrandr-dev libxfixes-dev libxcomposite-dev libxi-dev libxtst-dev libxv-dev \
    libdrm-dev libepoxy-dev dbus-dev
}

install_arch_deps() {
  pacman -Syu --noconfirm --needed \
    base-devel ca-certificates curl git pkgconf nasm yasm python make \
    autoconf automake libtool \
    libx11 libxext libxrandr libxfixes libxcomposite libxi libxtst libxv \
    libdrm libepoxy dbus
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
      autoconf automake libtool \
      gcc gcc-c++ clang \
      libX11-devel libxcb-devel libXext-devel libXrandr-devel libXfixes-devel libXcomposite-devel libXi-devel libXtst-devel libXv-devel \
      libdrm-devel libepoxy-devel dbus-devel
    dnf clean all || true
  elif command -v apk >/dev/null 2>&1; then
    install_alpine_deps
  elif command -v pacman >/dev/null 2>&1; then
    install_arch_deps
  else
    echo "unsupported package manager" >&2
    exit 1
  fi
}

configure_cargo_mirror() {
  [ -n "$CARGO_REGISTRY" ] || return 0
  mkdir -p "${CARGO_HOME:-$HOME/.cargo}"
  cat > "${CARGO_HOME:-$HOME/.cargo}/config.toml" <<EOF
[source.crates-io]
replace-with = "rdev-mirror"

[source.rdev-mirror]
registry = "$CARGO_REGISTRY"
EOF
}

ensure_rust() {
  [ -n "$RUSTUP_DIST_SERVER" ] && export RUSTUP_DIST_SERVER
  [ -n "$RUSTUP_UPDATE_ROOT" ] && export RUSTUP_UPDATE_ROOT
  if command -v cargo >/dev/null 2>&1 && command -v rustc >/dev/null 2>&1; then
    rustup target add "$TARGET" >/dev/null 2>&1 || true
    configure_cargo_mirror
    return
  fi
  curl -fSL --retry 5 --retry-delay 5 https://sh.rustup.rs -o /tmp/rustup-init.sh
  sh /tmp/rustup-init.sh -y --profile minimal --default-toolchain "$RUST_TOOLCHAIN" --target "$TARGET"
  # shellcheck source=/dev/null
  . "${CARGO_HOME:-$HOME/.cargo}/env"
  configure_cargo_mirror
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

apply_proxy

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

build_suffix=$(printf '%s-%s-%s' "$DISTRO" "$ARCH" "$TARGET" | tr -c 'A-Za-z0-9_.-' '_')
export CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-$CRATE_DIR/target/linux-package-$build_suffix}
export RDEV_DESKTOP_DIST_SUFFIX=${RDEV_DESKTOP_DIST_SUFFIX:-$build_suffix}

git config --global --add safe.directory '*' >/dev/null 2>&1 || true

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
  --features "$FEATURES" \
  --target "$TARGET"

cp "$CARGO_TARGET_DIR/$TARGET/release/rdev-client-gpu" "$DIST_DIR/rdev-client-gpu"
chmod +x "$DIST_DIR/rdev-client-gpu"
"$DIST_DIR/rdev-client-gpu" --version > "$DIST_DIR/VERSION.txt"
{
  echo "asset=$ASSET"
  echo "target=$TARGET"
  echo "distro=$DISTRO"
  echo "arch=$ARCH"
  echo "features=$FEATURES"
  echo "cargo_target_dir=$CARGO_TARGET_DIR"
  echo "native_dist_suffix=$RDEV_DESKTOP_DIST_SUFFIX"
  echo "enable_vaapi=$ENABLE_VAAPI"
  echo "enable_nvenc=$ENABLE_NVENC"
  if command -v ldd >/dev/null 2>&1; then
    echo
    echo "ldd:"
    ldd "$DIST_DIR/rdev-client-gpu" || true
  fi
} > "$DIST_DIR/BUILD_INFO.txt"

echo "$DIST_DIR"
