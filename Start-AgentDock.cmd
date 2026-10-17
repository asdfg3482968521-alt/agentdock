@echo off
setlocal
if not exist "%~dp0scripts\windows\AgentDock.Launcher.ps1" (
  echo Missing AgentDock launcher script.
  pause
  exit /b 1
)
start "" powershell.exe -NoProfile -Sta -ExecutionPolicy Bypass -WindowStyle Hidden -File "%~dp0scripts\windows\AgentDock.Launcher.ps1"
exit /b 0
