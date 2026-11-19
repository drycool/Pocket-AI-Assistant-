@echo off
chcp 65001 >nul
setlocal

rem ============================================================
rem  FastAgent — быстрый агент (DeepSeek-first, уровни доступа L0..L3)
rem  Уровень можно задать аргументом:  agent-fast.bat 1
rem  (0 = чат, 1 = чтение, 2 = безопасные починки, 3 = полный доступ)
rem ============================================================

set "USB_DRIVE=%~dp0..\"
rem Два раскладки флешки: FieldKit в корне (fastagent.exe рядом со scenarios\)
rem или репозиторий (FastAgent\fastagent.exe). Берём тот, что есть: сценарии
rem ищутся рядом с самим exe, поэтому рабочий каталог менять не нужно.
set "EXE=%USB_DRIVE%fastagent.exe"
if not exist "%EXE%" set "EXE=%USB_DRIVE%FastAgent\fastagent.exe"
if not exist "%EXE%" (
    echo [ERROR] Не найден fastagent.exe ни в корне флешки, ни в FastAgent\
    pause
    exit /b 1
)
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
