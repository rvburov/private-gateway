# Развёртывание на одном сервере

Ниже приведена инструкция для ручной установки. Она не означает, что эта версия уже проверена на вашем VPS. Для локальной разработки используйте `scripts/dev.sh` из README. Работайте в существующем checkout: облачная задача уже изолирована, отдельный Git worktree для неё не нужен.

## Что требуется

- Linux, Go 1.26.9 и Node.js 22.12+ или 24 для сборки; PostgreSQL 18.
- Xray, поддерживающий VLESS + REALITY и команду `run -test -config`.
- Публичный адрес узла и выбранный TCP-порт Xray; API остаётся на loopback.
- Ключи API, AES-GCM и REALITY; стабильная резервная копия мастер-ключа отдельно от дампа базы.
- Для браузера: отдельный пользователь, rootless Podman, Chromium/Xvfb/TigerVNC/noVNC; см. [browser.md](browser.md).

Проверьте доступность выбранного REALITY target с сервера и поддержку параметров профиля клиентом. Выбор домена и порта из примера сам по себе не гарантирует доступность соединения.

## Сборка и конфигурация

В checkout выполните:

```sh
mkdir -p .local/bin
go build -o .local/bin/gateway-api ./cmd/gateway-api
go build -o .local/bin/gatewayctl ./cmd/gatewayctl
npm --prefix web ci
npm --prefix web run build
```

Создайте системного пользователя `gateway`, если его ещё нет. Следующие команды установки выполняются после сборки из корня checkout:

```sh
sudo useradd --system --home-dir /var/lib/private-gateway --shell /usr/sbin/nologin gateway
sudo install -d -m 0755 /opt/private-gateway/bin /opt/private-gateway/web
sudo install -d -o root -g gateway -m 0750 /etc/private-gateway
sudo install -d -o gateway -g gateway -m 0700 /var/lib/private-gateway
sudo install -m 0755 .local/bin/gateway-api /opt/private-gateway/bin/gateway-api
sudo cp -R web/dist /opt/private-gateway/web/
```

`/opt/private-gateway` должен быть доступен сервису только для чтения. Установите PostgreSQL и выделите пользователю приложения отдельную базу. Права на миграцию нужны при обновлении схемы; публичный доступ к базе не требуется. Команды выше не заменяют процедуру обновления уже установленной версии.

Запустите `/opt/private-gateway/bin/gateway-api keygen` в защищённом терминале. Команда **выводит секреты в stdout**. Сохраните необходимые значения в `/etc/private-gateway/gateway.env`, доступном только root и пользователю `gateway`; не включайте вывод в CI-логи или Git. Для этого файла предпочтительны права `0640`, владелец `root:gateway` и каталог `0750`.

Задайте как минимум:

```ini
GATEWAY_DATABASE_URL=postgres://gateway:REPLACE_PASSWORD@127.0.0.1:5432/gateway?sslmode=disable
GATEWAY_API_TOKEN=REPLACE_WITH_GENERATED_TOKEN
GATEWAY_MASTER_KEY=REPLACE_WITH_GENERATED_MASTER_KEY
GATEWAY_REALITY_PRIVATE_KEY=REPLACE_WITH_GENERATED_REALITY_KEY
GATEWAY_SHORT_ID=REPLACE_WITH_GENERATED_SHORT_ID
GATEWAY_LISTEN=127.0.0.1:8080
GATEWAY_HOST=YOUR_SERVER_ADDRESS
GATEWAY_PORT=443
GATEWAY_SERVER_NAME=YOUR_REALITY_SERVER_NAME
GATEWAY_REALITY_TARGET=YOUR_REALITY_TARGET:443
GATEWAY_XRAY_CONFIG=/var/lib/private-gateway/xray.json
GATEWAY_XRAY_BINARY=/usr/local/bin/xray
GATEWAY_WEB_DIR=/opt/private-gateway/web/dist
```

`GATEWAY_MASTER_KEY` — стандартный base64 от ровно 32 байт. `GATEWAY_SHORT_ID` — 16 шестнадцатеричных символов. При удалённой базе используйте проверяемый TLS (`sslmode=verify-full` и доверенный сертификат). Пароль в URL должен быть закодирован по правилам URL. Пример с `sslmode=disable` относится к loopback.

Запустите миграции с теми же переменными и от имени владельца приложения. Временный systemd-unit загружает файл окружения без передачи секретов в аргументах команд:

```sh
sudo systemd-run --wait --pipe --collect \
  --property=User=gateway \
  --property=EnvironmentFile=/etc/private-gateway/gateway.env \
  --property=WorkingDirectory=/opt/private-gateway \
  /opt/private-gateway/bin/gateway-api migrate
```

Команда применяет миграции вверх; перед обновлением сохраняйте резервную копию базы. Не запускайте API со случайным новым мастер-ключом: существующие профили не расшифруются.

## Xray и systemd

Установите предоставленный unit, затем запустите сервис:

```sh
sudo install -m 0644 deploy/systemd/gateway-api.service /etc/systemd/system/gateway-api.service
sudo systemctl daemon-reload
sudo systemctl enable --now gateway-api
sudo systemctl status gateway-api
```

Для Xray настройте отдельный сервис, читающий `GATEWAY_XRAY_CONFIG`. Конфиг содержит UUID клиентов; ограничьте права на него и резервные копии. Настройте фиксированную команду перезапуска в `GATEWAY_XRAY_RESTART`: это JSON-массив аргументов, а не shell-строка. Например, для подготовленного user-service того же пользователя:

```ini
GATEWAY_XRAY_RESTART=["systemctl","--user","restart","private-gateway-xray.service"]
```

Такой вариант требует действующей пользовательской systemd-сессии/linger и корректных `XDG_RUNTIME_DIR` и D-Bus-настроек в окружении API. Unit оставляет `/run/user` доступным для соединения с сокетами, ограничивая запись в домашние каталоги через `ProtectHome=read-only`. Если используется системный unit Xray другого пользователя, подготовьте узко ограниченный helper или правило разрешения именно на этот restart, проверьте его отдельно. Не выдавайте API универсальный `sudo`, shell или возможность управлять произвольными unit-ами.

Привилегии для bind на TCP/443 настраиваются у сервиса Xray; API их не требует. При выборе rootless Xray можно использовать непривилегированный порт и отдельно настроенное перенаправление. Настройка привилегий и сетевых правил зависит от сервера и не выполняется приложением.

Добавление/отзыв устройства остаётся `pending`, пока не выполнено применение. Первое применение также требует работоспособной проверки Xray и настроенной команды перезапуска. Если проверка или перезапуск не завершились успешно, считайте конфигурацию неприменённой и устраните причину; не выдавайте такое состояние за успешный отзыв на работающем узле.

## Доступ и проверка

Откройте только порт трафика Xray. Администрируйте API и веб через SSH-туннель:

```sh
ssh -L 8080:127.0.0.1:8080 USER@YOUR_SERVER_ADDRESS
```

Откройте `http://127.0.0.1:8080`, введите административный токен и проверьте состояние. `/healthz` показывает состояние HTTP-процесса, `/readyz` — готовность сервиса. Готовность API ещё не подтверждает прохождение трафика через Xray.

Создайте тестовое устройство, примените конфигурацию, импортируйте профиль в Hiddify и проверьте соединение. Отзовите устройство, снова примените конфигурацию и убедитесь, что повторное подключение отклоняется. Затем выполните клиентские проверки [privacy-checks.md](privacy-checks.md). Проверка браузера описана отдельно в [browser.md](browser.md).

Сохраняйте PostgreSQL, мастер-ключ и необходимые сетевые ключи в раздельных защищённых резервных копиях. Восстановление нужно проверять в изолированной среде с тем же мастер-ключом. В CI используются только временные тестовые учётные данные; рабочие токены и ключи туда не нужны.
