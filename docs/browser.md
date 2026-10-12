# Удалённый браузер

Браузер запускается в отдельном Docker-контейнере с Chromium, Xvfb, VNC и noVNC. API обращается к небольшому HTTP worker внутри закрытой сети Docker; доступ к Docker socket ему не нужен. Контейнер браузера не получает ключ REALITY, мастер-ключ или доступ к PostgreSQL. Он работает под UID 10001 с read-only rootfs, без Linux capabilities, с лимитами RAM/CPU/PID и временными tmpfs для профиля и файлов.

В панели нажмите «Запустить браузер», дождитесь статуса «Запущен» и откройте ссылку вида `https://ДОМЕН:8443/browser/vnc.html`. Caddy защищает маршрут HTTP Basic Auth: пользователь `browser`, пароль совпадает с административным токеном панели. Порт 6080 наружу не открывается. Закрытие вкладки не завершает сеанс: нажмите «Остановить браузер», чтобы удалить временные файлы. Одновременно поддерживается один сеанс.

Серверная проверка после запуска:

```bash
sudo docker compose --env-file /etc/private-gateway-docker/stack.env \
  -f /opt/private-gateway-docker/current/docker/compose.yml \
  exec -T browser python3 /app/smoke.py --port 6080
```

Она проверяет HTTP-страницу, WebSocket, VNC handshake и размер экрана 1440×900. При ошибке смотрите `docker compose logs browser` из [инструкции по развёртыванию](deployment.md). Старые инструкции для Podman, `pasta`, `slirp4netns` и отдельного socket worker к Docker-стеку не относятся.

Chromium запущен с `--no-sandbox`, поскольку его внутренний sandbox несовместим с ограничениями контейнера. Границей является Docker-контейнер, а не отдельная VM. Для более сильной изоляции браузер следует вынести в отдельную VM; это пункт [roadmap](roadmap.md). Не используйте сеанс для хранения постоянных паролей: его данные удаляются после остановки.
