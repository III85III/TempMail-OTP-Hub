@echo off
setlocal
cd /d "%~dp0"
title III85III - Temp Mail & OTP Hub (Web Edition)
color 0b

echo ==============================================================================
echo       III85III - Universal Temp Mail & Live OTP Web Hub
echo ==============================================================================
echo.
echo [*] Working Directory : %CD%
echo [*] Web Dashboard URL : http://127.0.0.1:8585
echo ------------------------------------------------------------------------------
echo.
echo [*] Launching Web Dashboard in your default browser...
echo [*] You can generate batch accounts, view successes, errors, and read OTP codes in the website!
echo [*] Press Ctrl+C in this window when you wish to close the server.
echo.

otp-receiver.exe -port 8585

pause
