@echo off
REM =============================================================
REM  Bootstrap: install Portable Firefox into Tools\FirefoxPortable
REM
REM  Two modes:
REM   1. Put a PortableApps Firefox zip into the same folder as this
REM      script (e.g. FirefoxPortable_xx_English.paf.exe or .zip),
REM      or
REM   2. Set FFURL below to the official download link, and 7z (or
REM      tar) must be available to extract.
REM
REM  After install, firefox.exe must end up at:
REM     Tools\FirefoxPortable\firefox.exe
REM =============================================================
setlocal

set "ROOT=%~dp0.."
set "TARGET=%ROOT%\Tools\FirefoxPortable"
set "FFURL="

if exist "%TARGET%\firefox.exe" (
  echo [OK] Portable Firefox already installed.
  exit /b 0
)

REM --- Try to find a downloaded package next to this script ---
pushd "%~dp0"
for %%f in (FirefoxPortable_*.paf.exe FirefoxPortable_*.exe Firefox*.zip *.paf.exe) do (
  if exist "%%f" set "PKG=%%f"
)
popd

if defined PKG (
  echo [..] Extracting %PKG% ...
  "%PKG%" /S /D=%TARGET%
) else (
  echo [INFO] No Firefox package found in %~dp0
  echo        Download Firefox Portable (portableapps.com) and run this script again,
  echo        OR place the installer zip in this folder.
)

if exist "%TARGET%\firefox.exe" (
  echo [OK] Installed to %TARGET%
) else (
  echo [WARN] firefox.exe not found yet. Place Portable Firefox in %TARGET%
)
exit /b 0
