@echo off
setlocal EnableExtensions
set "AGENTDOCK_DIR=%~dp0"
rem Prefer the original native WPF desktop panel when it is installed.
if exist "%AGENTDOCK_DIR%agentdock-tray.exe" (
    start "" "%AGENTDOCK_DIR%agentdock-tray.exe"
    exit /b 0
)
if exist "%LOCALAPPDATA%\AgentDock\bin\agentdock-tray.exe" (
    start "" "%LOCALAPPDATA%\AgentDock\bin\agentdock-tray.exe"
    exit /b 0
)
rem Legacy small CLI bundle fallback: keep console visible and print errors.
if not exist "%AGENTDOCK_DIR%scripts\windows\AgentDock.Launcher.ps1" (
    echo [AgentDock] Desktop GUI and fallback launcher are both missing.
    echo Please install the original full desktop release:
    echo https://github.com/asdfg3482968521-alt/agentdock/releases/tag/agentdock-desktop-latest
    pause
    exit /b 1
)
echo AgentDock legacy launcher starting. This is NOT the original WPF desktop GUI.
echo If a PowerShell error occurs, the console will remain open for inspection.
powershell.exe -NoProfile -STA -ExecutionPolicy Bypass -File "%AGENTDOCK_DIR%scripts\windows\AgentDock.Launcher.ps1"
set "STATUS=%ERRORLEVEL%"
if not "%STATUS%"=="0" (
    echo.
    echo [AgentDock] Launcher exited with code %STATUS%.
    echo Read the error above. This console stays open until you press a key.
    pause
)
exit /b %STATUS%
