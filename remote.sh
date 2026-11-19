#!/usr/bin/env bash
# Удалённая сессия одним движением: брокер + меню оператора.
#
#   ./remote.sh            брокер (если не запущен) + меню оператора
#   ./remote.sh status     что запущено, где секрет, последний файл-билет
#   ./remote.sh stop       остановить брокер
#   ./remote.sh log        хвост журнала брокера
#   ./remote.sh secret     показать секрет (он же хранится в Data/remote-secret.txt)
#   ./remote.sh ticket [куда]  путь к свежему файлу-билету; с каталогом — скопировать туда
#   ./remote.sh clean [N]  оставить N свежих билетов (по умолчанию 3), старые удалить
#
# Секрет создаётся один раз и лежит в Data/remote-secret.txt (chmod 600);
# передавать его руками не нужно. Пути считаются от каталога этого файла,
# поэтому запускать можно из любого каталога.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PORT="${FASTAGENT_PORT:-8866}"
URL="http://127.0.0.1:${PORT}"
DATA="$ROOT/Data"
LOGS="$DATA/logs"
SECRET_FILE="$DATA/remote-secret.txt"
PID_FILE="$DATA/remote-broker.pid"
BROKER_LOG="$LOGS/broker.log"
TICKETS="$HOME/.cache/fastagent"

mkdir -p "$LOGS" "$TICKETS"

die() { echo "[ERROR] $*" >&2; exit 1; }

find_exe() {
  local c
  for c in "$ROOT/FastAgent/fastagent" "$ROOT/FastAgent/fastagent-linux-arm64" "$ROOT/fastagent.exe"; do
    [[ -f "$c" ]] || continue
    if [[ ! -x "$c" ]]; then chmod +x "$c" 2>/dev/null || true; fi
    [[ -x "$c" ]] && { echo "$c"; return 0; }
  done
  return 1
}

port_busy() { ss -ltn 2>/dev/null | grep -q "127.0.0.1:${PORT} "; }

# PID процесса, который держит порт (своими средствами, без пароля).
port_pid() {
  ss -ltnp 2>/dev/null | awk -v p=":${PORT} " '$0 ~ p { if (match($0, /pid=[0-9]+/)) { s=substr($0, RSTART+4, RLENGTH-4); print s; exit } }'
}

secret() {
  if [[ -s "$SECRET_FILE" ]]; then
    cat "$SECRET_FILE"
    return
  fi
  umask 077
  head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24 > "$SECRET_FILE"
  echo "[+] секрет создан: $SECRET_FILE (chmod 600)" >&2
  cat "$SECRET_FILE"
}

exe() { find_exe || die "нет FastAgent/fastagent — соберите: bash scripts/build-fast-agent.sh fast"; }

broker_running() { port_busy && [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; }

start_broker() {
  local e s pid
  e="$(exe)"; s="$(secret)"

  if port_busy; then
    pid="$(port_pid || true)"
    if [[ -n "$pid" && -r "/proc/$pid/cmdline" ]] && ! tr '\0' ' ' < "/proc/$pid/cmdline" | grep -q -- "-broker-secret ${s} "; then
      echo "[!] на порту ${PORT} висит чужой брокер (другой секрет) — перезапускаю" >&2
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 20); do port_busy || break; sleep 0.1; done
    else
      echo "[=] брокер уже слушает 127.0.0.1:${PORT} (PID ${pid:-?})"
      return 0
    fi
  fi

  echo "[*] запускаю брокер: 127.0.0.1:${PORT}, журнал $BROKER_LOG"
  setsid nohup "$e" -broker -broker-addr "127.0.0.1:${PORT}" \
      -broker-secret "$s" -ticket-exe "$e" >>"$BROKER_LOG" 2>&1 &
  pid=$!
  echo "$pid" > "$PID_FILE"

  local i
  for i in $(seq 50); do
    port_busy && { echo "[OK] брокер поднялся (PID $pid)"; return 0; }
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.1
  done
  echo "[ERROR] брокер не поднялся за 5 с. Журнал:" >&2
  tail -20 "$BROKER_LOG" >&2 || true
  exit 1
}

cmd_status() {
  local pid
  echo "каталог:  $ROOT"
  echo "бинарник: $(find_exe || echo 'НЕТ')"
  echo "секрет:   $SECRET_FILE$([[ -s "$SECRET_FILE" ]] && echo ' (есть)' || echo ' (будет создан)')"
  echo "билеты:   $TICKETS"
  if port_busy; then
    pid="$(port_pid || true)"
    echo "брокер:   слушает 127.0.0.1:${PORT} (PID ${pid:-?}), журнал $BROKER_LOG"
  else
    echo "брокер:   не запущен (поднимется при ./remote.sh)"
  fi
  local last
  last="$(ls -t "$TICKETS"/fastagent-*.exe 2>/dev/null | head -1 || true)"
  [[ -n "$last" ]] && echo "последний файл-билет: $last"
  return 0
}

cmd_stop() {
  local pid
  pid="$(cat "$PID_FILE" 2>/dev/null || true)"
  if [[ -z "$pid" ]] || ! kill -0 "$pid" 2>/dev/null; then
    pid="$(port_pid || true)"
  fi
  if [[ -z "$pid" ]]; then
    echo "[=] брокер не запущен"
    rm -f "$PID_FILE"
    return 0
  fi
  echo "[*] останавливаю брокер (PID $pid)"
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 30); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
  kill -9 "$pid" 2>/dev/null || true
  rm -f "$PID_FILE"
  echo "[OK] остановлен"
}

newest_ticket() { ls -t "$TICKETS"/fastagent-*.exe 2>/dev/null | head -1; }

cmd_ticket() {
  local t dest="${1:-}"
  t="$(newest_ticket || true)"
  if [[ -z "$t" ]]; then
    echo "[!] файлов-билетов ещё нет: они появляются, когда оператор создаёт сессию" >&2
    echo "    запустите: remote   (или $ROOT/remote.sh)" >&2
    exit 1
  fi
  echo "$t"
  [[ -n "$dest" ]] || return 0
  [[ -d "$dest" ]] || die "нет каталога: $dest"
  cp -f "$t" "$dest/" || die "не удалось скопировать в $dest"
  echo "[OK] скопирован: $dest/$(basename "$t")"
}

cmd_clean() {
  local keep="${1:-3}" n i
  [[ "$keep" =~ ^[0-9]+$ ]] || die "сколько оставить? число: remote clean 3"
  mapfile -t all < <(ls -t "$TICKETS"/fastagent-*.exe 2>/dev/null)
  n="${#all[@]}"
  if (( n <= keep )); then
    echo "[=] билетов $n — удалять нечего (оставляю $keep)"
    return 0
  fi
  for (( i = keep; i < n; i++ )); do
    rm -f -- "${all[$i]}" && echo "[-] ${all[$i]}"
  done
  echo "[OK] оставлено $keep из $n"
}

cmd_new() {
  local e s
  e="$(exe)"; s="$(secret)"
  start_broker >/dev/null
  echo "[i] секрет: $SECRET_FILE — руками не нужен"
  echo "[i] приглашение клиенту: «Сохраните файл и запустите двойным щелчком, окно не закрывайте»."
  echo "[i] файл-билет появится в $TICKETS — его и отправляйте в AnyDesk."
  echo
  exec "$e" -remote new -broker-url "$URL" -broker-secret "$s" \
      -ticket-exe "$e" -memory-dir "${MEMORY_DIR:-$HOME/memory}" "$@"
}


usage() {
  cat <<'EOF'
Удалённая диагностика одной командой.

  ./remote.sh            брокер (если не запущен) + меню оператора
  ./remote.sh status     что запущено, где секрет, последний файл-билет
  ./remote.sh stop       остановить брокер
  ./remote.sh log [N]    хвост журнала брокера (по умолчанию 40 строк)
  ./remote.sh secret     показать секрет
  ./remote.sh ticket [куда]  путь к свежему файлу-билету; с каталогом — копия туда
  ./remote.sh clean [N]  оставить N свежих билетов (по умолчанию 3)
  ./remote.sh help       эта справка

Секрет создаётся один раз: Data/remote-secret.txt (chmod 600).
Порт по умолчанию 8866, меняется переменной FASTAGENT_PORT.
Меню оператора после подключения гостя: 2 — диагностика, 6 — кейс (МЕДОК и др.).
EOF
}

case "${1:-new}" in
  new)      shift || true; cmd_new "$@" ;;
  status)   cmd_status ;;
  stop)     cmd_stop ;;
  log)      tail -n "${2:-40}" "$BROKER_LOG" ;;
  secret)   secret; echo ;;
  ticket|copy) shift || true; cmd_ticket "${1:-}" ;;
  clean)    shift || true; cmd_clean "${1:-3}" ;;
  -h|--help|help) usage ;;
  *) die "неизвестная команда: $1 (см. ./remote.sh help)" ;;
esac
