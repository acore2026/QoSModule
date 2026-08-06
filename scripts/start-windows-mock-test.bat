@echo off
setlocal

set SCRIPT_DIR=%~dp0
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT_DIR%start-windows-mock-test.ps1" %*

if errorlevel 1 (
  echo.
  echo Failed to start QoS mock test services.
  pause
  exit /b 1
)

echo.
echo QoS mock test startup command completed.
pause
