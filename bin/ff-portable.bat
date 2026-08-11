@echo off
REM =============================================================
REM  Portable Firefox - zero-trace launch (internal logic)
REM  Usage: call bin\ff-portable.bat
REM  Uses drive letter of this script so all data stays on USB.
REM =============================================================
setlocal

set "SELF=%~dp0"                 REM ...\bin\
set "ROOT=%SELF%.."
set "FF=%ROOT%\Tools\FirefoxPortable\firefox.exe"
set "PROF=%ROOT%\Data\firefox\profile"
set "CACHE=%ROOT%\Data\firefox\cache"
set "DLDIR=%ROOT%\Data\downloads"

if not exist "%FF%" (
  echo [ERROR] Firefox not found: %FF%
  echo         Run scripts\install-firefox.bat first.
  exit /b 1
)

REM Ensure writable folders exist (works from any drive letter)
if not exist "%PROF%"  mkdir "%PROF%"
if not exist "%CACHE%" mkdir "%CACHE%"
if not exist "%DLDIR%" mkdir "%DLDIR%"

REM Inject USB paths into user.js (replace X:\ placeholder with real drive)
set "USERJS=%ROOT%\Data\firefox\user.js"
powershell -NoProfile -Command ^
  "$p='%USERJS%'; $c=Get-Content $p -Raw; " ^
  "$c=$c -replace 'browser\.download\.dir\", \"[^\"]*\"','browser.download.dir", "%DLDIR%\"'; " ^
  "$c=$c -replace 'X:\\Data\\\\downloads','%DLDIR%'; Set-Content $p $c" >nul 2>&1

REM Override cache dir via profile (Firefox resolves profile-local paths)
if not exist "%PROF%\cache2" (
  mkdir "%PROF%\cache2" >nul 2>&1
)

REM Launch isolated portable profile
start "" "%FF%" -profile "%PROF%" -no-remote

endlocal
exit /b 0
