@echo off
REM rag_selftest.bat - verify the RAG path from a field machine before relying on it.
REM
REM Usage:  rag_selftest.bat "your question"
REM
REM Runs the checks in the order the failure modes occur: is the parser present, is each
REM service answering, how fast is the router's local path, and what does the full
REM question cost end to end. ASCII-only on purpose - see the note in rag_query.bat.

setlocal
set QUERY=%~1

if "%QUERY%"=="" (
    echo usage: rag_selftest.bat "your question"
    exit /b 2
)

set PI=100.124.79.91

echo === 1. jq present? ===
where jq >nul 2>&1
if errorlevel 1 (echo   MISSING - rag_query.bat cannot parse answers without it) else (echo   ok)

echo.
echo === 2. Jev gateway, %PI%:8030 ===
REM No `//` on lightrag_enabled: jq's alternative operator treats `false` as absent, so
REM (.lightrag_enabled // "?") prints "?" precisely when the answer is false - the one
REM value this line exists to report. Found by running the program, not by reading it.
curl -s -m 10 "http://%PI%:8030/health" | jq -r "\"  status=\" + (.status // \"?\") + \" lightrag_enabled=\" + (.lightrag_enabled|tostring) + \" vector_timeout_s=\" + (.vector_timeout_s|tostring)"
if errorlevel 1 echo   NO ANSWER - Jev is down or unreachable

echo.
echo === 3. LightRAG graph, %PI%:8020 ===
curl -s -m 10 -o nul -w "  http_code=%%{http_code}\n" "http://%PI%:8020/health"
if errorlevel 1 echo   unreachable - the fallback path will not work either

echo.
echo === 4. router local path only (execute=false, expect milliseconds) ===
curl -s -m 30 -X POST "http://%PI%:8030/query" -H "Content-Type: application/json" -d "{\"query\": \"%QUERY%\", \"execute\": false}" | jq -r "\"  strategy=\" + (.routing_decision.strategy // \"?\") + \" target=\" + (.target_agent // \"?\") + \" elapsed=\" + ((.elapsed_ms // 0)|tostring) + \"ms degraded=\" + ((.degraded // false)|tostring)"
if errorlevel 1 echo   NO ANSWER - Jev is down or unreachable

echo.
echo === 5. full question through rag_query.bat (router, then graph fallback) ===
echo   start %TIME%
call "%~dp0rag_query.bat" "%QUERY%"
echo   end   %TIME%

echo.
echo === verdict ===
echo   Compare the two timestamps above. A router-only answer is milliseconds to about
echo   ten seconds; a graph fallback is roughly forty. If section 5 printed nothing, both
echo   paths failed and the GPU2 node is probably asleep.

endlocal
exit /b 0
