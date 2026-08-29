@echo off
chcp 65001 >nul
setlocal EnableDelayedExpansion

rem ============================================================
rem  Проверка связи с Ollama на сервере gpu2 + автонастройка
rem  Data\config.json под доступный адрес.
rem
rem  Использование:
rem    check-ollama.bat          - ручной режим (пауза в конце)
rem    check-ollama.bat AUTO     - автоматический режим (для START.bat)
rem ============================================================

set "USB_DRIVE=%~dp0..\"
set "CFG=%USB_DRIVE%Data\config.json"
set "TPL_TS=%USB_DRIVE%Data\config.tailscale.json"
set "TPL_LAN=%USB_DRIVE%Data\config.lan.json"
set "MODE=%~1"

echo.
echo  === Проверка связи с Ollama на gpu2 ===

rem ----- Tailscale-адрес (работает из любой сети с Tailscale) -----
powershell -NoProfile -Command "$c=New-Object Net.Sockets.TcpClient;try{$iar=$c.BeginConnect('100.121.254.115',11434,$null,$null);if($iar.AsyncWaitHandle.WaitOne(2500)){$c.EndConnect($iar);Write-Output 'OK'}else{Write-Output 'FAIL'}}catch{Write-Output 'FAIL'}" > "%TEMP%\ollama_ts.txt" 2>nul
set /p TS_CHK=<"%TEMP%\ollama_ts.txt"
if "%TS_CHK%"=="OK" (
    echo   [OK]  Tailscale 100.121.254.115:11434  - доступен
    set "TS_OK=1"
) else (
    echo   [--]  Tailscale 100.121.254.115:11434  - недоступен
    set "TS_OK=0"
)

rem ----- LAN-адрес (только в одной сети с gpu2) -----
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
if "%TS_OK%"=="1" (
    set "BEST=%TPL_TS%"
    echo   Рекомендуемый адрес: Tailscale 100.121.254.115 ^(работает из любой сети^)
) else if "%LAN_OK%"=="1" (
    set "BEST=%TPL_LAN%"
    echo   Рекомендуемый адрес: LAN 192.168.0.87 ^(только в одной сети с gpu2^)
) else (
    echo   [ERROR] gpu2 недоступен по обоим адресам.
    echo   Проверьте: Wi-Fi/сеть на этом ПК, включен ли Tailscale, работает ли сервер gpu2.
    goto :END
)

rem ----- Синхронизация Data\config.json с доступным адресом -----
if not exist "%CFG%" (
    copy /y "%BEST%" "%CFG%" >nul
    echo   [OK] Создан Data\config.json с доступным адресом Ollama
    goto :END
)

if "%BEST%"=="%TPL_TS%" (set "OTHER=%TPL_LAN%") else (set "OTHER=%TPL_TS%")
fc /b "%CFG%" "%OTHER%" >nul 2>&1
if errorlevel 1 (
    echo   [OK] Data\config.json уже указывает на доступный адрес
) else (
    copy /y "%BEST%" "%CFG%" >nul
    echo   [OK] Data\config.json переключен на доступный адрес Ollama
)

:END
if /i "%MODE%"=="AUTO" exit /b 0
echo.
pause
exit /b 0
