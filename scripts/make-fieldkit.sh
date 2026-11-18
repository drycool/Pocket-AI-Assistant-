#!/usr/bin/env bash
# Сборка комплекта FieldKit/ на флешке: бинарники, образец конфига, документация,
# тесты и контрольные суммы. Ключи НЕ копируются — только config.fast.example.json.
#
# Использование:
#   bash scripts/make-fieldkit.sh
#
# Комплект пересобирается поверх FieldKit/ (существующие файлы заменяются).
# Бинарники должны быть собраны заранее:
#   bash scripts/build-fast-agent.sh fast
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FA="$ROOT/FastAgent"
KIT="$ROOT/FieldKit"
STAMP="$(date +%Y-%m-%d_%H-%M)"

if [[ ! -f "$FA/fastagent.exe" ]]; then
  echo "[ERROR] Нет $FA/fastagent.exe — сначала: bash scripts/build-fast-agent.sh fast" >&2
  exit 1
fi
if [[ ! -f "$FA/fastagent" ]]; then
  echo "[!] Нет $FA/fastagent (linux/arm64) — комплект будет только для Windows." >&2
fi

echo "[*] Комплект: $KIT (сборка $STAMP)"
mkdir -p "$KIT/tests" "$KIT/scripts"

# --- бинарники ---
cp -f "$FA/fastagent.exe" "$KIT/fastagent.exe"
[[ -f "$FA/fastagent" ]] && cp -f "$FA/fastagent" "$KIT/fastagent-linux-arm64"
[[ -f "$FA/fastagent-linux-amd64" ]] && cp -f "$FA/fastagent-linux-amd64" "$KIT/fastagent-linux-amd64"
echo "[+] fastagent.exe + fastagent-linux-arm64 + fastagent-linux-amd64"

# --- конфиг-образец (без ключа) ---
cp -f "$ROOT/Data/config.fast.example.json" "$KIT/config.fast.example.json"
echo "[+] config.fast.example.json"

# --- документация ---
cp -f "$ROOT/README.md"                    "$KIT/README.md"
cp -f "$FA/README.md"                      "$KIT/FastAgent-README.md"
cp -f "$FA/FIELD-TEST.md"                  "$KIT/FIELD-TEST.md"
cp -f "$ROOT/RUN-FIELD-TEST.txt"           "$KIT/RUN-FIELD-TEST.txt"
echo "[+] README.md, FastAgent-README.md, FIELD-TEST.md, RUN-FIELD-TEST.txt"

# --- тесты и прогонщик ---
cp -f "$ROOT"/tests/field-*.txt "$KIT/tests/"
cp -f "$ROOT/scripts/field-test-fastagent.bat" "$KIT/scripts/field-test-fastagent.bat"
echo "[+] tests/ ($(ls "$KIT"/tests/field-*.txt | wc -l) сценариев) + scripts/field-test-fastagent.bat"

# --- README-FIRST: порядок действий и что прислать ---
cat > "$KIT/README-FIRST.txt" <<EOF
КОМПЛЕКТ FIELDKIT — сборка $STAMP
=================================================

Это готовый набор для тестирования FastAgent (этап 10: инструменты и MCP).
Ключей в комплекте НЕТ: на флешке используется рабочий Data\config.fast.json
с ключом (он в .gitignore и в FieldKit не копируется).

ПОРЯДОК ДЕЙСТВИЙ
----------------
1. Вставить флешку admin-stick в гостевой Windows-ПК.
   SmartScreen: «Подробнее → Выполнить в любом случае».
2. Запустить из КОРНЯ флешки (не из FieldKit):
   START.bat (правый клик → «Запуск от имени администратора») → пункт [7].
   Это прогонит 8 сессий, обе самопроверки и -ask, соберёт один отчёт.
3. Отдать сводный отчёт:
   Data\logs\field-test-<штамп>.txt  (главный файл)
   Data\logs\field-L*-*.txt, field-ask-*.txt, field-policy-*.txt, field-selftest-*.txt
   Data\logs\actions_*.log

ЧТО ДОЛЖНО БЫТЬ (кратко)
------------------------
- [policy]   26/26 OK
- [selftest] три блока 6/6 OK (L0, L2, L3)
- L1-tools:  [tool] read_file / file_glob_search / grep_search → OK, без y/n
- L2-tools:  write_file → y → «записано N байт»; n → «оператор отказался»;
             запись в C:\Windows → «системный каталог — запись запрещена политикой»
- [ask]:     L1 read_file → OK; L2 write_file → «нужно подтверждение оператора,
             но сессия неинтерактивная»
- L0: [BLOCK] L0: команда не выполняется
- L2-blk: живой [BLOCK] на «wevtutil cl System»
- sfc/dism: итоги видны, «#< CLIXML» нет, «[timeout]»/«Error: 740» нет

ПОДРОБНО
--------
RUN-FIELD-TEST.txt (этот комплект) и FIELD-TEST.md (полный чек-лист).
Разбор сессий: FastAgent-README.md.

СОСТАВ
------
fastagent.exe                 windows/amd64 — главный бинарник
fastagent-linux-arm64         отладка на Pi5 (aarch64), не обязателен на Windows
fastagent-linux-amd64         оффлайн-путь: загрузочная Ubuntu на x86_64 (REMOTE-DIAG §8)
config.fast.example.json      образец: скопировать в Data\config.fast.json + ключ
tests/                        входные файлы 8 полевых сессий
scripts/field-test-fastagent.bat  прогонщик (из корня флешки — START.bat [7])
SHA256SUMS.txt                контрольные суммы (проверка: sha256sum -c)
EOF
echo "[+] README-FIRST.txt"

# --- контрольные суммы ---
(
  cd "$KIT"
  sha256sum fastagent.exe config.fast.example.json README.md FastAgent-README.md \
            FIELD-TEST.md RUN-FIELD-TEST.txt README-FIRST.txt tests/*.txt \
            scripts/field-test-fastagent.bat > SHA256SUMS.txt 2>/dev/null || true
  [[ -f fastagent-linux-arm64 ]] && sha256sum fastagent-linux-arm64 >> SHA256SUMS.txt
  [[ -f fastagent-linux-amd64 ]] && sha256sum fastagent-linux-amd64 >> SHA256SUMS.txt
)
echo "[+] SHA256SUMS.txt"

echo
echo "[OK] FieldKit готов:"
find "$KIT" -type f | sort | sed "s|$ROOT/||"
echo
du -sh "$KIT"
