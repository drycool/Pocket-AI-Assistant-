#!/usr/bin/env bash
# Синхронизация готового FieldKit на флешку admin-stick одной командой.
#
#   bash scripts/sync-flash.sh            найти смонтированную флешку и обновить
#   bash scripts/sync-flash.sh /media/dry/HBCD_PE_X64   явно указать точку монтирования
#
# Что делает: пересобирает комплект (make-fieldkit.sh если нужно), кладёт в корень
# флешки бинарники/сценарии/документы, обновляет bin/agent-fast.bat и копию
# FastAgent/ (запасной путь запуска), затем сверяет SHA256SUMS на флешке.
# Секрет и Data/config.fast.json на флешке не трогаются.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
M="${1:-}"

die() { echo "[ERROR] $*" >&2; exit 1; }

if [[ -z "$M" ]]; then
  for c in /media/"$USER"/* /run/media/"$USER"/* /media/*; do
    [[ -d "$c/scenarios" && -d "$c/Data" ]] && { M="$c"; break; }
  done
fi
[[ -n "$M" ]] || die "флешка не найдена. Подключите её и повторите (или укажите путь)."
[[ -d "$M/Data" ]] || die "$M не похожа на флешку admin-stick (нет Data/)"

echo "[*] комплект: $ROOT/FieldKit → $M"
bash "$ROOT/scripts/make-fieldkit.sh" >/dev/null
K="$ROOT/FieldKit"

# --- корень флешки: бинарники, сценарии, документы, тесты ---
rsync -a --delete "$K/scenarios/" "$M/scenarios/"
rsync -a --delete "$K/tests/" "$M/tests/"
rsync -a "$K"/fastagent.exe "$K"/fastagent-linux-* "$K"/config.fast.example.json \
         "$K"/*.md "$K"/RUN-*.txt "$K"/README-FIRST.txt "$K"/SHA256SUMS.txt "$M/"
rsync -a "$K/scripts/field-test-fastagent.bat" "$M/scripts/"

# --- запасной путь: bin/agent-fast.bat и копия FastAgent/ ---
rsync -a "$ROOT/bin/agent-fast.bat" "$M/bin/"
rsync -a "$ROOT/FastAgent/fastagent.exe" "$M/FastAgent/" 2>/dev/null || true
rsync -a "$ROOT/FastAgent/fastagent" "$ROOT/FastAgent/fastagent-linux-amd64" "$M/FastAgent/" 2>/dev/null || true
[[ -d "$ROOT/scenarios" ]] && rsync -a --delete "$ROOT/scenarios/" "$M/FastAgent/scenarios/"

sync

# --- сверка: каждая строка SHA256SUMS должна сойтись на флешке ---
echo "[*] проверка контрольных сумм на флешке"
bad=0
while read -r sum name; do
  [[ -f "$M/$name" ]] || { echo "  НЕТ: $name"; bad=$((bad+1)); continue; }
  got="$(sha256sum "$M/$name" | cut -d' ' -f1)"
  [[ "$got" == "$sum" ]] || { echo "  РАСХОЖДЕНИЕ: $name"; bad=$((bad+1)); }
done < "$K/SHA256SUMS.txt"
[[ "$bad" == 0 ]] || die "проверка не прошла: $bad проблем — флешку НЕ отключать, разобраться"

echo "[OK] флешка обновлена и сверена: $M"
echo "     снять безопасно: udisksctl unmount -b /dev/sdX1"
