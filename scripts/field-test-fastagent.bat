@echo off
chcp 65001 >nul
setlocal

rem ============================================================
rem  Полевой тест FastAgent на гостевом Windows-ПК.
rem  Запускает 3 скриптованных сессии (L0 / L1 / L2) и складывает
rem  стенограммы + журнал действий в один файл-доказательство.
rem
rem  Все команды L0/L1 — только чтение. На L2 выполняется лишь
rem  ipconfig /flushdns (безопасно), остальное должно быть
rem  заблокировано или отклонено — на этом ПК ничего не меняется.
rem
rem  Запуск: двойной щелчок или из командной строки.
rem ============================================================

set "USB_DRIVE=%~dp0..\"
set "EXE=%USB_DRIVE%FastAgent\fastagent.exe"
set "CFG=%USB_DRIVE%Data\config.fast.json"
set "TESTS=%USB_DRIVE%tests\"
set "REPORT=%USB_DRIVE%Data\logs\field-test-%DATE:/=-%_%TIME::=-%.txt"
set "REPORT=%REPORT: =0%"

echo ===================================================
echo   FIELD TEST: FastAgent (L0 / L1 / L2)
echo ===================================================
echo.

if not exist "%EXE%" (
    echo [ERROR] Нет %EXE%
    echo         Сборка: bash scripts/build-fast-agent.sh fast  (или взять из релиза)
    pause
    exit /b 1
)
if not exist "%CFG%" (
    echo [ERROR] Нет %CFG%
    echo         Скопируйте Data\config.fast.example.json в config.fast.json и вставьте ключ DeepSeek.
    pause
    exit /b 1
)

if not exist "%USB_DRIVE%Data\logs" mkdir "%USB_DRIVE%Data\logs"

echo [*] Отчет: %REPORT%
echo.
echo === Уровень L0 (команды выполняться не должны) ===
"%EXE%" -level 0 < "%TESTS%field-L0.txt" > "%REPORT%" 2>&1
type "%REPORT%"
echo.
echo === Уровень L1 (только чтение, без подтверждения) ===
"%EXE%" -level 1 < "%TESTS%field-L1.txt" >> "%REPORT%" 2>&1
type "%REPORT%"
echo.
echo === Уровень L2 (починки с подтверждением, опасное - блок) ===
"%EXE%" -level 2 < "%TESTS%field-L2.txt" >> "%REPORT%" 2>&1
type "%REPORT%"

echo.
echo ===================================================
echo [OK] Тест завершен. Файл-доказательство:
echo      %REPORT%
echo     Пришлите его целиком (можно вместе с Data\logs\actions_*.log).
echo ===================================================
pause
