#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_NAME="auroraops-client"
VERSION="${VERSION:-0.11.4}"
RELEASE="${RELEASE:-4}"
ARCH="${ARCH:-amd64}"
DIST_DIR="${ROOT_DIR}/dist"
WORK_DIR="${ROOT_DIR}/target/package-native"
BIN_SOURCE="${ROOT_DIR}/target/release/auroraops-agent"
STAGE="${WORK_DIR}/${PACKAGE_NAME}_${VERSION}-${RELEASE}_${ARCH}"

if [ ! -x "${BIN_SOURCE}" ]; then
  echo "missing ${BIN_SOURCE}; build the full release binary first" >&2
  exit 1
fi

rm -rf "${STAGE}"
install -d "${STAGE}/DEBIAN"
install -d "${STAGE}/etc/auroraops"
install -d "${STAGE}/etc/systemd/system"
install -d "${STAGE}/usr/local/bin"
install -d "${STAGE}/opt/auroraops"
install -d "${STAGE}/usr/share/applications"
install -d "${DIST_DIR}"

install -m 0755 "${BIN_SOURCE}" "${STAGE}/usr/local/bin/auroraops-agent"
install -m 0755 "${ROOT_DIR}/auroraops-client-launcher" "${STAGE}/opt/auroraops/auroraops-client-launcher"
install -m 0755 "${ROOT_DIR}/auroraops-client-config" "${STAGE}/opt/auroraops/auroraops-client-config"
install -m 0755 "${ROOT_DIR}/auroraops-uinput-setup" "${STAGE}/opt/auroraops/auroraops-uinput-setup"
install -m 0644 "${ROOT_DIR}/auroraops-agent.desktop" "${STAGE}/usr/share/applications/auroraops-agent.desktop"

cat > "${STAGE}/etc/auroraops/agent-config.json" <<'EOF'
{
  "serverHost": "192.168.200.124:8000",
  "deviceName": "admin-PC",
  "httpBase": "http://192.168.200.124:8000",
  "bindAddress": "127.0.0.1",
  "webPort": 0,
  "tcpAddress": "192.168.200.124:8099",
  "tryVaapi": false,
  "tryNvenc": false,
  "waylandSupport": false,
  "kmsSupport": false,
  "kmsDevice": null,
  "controlDisplayManager": true
}
EOF

cat > "${STAGE}/etc/systemd/system/auroraops-agent.service" <<'EOF'
[Unit]
Description=AuroraOps Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStartPre=-/opt/auroraops/auroraops-uinput-setup
ExecStart=/usr/local/bin/auroraops-agent --service --config /etc/auroraops/agent-config.json --port 18765
Restart=always
RestartSec=5
User=root
WorkingDirectory=/usr/local/bin

[Install]
WantedBy=multi-user.target
EOF

cat > "${STAGE}/DEBIAN/control" <<EOF
Package: ${PACKAGE_NAME}
Version: ${VERSION}-${RELEASE}
Section: utils
Priority: optional
Architecture: ${ARCH}
Maintainer: AuroraOps <opensource@auroraops.local>
Depends: libc6, libgcc-s1 | libgcc1, systemd, curl, xdg-utils, python3, policykit-1 | polkitd, libx11-6, libxext6, libxrandr2, libxfixes3, libxcomposite1, libxi6, libxtst6, libxinerama1, libxcursor1, libxkbcommon0, libwayland-client0, libwayland-cursor0, libdbus-1-3, libssl3 | libssl1.1, libglib2.0-0, libgstreamer1.0-0, libgstreamer-plugins-base1.0-0, libpango-1.0-0, libcairo2, libpangocairo-1.0-0, libavformat62 | libavformat61 | libavformat60 | libavformat59 | libavformat58, libavfilter11 | libavfilter10 | libavfilter9 | libavfilter8 | libavfilter7, libavcodec62 | libavcodec61 | libavcodec60 | libavcodec59 | libavcodec58, libavutil60 | libavutil59 | libavutil58 | libavutil57 | libavutil56, libswscale9 | libswscale8 | libswscale7 | libswscale6 | libswscale5, libswresample6 | libswresample5 | libswresample4 | libswresample3
Recommends: gstreamer1.0-plugins-base, gstreamer1.0-pipewire, libuinput-tools, whiptail | dialog, firefox | chromium | chromium-browser
Replaces: auroraops-agent, auroraops-client
Breaks: auroraops-agent
Description: AuroraOps native remote desktop client
 AuroraOps native client installs the Rust/Weylus agent as a host service,
 keeps it online with systemd, registers this host to AuroraOps Server, and
 bridges terminal and remote desktop control traffic over the AuroraOps TCP
 channel without the KARE desktop wrapper.
EOF

cat > "${STAGE}/DEBIAN/conffiles" <<'EOF'
/etc/auroraops/agent-config.json
EOF

cat > "${STAGE}/DEBIAN/postinst" <<'EOF'
#!/usr/bin/env bash
set -e

if command -v systemctl >/dev/null 2>&1; then
  /opt/auroraops/auroraops-uinput-setup || true
  systemctl daemon-reload || true
  systemctl disable auroraops-agent.service >/dev/null 2>&1 || true
  systemctl enable auroraops-agent.service || true
  systemctl restart auroraops-agent.service || systemctl start auroraops-agent.service || true
fi

exit 0
EOF

cat > "${STAGE}/DEBIAN/prerm" <<'EOF'
#!/usr/bin/env bash
set -e

if [ "${1:-}" = "remove" ] || [ "${1:-}" = "deconfigure" ]; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop auroraops-agent.service || true
  fi
fi

exit 0
EOF

cat > "${STAGE}/DEBIAN/postrm" <<'EOF'
#!/usr/bin/env bash
set -e

if [ "${1:-}" = "purge" ]; then
  rm -f /etc/auroraops/agent-config.json
  rmdir /etc/auroraops 2>/dev/null || true
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi

exit 0
EOF

chmod 0755 "${STAGE}/DEBIAN/postinst" "${STAGE}/DEBIAN/prerm" "${STAGE}/DEBIAN/postrm"
dpkg-deb --build --root-owner-group "${STAGE}" "${DIST_DIR}/${PACKAGE_NAME}_${VERSION}-${RELEASE}_${ARCH}.deb"
