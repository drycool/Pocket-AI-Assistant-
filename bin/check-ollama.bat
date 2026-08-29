@echo off
chcp 65001 >nul
setlocal EnableDelayedExpansion

rem ============================================================
rem  Проверка связи с локальной моделью (Ollama на gpu2)
rem  + автонастройка Data\config.json под доступный адрес.
rem
rem  Приоритет эндпоинтов:
rem    [1] Raspberry Pi 192.168.0.10:11434  - шлюз 24/7: будит gpu2
rem        по Wake-on-LAN и прогревает модель (работает всегда)
rem    [2] gpu2 напрямую 100.121.254.115:11434 - Tailscale (из любой сети)
rem    [3] gpu2 напрямую 192.168.0.87:11434   - LAN (без Tailscale)
rem
rem  Использование:
rem    check-ollama.bat          - ручной режим (пауза в конце)
rem    check-ollama.bat AUTO     - автоматический режим (для START.bat)
rem ============================================================

set "USB_DRIVE=%~dp0..\"
set "CFG=%USB_DRIVE%Data\config.json"
set "TPL_PI=%USB_DRIVE%Data\config.pi.json"
set "TPL_TS=%USB_DRIVE%Data\config.tailscale.json"
set "TPL_LAN=%USB_DRIVE%Data\config.lan.json"
set "MODE=%~1"

echo.
echo  === Проверка связи с Ollama (gpu2) ===

rem ----- [1] Raspberry Pi (шлюз, будит gpu2) -----
powershell -NoProfile -Command "$c=New-Object Net.Sockets.TcpClient;try{$iar=$c.BeginConnect('192.168.0.10',11434,$null,$null);if($iar.AsyncWaitHandle.WaitOne(2500)){$c.EndConnect($iar);Write-Output 'OK'}else{Write-Output 'FAIL'}}catch{Write-Output 'FAIL'}" > "%TEMP%\ollama_pi.txt" 2>nul
set /p PI_CHK=<"%TEMP%\ollama_pi.txt"
if "%PI_CHK%"=="OK" (
    echo   [OK]  Raspberry Pi 192.168.0.10:11434  - доступен ^(шлюз: будит gpu2^)
    set "PI_OK=1"
) else (
    echo   [--]  Raspberry Pi 192.168.0.10:11434  - недоступен
    set "PI_OK=0"
)

rem ----- [2] gpu2 напрямую, Tailscale -----
powershell -NoProfile -Command "$c=New-Object Net.Sockets.TcpClient;try{$iar=$c.BeginConnect('100.121.254.115',11434,$null,$null);if($iar.AsyncWaitHandle.WaitOne(2500)){$c.EndConnect($iar);Write-Output 'OK'}else{Write-Output 'FAIL'}}catch{Write-Output 'FAIL'}" > "%TEMP%\ollama_ts.txt" 2>nul
set /p TS_CHK=<"%TEMP%\ollama_ts.txt"
if "%TS_CHK%"=="OK" (
    echo   [OK]  Tailscale 100.121.254.115:11434  - доступен
    set "TS_OK=1"
) else (
    echo   [--]  Tailscale 100.121.254.115:11434  - недоступен
    set "TS_OK=0"
)

rem ----- [3] gpu2 напрямую, LAN -----
powershell -NoProfile -Command "$c=New-Object Net.Sockets.TcpClient;try{$iar=$c.BeginConnect('192.168.0.87',11434,$null,$null);if($iar.AsyncWaitHandle.WaitOne(2500)){$c.EndConnect($iar);Write-Output 'OK'}else{Write-Output 'FAIL'}}catch{Write-Output 'FAIL'}" > "%TEMP%\ollama_lan.txt" 2>nul
set /p LAN_CHK=<"%TEMP%\ollama_lan.txt"
if "%LAN_CHK%"=="OK" (
    echo   [OK]  LAN 192.168.0.87:11434  - доступен
    set "LAN_OK=1"
) else (
    echo   [--]  LAN 192.168.0.87:11434  - недоступен
    set "LAN_OK=0"
)

echo.
if "%PI_OK%"=="1" (
    set "BEST=%TPL_PI%"
    echo   Рекомендуемый адрес: Raspberry Pi 192.168.0.10 ^(шлюз 24/7: сам будит gpu2 и прогревает модель^)
) else if "%TS_OK%"=="1" (
    set "BEST=%TPL_TS%"
    echo   Рекомендуемый адрес: Tailscale 100.121.254.115 ^(работает из любой сети^)
) else if "%LAN_OK%"=="1" (
    set "BEST=%TPL_LAN%"
    echo   Рекомендуемый адрес: LAN 192.168.0.87 ^(gpu2 напрямую, без Tailscale^)
) else (
    echo   [ERROR] gpu2 недоступен ни по одному адресу.
    echo   Проверьте: Wi-Fi/сеть на этом ПК, работает ли Raspberry Pi ^(шлюз^), включен ли Tailscale, включен ли gpu2.
    goto :END
)

rem ----- Синхронизация Data\config.json с доступным адресом -----
if not exist "%CFG%" (
    copy /y "%BEST%" "%CFG%" >nul
    echo   [OK] Создан Data\config.json с доступным адресом Ollama
    goto :END
)

call :SYNC_BEST "%BEST%"
goto :END

:SYNC_BEST
rem %1 = выбранный шаблон; если config.json равен ЛЮБОМУ другому шаблону — переключаем
if "%1"=="%TPL_PI%" (
    set "A=%TPL_TS%"
    set "B=%TPL_LAN%"
) else if "%1"=="%TPL_TS%" (
    set "A=%TPL_PI%"
    set "B=%TPL_LAN%"
) else (
    set "A=%TPL_PI%"
    set "B=%TPL_TS%"
)
fc /b "%CFG%" "%A%" >nul 2>&1
if not errorlevel 1 goto :DO_COPY
fc /b "%CFG%" "%B%" >nul 2>&1
if not errorlevel 1 goto :DO_COPY
echo   [OK] Data\config.json уже указывает на доступный адрес
goto :EOF

:DO_COPY
copy /y "%1" "%CFG%" >nul
echo   [OK] Data\config.json переключен на доступный адрес Ollama
goto :EOF

:END
if /i "%MODE%"=="AUTO" exit /b 0
echo.
pause
exit /b 0
