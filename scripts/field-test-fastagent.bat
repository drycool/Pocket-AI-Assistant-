@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion

rem ============================================================
rem  Полевой тест FastAgent на гостевом Windows-ПК.
rem  Прогоняет 3 скриптованных сессии (L0 / L1 / L2) и собирает
rem  стенограммы + сводный отчёт в Data\logs\.
rem
rem  Безопасность: L0/L1 — только чтение. На L2 выполняется лишь
rem  ipconfig /flushdns (безопасно), остальное должно быть
rem  отклонено (n) или заблокировано. Гостевой ПК не меняется.
rem
rem  Запуск: двойной щелчок по этому файлу.
rem ============================================================

set "USB_DRIVE=%~dp0..\"
set "EXE=%USB_DRIVE%FastAgent\fastagent.exe"
set "CFG=%USB_DRIVE%Data\config.fast.json"
set "TESTS=%USB_DRIVE%tests\"
set "LOGDIR=%USB_DRIVE%Data\logs"

set "D=%DATE:/=-%"
set "T=%TIME::=%"
set "T=!T: =0!"
set "T=!T:,=-!"
set "STAMP=!D!_!T:~0,6!"
set "REPORT=%LOGDIR%\field-test-!STAMP!.txt"

echo ===================================================
echo   FIELD TEST: FastAgent (L0 / L1 / L2)
echo ===================================================
echo.

if not exist "%EXE%" (
    echo [ERROR] Не найден "%EXE%"
    echo         Сборка на Pi: bash scripts/build-fast-agent.sh fast
    pause
    exit /b 1
)
if not exist "%CFG%" (
    echo [ERROR] Не найден "%CFG%"
    echo         Скопируйте Data\config.fast.example.json в Data\config.fast.json
    echo         и вставьте ключ DeepSeek в поле api_key.
    pause
    exit /b 1
)
if not exist "%LOGDIR%" mkdir "%LOGDIR%"

echo [*] Сводный отчёт: %REPORT%
echo.

echo === [1/3] Уровень L0: команды выполняться НЕ должны ===
"%EXE%" -level 0 < "%TESTS%field-L0.txt" > "%LOGDIR%\field-L0-!STAMP!.txt" 2>&1
type "%LOGDIR%\field-L0-!STAMP!.txt"
echo.

echo === [2/3] Уровень L1: только чтение, без подтверждения ===
"%EXE%" -level 1 < "%TESTS%field-L1.txt" > "%LOGDIR%\field-L1-!STAMP!.txt" 2>&1
type "%LOGDIR%\field-L1-!STAMP!.txt"
echo.

echo === [3/3] Уровень L2: починки с подтверждением, опасное - блок ===
"%EXE%" -level 2 < "%TESTS%field-L2.txt" > "%LOGDIR%\field-L2-!STAMP!.txt" 2>&1
type "%LOGDIR%\field-L2-!STAMP!.txt"
echo.

rem --- сводный отчёт: три стенограммы + журнал действий ---
copy /b "%LOGDIR%\field-L0-!STAMP!.txt" + "%LOGDIR%\field-L1-!STAMP!.txt" + "%LOGDIR%\field-L2-!STAMP!.txt" "%REPORT%" >nul 2>&1
if exist "%LOGDIR%\actions_*.log" (
    echo. >> "%REPORT%"
    echo ===== ЖУРНАЛ ДЕЙСТВИЙ ===== >> "%REPORT%"
    type "%LOGDIR%\actions_*.log" >> "%REPORT%" 2>nul
)

echo ===================================================
echo [OK] Тест завершён.
echo.
echo      Сводный отчёт : %REPORT%
echo      Стенограммы   : %LOGDIR%\field-L0-!STAMP!.txt и др.
echo      Журнал команд : %LOGDIR%\actions_*.log
echo.
echo      Пришлите сводный отчёт целиком.
echo ===================================================
pause
