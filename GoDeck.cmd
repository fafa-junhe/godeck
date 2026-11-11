@echo off
cd /d "%~dp0dist\windows-amd64"
set "PATH=%~dp0dist\windows-amd64;%PATH%"
start "" "%~dp0dist\windows-amd64\GoDeck.exe" %*
