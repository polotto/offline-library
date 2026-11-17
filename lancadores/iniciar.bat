@echo off
rem Windows: clique duplo neste arquivo. Feche a janela para parar.
cd /d "%~dp0"
if not exist "bin\lab-windows-amd64.exe" (
  echo Este disco nao tem o programa para Windows.
  pause
  exit /b 1
)
start "" cmd /c "timeout /t 4 >nul & start http://127.0.0.1:8087/"
"bin\lab-windows-amd64.exe" servir --dados "%~dp0."
pause
