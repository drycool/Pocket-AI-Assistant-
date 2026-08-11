@echo off
chcp 65001 > nul
title Pocket AI Assistant — Control Center

setlocal
set "USB_DRIVE=%~dp0"
set "AGENT_EXE=%USB_DRIVE%Agent\agent.exe"
set "AGENT_PY=%USB_DRIVE%Agent\agent.py"
set "REPORTS_DIR=%USB_DRIVE%Data\reports"
set "LOGS_DIR=%USB_DRIVE%Data\logs"
set "FF_LAUNCH=%USB_DRIVE%bin\ff-portable.bat"

:MENU
cls
echo ===================================================
echo       POCKET AI ASSISTANT — CONTROL CENTER
echo ===================================================
echo  [1] Запустить Go-Агент (Диагностика и Чат)
echo  [2] Запустить Firefox Portable (Gemini Web UI)
echo  [3] Открыть папку с Отчетами (Data/reports)
echo  [4] Открыть папку с Логами (Data/logs)
echo  [0] Выход
echo ===================================================
set /p CHOICE="Выберите действие (0-4): "

if "%CHOICE%"=="1" goto RUN_AGENT
if "%CHOICE%"=="2" goto RUN_FIREFOX
if "%CHOICE%"=="3" goto OPEN_REPORTS
if "%CHOICE%"=="4" goto OPEN_LOGS
if "%CHOICE%"=="0" exit
goto MENU

:RUN_AGENT
cls
echo [*] Запуск AI-Агента...
echo ---------------------------------------------------
if exist "%AGENT_EXE%" (
    "%AGENT_EXE%"
) else if exist "%AGENT_PY%" (
    echo [!] agent.exe не найден. Запуск Python-версии...
    python "%AGENT_PY%"
) else (
    echo [ERROR] Ни agent.exe, ни agent.py не найдены в папке Agent!
    echo Скомпилируйте агент: cd Agent ^&^& go build -ldflags="-s -w" -o agent.exe main.go
    pause
)
echo.
echo ---------------------------------------------------
echo [OK] Работа агента завершена.
pause
goto MENU

:RUN_FIREFOX
cls
echo [*] Запуск Firefox Portable (изолированный профиль на USB)...
if exist "%FF_LAUNCH%" (
    call "%FF_LAUNCH%"
    echo [OK] Браузер запущен в отдельном процессе.
) else (
    echo [ERROR] Лаунчер браузера не найден: "%FF_LAUNCH%"
)
timeout /t 2 > nul
goto MENU

:OPEN_REPORTS
if not exist "%REPORTS_DIR%" mkdir "%REPORTS_DIR%"
start "" "%REPORTS_DIR%"
goto MENU

:OPEN_LOGS
if not exist "%LOGS_DIR%" mkdir "%LOGS_DIR%"
start "" "%LOGS_DIR%"
goto MENU
