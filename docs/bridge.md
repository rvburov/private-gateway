# Подключение через второй VPS-мост

## Что получится

```text
Обычный VPN-клиент → входной VPS-мост → выходной VPS → сайты
                   TCP/Vision           ваш сервер в США
                   или XHTTP
```

Сайты видят выходной IP американского VPS. Мост пересылает зашифрованный
TCP-поток без завершения TLS/REALITY, без подмены SNI и без доступа к ключам
устройств. Он не добавляет второй слой шифрования. Хостинг моста может наблюдать
соединение с американским VPS; схема не скрывает адрес назначения от сети моста.

В панели выдаются обычные VLESS URI и QR. Клиент должен поддерживать выбранный
вариант: TCP + REALITY + Vision либо XHTTP + REALITY. Поддержка XHTTP есть не во
всех клиентах. Hysteria2, XHTTP H3/UDP и автоматическое переключение транспортов
этот мост не добавляет. Два транспорта имеют отдельные процессы и порты, но сеть,
ресурсы входного VPS и его доступность остаются общими.

**Новый VPS ещё нужно предоставить и проверить.** Готовый установщик и успешные
локальные тесты не подтверждают доступность будущего моста из МТС, Теле2 или
домашнего Wi-Fi. Российское размещение само по себе не гарантирует доступ к США.

## Какие данные нужны владельцу и агенту

Можно передать в чат только открытые данные:

- IPv4 входного VPS, страну/регион и название хостинга;
- ОС и архитектуру (`Ubuntu 26.04 amd64` — целевая проверяемая установка);
- имя пользователя SSH и порт SSH; доступ с `sudo`;
- желаемые входные TCP-порты: начальный вариант `443` для TCP/Vision и `8444`
  для XHTTP; при нестандартном SSH эти порты не должны его занимать;
- есть ли на этом VPS другие службы и какие порты они занимают;
- какие выходные VPS нужно подключить. Сейчас основной: `162.35.172.6`,
  TCP/Vision `8443`, XHTTP `8444`.

Пароль SSH, приватный SSH-ключ, VLESS UUID/URI, REALITY private key, пароль панели
и коды 2FA в чат не отправлять. Вход выполняется вашим SSH-ключом локально либо
через уже согласованный доступ агента. На мосту не нужны ключи REALITY,
PostgreSQL, панель управления или купленные SOCKS5-прокси.

Для начальной установки достаточно небольшого VPS с Docker Engine и Compose v2;
1 vCPU и 1 ГБ RAM — отправная точка для проверки личной нагрузки, не гарантия
скорости. Важнее доступность обоих маршрутов, пропускная способность и лимит
трафика. Весь VPN-трафик проходит через мост и учитывается его хостингом.

## 1. Проверить доступ к будущему мосту с вашего компьютера

**На Windows в Git Bash**, без запущенного VPN и без его блока при обрыве.
На macOS/Linux команды выполняются в bash, вместо `curl.exe` используйте `curl`.
Замените значения на полученные у хостинга:

```bash
bridge_ip='IP_НОВОГО_VPS'
bridge_user='SSH_ПОЛЬЗОВАТЕЛЬ'
bridge_ssh_port='22'
ssh -p "$bridge_ssh_port" "$bridge_user@$bridge_ip"
```

При первом входе сверяйте отпечаток SSH-хоста с консолью хостинга. Успешный вход
по SSH доказывает доступ только к этому SSH-порту; порты VPN проверяются после
установки моста. Повторите доступ из МТС, Теле2 и домашнего Wi-Fi, которые вам нужны.
Ключ SSH подтверждает право входа и не исправляет сетевой тайм-аут до сервера.

## 2. Проверить путь с нового VPS к выходному

**В открытом SSH-терминале нового VPS**, а не на Windows и не на американском VPS:

```bash
uname -m
cat /etc/os-release
sudo ss -ltnp
ip route get 162.35.172.6
python3 - <<'PY'
import socket
for port in (8443, 8444):
    try:
        with socket.create_connection(('162.35.172.6', port), timeout=5):
            print(f'TCP {port}: connected')
    except OSError as error:
        print(f'TCP {port}: FAILED ({error})')
PY
curl -q -4 --proxy '' --noproxy '*' \
  --resolve rb-gateway-3708079.duckdns.org:8443:162.35.172.6 \
  --connect-timeout 5 --max-time 10 --fail --silent --show-error \
  --output /dev/null --write-out 'HTTP=%{http_code} TCP=%{time_connect} TLS=%{time_appconnect}\n' \
  https://rb-gateway-3708079.duckdns.org:8443/readyz
```

Нужны `connected` на используемых TCP-портах. HTTP 200 проверяет панель и TLS,
а не VLESS. Если путь с моста до выхода не работает, сначала выбирается другой
доступный входной адрес/маршрут; замена SNI на клиенте TCP-доступность не создаёт.

На чистом новом Ubuntu VPS установите Docker Engine и Compose plugin из
официального репозитория. Если Docker уже установлен или есть другие контейнеры,
сначала сообщите их состояние; эти команды рассчитаны на новую машину.
**В SSH нового VPS**:

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl python3
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
bridge_ubuntu_codename=$(. /etc/os-release && printf '%s' "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
bridge_arch=$(dpkg --print-architecture)
sudo tee /etc/apt/sources.list.d/docker.sources > /dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $bridge_ubuntu_codename
Components: stable
Architectures: $bridge_arch
Signed-By: /etc/apt/keyrings/docker.asc
EOF
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
```

Источник: [официальная установка Docker для Ubuntu](https://docs.docker.com/engine/install/ubuntu/).
Проверьте **на новом VPS**:

```bash
sudo docker version
sudo docker compose version
python3 --version
```

Установщик не меняет SSH, настройки сети и firewall. В панели хостинга разрешите
входящие TCP-порты моста и сохраните доступ к SSH. Если UFW уже активен,
сначала посмотрите `sudo ufw status verbose`, затем добавьте только выбранные
порты (`sudo ufw allow 443/tcp`, `sudo ufw allow 8444/tcp`). Не выполняйте
`ufw reset`, не включайте его с пустыми правилами SSH и не очищайте iptables/nftables.
Мост использует host networking: его слушатели подчиняются правилам host firewall.
Слушатели работают одновременно по IPv4 и IPv6 (`v4v6`); поддержка IPv6 в ядре
должна быть включена даже при использовании только публичного IPv4. Наличие
публичного IPv6-адреса не требуется. Если публикуете AAAA-запись моста, отдельно
проверьте этот маршрут и разрешения IPv6 firewall.

## 3. Подготовить назначения в панели и скачать конфигурацию

**В браузере**, используя пока рабочее подключение к панели:

1. Откройте `https://rb-gateway-3708079.duckdns.org:8443/`, войдите с 2FA.
2. В **Серверы** найдите нужный **выходной** VPS.
3. Выберите **Через VPS-мост**, введите адрес нового VPS и его порты TCP/Vision
   и XHTTP. Сохраните способ подключения.
4. Скачайте конфигурацию моста, сохраните файл как `bridge.json`.

Скачанный файл содержит назначения **всех выходных VPS, использующих тот же
нормализованный адрес моста**. Для нескольких выходных VPS задайте каждому свои
входные порты: например, первый `443/8444`, второй `9443/9444`. Целевые порты
каждого выхода остаются его собственными. Не называйте один мост разными DNS-именами:
панель не может доказать, что разные алиасы означают одну машину.

Пример файла с открытыми параметрами (панель подставляет актуальные значения):

```json
{
  "version": 1,
  "listeners": [
    {"name": "main-tcp", "transport": "tcp", "listen_port": 443,
     "target_host": "162.35.172.6", "target_port": 8443},
    {"name": "main-xhttp", "transport": "xhttp", "listen_port": 8444,
     "target_host": "162.35.172.6", "target_port": 8444}
  ]
}
```

Файл не содержит токенов, пользовательских UUID, паролей или ключей. Однако
сохранение адреса моста в панели **не устанавливает** пересылку на новом VPS.
Новые профили начнут направлять клиента на него сразу; до шага 4 он ещё не работает.

## 4. Установить мост из проверенного выпуска

В GitHub откройте **private-gateway → Actions → успешный CI/CD → Artifacts →
private-gateway-linux-amd64**. Выпуск должен содержать `install/install-bridge.sh`.
Скачайте и распакуйте ZIP. В нём находятся `release.tar.gz`, его `.sha256` и `install/`.

**На Windows в Git Bash**, при необходимости с рабочим VPN для скачивания:

```bash
artifact="$HOME/Downloads/private-gateway-linux-amd64"
bridge_config="$HOME/Downloads/bridge.json"
bridge_ip='IP_НОВОГО_VPS'
bridge_user='SSH_ПОЛЬЗОВАТЕЛЬ'
bridge_ssh_port='22'
ssh -p "$bridge_ssh_port" "$bridge_user@$bridge_ip" 'mkdir -p ~/private-gateway-bridge-install'
scp -P "$bridge_ssh_port" "$artifact/release.tar.gz" "$artifact/release.tar.gz.sha256" \
  "$bridge_config" "$bridge_user@$bridge_ip:private-gateway-bridge-install/"
scp -P "$bridge_ssh_port" -r "$artifact/install" \
  "$bridge_user@$bridge_ip:private-gateway-bridge-install/"
ssh -p "$bridge_ssh_port" "$bridge_user@$bridge_ip"
```

**Дальше в SSH-терминале нового VPS**:

```bash
cd "$HOME/private-gateway-bridge-install"
sha256sum --check release.tar.gz.sha256
python3 install/verify-release.py --tar release.tar.gz
sudo bash install/install-bridge.sh --archive release.tar.gz --config bridge.json --check
```

Сверьте напечатанный SHA с коммитом успешного Actions. `--check` проверяет файл,
доступность выходов, свободные входные порты, скачивает закреплённый HAProxy-образ
и проверяет конфигурации настоящим HAProxy; постоянные слушатели не запускает.
При ошибке остановитесь и пришлите текст ошибки без профиля устройства.

После успешной проверки, **в том же SSH-терминале**:

```bash
sudo bash install/install-bridge.sh --archive release.tar.gz --config bridge.json
sudo docker compose -p private-gateway-bridge \
  -f /etc/private-gateway-bridge/compose.json ps
```

Ожидаются независимые сервисы `tcp` и `xhttp` (либо только настроенный транспорт).
Для входного порта 443 управляющий HAProxy-процесс стартует с тремя разрешениями
`NET_BIND_SERVICE`, `SETUID`, `SETGID`; рабочий процесс после открытия слушателей
сбрасывает UID/GID до 65534. Файловая система контейнера доступна только для чтения,
Docker socket и секреты выхода не передаются.
Установщик отказывается работать на VPS с существующими контейнерами центральной
панели/выходного узла, при назначении выхода на эту же машину, пересечении портов
или невозможности соединиться с выходным VPS. TCP 22 зарезервирован для SSH;
нестандартный SSH-порт защищается проверкой занятого порта.

Повторный запуск с тем же файлом сохраняет контейнеры и назначения. При изменении
TCP-направления неизменённый XHTTP-сервис сохраняет контейнер. Пересоздание
изменённого сервиса обрывает его активные соединения; применение планируйте на
подходящее время. Ошибка запуска возвращает прежний стек; при первой неудачной
установке останавливается только проект `private-gateway-bridge`.

Мост не включён в автоматический деплой основной панели. Он не добавляется через
`scripts/add-server.sh`, не получает копию центральной БД и не регистрируется как
выходной агент. Обновлять его нужно отдельным проверенным выпуском и этой же командой.

## 5. Проверить с устройства и получить обычные ссылки

**На Windows в Git Bash**, отключив все VPN и их блок при обрыве, повторите для
своих входных портов. Число после `tcp_connect` и строка `Established connection`
или `Connected` подтверждают TCP; общий `CURL_EXIT=28` после соединения здесь
допустим, потому что TELNET-проба не ведёт VLESS/REALITY-обмен:

```bash
bridge_ip='IP_НОВОГО_VPS'
for port in 443 8444; do
  curl.exe -q -4 --proxy '' --noproxy '*' --connect-timeout 5 --max-time 7 \
    --verbose --output /dev/null --write-out '\ntcp_connect=%{time_connect}\n' \
    "telnet://$bridge_ip:$port" < /dev/null
done
```

Для текущего основного VPS можно отдельно проверить HTTPS панели через мост.
`--connect-to` сохраняет исходное TLS-имя и проверку сертификата:

```bash
curl.exe -q -4 --proxy '' --noproxy '*' \
  --connect-to "rb-gateway-3708079.duckdns.org:8443:$bridge_ip:443" \
  --connect-timeout 5 --max-time 10 --fail --silent --show-error \
  --output /dev/null --write-out 'HTTP=%{http_code} TCP=%{time_connect} TLS=%{time_appconnect}\n' \
  https://rb-gateway-3708079.duckdns.org:8443/readyz
```

Затем **на сайте → Устройства → нужное устройство → Профиль** скопируйте обычную
VLESS-ссылку TCP/Vision. Импортируйте её в Private Gateway Client или другой
совместимый клиент и подключитесь. Посмотрите внешний IP на HTTPS-сайте проверки:
для текущего основного выхода ожидается `162.35.172.6`, а не адрес моста.
Откройте несколько HTTPS-сайтов. Отдельно повторите с XHTTP-профилем и с телефона.
Проверьте на каждой нужной сети и сравните скорость/задержку: заранее обещать
сохранение скорости или успех на всех операторах нельзя.

Нажатие **Напрямую** в панели возвращает адрес выходного VPS в новых профилях.
После изменения маршрута обновите обычную подписку или замените импортированный
URI и переподключитесь. Старый специальный JSON-профиль SOCKS5-цепочки необходимо
удалить из клиента; обычная VLESS-ссылка такую локальную цепочку автоматически не снимает.

### Панель и загрузка подписки до подключения

Настройка моста не меняет DNS домена панели и адреса HTTPS-подписки. Если исходная
панель напрямую недоступна, для первого входа/импорта используйте рабочее
подключение; после подключения к новому VPN панель и подписка могут загружаться
через него. Обычный VLESS URI, уже импортированный в клиент, не требует повторного
скачивания подписки при каждом подключении.

Не меняйте вручную HTTPS-ссылку подписки на IP моста и не отключайте проверку TLS:
сертификат выдан домену панели. Отдельный постоянный домен для панели через мост
потребует согласованной настройки DNS и сертификата; это не происходит при выдаче
обычного VLESS-профиля. Успешный `curl --connect-to` — разовая диагностическая
проверка, он не меняет настройки браузера или клиента.

## Диагностика и возврат предыдущей конфигурации

**В SSH на VPS-мосте**:

```bash
sudo docker compose -p private-gateway-bridge -f /etc/private-gateway-bridge/compose.json ps
sudo docker compose -p private-gateway-bridge -f /etc/private-gateway-bridge/compose.json logs --tail 80
sudo ss -ltnp
```

Не включайте полный лог пользовательских соединений ради обычной диагностики.
Состояние `healthy` проверяет процесс/конфигурацию, а не авторизацию VLESS или
доступность сайтов. Для подтверждения работы нужны проверки шага 5.

При неудачном автоматическом откате установщик пишет отдельное сообщение.
Не удаляйте файлы `/etc/private-gateway-bridge`: `installed.json` хранит текущие
назначения, `previous.json` — предыдущие после успешного изменения, `compose-*.json`
и `configs/` позволяют их восстановить. Чтобы вернуть предыдущее назначение,
**на мосту** сохраните его в файл и повторите проверенный установщик:

```bash
sudo python3 - <<'PY' > "$HOME/bridge-previous.json"
import json
from pathlib import Path
data = json.loads(Path('/etc/private-gateway-bridge/previous.json').read_text())
print(json.dumps(data['config'], indent=2))
PY
cd "$HOME/private-gateway-bridge-install"
sudo bash install/install-bridge.sh --archive release.tar.gz --config "$HOME/bridge-previous.json"
```

В панели затем верните те же адреса и порты. Если предыдущего файла ещё нет,
восстанавливать нечего: исправьте исходный `bridge.json` после проверки ошибки.

Техническая основа: [TCP mode HAProxy](https://docs.haproxy.org/3.2/configuration.html#4.2-mode)
и [host networking Docker](https://docs.docker.com/engine/network/drivers/host/).
