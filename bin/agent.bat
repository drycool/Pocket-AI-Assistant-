@echo off
chcp 65001 > nul

set "BIN_DIR=%~dp0"
set "AGENT_EXE=%BIN_DIR%..\Agent\agent.exe"
set "AGENT_PY=%BIN_DIR%..\Agent\agent.py"

if exist "%AGENT_EXE%" (
    "%AGENT_EXE%"
) else if exist "%AGENT_PY%" (
    python "%AGENT_PY%"
) else (
    echo [ERROR] agent.exe или agent.py не найдены!
    pause
)
