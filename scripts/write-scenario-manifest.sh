#!/usr/bin/env bash
# Эталон файла, который уходит клиенту: размер и sha256 в scenarios/manifest.json.
# Пишется после сборки fastagent.exe (см. build-fast-agent.sh). Сверка — в
# стартовом блоке оператора (R5): пока файл не совпал с эталоном, диагностика
# заблокирована.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXE="${1:-$ROOT/FastAgent/fastagent.exe}"
OUT="$ROOT/scenarios/manifest.json"

if [[ ! -f "$EXE" ]]; then
  echo "[ERROR] нет $EXE — сначала соберите: bash scripts/build-fast-agent.sh fast" >&2
  exit 1
fi
mkdir -p "$(dirname "$OUT")"
SIZE=$(stat -c %s "$EXE")
SUM=$(sha256sum "$EXE" | awk '{print $1}')
NAME=$(basename "$EXE")

python3 - "$OUT" "$NAME" "$SIZE" "$SUM" <<'PY'
import json, sys
out, name, size, sha = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
data = {"guest_exe": {"name": name, "size": size, "sha256": sha}}
json.dump(data, open(out, "w"), ensure_ascii=False, indent=2)
open(out, "a").write("\n")
print(f"[+] {out}: {name}, {size} Б, sha256 {sha[:8]}…")
PY
