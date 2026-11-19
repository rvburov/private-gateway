# Выходной VPN-сервер: ручная настройка и диагностика

Для новой установки используйте [общий мастер настройки](setup-vps.md). Он
создаёт пользователя и SSH-доступ, готовит отдельные ключи REALITY на VPS и
устанавливает агент для текущего сайта управления. Ниже сохранён ручной
порядок с отдельной проверкой каждого этапа.

## 1. Подключиться к новому выходному VPS

**Где:** ваш компьютер, Windows — Git Bash; macOS/Linux — терминал bash.
Установка предназначена для нового чистого Ubuntu 26.04 amd64 с доступом `sudo`.

**Действие:** подставьте данные нового VPS и выполните:

```bash
node_ip='IP_НОВОГО_VPS'
node_user='SSH_ПОЛЬЗОВАТЕЛЬ'
node_ssh_port='22'
ssh -p "$node_ssh_port" "$node_user@$node_ip"
```

При первом входе сверьте отпечаток SSH-хоста с консолью хостинга.

**Проверка — в открывшемся SSH на новом VPS:**

```bash
id -un
. /etc/os-release
printf 'OS=%s %s\n' "$ID" "$VERSION_ID"
uname -m
sudo -v
sudo -n true && printf 'SUDO_OK\n'
```

**Ожидаемый вывод:** имя SSH-пользователя и строки:

```text
OS=ubuntu 26.04
x86_64
SUDO_OK
```

До установки по инструкции [«Сеть VPS: восстановление и профилактика»](vps-network.md)
сохраните сетевые данные и проверьте доступ к консоли noVNC хостинга. Используйте
параметры нового VPS, не адреса нашего моста. DHCP сам по себе не ошибка;
статический IPv4 допустим только при поддержке провайдера и известных выданных
IP, префиксе, шлюзе и модели сети.

## 2. Установить Docker

**Где:** SSH нового VPS. Файл репозитория Docker:
`/etc/apt/sources.list.d/docker.sources`.

**Действие:**

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl python3 nano iproute2
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
node_ubuntu_codename=$(. /etc/os-release && printf '%s' "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
node_arch=$(dpkg --print-architecture)
sudo tee /etc/apt/sources.list.d/docker.sources > /dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $node_ubuntu_codename
Components: stable
Architectures: $node_arch
Signed-By: /etc/apt/keyrings/docker.asc
EOF
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
```

Команды рассчитаны на чистый сервер. Порядок установки сверяется с
[официальной инструкцией Docker](https://docs.docker.com/engine/install/ubuntu/).

**Проверка:**

```bash
sudo systemctl is-active docker
sudo docker version --format 'SERVER={{.Server.Version}}'
sudo docker compose version
```

**Ожидаемый вывод:**

```text
active
SERVER=<номер версии>
Docker Compose version <номер версии>
```

## 3. Проверить порты и доступ нового VPS к панели

**Где:** SSH нового VPS.

**Действие:** проверьте, что порты свободны и новый VPS достигает панели.

**Проверка:**

```bash
if node_listeners=$(sudo ss -H -ltn '( sport = :8443 or sport = :8444 )'); then
  if [ -z "$node_listeners" ]; then
    printf 'PORTS_FREE\n'
  else
    printf 'PORTS_BUSY\n'
  fi
else
  printf 'PORT_CHECK_FAILED\n'
fi
curl -q -4 --proxy '' --noproxy '*' --connect-timeout 5 --max-time 10 \
  --fail --silent --show-error --output /dev/null --write-out 'PANEL_HTTP=%{http_code}\n' \
  https://rb-gateway-3708079.duckdns.org:8443/readyz
```

**Ожидаемый вывод:**

```text
PORTS_FREE
PANEL_HTTP=200
```

При `PORTS_BUSY`, `PORT_CHECK_FAILED` или ошибке HTTPS не продолжайте установку.

## 4. Разрешить входящие порты нового VPS

**Где:** браузер → личный кабинет хостинга → firewall нового VPS.

**Действие:** разрешите входящие **TCP 8443** и **TCP 8444**. Сохраните
разрешение на ваш SSH-порт. Исходящие подключения к панели, REALITY target и
сайтам должны быть разрешены. Не очищайте существующие правила firewall.

**Проверка:** на странице firewall должны быть активные разрешения для обоих
TCP-портов. Фактическая проверка с компьютера выполняется в шаге 14, после
запуска слушателей. До этого подключения к портам могут завершаться отказом.

**Ожидаемый результат:** TCP 8443, TCP 8444 и SSH разрешены.
Консольного вывода у действия в кабинете хостинга нет.

Узел публикует порты через Docker. Для ограничения источников используйте
firewall хостинга либо согласованные правила Docker; одного UFW недостаточно.

## 5. Скачать выпуск на компьютер

**Где:** браузер на вашем компьютере →
[GitHub → Private Gateway → Actions → CI/CD](https://github.com/rvburov/private-gateway/actions/workflows/ci.yml).

**Действие:** выберите успешный запуск, откройте **Artifacts**, скачайте
`private-gateway-linux-amd64`. Распакуйте ZIP в
`$HOME/Downloads/private-gateway-linux-amd64` на вашем компьютере.

**Проверка — в локальном Git Bash, не в SSH:**

```bash
artifact="$HOME/Downloads/private-gateway-linux-amd64"
if [ -s "$artifact/release.tar.gz" ] && [ -s "$artifact/release.tar.gz.sha256" ] && \
   [ -f "$artifact/install/install-node.sh" ] && [ -f "$artifact/install/verify-release.py" ]; then
  printf 'ARTIFACT_FILES_OK\n'
else
  printf 'ARTIFACT_FILES_MISSING\n'
fi
```

**Ожидаемый вывод:**

```text
ARTIFACT_FILES_OK
```

## 6. Передать выпуск на новый VPS

**Где:** локальный Git Bash на вашем компьютере.
Файлы будут в `$HOME/private-gateway-node-install/` SSH-пользователя на VPS.

**Действие:**

```bash
artifact="$HOME/Downloads/private-gateway-linux-amd64"
node_ip='IP_НОВОГО_VPS'
node_user='SSH_ПОЛЬЗОВАТЕЛЬ'
node_ssh_port='22'
ssh -p "$node_ssh_port" "$node_user@$node_ip" 'mkdir -p ~/private-gateway-node-install'
scp -P "$node_ssh_port" "$artifact/release.tar.gz" "$artifact/release.tar.gz.sha256" \
  "$node_user@$node_ip:private-gateway-node-install/"
scp -P "$node_ssh_port" -r "$artifact/install" \
  "$node_user@$node_ip:private-gateway-node-install/"
```

**Проверка:**

```bash
ssh -p "$node_ssh_port" "$node_user@$node_ip" \
  'test -s "$HOME/private-gateway-node-install/release.tar.gz" && test -s "$HOME/private-gateway-node-install/release.tar.gz.sha256" && test -f "$HOME/private-gateway-node-install/install/install-node.sh" && test -f "$HOME/private-gateway-node-install/install/verify-release.py" && printf "UPLOAD_OK\n"'
```

**Ожидаемый вывод:**

```text
UPLOAD_OK
```

## 7. Проверить выпуск на VPS

**Где:** SSH нового VPS, каталог `$HOME/private-gateway-node-install/`.

**Действие:**

```bash
cd "$HOME/private-gateway-node-install"
```

**Проверка:**

```bash
sha256sum --check release.tar.gz.sha256
python3 install/verify-release.py --tar release.tar.gz
```

**Ожидаемый вывод:**

```text
release.tar.gz: OK
<SHA коммита из выбранного успешного запуска GitHub, 40 символов>
```

При несовпадении контрольной суммы не продолжайте.

## 8. Создать ключи TCP/Vision и XHTTP

**Где:** SSH нового VPS, каталог `$HOME/private-gateway-node-install/`.
Файлы ключей: `$HOME/.private-gateway-keygen/tcp.keys` и
`$HOME/.private-gateway-keygen/xhttp.keys`.

**Действие:** выполните один раз:

```bash
umask 077
install -d -m 0700 "$HOME/.private-gateway-keygen"
tar -xOf release.tar.gz bin/xray > "$HOME/.private-gateway-keygen/xray"
chmod 0700 "$HOME/.private-gateway-keygen/xray"
(set -o noclobber; "$HOME/.private-gateway-keygen/xray" x25519 > "$HOME/.private-gateway-keygen/tcp.keys")
(set -o noclobber; "$HOME/.private-gateway-keygen/xray" x25519 > "$HOME/.private-gateway-keygen/xhttp.keys")
python3 - "$HOME/.private-gateway-keygen" <<'PY'
import json
import secrets
import sys
from pathlib import Path

root = Path(sys.argv[1])
parameters = {
    'tcp_short_id': secrets.token_hex(8),
    'xhttp_short_id': secrets.token_hex(8),
    'xhttp_path': '/' + secrets.token_hex(16),
}
with (root / 'public-parameters.json').open('x') as output:
    json.dump(parameters, output)
for name in ('tcp', 'xhttp'):
    values = dict(line.split(':', 1) for line in (root / (name + '.keys')).read_text().splitlines() if ':' in line)
    public = values.get('Password (PublicKey)', values.get('PublicKey', '')).strip()
    if not public:
        raise SystemExit('Не найден открытый ключ: ' + name)
    print(name.upper() + '_PUBLIC_KEY=' + public)
    print(name.upper() + '_SHORT_ID=' + parameters[name + '_short_id'])
print('XHTTP_PATH=' + parameters['xhttp_path'])
print('KEYS_OK')
PY
```

**Проверка:**

```bash
stat -c '%a %n' "$HOME/.private-gateway-keygen/tcp.keys" \
  "$HOME/.private-gateway-keygen/xhttp.keys"
```

**Ожидаемый вывод действия:**

```text
TCP_PUBLIC_KEY=<открытый ключ TCP>
TCP_SHORT_ID=<16 hex-символов>
XHTTP_PUBLIC_KEY=<открытый ключ XHTTP>
XHTTP_SHORT_ID=<16 hex-символов>
XHTTP_PATH=/<32 hex-символа>
KEYS_OK
```

**Ожидаемый вывод проверки:** две строки с правами `600`.
Закрытые ключи остаются в файлах; не печатайте их и не отправляйте в чат.
Для открытого ключа используется `Password (PublicKey)`, а не `Hash32`.
При повторном входе в SSH продолжайте с готовыми файлами, не меняйте ключи.

## 9. Добавить выходной VPS на сайт

**Где:** браузер → `https://rb-gateway-3708079.duckdns.org:8443/` →
вход с 2FA → **Серверы → Добавить VPS**.

**Действие:** заполните поля:

| Поле | Значение |
| --- | --- |
| Название | Например, `Второй выходной VPS` |
| Адрес VPS | `IP_НОВОГО_VPS`, без схемы и порта |
| Порт TCP/Vision | `8443` |
| Server name REALITY | `www.cloudflare.com` |
| Открытый ключ REALITY | `TCP_PUBLIC_KEY` из шага 8 |
| Short ID | `TCP_SHORT_ID` из шага 8 |

В **Дополнительные транспорты VPS** включите **XHTTP + REALITY (HTTP/2)**:

| Поле | Значение |
| --- | --- |
| Порт XHTTP | `8444` |
| Открытый ключ XHTTP | `XHTTP_PUBLIC_KEY` из шага 8 |
| Short ID XHTTP | `XHTTP_SHORT_ID` из шага 8 |
| Путь XHTTP | `XHTTP_PATH` из шага 8, включая `/` |

**XHTTP + TLS (HTTP/3, UDP)** оставьте выключенным.
Нажмите **Сохранить VPS**. Сохраните локально идентификатор и код подключения.

**Проверка:** в браузере открылось окно **Подключить агент VPS** с полями
**Идентификатор VPS** и **Код подключения**. В карточке указан адрес нового VPS.

**Ожидаемый результат:** код подключения выдан. Консольного вывода у этого
действия нет. Код действует 15 минут; истекший код получите через
**Переподключить агент**. Вводите его только в скрытый запрос установщика.

## 10. Создать закрытый файл настроек

**Где:** SSH нового VPS. Создаваемый файл: `/root/private-gateway-node.env`.

**Действие:**

```bash
sudo python3 - "$HOME/.private-gateway-keygen" <<'PY'
import json
import os
import sys
from pathlib import Path

root = Path(sys.argv[1])
parameters = json.loads((root / 'public-parameters.json').read_text())
def private_key(name):
    values = dict(line.split(':', 1) for line in (root / (name + '.keys')).read_text().splitlines() if ':' in line)
    value = values.get('PrivateKey', '').strip()
    if not value:
        raise SystemExit('Не найден локальный закрытый ключ: ' + name)
    return value
values = {
    'GATEWAY_CONTROL_URL': 'https://rb-gateway-3708079.duckdns.org:8443',
    'GATEWAY_NODE_ID': 'ID_ИЗ_ПАНЕЛИ',
    'GATEWAY_HOST': 'IP_НОВОГО_VPS',
    'GATEWAY_PORT': '8443',
    'GATEWAY_REALITY_PRIVATE_KEY': private_key('tcp'),
    'GATEWAY_SERVER_NAME': 'www.cloudflare.com',
    'GATEWAY_SHORT_ID': parameters['tcp_short_id'],
    'GATEWAY_REALITY_TARGET': 'www.cloudflare.com:443',
    'GATEWAY_XHTTP_ENABLED': '1',
    'GATEWAY_XHTTP_PORT': '8444',
    'GATEWAY_XHTTP_PRIVATE_KEY': private_key('xhttp'),
    'GATEWAY_XHTTP_SHORT_ID': parameters['xhttp_short_id'],
    'GATEWAY_XHTTP_PATH': parameters['xhttp_path'],
    'GATEWAY_XHTTP_H3_ENABLED': '0',
}
fd = os.open('/root/private-gateway-node.env', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as output:
    for name, value in values.items():
        output.write(name + '=' + value + '\n')
print('Создан /root/private-gateway-node.env; ключи не выведены')
PY
sudo nano /root/private-gateway-node.env
```

В открытом редакторе замените `ID_ИЗ_ПАНЕЛИ` на **Идентификатор VPS** из шага 9,
`IP_НОВОГО_VPS` — на адрес из карточки. Сохраните: `Ctrl+O`, Enter; выйдите: `Ctrl+X`.
Если выбрали другой SNI, одинаково измените домен в `GATEWAY_SERVER_NAME`
и `GATEWAY_REALITY_TARGET`; в последнем сохраните `:443`.

**Проверка:**

```bash
sudo stat -c 'OWNER=%U MODE=%a' /root/private-gateway-node.env
if [ "$(sudo stat -c '%u %a' /root/private-gateway-node.env)" != '0 600' ]; then
  printf 'ENV_CHECK_FAILED\n'
else
  sudo grep -Eq 'ID_ИЗ_ПАНЕЛИ|IP_НОВОГО_VPS' /root/private-gateway-node.env
  node_env_check=$?
  case "$node_env_check" in
    0) printf 'ENV_NOT_READY\n' ;;
    1) printf 'ENV_READY\n' ;;
    *) printf 'ENV_CHECK_FAILED\n' ;;
  esac
fi
```

**Ожидаемый вывод:**

```text
OWNER=root MODE=600
ENV_READY
```

`GATEWAY_CONTROL_URL` должен оставаться точным HTTPS origin панели без `/`
на конце. Открытые параметры файла должны совпадать с карточкой VPS.

## 11. Установить и зарегистрировать агент

**Где:** SSH нового VPS, каталог `$HOME/private-gateway-node-install/`.
Установщик сохранит настройки в `/etc/private-gateway-node/stack.env`,
токен агента — в `/var/lib/private-gateway-node/agent.token`.

**Действие:**

```bash
cd "$HOME/private-gateway-node-install"
sudo bash install/install-node.sh --archive release.tar.gz --env-file /root/private-gateway-node.env
```

В запрос **Одноразовый токен узла:** вставьте код из шага 9, нажмите Enter.
Ввод скрыт. Дождитесь завершения команды.

**Проверка:**

```bash
sudo test -s /var/lib/private-gateway-node/agent.token && printf 'TOKEN_SAVED\n'
sudo docker ps --filter label=com.docker.compose.project=private-gateway-node \
  --filter label=com.docker.compose.service=node-agent --format '{{.Status}}'
```

**Ожидаемый вывод:** `TOKEN_SAVED` и статус агента, начинающийся с `Up`.
Это проверка сохранения токена и запуска контейнера; связь с панелью проверяется
в следующем шаге.
После первой установки может появиться сообщение:

```text
Агент запущен. Нажмите «Применить» для узла в панели, затем запустите --resume.
```

Если регистрация не успела до истечения кода, получите новый через
**Переподключить агент** и используйте `--resume` из следующего шага:
при отсутствии сохраненного токена установщик снова запросит код.

## 12. Применить конфигурацию и проверить Xray

**Где:** сначала браузер → **Серверы → карточка нового VPS**,
затем SSH нового VPS, каталог `$HOME/private-gateway-node-install/`.

**Действие:** на сайте нажмите **Применить**, затем в SSH выполните:

```bash
sudo bash install/install-node.sh --resume --archive release.tar.gz
```

**Проверка — в SSH:**

```bash
sudo docker ps --filter label=com.docker.compose.project=private-gateway-node \
  --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
node_agent=$(sudo docker ps -q --filter label=com.docker.compose.project=private-gateway-node \
  --filter label=com.docker.compose.service=node-agent)
sudo docker exec "$node_agent" python3 /usr/local/bin/wait-xray.py && printf 'TCP_XRAY_READY\n'
sudo docker exec "$node_agent" python3 /usr/local/bin/wait-xray.py xhttp && printf 'XHTTP_XRAY_READY\n'
```

**Проверка — на сайте:** в карточке **Примененная / нужная версия** совпадают и
больше нуля; последняя связь агента свежая, ошибки агента нет.

**Ожидаемый вывод установщика:**

```text
Удалённый узел: настроенные процессы Xray работают. Сверьте применённую ревизию в панели.
```

В таблице Docker — `node-agent`, `xray` и `xray-xhttp` со статусом `Up`;
затем обязательны строки `TCP_XRAY_READY` и `XHTTP_XRAY_READY`. Они подтверждают
запуск процессов с актуальной конфигурацией. Статуса Docker `(healthy)` для
этого недостаточно.

## 13. Создать первое устройство и применить его доступ

**Где:** браузер → панель Private Gateway.

**Действие:**

1. В **Серверы** у нового VPS оставьте **Напрямую к VPS**.
2. Откройте **Устройства → Добавить устройство**.
3. Введите **Название устройства**, например `Windows`.
4. В **Сервер подключения** выберите новый выходной VPS.
5. Нажмите **Добавить устройство**.
6. Вернитесь в **Серверы → карточка этого VPS → Применить**.

**Проверка:** на сайте версии снова совпадают, устройство появилось в списке.
Дополнительно **в SSH нового VPS**:

```bash
cd "$HOME/private-gateway-node-install"
sudo bash install/install-node.sh --resume --archive release.tar.gz
```

**Ожидаемый вывод:**

```text
Удалённый узел: настроенные процессы Xray работают. Сверьте применённую ревизию в панели.
```

## 14. Проверить входящие порты с компьютера

**Где:** Windows, локальный Git Bash. Все VPN отключены, их блок при обрыве
выключен. На macOS/Linux используйте `curl` вместо `curl.exe`.

**Действие:** подставьте адрес нового выходного VPS и проверьте оба порта.

**Проверка:**

```bash
node_ip='IP_НОВОГО_VPS'
for port in 8443 8444; do
  curl.exe -q -4 --proxy '' --noproxy '*' --connect-timeout 5 --max-time 7 \
    --verbose --output /dev/null --write-out '\ntcp_connect=%{time_connect}\n' \
    "telnet://$node_ip:$port" < /dev/null
done
```

**Ожидаемый вывод для каждого порта:** строка `Connected` или
`Established connection` с нужным адресом/портом и `tcp_connect` больше нуля.
Затем возможен `curl: (28) Time-out`: TELNET-проба не выполняет VLESS-обмен.
Если подключения не было и `tcp_connect=0.000000`, порт из этой сети не доступен.

## 15. Получить профиль и проверить выходной IP

**Где:** браузер → **Устройства → созданное устройство → Профиль**;
затем VPN-клиент на вашем компьютере и локальный Git Bash.

**Действие:** выберите **VLESS · TCP + REALITY + Vision (URI и QR)**,
скопируйте ссылку или QR, импортируйте в клиент. Подключите этот профиль
в режиме VPN всей системы/TUN. Все другие VPN должны быть отключены.

**Проверка — в локальном Git Bash при включенном проверяемом VPN:**

```bash
curl.exe -q -4 --proxy '' --noproxy '*' --connect-timeout 5 --max-time 15 \
  --fail --silent --show-error https://api.ipify.org/
printf '\n'
curl.exe -q -4 --proxy '' --noproxy '*' --connect-timeout 5 --max-time 15 \
  --fail --silent --show-error --output /dev/null --write-out 'HTTPS=%{http_code}\n' \
  https://example.com/
```

**Ожидаемый вывод:**

```text
<публичный IP нового выходного VPS>
HTTPS=200
```

## 16. Отдельно проверить XHTTP

**Где:** сайт → **Устройства → то же устройство → Профиль**,
VPN-клиент и локальный Git Bash.

**Действие:** получите профиль **VLESS · XHTTP + REALITY**, импортируйте его,
отключите TCP/Vision и подключите XHTTP в режиме VPN всей системы/TUN.

**Проверка:** повторите обе команды шага 15.

**Ожидаемый вывод:** тот же публичный IP нового выходного VPS и `HTTPS=200`.
Повторите подключение на телефоне и в каждой нужной сети.

Перед приёмкой и после обновлений выполните [проверки VPS](vps-network.md):
сверьте запущенное ядро и необходимость перезагрузки, сеть, политику перезапуска
и состояние служб. После нужной перезагрузки повторите шаги 15–16 с реальным
VPN и выходным IP. Настройка мониторинга `sysstat` описана там отдельно;
установщик её не выполняет.

Для входа через отдельный сервер следующий порядок: [настройка VPS-моста](bridge.md).
На выходном VPS сохраняется установленный Xray с агентом. `--resume` проверяет
текущий выпуск и не является командой обновления на другую версию.
