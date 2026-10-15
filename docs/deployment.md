# Развёртывание на VPS через Docker

Основной способ установки — Docker Compose на Ubuntu 26.04 amd64. Стек содержит PostgreSQL 18, Go API с панелью, Xray, временный Chromium/noVNC и Caddy. На сервере не нужны Go и Node.js: GitHub Actions собирает проверяемый архив и доставляет его по SSH. Для первого запуска не нужно скачивать архив на ПК: достаточно один раз подготовить ограниченный SSH-приёмник на VPS. После этого Docker запускает контейнеры самостоятельно после перезагрузки сервера.

Для текущего VPS используйте IPv4 `162.35.172.6` и домен панели `rb-gateway-3708079.duckdns.org`. Домен должен возвращать этот IPv4. Порт 443 занимает Xray, панель работает на HTTPS-порту 8443, Caddy использует порт 80 для выпуска сертификата. SSH остаётся на порту 22. Убедитесь в панели провайдера, что входящие TCP 22, 80, 443 и 8443 разрешены. Дополнительные порты API, PostgreSQL, браузера и noVNC наружу не публикуются.

## Чистый переход с прежней установки

Выбран новый набор устройств, база и ключи. Установщик сначала проверяет архив, собирает образы и запускает новую БД и API. Перед переключением портов он сохраняет конфигурацию и дамп старой базы в `/var/backups/private-gateway/`, затем останавливает старые службы. Если проверка Docker не проходит, он возвращает старые службы и удаляет незавершённый новый стек. После успеха старые файлы и учётные записи остаются для отката до реальной проверки клиента. **Старые профили VLESS с новой установкой не работают.** Создайте устройство заново и импортируйте новый URI.

В репозитории удалены прежний systemd-установщик и Podman worker. Старый выделенный SSH deploy-key переиспользуется: временный приёмник разрешает только передачу проверенного релиза, затем установщик заменяет его обработчиком обновлений Docker. После каждого коммита в `master` workflow **CI/CD** проверяет код, собирает релиз и только после успеха отправляет его на сервер. При ошибке проверок развёртывания не будет. Если более новый коммит уже попал в `master`, устаревший релиз не развёртывается.

## Первая настройка без ручной передачи архива

1. Создайте пять GitHub Secrets из раздела ниже. Они нужны только для SSH-доставки, а секреты приложения создаются на VPS.
2. Откройте **Actions → CI/CD** для последнего коммита `master`. После зелёного задания `verify` скачайте маленький артефакт **private-gateway-bootstrap** и распакуйте его на Windows, например в `$HOME\Downloads\private-gateway-bootstrap`. В нём пять файлов: `bootstrap.sh`, `bootstrap-receiver.sh`, `verify-release.py`, `commit.sha`, `SHA256SUMS`. Репозиторий закрытый, поэтому анонимная загрузка с `raw.githubusercontent.com` на VPS возвращает `404`. Большой артефакт **private-gateway-linux-amd64** скачивать не нужно.
3. Скопируйте только эту папку на VPS из PowerShell:

   ```powershell
   scp -r "$HOME\Downloads\private-gateway-bootstrap" "romanburov@162.35.172.6:~/"
   ```

   Подключитесь к VPS под `romanburov`. Проверка checksum должна пройти до запуска скрипта. `SHA` берётся из `commit.sha` того же артефакта, так что он соответствует проверенной сборке:

   ```bash
   cd ~/private-gateway-bootstrap
   sha256sum -c SHA256SUMS
   SHA=$(cat commit.sha)
   test -f "$HOME/private-gateway-linux-amd64/deploy-key.pub"
   sudo bash ./bootstrap.sh \
     --ref "$SHA" \
     --host 162.35.172.6 \
     --domain rb-gateway-3708079.duckdns.org \
     --deploy-key "$HOME/private-gateway-linux-amd64/deploy-key.pub"
   ```

   Запускайте `sudo bash` только если каждый файл получил `OK` и публичный ключ найден. Ожидаемая строка: `VPS is ready to receive the first Docker release from GitHub Actions.` Эта настройка сама по себе ещё не запускает Docker. Она сохраняет старые настройки SSH в `/var/backups/private-gateway/`. Не удаляйте старые файлы и службы до успешной установки и проверки клиента.
4. Вернитесь в **Actions → CI/CD** для текущего коммита: после зелёного `verify` этап `Deploy Docker to VPS` передаст архив, проверит его SHA и установит Docker. Если он уже упал до настройки SSH, нажмите **Re-run failed jobs**. При следующем коммите в `master` то же произойдёт автоматически. Первая установка может длиться несколько минут: Docker загружает образы и собирает браузер. Успех отмечен строкой `First Docker deployment verified: <SHA>`; затем последующие обновления выводят `Docker deployment verified: <SHA>`.
5. Откройте `https://rb-gateway-3708079.duckdns.org:8443/`. Новый токен создан автоматически. Прочитайте его **на своём сервере**, не отправляя в переписку:

   ```bash
   sudo sed -n 's/^GATEWAY_API_TOKEN=//p' /etc/private-gateway-docker/stack.env
   ```

   Введите токен в поле входа. Проверьте «API подключён», «База данных подключена», «Xray настроен» и равенство применённой и новой ревизий.

6. Создайте новое устройство, нажмите «Проверить» и «Применить конфигурацию», скопируйте новый VLESS URI и импортируйте в Hiddify. Для телефона перенесите URI как текст. `127.0.0.1` в сообщении об импорте на iPhone относится к самому iPhone, не к серверу. Проверьте подключение с мобильного интернета и открытие HTTPS-сайта. Один успешный TCP-тест порта 443 не доказывает, что VLESS работает.
7. Для браузера нажмите «Запустить браузер» и откройте ссылку из панели. Если браузер запросит HTTP Basic Auth, пользователь — `browser`, пароль — **тот же новый токен**. После завершения нажмите «Остановить браузер». Данные сессии находятся в tmpfs и стираются при остановке.

## GitHub Secrets для автоматического развёртывания

В GitHub откройте **Settings → Secrets and variables → Actions → New repository secret**. Создайте пять секретов:

| Имя | Значение |
| --- | --- |
| `DEPLOY_HOST` | `162.35.172.6` |
| `DEPLOY_PORT` | `22` |
| `DEPLOY_USER` | `gateway-deploy` |
| `DEPLOY_SSH_KEY` | Полное содержимое закрытого ключа Windows `$HOME\.ssh\private-gateway-actions`. Вставляйте его только в поле GitHub Secret, не в переписку и не в репозиторий. |
| `DEPLOY_KNOWN_HOSTS` | Строка ключа хоста вида `162.35.172.6 ssh-ed25519 ...` из доверенного подключения. На VPS проверьте отпечаток `sudo ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`, затем получите публичный ключ через `sudo cat /etc/ssh/ssh_host_ed25519_key.pub` и добавьте перед ним `162.35.172.6 `. Отпечаток должен совпасть с ранее сохранённым доверенным отпечатком сервера. |

Это только SSH-доступ для доставки релиза. `GATEWAY_API_TOKEN`, ключи REALITY, пароль PostgreSQL и другие секреты приложения остаются в `/etc/private-gateway-docker/stack.env` на VPS. `.env` на вашем ПК GitHub Actions не читает. Закрытый SSH-ключ должен работать без интерактивного ввода пароля в Actions. Настройка секретов и SSH-приёмника выполняется один раз; после неё ваш ПК для развёртывания не нужен.

Для проверки откройте **Actions → CI/CD** после следующего коммита в `master`: сначала завершится `verify`, затем `Deploy Docker to VPS`. Успешный этап выводит `Docker deployment verified: <SHA>`. На VPS можно сравнить `sudo cat /etc/private-gateway-docker/installed.version` с SHA коммита и проверить `sudo docker compose --env-file /etc/private-gateway-docker/stack.env -f /opt/private-gateway-docker/current/docker/compose.yml ps`. Новый релиз сохраняет существующие устройства, базу и ключи Docker. Перед миграцией создаётся дамп PostgreSQL и копия env в `/var/backups/private-gateway/`; при ошибке выполняется откат. Если откат тоже завершился ошибкой, не повторяйте запуск без проверки этого backup и состояния контейнеров.

## Проверка и диагностика

Эти команды не выводят URI устройств:

```bash
sudo docker compose --env-file /etc/private-gateway-docker/stack.env \
  -f /opt/private-gateway-docker/current/docker/compose.yml ps
sudo docker compose --env-file /etc/private-gateway-docker/stack.env \
  -f /opt/private-gateway-docker/current/docker/compose.yml logs --tail=100 api xray browser caddy
curl --noproxy '*' -fsS \
  --resolve rb-gateway-3708079.duckdns.org:8443:127.0.0.1 \
  https://rb-gateway-3708079.duckdns.org:8443/readyz
sudo ss -lntp | grep -E ':(22|80|443|8443)[[:space:]]'
```

Если Hiddify показывает тайм-аут, а порт 443 отвечает, проверьте настоящий VLESS-трафик на VPS:

```bash
sudo python3 /opt/private-gateway-docker/current/docker/vpn-smoke.py
```

Проверка берёт один активный профиль через внутренний API, сверяет его с загруженной конфигурацией Xray, запускает временный локальный Xray-клиент и делает HTTPS-запрос через VLESS. URI, UUID и ключи не выводятся; временный конфиг клиента удаляется после проверки. При сбое команда показывает этап: сверка профиля, соединение VLESS, TLS или ответ сайта. `VLESS traffic OK` подтверждает работоспособность профиля и Xray **на сервере**; подключение из сети телефона нужно проверить отдельно.

Если проверка обрывается на `HTTPS handshake through VLESS`, запустите дополнительную изолированную проверку:

```bash
sudo python3 /opt/private-gateway-docker/current/docker/vpn-smoke.py --isolated
```

Она сверяет публичный ключ профиля с закрытым ключом Xray через OpenSSL, затем временно запускает вторую копию Xray на локальном порту с той же конфигурацией и удаляет временные файлы. Работающий контейнер не меняется. Если изолированная проверка проходит, ищите причину в сетевом пути Docker или окружении контейнера. Если она падает, строка `isolated Xray` покажет тип отказа без вывода UUID и ключей.

Если результат — `REALITY rejected: handshake did not complete successfully`, проверьте на временной копии другое имя сайта прикрытия: `sudo python3 /opt/private-gateway-docker/current/docker/vpn-smoke.py --isolated --isolated-target www.cloudflare.com`. Это меняет SNI и target только во временном тесте; рабочий профиль остаётся прежним. При неудаче можно повторить с `www.wikipedia.org` или `www.apple.com`. Успех покажет, что исходный target не подходит для этой версии Xray и сетевого пути VPS; после этого нужно отдельно обновить серверную конфигурацию и перевыпустить URI устройств.

С Windows проверьте DNS, TCP и настоящий HTTPS с проверкой сертификата:

```powershell
Resolve-DnsName rb-gateway-3708079.duckdns.org -Type A
Test-NetConnection rb-gateway-3708079.duckdns.org -Port 443
curl.exe --noproxy "*" --silent --show-error --output NUL `
  --write-out "http=%{http_code} tls=%{ssl_verify_result}`n" `
  https://rb-gateway-3708079.duckdns.org:8443/
```

`http=200 tls=0` подтверждает панель, но не клиентский VLESS-трафик. Если Hiddify всё ещё не работает, сравните URI с новым профилем, проверьте применённую ревизию, журналы Xray **на сервере**, попытку с другой мобильной сети и время устройства. Не публикуйте URI, UUID доступа, ключи или полные журналы без очистки.

## Хранение, откат и удаление старого стека

Файл `/etc/private-gateway-docker/stack.env` с правами `0600` содержит секреты. PostgreSQL находится в Docker volume `private-gateway_postgres-data`, состояние Xray — в `/var/lib/private-gateway-docker/xray`, сертификаты Caddy — в Docker volume `private-gateway_caddy-data`. Для восстановления нужны согласованные копии базы и env-файла; один дамп базы не позволит расшифровать профили. Делайте внешнюю зашифрованную копию и проверяйте восстановление отдельно. `docker compose down --volumes` удаляет новую базу и сертификаты; не используйте эту команду для обычной остановки. Для перезапуска сервера достаточно `sudo reboot`, контейнеры используют `restart: unless-stopped`.

После **успешной проверки нового клиента и резервной копии** можно удалить старые systemd-службы, Podman worker, host PostgreSQL и старые папки релизов. До этого они отключены, но их файлы оставлены как точка возврата. Не удаляйте их во время диагностики Hiddify. Очистка старой установки остаётся задачей [roadmap](roadmap.md). Не запускайте старые и новые Xray/Caddy одновременно на портах 443/8443.
