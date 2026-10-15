# Private Gateway

Платформа управления личным VLESS + REALITY шлюзом и отдельным удалённым браузером. Xray передаёт трафик, Go API управляет устройствами и конфигурациями, PostgreSQL 18 хранит состояние. Клиентские профили выдаются стандартной VLESS-ссылкой и QR-кодом для совместимых приложений; для sing-box и Mihomo доступны конфигурации их формата.

## Что работает в версии 0.1

- Go API с `chi` и авторизацией Bearer; в production опубликован только через HTTPS Caddy.
- PostgreSQL 18, `pgx/v5`, запросы `sqlc`, встроенные миграции `golang-migrate`.
- Создание и отзыв устройств, отдельный UUID каждого устройства, экспорт VLESS URI, QR-кода и конфигураций sing-box/Mihomo.
- AES-256-GCM для учётных данных; главный ключ шифрования хранится вне базы.
- Проверка конфигурации настоящим Xray, атомарная запись и откат при ошибке перезапуска.
- CLI `gatewayctl` на Cobra и панель React / TypeScript / Vite / TanStack Query.
- Управление временным браузером в отдельном Docker-контейнере; noVNC через HTTPS и Basic Auth.
- Журнал действий, защищённые Prometheus metrics, HTTP instrumentation OpenTelemetry.
- CI: тесты Go/PostgreSQL, проверка гонок, сборка панели и тесты интерфейса.
- Docker Compose установщик Ubuntu 26.04 из проверенной сборки GitHub Actions; после успешной проверки коммита в `master` релиз автоматически доставляется на VPS.

Добавление или отзыв устройства **создаёт ожидающую применения ревизию**. Чтобы реально изменить доступ, выполните `config apply`. Панель показывает различие между желаемой и применённой ревизиями. Ключ отозванного устройства перестаёт работать после успешного применения.

## Локальная разработка

Нужны Linux, Docker с Compose, Node.js 22.12+ или 24, Go 1.26.9, `curl`, `python3`, `sha256sum`. В подготовленной облачной среде:

```bash
bash scripts/setup-cloud.sh
bash scripts/dev.sh start
bash scripts/dev.sh test
```

`setup-cloud.sh` устанавливает проверенные Go, sqlc и Xray в `/workspace/.toolchains`, собирает приложения и панель, запускает PostgreSQL 18 и выполняет миграции. `prepare` создаёт `.env` со случайными локальными секретами, только если файла ещё нет. Существующий `.env` сохраняется. Данные PostgreSQL остаются в Docker volume.

На другом Linux-хосте с установленными Go и Xray можно выполнить `bash scripts/dev.sh prepare`. Если требуется установщик инструментов, задайте `GATEWAY_TOOLCHAIN_ROOT` в доступный каталог. Go cache по умолчанию рассчитан на `/workspace`; вне облака используйте обычный Go из `PATH`.

Панель находится на loopback-порту 8080. Для входа используйте `GATEWAY_API_TOKEN` из своего локального `.env`; не отправляйте его в переписку или журналы. API и панель имеют один origin. Для CLI:

```bash
set -a
source .env
set +a
bin/gatewayctl status
bin/gatewayctl node list
bin/gatewayctl device add macbook --node NODE_ID
bin/gatewayctl device list
bin/gatewayctl config validate
bin/gatewayctl config apply
bin/gatewayctl device export DEVICE_ID
bin/gatewayctl device revoke DEVICE_ID
bin/gatewayctl config apply
```

Экспорт выводит действующий секретный URI. UUID устройства из `device list` — идентификатор записи, отличный от UUID доступа внутри профиля.

Локальные параметры используют `127.0.0.1:8443` для Xray: это проверка разработки, а не общедоступный VPN. Перед развёртыванием задайте реальный `GATEWAY_HOST`, порт, SNI/target REALITY и постоянные секреты согласно [инструкции](docs/deployment.md).

Остановка процессов и PostgreSQL с сохранением данных:

```bash
bash scripts/dev.sh stop
```

## Удалённый браузер

На VPS он входит в Docker Compose по [docs/browser.md](docs/browser.md). Браузер работает, но удалённая графическая сессия noVNC сейчас реагирует на ввод и прокрутку с задержкой около 1–3 секунд. Для обычного просмотра сайтов удобнее локальный браузер с совместимым VLESS + REALITY клиентом. Управление через панель или:

```bash
bin/gatewayctl browser start
bin/gatewayctl browser status
bin/gatewayctl browser stop
```

Сессия временная: после остановки данные браузера удаляются. Контейнер не получает секретов шлюза и не монтирует каталоги хоста. noVNC доступен через HTTPS Caddy с отдельной Basic Auth.

## Проверки и документация

`scripts/dev.sh test` выполняет тесты с настоящим PostgreSQL и Xray. Тест Xray передаёт HTTP через локальный REALITY-туннель, отзывает старый ключ и проверяет новый. Без `TEST_DATABASE_URL` интеграционные тесты PostgreSQL пропускаются; без `PRIVATE_GATEWAY_REAL_XRAY_BINARY` реальные проверки Xray пропускаются. Такой запуск не заменяет интеграционные проверки.

```bash
go test -race ./...
go vet ./...
cd web
npm ci
npm test
npm run build
```

- [Архитектура и масштабирование](docs/architecture.md)
- [Развёртывание, ключи и резервные копии](docs/deployment.md)
- [OpenAPI 3.1](docs/openapi.yaml)
- [Проверки IP, DNS, IPv6, WebRTC и kill switch](docs/privacy-checks.md)
- [Импорт в совместимые клиенты](docs/clients.md)
- [Изолированный браузер](docs/browser.md)
- [История ошибок и их устранение](docs/troubleshooting.md)
- [Roadmap оставшихся задач](docs/roadmap.md)
- [Правила работы с репозиторием](AGENTS.md)

Версия 0.1 управляет одним Xray-узлом. На текущем VPS подтверждены Docker-развёртывание, вход в панель, серверный VLESS smoke test и подключение iPhone через Hiddify по LTE с исходящим IPv4 VPS. Это не заменяет проверки каждого нового устройства и режима маршрутизации; фактические результаты приведены в [проверках приватности](docs/privacy-checks.md). Текущие ограничения и планы описаны в [roadmap](docs/roadmap.md).
