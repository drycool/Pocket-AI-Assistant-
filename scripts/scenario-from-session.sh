#!/usr/bin/env bash
# scenario-from-session.sh — черновик сценария ПОСЛЕ работы, а не до неё.
#
# Сценарии нельзя придумать заранее: пока проблема не найдена и починка не
# проверена, любой «регламент» — это догадка, переодетая в JSON. Этот скрипт
# берёт архив удалённой сессии (Data/logs/remote-<код>-<дата>.txt), вытаскивает
# из него паспорт случая (машина, дата, шаги) и кладёт ЧЕРНОВИК в
# scenarios/learned/. Каталог меню читает только *.json, поэтому черновик
# (.json.draft) не может случайно попасть в меню и выполниться раньше времени.
#
# Заполнить остаётся то, что знает только человек: симптом словами клиента,
# причину и то, что реально помогло. После проверки на месте — переименовать
# .json.draft в .json; тогда кейс появится в меню с меткой «[из практики]».
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/.." && pwd)
outdir="$root/scenarios/learned"

usage() {
	cat >&2 <<'EOF'
Использование:
  scripts/scenario-from-session.sh <архив сессии> [--symptom "словами клиента"] [--title "заголовок"]

Пример:
  scripts/scenario-from-session.sh Data/logs/remote-bd3ffb-20261119-084845.txt \
      --symptom "станция не видит МЕДОК Сервер, остальные работают"

Скрипт НЕ придумывает причину и починку: он оставляет их пустыми и печатает
шаги сессии, по которым их надо восстановить.
EOF
	exit 2
}

[ $# -ge 1 ] || usage
archive=$1
shift
symptom=""
title=""
while [ $# -gt 0 ]; do
	case "$1" in
	--symptom) symptom=${2:-}; shift 2 ;;
	--title) title=${2:-}; shift 2 ;;
	-h|--help) usage ;;
	*) echo "[ERROR] неизвестный аргумент: $1" >&2; usage ;;
	esac
done

[ -f "$archive" ] || { echo "[ERROR] архива нет: $archive" >&2; exit 1; }
mkdir -p "$outdir"

python3 - "$archive" "$symptom" "$title" "$outdir" <<'PY'
import json
import os
import re
import sys
from datetime import datetime

archive, symptom, title, outdir = sys.argv[1:5]
lines = open(archive, encoding="utf-8", errors="replace").read().splitlines()

def header(name):
    for line in lines:
        m = re.match(r"^%s:\s*(.*)$" % re.escape(name), line)
        if m:
            return m.group(1).strip()
    return ""

code = header("Сессия")
host = header("Гость")
start = header("Начало")
date = start.split(" ")[0] if start else datetime.now().strftime("%Y-%m-%d")

# Шаги: строка консоли + строка «вид: ...» после неё. Вывод шага не тащим в
# черновик целиком — он уже лежит в архиве; здесь нужен список, по которому
# человек восстанавливает проверки.
steps = []
cur = None
for line in lines:
    m = re.match(r"^\[(\d\d:\d\d:\d\d)\] fa> (.*)$", line)
    if m:
        cur = {"time": m.group(1), "line": m.group(2), "kind": "", "state": "", "code": ""}
        steps.append(cur)
        continue
    if cur is not None and not cur["kind"]:
        m = re.match(r"^\s+вид: ([^|]+)\| итог: ([^|]+)\| код (\S+)", line)
        if m:
            cur["kind"] = m.group(1).strip()
            cur["state"] = m.group(2).strip()
            cur["code"] = m.group(3).strip()

if not symptom:
    symptom = "TODO: симптом словами клиента"
if not title:
    short = symptom if len(symptom) <= 60 else symptom[:57] + "…"
    title = "Случай %s %s: %s" % (host or "без имени", date, short)

base = "remote-%s-%s" % (code or "no-code", date.replace("-", ""))
path = os.path.join(outdir, base + ".json.draft")
if os.path.exists(path):
    sys.exit("[ERROR] черновик уже есть: %s (не перезаписываю)" % path)

draft = {
    "name": base,
    "title": title,
    "section": "learned",
    "section_title": "Из практики",
    # Паспорт: машина, дата, симптом. По нему кейс узнаётся при рецидиве.
    "knowledge": {
        "host": host,
        "date": date,
        "symptom": symptom,
        "root_cause": "",
        "fix": "",
        "verified": False,
        "session": os.path.basename(archive).rsplit(".", 1)[0],
    },
    # Проверки и починки заполняет человек по фактам сессии. Пустыми они не
    # страшны: .json.draft не читается каталогом меню.
    "checks": [],
    "fixes": [],
    "practice": [],
    "_draft": {
        "инструкция": [
            "1. Заполните checks: те команды, которые реально дали ответ в этой сессии (только чтение).",
            "2. Заполните fixes: то, что реально помогло; recheck — как убедились.",
            "3. В knowledge впишите root_cause и fix; verified=true — ТОЛЬКО после проверки на месте.",
            "4. Переименуйте .json.draft в .json — кейс появится в меню с меткой «[из практики]».",
            "5. Причина и починка не выдумываются: если опыта не было, оставьте пусто и не переименовывайте.",
        ],
        "шаги_сессии": steps,
    },
}

with open(path, "w", encoding="utf-8") as f:
    json.dump(draft, f, ensure_ascii=False, indent=2)
    f.write("\n")

print("[OK] черновик: %s" % path)
print("[i] гость: %s | дата: %s | шагов: %d" % (host or "?", date, len(steps)))
if steps:
    print("\nШаги сессии (по ним восстанавливаются проверки):")
    for s in steps:
        kind = s["kind"] or "?"
        state = s["state"] or "?"
        print("  [%s] %-12s %-10s %s" % (s["time"], kind, state, s["line"]))
print("\nДальше: заполнить checks/fixes/knowledge и переименовать .json.draft в .json.")
PY
