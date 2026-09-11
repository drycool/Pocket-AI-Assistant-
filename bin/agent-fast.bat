@echo off
chcp 65001 >nul
setlocal

rem ============================================================
rem  FastAgent — быстрый агент (DeepSeek-first, уровни доступа L0..L3)
rem  Уровень можно задать аргументом:  agent-fast.bat 1
rem  (0 = чат, 1 = чтение, 2 = безопасные починки, 3 = полный доступ)
rem ============================================================

set "USB_DRIVE=%~dp0..\"
set "EXE=%USB_DRIVE%FastAgent\fastagent.exe"
set "CFG=%USB_DRIVE%Data\config.fast.json"
set "LEVEL=%~1"

if not exist "%CFG%" (
    echo [WARN] Нет %CFG%
    echo        Скопируйте Data\config.fast.example.json в Data\config.fast.json и вставьте ключ DeepSeek.
)

if not exist "%EXE%" (
    echo [ERROR] Не найден %EXE%
    echo         Сборка: cd FastAgent ^&^& GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o fastagent.exe main.go
    pause
    exit /b 1
)

if "%LEVEL%"=="" (
    "%EXE%"
) else (
    "%EXE%" -level %LEVEL%
)
exit /b %ERRORLEVEL%
