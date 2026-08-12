@echo off

set "BIN_DIR=%~dp0"
set "TOOLS_DIR=%BIN_DIR%..\Tools\FirefoxPortable"
set "PROFILE_DIR=%BIN_DIR%..\Data\firefox"

if not exist "%PROFILE_DIR%" mkdir "%PROFILE_DIR%"

if exist "%TOOLS_DIR%\FirefoxPortable.exe" (
    start "" "%TOOLS_DIR%\FirefoxPortable.exe" -profile "%PROFILE_DIR%" -no-remote
) else if exist "%TOOLS_DIR%\firefox.exe" (
    start "" "%TOOLS_DIR%\firefox.exe" -profile "%PROFILE_DIR%" -no-remote
) else if exist "%TOOLS_DIR%\App\Firefox64\firefox.exe" (
    start "" "%TOOLS_DIR%\App\Firefox64\firefox.exe" -profile "%PROFILE_DIR%" -no-remote
) else if exist "%TOOLS_DIR%\App\Firefox\firefox.exe" (
    start "" "%TOOLS_DIR%\App\Firefox\firefox.exe" -profile "%PROFILE_DIR%" -no-remote
) else (
    echo [ERROR] firefox.exe ili FirefoxPortable.exe ne najden v Tools\FirefoxPortable\
    echo Prover'te raspolozhenie fajlov v papke Tools!
    pause
)