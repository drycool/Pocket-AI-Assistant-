#!/usr/bin/env bash
# ============================================================
#  Установка Ollama Gateway как systemd-сервиса на Raspberry Pi
#  Запуск (один раз, с флешки или с Pi):
#      sudo bash scripts/install-gateway.sh
#  Сервис: ollama-gateway.service — автозапуск при загрузке Pi,
#          перезапуск при сбое. Прослушивает 0.0.0.0:11434.
#
#  Защита внешнего доступа (проброс порта): задайте токен ДО запуска:
#      AUTH_TOKEN=мой-секрет sudo bash scripts/install-gateway.sh
#  LAN и Tailscale работают без токена (доверенные сети).
# ============================================================
set -euo pipefail

SERVICE="ollama-gateway"
SRC="$(cd "$(dirname "$0")" && pwd)/pi-ollama-gateway.py"
DEST="/usr/local/bin/pi-ollama-gateway.py"

if [ "$(id -u)" -ne 0 ]; then
    echo "[ERROR] Запустите с sudo: sudo bash scripts/install-gateway.sh"
    exit 1
fi

cp "$SRC" "$DEST"
chmod +x "$DEST"

cat > "/etc/systemd/system/${SERVICE}.service" <<EOF
[Unit]
Description=Ollama Gateway (wake gpu2 via WoL + relay to Ollama)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/env python3 ${DEST}
Restart=always
RestartSec=5
Environment=GPU2_HOST=192.168.0.87
Environment=GPU2_PORT=11434
Environment=GPU2_MAC=04:d9:f5:ae:f1:76
Environment=MODEL=qwen3:8b
Environment=PRELOAD=1
Environment=WAKE_RETRIES=60
Environment=AUTH_TOKEN=${AUTH_TOKEN:-}

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now "${SERVICE}"
sleep 2
systemctl --no-pager --full status "${SERVICE}" || true

LAN_IP="$(hostname -I | awk '{print $1}')"
echo ""
echo "[OK] Ollama Gateway установлен и запущен:"
echo "     http://${LAN_IP}:11434  (LAN, статический IP Pi)"
echo "     http://100.124.79.91:11434  (Tailscale, удалённо)"
if [ -n "${AUTH_TOKEN:-}" ]; then
    echo "     Внешний доступ: токен включен (AUTH_TOKEN задан)"
else
    echo "     Внешний доступ: БЕЗ токена! Для проброса порта задайте AUTH_TOKEN."
fi
echo ""
echo "     Проверка: curl http://${LAN_IP}:11434/api/tags"
echo "     Логи:     journalctl -u ${SERVICE} -f"
