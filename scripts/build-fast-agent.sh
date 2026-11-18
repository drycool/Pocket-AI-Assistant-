#!/usr/bin/env bash
# Сборка FastAgent (и классического агента) с Raspberry Pi под Windows и Linux.
# Использование:  bash scripts/build-fast-agent.sh [all|fast|agent]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="${1:-fast}"

build_fast() {
  echo "[*] FastAgent: linux/arm64 -> FastAgent/fastagent (отладка на Pi, R1-брокер)"
  (cd "$ROOT/FastAgent" && GOOS=linux GOARCH=arm64 go build -o fastagent .)
  echo "[*] FastAgent: windows/amd64 -> FastAgent/fastagent.exe"
  (cd "$ROOT/FastAgent" && GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o fastagent.exe .)
  echo "[*] FastAgent: linux/amd64 -> FastAgent/fastagent-linux-amd64 (оффлайн-загрузка Ubuntu, REMOTE-DIAG §8)"
  (cd "$ROOT/FastAgent" && GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o fastagent-linux-amd64 .)
  ls -la "$ROOT/FastAgent/fastagent" "$ROOT/FastAgent/fastagent.exe" "$ROOT/FastAgent/fastagent-linux-amd64"
  # R5: эталон для стартового блока оператора (размер + sha256 файла клиента)
  bash "$ROOT/scripts/write-scenario-manifest.sh"
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
