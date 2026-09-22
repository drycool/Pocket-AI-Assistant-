@echo off
REM rag_query.bat - ask the local knowledge base a question, from a field machine.
REM
REM Usage:  rag_query.bat "your question"            -> router only
REM         rag_query.bat "your question" graph      -> router first, then the LightRAG graph
REM
REM The router (http://100.124.79.91:8030) answers an exact FTS5 hit in tens of
REM milliseconds and synthesises an answer through its agent tier in about ten seconds.
REM
REM The graph fallback is opt-in, not automatic, and that is a measured decision, not a
REM preference. Measured 2026-09-22 on an Espero manual lookup:
REM
REM   router (exact FTS5 + agent)   10.1 s   correct torque specification
REM   LightRAG graph, direct        37.0 s   answer about Argon ONE V3 case assembly for
REM                                          Raspberry Pi 5 - a different subject entirely,
REM                                          and the model itself said the retrieved context
REM                                          did not match the question
REM
REM The graph carves retrieval from a knowledge base that mixes the Espero manual with
REM Raspberry Pi accessory notes, so for manual lookups it is slower AND, on this query,
REM less accurate. Silently preferring it would be a pessimisation dressed up as a
REM fallback. Ask for it when you actually want synthesis across documents.
REM
REM When the GPU2 VRAM upgrade lands and JEV_LIGHTRAG_ENABLED=1 is set, the router serves
REM the graph itself with its own budget, and this second mode becomes redundant.
REM
REM This file is deliberately ASCII-only. A Cyrillic literal in a .bat depends on the
REM console code page, and that is how the earlier field runs produced mojibake and
REM truncated verdicts. The question arrives as an argument, so its encoding is whatever
REM the calling console uses.
REM
REM The answer is never captured into a batch variable. Model answers contain &, |, >,
REM quotes and newlines, and cmd.exe would either mangle or execute them. The answer goes
REM to a temp file and is printed with `type`, which preserves it byte for byte.
REM
REM Known limitation: a double quote inside the question breaks the JSON body. The previous
REM version had the same weakness; escaping it portably in cmd.exe is not possible, so the
REM behaviour is documented rather than half-fixed.

setlocal
set QUERY=%~1
set MODE=%~2

if "%QUERY%"=="" (
    echo usage: rag_query.bat "your question" [graph]
    exit /b 2
)

set JEV_URL=http://100.124.79.91:8030
set LIGHTRAG_URL=http://100.124.79.91:8020
set RAW=%TEMP%\jev_rag_%RANDOM%%RANDOM%.json
set ANS=%TEMP%\jev_rag_%RANDOM%%RANDOM%.txt

REM --- 1) the router, with synthesis -----------------------------------------
curl -s -m 90 -X POST "%JEV_URL%/query" -H "Content-Type: application/json" -d "{\"query\": \"%QUERY%\", \"execute\": true}" -o "%RAW%" 2>nul
jq -r ".agent_response // empty" "%RAW%" > "%ANS%" 2>nul

for %%F in ("%ANS%") do if %%~zF GTR 2 (
    type "%ANS%"
    echo.
    echo ---
    jq -r "\"router: strategy=\" + (.routing_decision.strategy // \"?\") + \" elapsed=\" + ((.elapsed_ms // 0)|tostring) + \"ms degraded=\" + ((.degraded // false)|tostring) + (if .fallback_reason then \" fallback=\" + .fallback_reason else \"\" end)" "%RAW%" 2>nul
    del "%RAW%" "%ANS%" 2>nul
    exit /b 0
)

REM --- 2) the router had nothing ---------------------------------------------
if /i not "%MODE%"=="graph" (
    echo [the router returned no answer]
    echo [to ask the LightRAG graph directly, re-run with:  rag_query.bat "your question" graph]
    echo [note: the graph path costs roughly 40 seconds and mixes the manual with]
    echo [      Raspberry Pi accessory notes, so check that the answer matches the question]
    del "%RAW%" "%ANS%" 2>nul
    exit /b 1
)

echo [router had no answer - asking the LightRAG graph directly] 1>&2
curl -s -m 150 -X POST "%LIGHTRAG_URL%/query" -H "Content-Type: application/json" -d "{\"query\": \"%QUERY%\", \"mode\": \"hybrid\"}" -o "%RAW%" 2>nul
jq -r ".response // .context // empty" "%RAW%" 2>nul
echo.
echo ---
echo source: LightRAG graph, direct (not the router)

del "%RAW%" "%ANS%" 2>nul
exit /b 0
