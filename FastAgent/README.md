# FastAgent — быстрый агент с регулируемым уровнем доступа

Параллельный агент для админской флешки (классический `Agent/` не изменяется и работает рядом).
Цели: **скорость** (облачный DeepSeek первым + стриминг) и **безопасность на чужом ПК**
(уровни доступа L0–L3 + журнал действий).

## Что внутри

| Решение | Зачем |
|---|---|
| Приоритет №1 — облачный **DeepSeek** (`https://api.deepseek.com/v1`, модель `deepseek-flash`) | Первый токен ~0.9 с, не требует gpu2/Pi/WoL |
| Локальная **Ollama (gpu2 через Pi-шлюз)** вторым провайдером | Резерв, когда нет интернета/лимит; включается автоматически |
| Стриминг (SSE) с печатью по мере поступления | Ощущаемая скорость: текст виден сразу, а не через 5–30 с |
| Таймаут «первого токена» (25 с) | Мёртвый провайдер отбрасывается быстро, без ожидания полного таймаута |
| Короткий системный промпт + обрезка истории (`history_limit`) | Меньше входных токенов → меньше latency и цена |
| Уровни доступа L0–L3 с белыми списками | Агент выполняет команды, но только разрешённые на выбранном уровне |
| Журнал действий `Data/logs/actions_<дата>.log` (JSON Lines) | Видно, что и с каким вердиктом запускалось |

## Уровни доступа

| Уровень | Название | Что выполняет |
|---|---|---|
| `L0` | chat | Ничего не выполняет — только диалог и объяснения (безопасно на любом ПК) |
| `L1` | read | Только чтение из белого списка, **без подтверждения**: `ipconfig /all`, `systeminfo`, `Get-*`, `Test-*`, `netstat`, `tasklist`, `nslookup`, `reg query`, `uname`, `df`, `journalctl`, `systemctl status`… |
| `L2` | fix | L1 + безопасные починки, **каждая с подтверждением `y`**: `ipconfig /flushdns`, `netsh winsock reset`, `Restart-Service`, `sfc /scannow`, `dism … /restorehealth`, `gpupdate /force`, `systemctl restart`… |
| `L3` | admin | Любые команды, подтверждение обязательно; **опасные — двойное** (`yes`): `format`, `diskpart`, `bcdedit`, `reg delete`, `del /f`, `Remove-Item -Recurse`, `shutdown`, `Set-ExecutionPolicy`, `rm -rf`, `mkfs`… |

Ключевые правила политики (`classify()` в `main.go`):

1. Порядок проверок: **опасное → изменяющее → чтение**. Иначе `ipconfig /flushdns` поймается общим `^ipconfig` и выполнится без подтверждения (эта регрессия закрыта тестом).
2. Незнакомая команда ниже L3 — не выполняется, агент прямо говорит: «нужен уровень 3».
3. Повышение уровня в рантайме требует ввода `yes` (защита от случайного `/level 3`).
4. На L0 не выполняется ничего, даже при подтверждении.

## Запуск

```bat
:: с флешки (Windows)
bin\agent-fast.bat          :: уровень из Data\config.fast.json
bin\agent-fast.bat 1        :: принудительно L1 (чтение)
bin\agent-fast.bat 3        :: полный доступ (опасные — двойное подтверждение)
```

Из `START.bat` — пункт **[6]**.

Внутренние команды агента: `/level N`, `/level`, `/diag`, `/report`, `/follow on|off`, `/clear`, `exit`.

## Конфиг `Data/config.fast.json` (в .gitignore, ключ не попадает в git)

```json
{
  "access_level": 1,
  "stream": true,
  "auto_followup": true,
  "max_tokens": 900,
  "temperature": 0,
  "first_token_timeout_sec": 25,
  "total_timeout_sec": 180,
  "history_limit": 10,
  "providers": [
    { "name": "DeepSeek-Cloud", "type": "openai",
      "base_url": "https://api.deepseek.com/v1", "api_key": "sk-...", "model": "deepseek-flash" },
    { "name": "Local-Ollama-gpu2-via-Pi", "type": "openai",
      "base_url": "http://192.168.11.111:11434/v1", "api_key": "ollama",
      "model": "qwen3:8b", "reasoning_effort": "none" }
  ]
}
```

Образец без ключа — `Data/config.fast.example.json`. Доступные модели у DeepSeek проверяются так:

```bash
curl -s https://api.deepseek.com/v1/models -H "Authorization: Bearer $DEEPSEEK_API_KEY"
# -> deepseek-flash, deepseek-v4-pro
```

## Сборка и тесты

```bash
bash scripts/build-fast-agent.sh fast     # linux/arm64 (отладка) + windows/amd64 (FastAgent/fastagent.exe)
cd FastAgent && go test ./...             # юнит-тесты политики доступа (35 кейсов)
```

## Результаты физической проверки (сен 2026, Pi5 + gpu2 + DeepSeek)

| Проверка | Результат |
|---|---|
| Стриминг DeepSeek (`-level 1`) | `ttft 0.92 с`, ответ и авто-выполнение `whoami` (вердикт `чтение`) |
| L0 — `whoami` от модели | `[BLOCK] L0: команда не выполняется` |
| L3 — `format C: /fs:ntfs` | `ОПАСНО (двойное подтверждение)` → `Отменено оператором` (не выполнено) |
| Fallback DeepSeek (битый ключ, 401) → Ollama через Pi-шлюз | `[fallback] … → следующий провайдер`, `ttft 0.13 с` |
| Журнал действий | `L1 чтение approved=True code=0 cmd=whoami`, `L3 ОПАСНО approved=False` |

## Известные ограничения

- Стриминг: печать `AI > ` начинается до проверки провайдера, поэтому при fallback строка `[fallback]` идёт после первого `AI >` — косметика.
- `auto_followup` добавляет второй (и последующие) запрос к модели — при экономии токенов выключается `/follow off`.
- Политика работает по белым спискам (regex) — если нужной команды нет в списке, добавьте её в `reReadSafe`/`reFixAllowed` и запустите тесты.
- На Linux команды исполняются через `/bin/sh -c`, на Windows — через `powershell -NoProfile -NonInteractive -Command`.
