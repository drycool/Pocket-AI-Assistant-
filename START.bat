@echo off
chcp 65001
title Pocket AI Assistant - Control Center

setlocal
set "USB_DRIVE=%~dp0"
set "AGENT_BAT=%USB_DRIVE%bin\agent.bat"
set "FF_BAT=%USB_DRIVE%bin\ff-portable.bat"
set "CHECK_BAT=%USB_DRIVE%bin\check-ollama.bat"
set "FAST_BAT=%USB_DRIVE%bin\agent-fast.bat"
set "FASTTEST_BAT=%USB_DRIVE%scripts\field-test-fastagent.bat"
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
echo  [6] FastAgent - быстрый агент (DeepSeek/облако, уровни доступа L0-L3)
echo  [7] Полевой тест FastAgent (L0/L1/L2, отчет для отправки)
echo  [0] Выход
echo ===================================================
set /p CHOICE="Выберите действие (0-7): "

if "%CHOICE%"=="1" goto RUN_AGENT
if "%CHOICE%"=="2" goto RUN_FIREFOX
if "%CHOICE%"=="3" goto OPEN_REPORTS
if "%CHOICE%"=="4" goto OPEN_LOGS
if "%CHOICE%"=="5" goto CHECK_OLLAMA
if "%CHOICE%"=="6" goto RUN_FAST
if "%CHOICE%"=="7" goto RUN_FASTTEST
if "%CHOICE%"=="0" exit
goto MENU

:RUN_FASTTEST
cls
echo ---------------------------------------------------
echo [*] Полевой тест FastAgent: L0 -^> L1 -^> L2
echo     Отчет будет в Data\logs\field-test-*.txt
echo     На гостевом ПК ничего не меняется (только чтение
echo     и один безопасный ipconfig /flushdns).
echo ---------------------------------------------------
if exist "%FASTTEST_BAT%" goto FASTTEST_RUN
echo [ERROR] Не найден %FASTTEST_BAT%
pause
goto MENU
:FASTTEST_RUN
call "%FASTTEST_BAT%"
goto MENU

:RUN_FAST
cls
echo ---------------------------------------------------
echo [*] FastAgent: облачная модель + уровни доступа
echo     L0 чат ^| L1 чтение ^| L2 починки ^| L3 полный доступ
echo     Уровень по умолчанию задан в Data\config.fast.json
echo ---------------------------------------------------
if exist "%FAST_BAT%" goto FAST_RUN
echo [ERROR] Не найден %FAST_BAT%
pause
goto MENU
:FAST_RUN
call "%FAST_BAT%"
echo.
echo ---------------------------------------------------
echo [OK] Работа FastAgent завершена.
pause
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
