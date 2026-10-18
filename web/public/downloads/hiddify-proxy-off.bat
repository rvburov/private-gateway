@echo off
chcp 65001 >nul
set "HTTP_PROXY="
set "HTTPS_PROXY="
setx HTTP_PROXY "" >nul
setx HTTPS_PROXY "" >nul
echo Переменные прокси очищены. Для других программ откройте новое окно.
pause
