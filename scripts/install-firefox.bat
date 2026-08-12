@echo off
title Installation Firefox Portable

set "ROOT_DIR=%~dp0.."
set "TOOLS_DIR=%ROOT_DIR%\Tools\FirefoxPortable"
set "TEMP_ZIP=%ROOT_DIR%\Data\ff_temp.zip"

echo ===================================================
echo       USTANOVKA FIREFOX PORTABLE
echo ===================================================

if not exist "%ROOT_DIR%\Data" mkdir "%ROOT_DIR%\Data"
if not exist "%TOOLS_DIR%" mkdir "%TOOLS_DIR%"

echo [*] Skachivanie Firefox Portable...
powershell -Command "[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; $web = New-Object System.Net.WebClient; $web.Headers.Add('User-Agent', 'Mozilla/5.0'); $web.DownloadFile('https://download.mozilla.org/?product=firefox-latest-ssl&os=win64&lang=en-US', '%TEMP_ZIP%')"

if not exist "%TEMP_ZIP%" goto ERROR_DL

echo [*] Podgotovka papki Tools...
echo [INFO] Skachan oficial'nyj ustonavshik Firefox.
echo [INFO] Zapuskaem ustanovku. Ukazhite put' raspakovki:
echo %TOOLS_DIR%
echo ---------------------------------------------------

"%TEMP_ZIP%"

if exist "%TEMP_ZIP%" del "%TEMP_ZIP%"

echo ===================================================
echo [OK] Ustanovka zavershena!
echo ===================================================
pause
exit /b 0

:ERROR_DL
echo.
echo [ERROR] Ne udalos' skachat' fajl! Prover'te soedinenie s Set'yu.
pause
exit /b 1