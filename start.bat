@echo off
rem Switch the console to UTF-8 so this file's text displays correctly in GBK terminals.
chcp 65001 >nul 2>&1
rem ARTEX supervisor script (Windows)
rem
rem Usage:
rem   start.bat                  Run in the foreground (Ctrl-C to stop)
rem   start.bat -addr :9000      Pass extra arguments through to artex unchanged
rem
rem Run artex.exe and decide whether to restart it based on its exit code.
rem
rem   0      Normal user stop    -> Exit the loop
rem   75     Restart requested   -> Restart immediately (after "One-click Update" or "Rollback" in the UI)
rem   Other  Crash               -> Restart with backoff (1->2->4… up to 60 seconds)
rem
rem artex handles downloads, SHA256 verification, and binary replacement at startup
rem in the selfupdate package. Keep this script simple; see the explanation at the top of start.sh.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] Executable not found: %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] Exited normally
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem Update/rollback is ready: artex replaces the binary at the next startup.
	echo [artex] Restart requested: applying the new version…
	set /a delay=1
	goto loop
)

echo [artex] Unexpected exit ^(code=!code!^), restarting in !delay!s 1>&2
rem timeout fails in redirected consoles; use ping instead (N seconds requires N+1 pings).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
