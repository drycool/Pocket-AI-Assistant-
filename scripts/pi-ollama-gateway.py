#!/usr/bin/env python3
"""
Ollama Gateway для Raspberry Pi (работает 24/7).

Назначение: USB-флешка (агент) подключается к Pi, а Pi уже сам:
  1. проверяет доступность gpu2 (сервер с Ollama);
  2. если gpu2 выключен/спит - будит его по Wake-on-LAN (MAC 04:d9:f5:ae:f1:76);
  3. ждёт поднятия Ollama (до WAKE_RETRIES x 5 сек);
  4. прогревает модель (загружает в VRAM), чтобы первый запрос агента был быстрым;
  5. прозрачно ретранслирует трафик на gpu2:11434.

Безопасность:
  - LAN (192.168.0.0/16, 10/8, 172.16/12) и Tailscale (100.64.0.0/10) -
    доверенные сети, работают без пароля.
  - Все остальные подключения (интернет через проброс порта) требуют
    заголовок "Authorization: Bearer <AUTH_TOKEN>". Агент шлёт его
    автоматически из api_key в Data/config.json.
  - Проверка токена выполняется ДО пробуждения gpu2: внешние зонды
    не смогут включить сервер.

Переменные окружения (опционально, есть значения по умолчанию):
  GPU2_HOST   - адрес сервера с Ollama  (по умолчанию 192.168.0.87)
  GPU2_PORT   - порт Ollama             (по умолчанию 11434)
  GPU2_MAC    - MAC gpu2 для Wake-on-LAN (по умолчанию 04:d9:f5:ae:f1:76)
  LISTEN_PORT - порт на Pi              (по умолчанию 11434)
  MODEL       - модель для прогрева     (по умолчанию qwen3:8b)
  WAKE_RETRIES- сколько раз ждать по 5с (по умолчанию 60 => до 5 минут)
  PRELOAD     - прогревать модель после пробуждения (1/0, по умолчанию 1)
  AUTH_TOKEN  - токен для внешних подключений (пусто = не требовать)
"""
import ipaddress
import json
import logging
import os
import socket
import socketserver
import threading
import time
import urllib.request

GPU2_HOST = os.environ.get("GPU2_HOST", "192.168.0.87")
GPU2_PORT = int(os.environ.get("GPU2_PORT", "11434"))
GPU2_MAC = os.environ.get("GPU2_MAC", "04:d9:f5:ae:f1:76")
LISTEN_PORT = int(os.environ.get("LISTEN_PORT", "11434"))
MODEL = os.environ.get("MODEL", "qwen3:8b")
WAKE_RETRIES = int(os.environ.get("WAKE_RETRIES", "60"))
PRELOAD = os.environ.get("PRELOAD", "1") == "1"
AUTH_TOKEN = os.environ.get("AUTH_TOKEN", "")

LOG = logging.getLogger("ollama-gateway")

# Сериализуем пробуждение+прогрев: параллельные подключения не дублируют WoL.
WAKE_LOCK = threading.Lock()

# Доверенные подсети (работают без токена): LAN + Tailscale.
TRUSTED_NETS = [
    ipaddress.ip_network("192.168.0.0/16"),
    ipaddress.ip_network("10.0.0.0/8"),
    ipaddress.ip_network("172.16.0.0/12"),
    ipaddress.ip_network("100.64.0.0/10"),   # Tailscale
    ipaddress.ip_network("127.0.0.0/8"),
]


def is_trusted(ip_str):
    try:
        ip = ipaddress.ip_address(ip_str)
    except ValueError:
        return False
    return any(ip in net for net in TRUSTED_NETS)


def read_request_head(sock, max_bytes=16384, timeout=6.0):
    """Читает начало HTTP-запроса клиента (до конца заголовков)."""
    sock.settimeout(timeout)
    buf = b""
    while len(buf) < max_bytes:
        try:
            chunk = sock.recv(4096)
        except (socket.timeout, OSError):
            break
        if not chunk:
            break
        buf += chunk
        if b"\r\n\r\n" in buf or b"\n\n" in buf:
            break
    return buf


def auth_ok(head_bytes):
    """True, если в заголовках есть Authorization: Bearer <AUTH_TOKEN>."""
    text = head_bytes.decode("utf-8", "ignore")
    for line in text.splitlines():
        if line.lower().lstrip().startswith("authorization:"):
            value = line.split(":", 1)[1].strip()
            parts = value.split(None, 1)
            if len(parts) == 2 and parts[0].lower() == "bearer" and parts[1].strip() == AUTH_TOKEN:
                return True
            return False
    return False


def gpu2_up(timeout=2.0):
    try:
        with socket.create_connection((GPU2_HOST, GPU2_PORT), timeout=timeout):
            return True
    except OSError:
        return False


def wake_gpu2():
    """Wake-on-LAN: 6x0xFF + 16 повторов MAC, широковещательный UDP :9."""
    mac = bytes(int(x, 16) for x in GPU2_MAC.replace("-", ":").split(":"))
    packet = b"\xff" * 6 + mac * 16
    for bcast in ("255.255.255.255", "192.168.0.255"):
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
                s.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
                s.sendto(packet, (bcast, 9))
            LOG.info("WoL-пакет отправлен на %s:9 (MAC %s)", bcast, GPU2_MAC)
        except OSError as e:
            LOG.warning("WoL на %s не удался: %s", bcast, e)


def wait_gpu2():
    """Пробуждение gpu2 и ожидание подъёма Ollama. True - если пришлось будить."""
    if gpu2_up():
        return False
    LOG.info("gpu2 (%s:%d) не отвечает - пробуждаю...", GPU2_HOST, GPU2_PORT)
    wake_gpu2()
    for attempt in range(1, WAKE_RETRIES + 1):
        time.sleep(5)
        if gpu2_up():
            LOG.info("gpu2 поднялся (попытка %d)", attempt)
            return True
    LOG.error("gpu2 не поднялся за %d c - проверьте WoL/питание", WAKE_RETRIES * 5)
    return False


def preload_model():
    """Прогрев модели: POST /api/generate с одним полем model (30-90 c)."""
    try:
        req = urllib.request.Request(
            "http://%s:%d/api/generate" % (GPU2_HOST, GPU2_PORT),
            data=json.dumps({"model": MODEL}).encode("utf-8"),
            headers={"Content-Type": "application/json"},
        )
        with urllib.request.urlopen(req, timeout=240) as resp:
            resp.read()
        LOG.info("Модель %s прогрета (загружена в VRAM)", MODEL)
    except Exception as e:
        LOG.warning("Прогрев модели не удался: %s", e)


def pump(src, dst, tag):
    """Перекачка байтов в одну сторону."""
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except OSError:
        pass
    finally:
        try:
            dst.shutdown(socket.SHUT_WR)
        except OSError:
            pass


class RelayHandler(socketserver.BaseRequestHandler):
    def handle(self):
        peer = self.client_address[0]
        LOG.info("Подключение от %s", peer)

        # --- Авторизация внешних подключений (интернет/проброс порта) ---
        head = b""
        if AUTH_TOKEN and not is_trusted(peer):
            head = read_request_head(self.request)
            if not auth_ok(head):
                LOG.warning("401: внешнее подключение %s без валидного токена", peer)
                try:
                    self.request.sendall(
                        b"HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n")
                except OSError:
                    pass
                self.request.close()
                return
            LOG.info("Авторизация внешнего подключения %s: OK", peer)

        # --- Пробуждение gpu2 и прогрев модели ---
        with WAKE_LOCK:
            woken = wait_gpu2()
            if not woken and not gpu2_up():
                LOG.error("gpu2 недоступен - разрыв соединения с %s", peer)
                self.request.close()
                return
            if PRELOAD and woken:
                preload_model()

        # --- Прозрачная ретрансляция ---
        try:
            up = socket.create_connection((GPU2_HOST, GPU2_PORT), timeout=10)
        except OSError as e:
            LOG.error("Не удалось подключиться к gpu2: %s", e)
            self.request.close()
            return
        if head:
            try:
                up.sendall(head)
            except OSError:
                up.close()
                self.request.close()
                return
        t1 = threading.Thread(target=pump, args=(self.request, up, "c->s"), daemon=True)
        t2 = threading.Thread(target=pump, args=(up, self.request, "s->c"), daemon=True)
        t1.start()
        t2.start()
        t1.join()
        t2.join()
        up.close()
        LOG.info("Соединение с %s закрыто", peer)


class ThreadedServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
    allow_reuse_address = True
    daemon_threads = True


def main():
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
    )
    LOG.info("Ollama Gateway: 0.0.0.0:%d -> %s:%d (MAC %s, модель %s, preload=%s, auth=%s)",
             LISTEN_PORT, GPU2_HOST, GPU2_PORT, GPU2_MAC, MODEL, PRELOAD,
             "вкл" if AUTH_TOKEN else "выкл")
    with ThreadedServer(("0.0.0.0", LISTEN_PORT), RelayHandler) as srv:
        srv.serve_forever()


if __name__ == "__main__":
    main()
