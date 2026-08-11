import os
import sys
import json
import argparse
from datetime import datetime

# Фиксация путей относительно флешки
BASE_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATA_DIR = os.path.join(BASE_DIR, "Data")
SESSIONS_DIR = os.path.join(DATA_DIR, "sessions")
CONFIG_FILE = os.path.join(DATA_DIR, "config.json")

os.makedirs(SESSIONS_DIR, exist_ok=True)

try:
    import google.generativeai as genai
except ImportError:
    print("[ERROR] Не найден модуль google-generativeai.")
    print("Установите его: pip install google-generativeai")
    sys.exit(1)


class AdminStickAgent:
    def __init__(self, data_path=None):
        self.data_dir = data_path or DATA_DIR
        self.sessions_dir = os.path.join(self.data_dir, "sessions")
        self.config_path = os.path.join(self.data_dir, "config.json")

        os.makedirs(self.sessions_dir, exist_ok=True)

        self.api_key = self._get_api_key()
        genai.configure(api_key=self.api_key)

        # Современная быстрая модель
        self.model = genai.GenerativeModel("gemini-2.5-flash")
        self.session_id = None
        self.session_file = None
        self.history = []

    def _get_api_key(self):
        """Получение ключа из ENV или локального config.json на флешке"""
        api_key = os.environ.get("GEMINI_API_KEY")
        if api_key:
            return api_key

        if os.path.exists(self.config_path):
            try:
                with open(self.config_path, "r", encoding="utf-8") as f:
                    cfg = json.load(f)
                    if cfg.get("GEMINI_API_KEY"):
                        return cfg["GEMINI_API_KEY"]
            except Exception:
                pass

        print("\n[!] GEMINI_API_KEY не найден ни в ENV, ни в Data/config.json.")
        api_key = input("Введите ваш Gemini API Key: ").strip()

        if not api_key:
            print("[ERROR] Ключ обязателен для работы агента.")
            sys.exit(1)

        save = input("Сохранить ключ в Data/config.json на флешке? (y/n): ").strip().lower()
        if save == 'y':
            os.makedirs(os.path.dirname(self.config_path), exist_ok=True)
            with open(self.config_path, "w", encoding="utf-8") as f:
                json.dump({"GEMINI_API_KEY": api_key}, f, indent=2)
            print("[OK] Ключ сохранен в Data/config.json")

        return api_key

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

        # Создание новой сессии
        pc_name = input("Идентификатор ПК (например, Work-PC / Win10-Guest) [Guest-PC]: ").strip() or "Guest-PC"
        timestamp = datetime.now().strftime("%Y-%m-%d_%H-%M")
        self.session_id = f"{timestamp}_{pc_name}"
        self.session_file = os.path.join(self.sessions_dir, f"{self.session_id}.json")

        system_prompt = (
            "Ты — карманный AI-инженер и диагност систем Windows/Linux. "
            "Твоя задача — анализировать проблемы гостевого ПК, давать точные PowerShell, CMD или Bash команды "
            "и помогать локализовать сбои. Отвечай лаконично и по делу."
        )

        self.history = [
            {"role": "user", "parts": [system_prompt]},
            {"role": "model", "parts": ["Принято. Я готов к работе с этим компьютером."]}
        ]
        self._save_session()

    def _save_session(self):
        """Атомарное автосохранение истории на USB после каждого ответа"""
        data = {
            "session_id": self.session_id,
            "updated_at": datetime.now().isoformat(),
            "history": self.history
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

        # Конвертируем историю в формат для SDK
        sdk_history = [{"role": h["role"], "parts": h["parts"]} for h in self.history]
        chat = self.model.start_chat(history=sdk_history)

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

                # Отправка в SDK и фиксация в локальном JSON
                self.history.append({"role": "user", "parts": [user_input]})

                response = chat.send_message(user_input)
                print(f"\nGemini > {response.text}\n")

                self.history.append({"role": "model", "parts": [response.text]})

                # Автосохранение на флешку
                self._save_session()

            except KeyboardInterrupt:
                print("\n[!] Прервано пользователем. Контекст сохранен на флешку.")
                self._save_session()
                break
            except Exception as e:
                print(f"\n[ERROR] Ошибка запроса: {e}\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Admin Stick CLI AI Agent")
    parser.add_argument("--data", help="Путь к директории Data", default=DATA_DIR)
    args = parser.parse_args()

    agent = AdminStickAgent(data_path=args.data)
    agent.run()
