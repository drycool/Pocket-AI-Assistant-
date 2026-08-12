@echo off
chcp 65001
title Pocket AI Assistant — Control Center

setlocal
set "USB_DRIVE=%~dp0"
set "AGENT_BAT=%USB_DRIVE%bin\agent.bat"
set "FF_BAT=%USB_DRIVE%bin\ff-portable.bat"
set "REPORTS_DIR=%USB_DRIVE%Data\reports"
set "LOGS_DIR=%USB_DRIVE%Data\logs"

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
if exist "%AGENT_BAT%" (
    call "%AGENT_BAT%"
) else (
    echo [ERROR] Не найден %AGENT_BAT%
    pause
)
echo.
echo ---------------------------------------------------
echo [OK] Работа завершена.
pause
goto MENU

:RUN_FIREFOX
cls
echo [*] Запуск Firefox Portable...
if exist "%FF_BAT%" (
    call "%FF_BAT%"
    echo [OK] Браузер запущен.
) else (
    echo [ERROR] Не найден %FF_BAT%
    pause
)
timeout /t 2
goto MENU

:OPEN_REPORTS
if not exist "%REPORTS_DIR%" mkdir "%REPORTS_DIR%"
start "" "%REPORTS_DIR%"
goto MENU

:OPEN_LOGS
if not exist "%LOGS_DIR%" mkdir "%LOGS_DIR%"
start "" "%LOGS_DIR%"
goto MENU