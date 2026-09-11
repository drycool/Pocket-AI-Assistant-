#!/usr/bin/env bash
# Сборка FastAgent (и классического агента) с Raspberry Pi под Windows и Linux.
# Использование:  bash scripts/build-fast-agent.sh [all|fast|agent]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="${1:-fast}"

build_fast() {
  echo "[*] FastAgent: linux/arm64 (для отладки на Pi)"
  (cd "$ROOT/FastAgent" && GOOS=linux GOARCH=arm64 go build -o /tmp/fastagent-linux .)
  echo "[*] FastAgent: windows/amd64 -> FastAgent/fastagent.exe"
  (cd "$ROOT/FastAgent" && GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o fastagent.exe .)
  ls -la "$ROOT/FastAgent/fastagent.exe"
}

build_agent() {
  echo "[*] Agent (классический): windows/amd64 -> Agent/agent.exe"
  (cd "$ROOT/Agent" && GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o agent.exe main.go)
  ls -la "$ROOT/Agent/agent.exe"
}

case "$TARGET" in
  fast)  build_fast ;;
  agent) build_agent ;;
  all)   build_fast; build_agent ;;
  *) echo "Использование: $0 [all|fast|agent]"; exit 1 ;;
esac

echo "[OK] Готово."
