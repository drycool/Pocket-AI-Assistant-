@echo off
chcp 65001 >nul
setlocal

rem ============================================================
rem  Проверка связи с локальной моделью (Ollama на gpu2)
rem  + автонастройка Data\config.json под доступный адрес.
rem
rem  Приоритет эндпоинтов:
rem    [1] Raspberry Pi 192.168.0.10:11434   - шлюз 24/7 (LAN): будит gpu2
rem    [2] Raspberry Pi 100.124.79.91:11434  - шлюз 24/7 (Tailscale):
rem        работает из любой сети, сам будит gpu2
rem    [3] gpu2 напрямую 100.121.254.115:11434 - Tailscale (из любой сети)
rem    [4] Публичный 178.159.221.63:11434    - проброс порта на роутере,
rem        работает из любого интернета БЕЗ Tailscale (нужен токен в
rem        Data\config.public.json; проверка пропускается, если файла нет)
rem    [5] gpu2 напрямую 192.168.0.87:11434  - LAN (без Tailscale)
rem
rem  Использование:
rem    check-ollama.bat          - ручной режим (пауза в конце)
rem    check-ollama.bat AUTO     - автоматический режим (для START.bat)
rem ============================================================

set "USB_DRIVE=%~dp0..\"
set "PS1=%USB_DRIVE%scripts\check-ollama.ps1"
set "CFG=%USB_DRIVE%Data\config.json"
set "TPL_PI=%USB_DRIVE%Data\config.pi.json"
set "TPL_PITS=%USB_DRIVE%Data\config.pi-ts.json"
set "TPL_TS=%USB_DRIVE%Data\config.tailscale.json"
set "TPL_PUB=%USB_DRIVE%Data\config.public.json"
set "TPL_LAN=%USB_DRIVE%Data\config.lan.json"
set "MODE=%~1"

echo.
echo  === Проверка связи с Ollama на gpu2 ===

rem ----- [1] Raspberry Pi по LAN (шлюз, будит gpu2) -----
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" -HostIP 192.168.0.10 >nul 2>&1
if errorlevel 1 goto :PI_KO
echo   [OK]  Raspberry Pi 192.168.0.10:11434  - доступен, шлюз будит gpu2
set "PI_OK=1"
goto :PITS_CHECK
:PI_KO
echo   [--]  Raspberry Pi 192.168.0.10:11434  - недоступен
set "PI_OK=0"

:PITS_CHECK
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" -HostIP 100.124.79.91 >nul 2>&1
if errorlevel 1 goto :PITS_KO
echo   [OK]  Raspberry Pi 100.124.79.91:11434  - доступен, шлюз будит gpu2
set "PITS_OK=1"
goto :TS_CHECK
:PITS_KO
echo   [--]  Raspberry Pi 100.124.79.91:11434  - недоступен
set "PITS_OK=0"

:TS_CHECK
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" -HostIP 100.121.254.115 >nul 2>&1
if errorlevel 1 goto :TS_KO
echo   [OK]  Tailscale 100.121.254.115:11434  - доступен
set "TS_OK=1"
goto :PUB_CHECK
:TS_KO
echo   [--]  Tailscale 100.121.254.115:11434  - недоступен
set "TS_OK=0"

:PUB_CHECK
if exist "%TPL_PUB%" goto :PUB_TEST
set "PUB_OK=0"
echo   [--]  Публичный 178.159.221.63:11434  - нет шаблона config.public.json
goto :LAN_CHECK
:PUB_TEST
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" -HostIP 178.159.221.63 >nul 2>&1
if errorlevel 1 goto :PUB_KO
echo   [OK]  Публичный 178.159.221.63:11434  - доступен, работает без Tailscale
set "PUB_OK=1"
goto :LAN_CHECK
:PUB_KO
echo   [--]  Публичный 178.159.221.63:11434  - недоступен (проброс порта на роутере?)
set "PUB_OK=0"

:LAN_CHECK
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS1%" -HostIP 192.168.0.87 >nul 2>&1
if errorlevel 1 goto :LAN_KO
echo   [OK]  LAN 192.168.0.87:11434  - доступен
set "LAN_OK=1"
goto :PICK
:LAN_KO
echo   [--]  LAN 192.168.0.87:11434  - недоступен
set "LAN_OK=0"

:PICK
echo.
if "%PI_OK%"=="1" goto :PICK_PI
if "%PITS_OK%"=="1" goto :PICK_PITS
if "%TS_OK%"=="1" goto :PICK_TS
if "%PUB_OK%"=="1" goto :PICK_PUB
if "%LAN_OK%"=="1" goto :PICK_LAN
echo   [ERROR] gpu2 недоступен ни по одному адресу.
echo   Проверьте: сеть, Raspberry Pi, Tailscale, проброс порта, включен ли gpu2.
goto :END

:PICK_PI
set "BEST=%TPL_PI%"
echo   Рекомендуемый адрес: Raspberry Pi 192.168.0.10 (шлюз 24/7)
goto :SYNC

:PICK_PITS
set "BEST=%TPL_PITS%"
echo   Рекомендуемый адрес: Raspberry Pi 100.124.79.91 (шлюз через Tailscale)
goto :SYNC

:PICK_TS
set "BEST=%TPL_TS%"
echo   Рекомендуемый адрес: Tailscale 100.121.254.115
goto :SYNC

:PICK_PUB
set "BEST=%TPL_PUB%"
echo   Рекомендуемый адрес: Публичный 178.159.221.63 (через интернет)
goto :SYNC

:PICK_LAN
set "BEST=%TPL_LAN%"
echo   Рекомендуемый адрес: LAN 192.168.0.87
goto :SYNC

:SYNC
if exist "%CFG%" goto :SYNC_EXISTS
copy /y "%BEST%" "%CFG%" >nul
echo   [OK] Создан Data\config.json с доступным адресом Ollama
goto :END

:SYNC_EXISTS
call :SYNC_BEST "%BEST%"
goto :END

:SYNC_BEST
rem Если config.json равен ЛЮБОМУ из остальных шаблонов - переключаем на доступный
if "%~1"=="%TPL_PI%" goto :SB_PI
if "%~1"=="%TPL_PITS%" goto :SB_PITS
if "%~1"=="%TPL_TS%" goto :SB_TS
if "%~1"=="%TPL_PUB%" goto :SB_PUB
set "A=%TPL_PI%"
set "B=%TPL_PITS%"
set "C=%TPL_TS%"
set "D=%TPL_PUB%"
goto :SB_CMP
:SB_PI
set "A=%TPL_PITS%"
set "B=%TPL_TS%"
set "C=%TPL_PUB%"
set "D=%TPL_LAN%"
goto :SB_CMP
:SB_PITS
set "A=%TPL_PI%"
set "B=%TPL_TS%"
set "C=%TPL_PUB%"
set "D=%TPL_LAN%"
goto :SB_CMP
:SB_TS
set "A=%TPL_PI%"
set "B=%TPL_PITS%"
set "C=%TPL_PUB%"
set "D=%TPL_LAN%"
goto :SB_CMP
:SB_PUB
set "A=%TPL_PI%"
set "B=%TPL_PITS%"
set "C=%TPL_TS%"
set "D=%TPL_LAN%"
:SB_CMP
fc /b "%CFG%" "%A%" >nul 2>&1
if not errorlevel 1 goto :DO_COPY
fc /b "%CFG%" "%B%" >nul 2>&1
if not errorlevel 1 goto :DO_COPY
fc /b "%CFG%" "%C%" >nul 2>&1
if not errorlevel 1 goto :DO_COPY
fc /b "%CFG%" "%D%" >nul 2>&1
if not errorlevel 1 goto :DO_COPY
echo   [OK] Data\config.json уже указывает на доступный адрес
goto :EOF

:DO_COPY
copy /y "%~1" "%CFG%" >nul
echo   [OK] Data\config.json переключен на доступный адрес Ollama
goto :EOF

:END
if /i "%MODE%"=="AUTO" exit /b 0
echo.
pause
exit /b 0
