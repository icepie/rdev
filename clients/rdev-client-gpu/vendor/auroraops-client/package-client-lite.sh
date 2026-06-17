#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_NAME="auroraops-client"
VERSION="${VERSION:-0.11.4}"
RELEASE="${RELEASE:-2}"
ARCH="${ARCH:-amd64}"
DIST_DIR="${ROOT_DIR}/dist"
WORK_DIR="${ROOT_DIR}/target/package-lite"
BIN_SOURCE="${ROOT_DIR}/agent-service-lite/target/release/auroraops-agent-service"
STAGE="${WORK_DIR}/${PACKAGE_NAME}_${VERSION}-${RELEASE}_${ARCH}"

if [ ! -x "${BIN_SOURCE}" ]; then
  echo "missing ${BIN_SOURCE}; run cargo build in agent-service-lite first" >&2
  exit 1
fi

rm -rf "${STAGE}"
install -d "${STAGE}/DEBIAN"
install -d "${STAGE}/etc/auroraops"
install -d "${STAGE}/etc/systemd/system"
install -d "${STAGE}/opt/auroraops"
install -d "${STAGE}/usr/share/applications"
install -d "${DIST_DIR}"

install -m 0755 "${BIN_SOURCE}" "${STAGE}/opt/auroraops/auroraops-agent"
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
ExecStart=/opt/auroraops/auroraops-agent --config /etc/auroraops/agent-config.json --port 18765
Restart=always
RestartSec=5
User=root
WorkingDirectory=/opt/auroraops

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
Depends: libc6, libgcc-s1 | libgcc1, systemd, curl, xdg-utils, python3, policykit-1 | polkitd
Recommends: whiptail | dialog, firefox | chromium | chromium-browser
Conflicts: auroraops-agent
Replaces: auroraops-agent
Description: AuroraOps lightweight service client
 AuroraOps lightweight client installs the Rust service agent, keeps it
 online with systemd, registers this host to AuroraOps Server, and bridges
 terminal and desktop proxy traffic over the AuroraOps TCP channel.
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
