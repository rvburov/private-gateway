@echo off
chcp 65001 >nul
set "HTTP_PROXY=http://127.0.0.1:2334"
set "HTTPS_PROXY=http://127.0.0.1:2334"
setx HTTP_PROXY "%HTTP_PROXY%" >nul
setx HTTPS_PROXY "%HTTPS_PROXY%" >nul
echo Прокси включён. IP в этом окне:
curl.exe -4 -sS --max-time 15 https://ipinfo.io
echo(
pause
