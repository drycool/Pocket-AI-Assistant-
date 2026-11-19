@echo off
chcp 65001 >nul
setlocal

rem ============================================================
rem  FastAgent — быстрый агент (DeepSeek-first, уровни доступа L0..L3)
rem
rem    agent-fast.bat                      уровень из Data\config.fast.json
rem    agent-fast.bat 1                    принудительно L1 (только чтение)
rem    agent-fast.bat "" medoc/server-zillya
rem                                        чат по кейсу: проверенные действия
rem                                        и паспорт случая в системном промпте
rem
rem  Уровни: 0 = чат, 1 = чтение, 2 = безопасные починки (запись с
rem  подтверждением), 3 = полный доступ.
rem ============================================================

set "USB_DRIVE=%~dp0..\"
rem Два раскладки флешки: FieldKit в корне (fastagent.exe рядом со scenarios\)
rem или репозиторий (FastAgent\fastagent.exe). Берём тот, что есть: сценарии
rem ищутся в scenarios\ корня комплекта (два уровня выше exe), каталог не важен.
set "EXE=%USB_DRIVE%fastagent.exe"
if not exist "%EXE%" set "EXE=%USB_DRIVE%FastAgent\fastagent.exe"
if not exist "%EXE%" (
    echo [ERROR] Не найден fastagent.exe ни в корне флешки, ни в FastAgent\
    pause
    exit /b 1
)
set "CFG=%USB_DRIVE%Data\config.fast.json"
set "LEVEL=%~1"
set "SCEN=%~2"

if not exist "%CFG%" (
    echo [WARN] Нет %CFG%
    echo        Скопируйте Data\config.fast.example.json в Data\config.fast.json и вставьте ключ DeepSeek.
)

set "FA_ARGS="
if not "%LEVEL%"=="" set "FA_ARGS=%FA_ARGS% -level %LEVEL%"
if not "%SCEN%"==""  set "FA_ARGS=%FA_ARGS% -scenario "%SCEN%""

if "%SCEN%"=="" (
    echo [*] Чат с агентом. Проверенные действия кейса: /scenario ^<имя^>
) else (
    echo [*] Чат по кейсу %SCEN% - в системном промпте проверенные действия и паспорт случая.
)
echo.

"%EXE%"%FA_ARGS%
exit /b %ERRORLEVEL%
