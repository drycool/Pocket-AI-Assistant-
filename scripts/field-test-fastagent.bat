@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion

rem ============================================================
rem  Полевой тест FastAgent на гостевом Windows-ПК.
rem
rem  Пять ОТДЕЛЬНЫХ сессий (каждая со своим файлом ввода) — так ответы
rem  y/n не "съедают" следующие вопросы:
rem    L0    — команды выполняться не должны (блок)
rem    L1    — только чтение, без подтверждения
rem    L2-no — починка, оператор отказывает (n)
rem    L2-yes— починка, оператор подтверждает (y)  [ipconfig /flushdns]
rem    L2-blk— опасная (format) и незнакомая команда -> блок
rem
rem  Безопасность: гостевой ПК меняется только одним безобидным
rem  сбросом кэша DNS в сессии L2-yes.
rem
rem  Запуск: двойной щелчок по этому файлу.
rem ============================================================

set "USB_DRIVE=%~dp0..\"
set "EXE=%USB_DRIVE%FastAgent\fastagent.exe"
set "CFG=%USB_DRIVE%Data\config.fast.json"
set "TESTS=%USB_DRIVE%tests\"
set "LOGDIR=%USB_DRIVE%Data\logs"

set "D=%DATE:/=-%"
set "T=%TIME::=%"
set "T=!T: =0!"
set "T=!T:,=-!"
set "STAMP=!D!_!T:~0,6!"
set "REPORT=%LOGDIR%\field-test-!STAMP!.txt"

echo ===================================================
echo   FIELD TEST: FastAgent (L0 / L1 / L2)
echo ===================================================
echo.

if not exist "%EXE%" (
    echo [ERROR] Не найден "%EXE%"
    echo         Сборка на Pi: bash scripts/build-fast-agent.sh fast
    pause
    exit /b 1
)
if not exist "%CFG%" (
    echo [ERROR] Не найден "%CFG%"
    echo         Скопируйте Data\config.fast.example.json в Data\config.fast.json
    echo         и вставьте ключ DeepSeek в поле api_key.
    pause
    exit /b 1
)
if not exist "%LOGDIR%" mkdir "%LOGDIR%"

rem --- предупреждение о правах: sfc/dism/wevtutil требуют администратора ---
net session >nul 2>&1
if errorlevel 1 (
    echo [!] Скрипт запущен НЕ от администратора.
    echo     Команды, которым нужны права ^(dism, sfc, некоторые wevtutil^),
    echo     вернут код 1 или 5. Для полного теста закройте окно и запустите
    echo     этот файл через правый клик -^> "Запуск от имени администратора".
    echo.
)

rem --- гигиена отчёта: прошлые журналы действий откладываем, иначе в сводный
rem     отчёт попадут команды ПРЕДЫДУЩИХ прогонов (у нас так и было: в отчёте
rem     прогона 4 лежали записи с '??' от прогонов 2-3) ---
for %%F in ("%LOGDIR%\actions_*.log") do (
    if exist "%%~fF" move /y "%%~fF" "%LOGDIR%\prev_!STAMP!_%%~nF.log" >nul 2>&1
)

echo [*] Сводный отчёт: %REPORT%
echo.

call :RUN "L0"    "0" "field-L0.txt"        "команды выполняться НЕ должны"
call :RUN "L1"    "1" "field-L1.txt"        "только чтение, без вопросов"
call :RUN "L2-no" "2" "field-L2-fix-no.txt" "отказ оператора (n)"
call :RUN "L2-yes" "2" "field-L2-fix-yes.txt" "подтверждение (y): ipconfig /flushdns"
call :RUN "L2-blk" "2" "field-L2-block.txt" "format и незнакомая команда -> BLOCK"

rem --- сводный отчёт + журнал действий ---
copy /b "%LOGDIR%\field-L0-!STAMP!.txt" + "%LOGDIR%\field-L1-!STAMP!.txt" + "%LOGDIR%\field-L2-no-!STAMP!.txt" + "%LOGDIR%\field-L2-yes-!STAMP!.txt" + "%LOGDIR%\field-L2-blk-!STAMP!.txt" "%REPORT%" >nul 2>&1
if exist "%LOGDIR%\actions_*.log" (
    echo. >> "%REPORT%"
    echo ===== ЖУРНАЛ ДЕЙСТВИЙ ===== >> "%REPORT%"
    type "%LOGDIR%\actions_*.log" >> "%REPORT%" 2>nul
)

echo ===================================================
echo [OK] Тест завершён.
echo.
echo      Сводный отчёт : %REPORT%
echo      Стенограммы   : %LOGDIR%\field-L*-!STAMP!.txt
echo      Журнал команд : %LOGDIR%\actions_*.log
echo.
echo      Пришлите сводный отчёт целиком.
echo ===================================================
pause
exit /b 0

:RUN
set "TAG=%~1"
set "LEVEL=%~2"
set "INFILE=%~3"
set "TITLE=%~4"
set "OUT=%LOGDIR%\field-!TAG!-!STAMP!.txt"
echo === [!TAG!] уровень L!LEVEL!: !TITLE! ===
"%EXE%" -level !LEVEL! < "%TESTS%!INFILE!" > "!OUT!" 2>&1
type "!OUT!"
echo.
exit /b 0
