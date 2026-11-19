#!/usr/bin/env bash
# Диагностическая сессия, которой управляет мозг, а не регламент.
#
#   scripts/diag.sh start "цель сессии"   создать сессию и окно консоли
#   scripts/diag.sh wait [сек]            ждать, пока гость подключится
#   scripts/diag.sh send "read C:\\x.log" --ask "зачем это смотрим" [--timeout 180]
#   scripts/diag.sh out [строк]           хвост вывода консоли как есть
#   scripts/diag.sh status                код сессии, состояние, шаги, путь протокола
#   scripts/diag.sh finish                закрыть сессию, сохранить архив и протокол
#
# Зачем это отдельно от scenarios/*.json. Регламент отвечает на вопрос «что
# проверить в этом классе проблемы» и умеет только это. Живая локализация —
# это цепочка «посмотрел → понял → следующий шаг зависит от того, что увидел»,
# и её нельзя выложить в JSON: следующий шаг выбирается по результату
# предыдущего. Поэтому здесь остаются только РУКИ: постоянное окно консоли
# оператора (tmux + pipe-pane), одна команда за раз, и протокол в markdown,
# куда каждый шаг пишется вместе с причиной. Голова — снаружи.
#
# Что гарантирует безопасность:
#   * каждая команда по-прежнему идёт через подпись и через политику гостя —
#     этот скрипт политику не ослабляет и подтверждений не подставляет;
#   * по умолчанию разрешены только чтение и снимок (read/grep/glob/diag/
#     quick/report/journal/save), а run/put (изменения) отклоняются на стороне
#     скрипта; report — текст в окно клиента, тоже не изменение;
#   * панель вывода дублируется в файл, поэтому ни один ответ не теряется и
#     протокол можно проверить построчно.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA="$ROOT/Data"
LOGS="$DATA/logs"
SESS="fa-diag"
STATE="$DATA/diag-session.env"
mkdir -p "$LOGS"

die() { echo "[ERROR] $*" >&2; exit 1; }
note() { echo "[i] $*"; }

need_state() {
  [[ -f "$STATE" ]] || die "сессия не запущена: scripts/diag.sh start \"цель\""
  # shellcheck disable=SC1090
  . "$STATE"
}

pane_now() { tmux capture-pane -p -J -t "$SESS" 2>/dev/null || true; }

cmd_start() {
  local goal="${1:-диагностика}"
  tmux has-session -t "$SESS" 2>/dev/null && die "окно $SESS уже открыто: сначала scripts/diag.sh finish"
  local ts pane ledger
  ts="$(date +%Y%m%d-%H%M%S)"
  pane="$LOGS/diag-$ts.pane.log"
  ledger="$LOGS/diag-$ts.md"
  : > "$pane"
  cat > "$ledger" <<EOF
# Диагностическая сессия $ts

**Цель:** $goal

Роли: голова — оператор (модель), руки — это окно консоли, политика — гость.
Каждый шаг ниже: зачем смотрели, что отправили, что пришло. Выводы — отдельными
строками «Вывод:» после шага, чтобы доказанное и предположенное не смешивались.

EOF
  tmux new-session -d -s "$SESS" -x 220 -y 60 -c "$ROOT"
  tmux pipe-pane -t "$SESS" -o "cat >> '$pane'"
  tmux send-keys -t "$SESS" './remote.sh new -plain' Enter

  local i code=""
  for i in $(seq 1 60); do
    code="$(grep -o 'Сессия: [0-9a-f]\+' "$pane" 2>/dev/null | tail -1 | awk '{print $2}' || true)"
    [[ -n "$code" ]] && break
    sleep 0.5
  done
  [[ -n "$code" ]] || { pane_now | tail -20; die "сессия не создалась (см. вывод выше)"; }

  {
    printf 'CODE=%q\n' "$code"
    printf 'PANE=%q\n' "$pane"
    printf 'LEDGER=%q\n' "$ledger"
    printf 'TS=%q\n' "$ts"
    printf 'GOAL=%q\n' "$goal"
  } > "$STATE"

  note "сессия: $code"
  note "протокол: $ledger"
  note "билет: $(ls -t "$HOME"/.cache/fastagent/fastagent-"$code".exe 2>/dev/null | head -1 || echo '(ещё не готов)')"
  note "ждать подключения: scripts/diag.sh wait"
}

cmd_wait() {
  need_state
  local limit="${1:-900}" i
  for i in $(seq 1 $((limit * 2))); do
    if grep -q 'Гость на связи' "$PANE" 2>/dev/null; then
      note "гость на связи (сессия $CODE)"
      return 0
    fi
    sleep 0.5
  done
  pane_now | tail -15
  die "гость не подключился за ${limit} с"
}

# Отправка одной команды. --ask пишет в протокол, зачем она; без --ask шаг
# всё равно пишется, но с пометкой «причина не указана» — это видимый долг.
cmd_send() {
  need_state
  [[ $# -ge 1 ]] || die "нужна команда: scripts/diag.sh send \"read C:\\x.log\" --ask \"...\""
  local line="$1"; shift || true
  local ask="причина не указана" timeout=180
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --ask|-a) ask="${2:-}"; shift 2 ;;
      --timeout|-t) timeout="${2:-180}"; shift 2 ;;
      *) die "неизвестный ключ: $1" ;;
    esac
  done

  local verb
  verb="$(printf '%s' "$line" | awk '{print tolower($1)}')"
  case "$verb" in
    read|cat|grep|grep_search|glob|file_glob_search|diag|quick|probe|report|say|journal|log|save|help|\?)
      ;;
    run|cmd|exec|put|upload|fix)
      [[ "${FA_DIAG_ALLOW_CHANGE:-0}" == "1" ]] \
        || die "«$verb» — это изменение; в режиме головы разрешено только чтение. Если действительно нужно: FA_DIAG_ALLOW_CHANGE=1 (и подтверждение всё равно спросит консоль)"
      ;;
    *) die "команда «$verb» не из разрешённых: read/grep/glob/diag/quick/report/journal/save" ;;
  esac

  local off step before
  off="$(wc -c < "$PANE")"
  step=$(( $(grep -c '^### Шаг ' "$LEDGER" 2>/dev/null || true) + 1 ))
  before="$(date +%H:%M:%S)"

  printf '### Шаг %d — %s\n\n**Зачем:** %s\n\n**Команда:** `%s`\n\n' \
    "$step" "$before" "$ask" "$line" >> "$LEDGER"

  tmux send-keys -t "$SESS" -- "$line"
  sleep 0.05
  tmux send-keys -t "$SESS" Enter

  local block
  block="$(python3 - "$PANE" "$off" "$timeout" <<'PY'
import sys, time
pane, off, timeout = sys.argv[1], int(sys.argv[2]), float(sys.argv[3])
marks = ("── результат", "── ОТКАЗ", "неизвестная команда", "нужно подтверждение")
deadline = time.time() + timeout
text = ""
while time.time() < deadline:
    try:
        with open(pane, "rb") as f:
            f.seek(off)
            text = f.read().decode("utf-8", "replace")
    except FileNotFoundError:
        time.sleep(0.2); continue
    idx = -1
    for m in marks:
        j = text.find(m)
        if j >= 0 and (idx < 0 or j < idx):
            idx = j
    if idx >= 0:
        rest = text[idx:]
        k = rest.rfind("fa> ")
        if k >= 0:
            print(rest[:k].rstrip())
            sys.exit(0)
    time.sleep(0.3)
print("[!] ТАЙМАУТ: результата за %.0f с не пришло. Последнее из вывода:" % timeout)
print(text[-3000:])
sys.exit(0)
PY
)"
  printf '```text\n%s\n```\n\n' "$block" >> "$LEDGER"
  printf '%s\n' "$block"
  note "шаг $step записан: $LEDGER"
}

cmd_out() {
  need_state
  pane_now | tail -n "${1:-40}"
}

cmd_status() {
  need_state
  local paired="нет" steps
  grep -q 'Гость на связи' "$PANE" 2>/dev/null && paired="да"
  steps="$(grep -c '^### Шаг ' "$LEDGER" 2>/dev/null || true)"
  echo "сессия:   $CODE"
  echo "гость:    $paired"
  echo "шагов:    $steps"
  echo "протокол: $LEDGER"
  echo "панель:   $PANE"
  echo "цель:     $GOAL"
}

cmd_finish() {
  need_state
  tmux send-keys -t "$SESS" -- 'quit' 2>/dev/null || true
  sleep 0.05
  tmux send-keys -t "$SESS" Enter 2>/dev/null || true
  local i
  for i in $(seq 1 40); do
    grep -q 'сессия закрыта' "$PANE" 2>/dev/null && break
    sleep 0.5
  done
  sleep 0.5
  printf '\n---\n\nСессия закрыта %s.\n' "$(date '+%d.%m.%Y %H:%M:%S')" >> "$LEDGER"
  local arc
  arc="$(ls -t "$LOGS"/remote-"$CODE"-*.txt 2>/dev/null | head -1 || true)"
  tmux kill-session -t "$SESS" 2>/dev/null || true
  note "протокол: $LEDGER"
  [[ -n "$arc" ]] && note "архив:    $arc"
  note "состояние снято: $STATE (файл оставлен для истории; следующий start его перезапишет)"
}

case "${1:-status}" in
  start)  shift || true; cmd_start "${1:-диагностика}" ;;
  wait)   shift || true; cmd_wait "${1:-900}" ;;
  send)   shift; cmd_send "$@" ;;
  out)    shift || true; cmd_out "${1:-40}" ;;
  status) cmd_status ;;
  finish) cmd_finish ;;
  -h|--help|help)
    sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
    ;;
  *) die "неизвестная команда: $1 (см. scripts/diag.sh help)" ;;
esac
