@echo off
chcp 65001
title Pocket AI Assistant - Control Center

setlocal
set "USB_DRIVE=%~dp0"
set "AGENT_BAT=%USB_DRIVE%bin\agent.bat"
set "FF_BAT=%USB_DRIVE%bin\ff-portable.bat"
set "CHECK_BAT=%USB_DRIVE%bin\check-ollama.bat"
set "REPORTS_DIR=%USB_DRIVE%Data\reports"
set "LOGS_DIR=%USB_DRIVE%Data\logs"

:MENU
cls
echo ===================================================
echo       POCKET AI ASSISTANT - CONTROL CENTER
echo ===================================================
echo  [1] Запустить Go-Агент (Диагностика и Чат)
echo  [2] Запустить Firefox Portable (Gemini Web UI)
echo  [3] Открыть папку с Отчетами (Data/reports)
echo  [4] Открыть папку с Логами (Data/logs)
echo  [5] Проверить связь с Ollama gpu2 (Локальная модель)
echo  [0] Выход
echo ===================================================
set /p CHOICE="Выберите действие (0-5): "

if "%CHOICE%"=="1" goto RUN_AGENT
if "%CHOICE%"=="2" goto RUN_FIREFOX
if "%CHOICE%"=="3" goto OPEN_REPORTS
if "%CHOICE%"=="4" goto OPEN_LOGS
if "%CHOICE%"=="5" goto CHECK_OLLAMA
if "%CHOICE%"=="0" exit
goto MENU

:RUN_AGENT
cls
echo [*] Проверка доступа к локальной модели (Ollama gpu2)...
if exist "%CHECK_BAT%" goto RUN_CHECK
echo [WARN] Не найден %CHECK_BAT%
goto RUN_GO
:RUN_CHECK
call "%CHECK_BAT%" AUTO
:RUN_GO
echo ---------------------------------------------------
echo [*] Запуск AI-Агента...
echo ---------------------------------------------------
if not exist "%AGENT_BAT%" goto RUN_NO_AGENT
call "%AGENT_BAT%"
goto RUN_DONE
:RUN_NO_AGENT
echo [ERROR] Не найден %AGENT_BAT%
pause
:RUN_DONE
echo.
echo ---------------------------------------------------
echo [OK] Работа завершена.
pause
goto MENU

:CHECK_OLLAMA
cls
if exist "%CHECK_BAT%" goto CHECK_RUN
echo [ERROR] Не найден %CHECK_BAT%
pause
goto MENU
:CHECK_RUN
call "%CHECK_BAT%"
goto MENU

:RUN_FIREFOX
cls
echo [*] Запуск Firefox Portable...
if exist "%FF_BAT%" goto FF_RUN
echo [ERROR] Не найден %FF_BAT%
pause
goto MENU
:FF_RUN
call "%FF_BAT%"
echo [OK] Браузер запущен.
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
