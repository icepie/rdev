#!/usr/bin/env bash
set -euo pipefail

systemctl disable --now auroraops-agent.service 2>/dev/null || true
rm -f /etc/systemd/system/auroraops-agent.service
systemctl daemon-reload

echo "AuroraOps agent service removed."
echo "Configuration is preserved at /etc/auroraops/agent-config.json."
