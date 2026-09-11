import os
import sys
import json
import argparse
import urllib.request
import urllib.error
from datetime import datetime

# Фиксация путей относительно флешки
BASE_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATA_DIR = os.path.join(BASE_DIR, "Data")
SESSIONS_DIR = os.path.join(DATA_DIR, "sessions")
CONFIG_FILE = os.path.join(DATA_DIR, "config.json")

os.makedirs(SESSIONS_DIR, exist_ok=True)

SYSTEM_PROMPT = (
    "Ты — карманный AI-инженер и диагност систем Windows/Linux. "
    "Твоя задача — анализировать проблемы гостевого ПК, давать точные PowerShell, CMD или Bash команды "
    "и помогать локализовать сбои. Отвечай лаконично и по делу."
)


def call_openai(provider, messages):
    """Стандарт OpenAI /v1/chat/completions (DeepSeek, Groq, OpenRouter, Ollama...)."""
    base_url = provider.get("base_url", "").rstrip("/")
    if not base_url:
        raise RuntimeError(f"пустой base_url для провайдера {provider.get('name')}")
    url = base_url + "/chat/completions"

    body = {"model": provider.get("model"), "messages": messages}
    # reasoning_effort="none" отключает thinking (qwen3/ornith на Ollama 0.33+).
    if provider.get("reasoning_effort"):
        body["reasoning_effort"] = provider["reasoning_effort"]
    if provider.get("max_tokens"):
        body["max_tokens"] = provider["max_tokens"]
    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + provider.get("api_key", ""),
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        if e.code in (429, 503):
            raise RuntimeError(f"лимит/перегрузка (HTTP {e.code})")
        raise RuntimeError(f"HTTP {e.code}: {e.read().decode('utf-8', 'ignore')}")

    choices = data.get("choices") or []
    if not choices:
        raise RuntimeError(f"ответ без choices: {str(data)[:200]}")

    message = choices[0].get("message", {})
    content = (message.get("content") or "").strip()
    if content:
        return content

    # content пуст: у thinking-моделей текст может уйти в reasoning.
    reasoning = (message.get("reasoning") or "").strip()
    if reasoning:
        return "[reasoning] " + reasoning

    if choices[0].get("finish_reason") == "length":
        raise RuntimeError(
            "ответ обрезан по лимиту токенов (finish_reason=length) — "
            "увеличьте max_tokens или задайте reasoning_effort=none"
        )

    return "Пустой ответ от модели."


def call_gemini(provider, messages):
    """Нативный REST Gemini (тип "gemini")."""
    model = provider.get("model", "gemini-2.5-flash")
    url = (
        f"https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent"
        f"?key={provider.get('api_key')}"
    )

    history = [{"role": "user", "parts": [{"text": m["content"]}]} for m in messages[1:]]
    body = {"contents": history}
    if messages and messages[0]["role"] == "system":
        body["systemInstruction"] = {"parts": [{"text": messages[0]["content"]}]}

    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        if e.code in (429, 503):
            raise RuntimeError(f"лимит/перегрузка (HTTP {e.code})")
        raise RuntimeError(f"HTTP {e.code}: {e.read().decode('utf-8', 'ignore')}")

    err = data.get("error")
    if err:
        raise RuntimeError(f"API Error: {err.get('message')}")

    candidates = data.get("candidates") or []
    if candidates:
        parts = candidates[0].get("content", {}).get("parts") or []
        if parts and parts[0].get("text", "").strip():
            return parts[0]["text"]
    return "Пустой ответ от модели."


def call_provider(provider, messages):
    ptype = provider.get("type", "gemini").lower()
    if ptype == "openai":
        return call_openai(provider, messages)
    return call_gemini(provider, messages)


def send_message(providers, messages):
    """Единая абстракция с fallback: при лимите/ошибке переходим на следующий провайдер."""
    errs = []
    for p in providers:
        try:
            return call_provider(p, messages)
        except Exception as e:
            errs.append(f"[{p.get('name')}] {e}")
    raise RuntimeError("все провайдеры недоступны: " + " | ".join(errs))


def load_providers():
    """Читает приоритетный список провайдеров из Data/config.json."""
    env = os.environ.get("GEMINI_API_KEY")
    if env:
        return [{"name": "Gemini-ENV", "type": "gemini", "api_key": env, "model": "gemini-2.5-flash"}]

    if os.path.exists(CONFIG_FILE):
        try:
            with open(CONFIG_FILE, "r", encoding="utf-8") as f:
                cfg = json.load(f)
                valid = [
                    p for p in cfg.get("providers", [])
                    if p.get("api_key", "").strip() and p.get("model", "").strip()
                ]
                if valid:
                    return valid
        except Exception:
            pass

    print("\n[!] В Data/config.json нет списка провайдеров.")
    print("    (Формат: {\"providers\":[{\"name\",\"type\",\"base_url\",\"api_key\",\"model\"}]})")
    api_key = input("Введите Gemini API Key (или другого провайдера): ").strip()
    if not api_key:
        print("[ERROR] Ключ обязателен для работы агента.")
        sys.exit(1)

    save = input("Добавить провайдер (Gemini) в Data/config.json? (y/n): ").strip().lower()
    if save == 'y':
        os.makedirs(os.path.dirname(CONFIG_FILE), exist_ok=True)
        with open(CONFIG_FILE, "w", encoding="utf-8") as f:
            json.dump({"providers": [{"name": "Gemini-Free", "type": "gemini", "api_key": api_key, "model": "gemini-2.5-flash"}]}, f, indent=2)
        print("[OK] Провайдер сохранен в Data/config.json")

    return [{"name": "Gemini-Free", "type": "gemini", "api_key": api_key, "model": "gemini-2.5-flash"}]


class AdminStickAgent:
    def __init__(self, data_path=None):
        self.data_dir = data_path or DATA_DIR
        self.sessions_dir = os.path.join(self.data_dir, "sessions")
        self.config_path = os.path.join(self.data_dir, "config.json")

        os.makedirs(self.sessions_dir, exist_ok=True)

        self.providers = load_providers()
        self.session_id = None
        self.session_file = None
        self.history = []

    def session_menu(self):
        """Простое TUI-меню выбора или восстановления сессии"""
        print("\n=== POCKET AGENT — МЕНЕДЖЕР СЕССИЙ ===")
        print("1. Создать новую сессию диагностики")
        print("2. Продолжить существующую сессию")
        choice = input("Выберите действие (1/2) [1]: ").strip() or "1"

        if choice == "2":
            files = [f for f in os.listdir(self.sessions_dir) if f.endswith(".json")]
            if files:
                print("\nСписок сохраненных сессий на флешке:")
                for idx, f in enumerate(files, 1):
                    print(f" [{idx}] {f}")

                sel = input("\nВыберите номер сессии: ").strip()
                try:
                    selected_file = files[int(sel) - 1]
                    self.session_file = os.path.join(self.sessions_dir, selected_file)
                    self._load_session()
                    return
                except (IndexError, ValueError):
                    print("[!] Неверный выбор. Создаем новую сессию.")

        pc_name = input("Идентификатор ПК (например, Work-PC / Win10-Guest) [Guest-PC]: ").strip() or "Guest-PC"
        timestamp = datetime.now().strftime("%Y-%m-%d_%H-%M")
        self.session_id = f"{timestamp}_{pc_name}"
        self.session_file = os.path.join(self.sessions_dir, f"{self.session_id}.json")

        self.history = [
            {"role": "user", "content": "Данные автодиагностики системы пока не переданы. Готов к работе."},
        ]
        self._save_session()

    def _save_session(self):
        """Атомарное автосохранение истории на USB после каждого ответа"""
        data = {
            "session_id": self.session_id,
            "updated_at": datetime.now().isoformat(),
            "history": self.history,
        }
        tmp = self.session_file + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
        os.replace(tmp, self.session_file)

    def _load_session(self):
        with open(self.session_file, "r", encoding="utf-8") as f:
            data = json.load(f)
            self.session_id = data["session_id"]
            self.history = data["history"]
        print(f"\n[OK] Сессия '{self.session_id}' загружена. Записей в истории: {len(self.history)}")

    def run(self):
        self.session_menu()

        print(f"\n--- АГЕНТ АКТИВЕН (Сессия: {self.session_id}) ---")
        print("Служебные команды: exit / quit — выход\n")

        while True:
            try:
                user_input = input("Вы > ").strip()
                if not user_input:
                    continue

                if user_input.lower() in ["exit", "quit"]:
                    print("\n[OK] Сессия сохранена. Завершение работы.")
                    self._save_session()
                    break

                self.history.append({"role": "user", "content": user_input})
                messages = [{"role": "system", "content": SYSTEM_PROMPT}] + self.history

                print("AI > ", end="", flush=True)
                text = send_message(self.providers, messages)
                print(f"{text}\n")

                self.history.append({"role": "assistant", "content": text})
                self._save_session()

            except KeyboardInterrupt:
                print("\n[!] Прервано пользователем. Контекст сохранен на флешку.")
                self._save_session()
                break
            except Exception as e:
                self.history = self.history[:-1]
                print(f"[ERROR] Ошибка запроса: {e}\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Admin Stick CLI AI Agent")
    parser.add_argument("--data", help="Путь к директории Data", default=DATA_DIR)
    args = parser.parse_args()

    agent = AdminStickAgent(data_path=args.data)
    agent.run()
