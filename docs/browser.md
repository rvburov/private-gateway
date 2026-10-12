# Изолированный браузер на сервере

Браузер — дополнительный компонент. API создаёт одно временное окружение Chromium + Xvfb + TigerVNC + noVNC в **rootless Podman**. Cookies, история и файлы находятся в tmpfs и удаляются при остановке; долговременный профиль пока не поддерживается.

Для этого компонента нужны Linux, Podman с локальным remote-клиентом, slirp4netns, subordinate UID/GID ranges, утилиты newuidmap/newgidmap и cgroup v2 с делегированными лимитами памяти, CPU и процессов. Rootless storage и слои образа остаются на диске пользователя worker; writable browser profile — только в tmpfs. Планируйте около 1 ГБ на образ и 1 ГБ RAM на контейнер. При недоступном Podman, образе или ограничениях ядра API возвращает ошибку, а не готовый браузер.

## Разделение пользователей

В production запускайте API как `gateway`, а Podman worker — как отдельного `gateway-browser`. Worker не должен читать ключи REALITY, токен API, ключ шифрования БД, данные PostgreSQL или административные SSH-ключи. API получает доступ к **Unix socket worker**, а не к rootful Docker/Podman. Этот socket даёт полный контроль над контейнерами пользователя worker; поэтому worker должен обслуживать только этот компонент. Контейнеру socket и файлы хоста никогда не монтируются.

Пример для systemd-хоста (административные команды выполняются при развёртывании):

```sh
sudo groupadd --system gateway-browser-control
sudo useradd --create-home --shell /bin/bash gateway-browser
sudo usermod --append --groups gateway-browser-control gateway-browser
sudo usermod --append --groups gateway-browser-control gateway
# Проверьте /etc/subuid и /etc/subgid: у gateway-browser должны быть
# непересекающиеся диапазоны минимум по 65536 subordinate IDs.
sudo install -m 0644 deploy/browser/tmpfiles.conf /etc/tmpfiles.d/private-gateway-browser.conf
sudo systemd-tmpfiles --create /etc/tmpfiles.d/private-gateway-browser.conf
sudo install -d -o gateway-browser -g gateway-browser /home/gateway-browser/.config/systemd/user
sudo install -o gateway-browser -g gateway-browser -m 0644 \
  deploy/browser/private-gateway-browser-worker.socket \
  deploy/browser/private-gateway-browser-worker.service \
  /home/gateway-browser/.config/systemd/user/
sudo loginctl enable-linger gateway-browser
worker_uid=$(id -u gateway-browser)
sudo -u gateway-browser env XDG_RUNTIME_DIR=/run/user/"$worker_uid" \
  systemctl --user daemon-reload
sudo -u gateway-browser env XDG_RUNTIME_DIR=/run/user/"$worker_uid" \
  systemctl --user enable --now private-gateway-browser-worker.socket
```

Учитывайте реальный home worker, если он отличается от примера. После добавления группы перезапустите API, чтобы процесс получил новые supplementary groups. Устанавливайте Podman, slirp4netns и UID mapping utilities через официальный пакетный менеджер ОС. Не включайте rootful engine, `--privileged` или `seccomp=unconfined` для обхода ошибок.

В окружении API:

```dotenv
GATEWAY_BROWSER_CONNECTION=unix:///run/private-gateway-browser/podman.sock
GATEWAY_BROWSER_IMAGE=localhost/private-gateway-browser:0.1
GATEWAY_BROWSER_PORT=6080
```

Без `GATEWAY_BROWSER_CONNECTION` API обращается к локальному Podman текущего пользователя. Это допустимо для разработки без настоящих шлюзовых секретов; production использует отдельного worker. Manager отклоняет запуск под UID 0 и engine с `host.security.rootless=false`. Соединения с worker на другом сервере пока не поддерживаются: HTTP readiness проверяется на loopback того же хоста.

## Сборка образа

Соберите образ **от имени worker**, чтобы он попал в его rootless storage. Worker должен иметь доступ к отдельной копии каталога `deploy/browser`, без ключей и конфигурации приложения:

```sh
podman build --format docker --tag localhost/private-gateway-browser:0.1 \
  --file deploy/browser/Containerfile deploy/browser
podman info --format '{{.Host.Security.Rootless}}'
```

Последняя команда должна вернуть `true`. Образ использует Debian-пакеты через HTTPS с проверкой подписей apt. При корпоративном HTTPS proxy передавайте существующие proxy bindings через `--build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg NO_PROXY`. Для доверенного CA предусмотрен временный build secret `trust_ca`; например, BuildKit:

Формат `docker` нужен для сохранения `HEALTHCHECK` в образе Podman. Без него стандартный OCI-формат Podman игнорирует проверку и API не сможет подтвердить готовность браузера.

```sh
docker build --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg NO_PROXY \
  --secret id=trust_ca,src=/etc/ssl/certs/ca-certificates.crt \
  --tag localhost/private-gateway-browser:0.1 \
  --file deploy/browser/Containerfile deploy/browser
```

Docker-команда проверяет сборку образа; она не подтверждает работоспособность rootless Podman. При таком способе образ необходимо экспортировать и загрузить через `podman load` от имени worker. Значения proxy и доверенный CA не сохраняются в слоях образа. Не отключайте проверку TLS, подписей или checksums. Базовый образ закреплён по digest официального образа Python; обновляйте его после проверки новой версии. Пакеты Debian получают обновления при пересборке без устаревшего кэша.

## Запуск и доступ

Используйте кнопки браузера в веб-интерфейсе либо API с административным Bearer-токеном:

- `GET /api/v1/browser` — проверить готовность;
- `POST /api/v1/browser/start` — создать браузер и дождаться работоспособности;
- `POST /api/v1/browser/stop` — остановить и удалить окружение.

API проверяет состояние rootless engine, image healthcheck всех обязательных процессов и HTTP-страницу noVNC. Успешный ответ содержит `running: true` и URL `http://127.0.0.1:6080/vnc.html?autoconnect=true&resize=remote`. Неудачный запуск не возвращает URL готового браузера. Для диагностики выполните от имени worker `podman logs private-gateway-browser` и `podman healthcheck run private-gateway-browser`.

Команда `python3 deploy/browser/smoke.py --port 6080` дополнительно проверяет настоящий путь HTTP → WebSocket → VNC и размер framebuffer. Выполняйте её на сервере либо через уже открытый SSH tunnel. Она не обращается к публичным сайтам.

noVNC публикуется **только на 127.0.0.1** worker-хоста. Подключение из своего компьютера:

```sh
ssh -N -L 127.0.0.1:6080:127.0.0.1:6080 administrator@gateway-host
```

Откройте возвращённый loopback URL на своём компьютере. Оставляйте SSH tunnel открытым только во время сеанса. Если локальный порт занят, используйте другой локальный порт в `-L` и открываемом URL; значение `GATEWAY_BROWSER_PORT` задаёт серверный порт. VNC внутри контейнера доступен только на его loopback; прямой VNC не публикуется. Сам noVNC не имеет отдельного пароля: доступ ограничивают SSH и права локальных пользователей worker-хоста. Не публикуйте этот порт через публичный reverse proxy.

## Граница изоляции

Manager задаёт non-root UID 10001, `--cap-drop=ALL`, `no-new-privileges`, read-only filesystem, отдельные PID/network namespaces, три ограниченных tmpfs, 1 ГБ памяти без дополнительного swap, 2 CPU и максимум 256 процессов. Временный `/home/browser` монтируется с правом создания каталогов, а процесс браузера создаёт внутри него свой `private` с режимом `0700`: Podman не принимает `uid` и `gid` в опциях `--tmpfs`. Host mounts отсутствуют. `slirp4netns:allow_host_loopback=false` закрывает доступ из контейнера к loopback-службам хоста через gateway slirp. Доступ в интернет разрешён; компонент не является firewall для всего исходящего трафика.

Chromium запускается с `--no-sandbox`: его вложенный namespace/setuid sandbox несовместим с запрещёнными здесь привилегиями на типичном хосте. Границей изоляции служит rootless OCI-контейнер и отдельный пользователь worker, а не Chromium process sandbox. Компрометация браузера может затронуть другие процессы и данные внутри этого временного контейнера. Изоляция ядром не равна отдельной VM; для более строгой границы используйте отдельную VM и отдельный worker protocol в последующей версии.

## Что проверить на целевом хосте

Убедитесь, что API стартует браузер через выделенный socket и возвращает готовность. В SSH tunnel проверьте видимое окно Chromium и открытие HTTPS-страницы. Остановите браузер, проверьте `running: false`, затем начните новый сеанс: cookies и файлы предыдущего должны отсутствовать. Проверьте `ss -ltn`: серверный 6080 слушает только 127.0.0.1, а внешний IP хоста не отвечает на этом порту. Контейнер должен терять готовность при завершении Chromium/Xvfb/TigerVNC/noVNC; API не должен возвращать успех только потому, что сохранилась HTTP-страница.
