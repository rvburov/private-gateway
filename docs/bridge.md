## 1. Подключиться к отдельному VPS-мосту и проверить ОС

**Действие:** арендуйте чистый VPS: Ubuntu 26.04 amd64, публичный IPv4, пользователь с `sudo`. Начальная конфигурация для личной нагрузки — 1 vCPU и 1 ГБ RAM. Запишите IP, SSH-порт и пользователя. Полноценный выходной VPS добавляется по [другой инструкции](node-setup.md).

Настройте вход по [SSH-ключу: создание, установка открытой части и проверка](ssh-access.md).
SSH-ключ используется для управления мостом. VPN-ключи REALITY остаются на
американском выходном VPS; на мосту новые пары REALITY не создаются.

Выключите все VPN и их настройку «Блок при обрыве / Kill Switch». Подставьте данные нового VPS:

```bash
bridge_ip='IP_НОВОГО_VPS'
bridge_user='SSH_ПОЛЬЗОВАТЕЛЬ'
bridge_ssh_port='22'
bridge_key="$HOME/.ssh/private_gateway_bridge_ed25519"
ssh -i "$bridge_key" -o IdentitiesOnly=yes -p "$bridge_ssh_port" "$bridge_user@$bridge_ip"
```

При первом входе сверьте отпечаток SSH с консолью хостинга.

**Где:** ваш компьютер → Windows Git Bash; на macOS/Linux — терминал bash. После входа — SSH нового VPS-моста.

**Проверка:** после входа:

```bash
printf 'SSH_OK user=%s host=%s\n' "$(id -un)" "$(hostname)"
sudo -v
```

```bash
. /etc/os-release
printf 'OS=%s VERSION=%s\n' "$ID" "$VERSION_ID"
printf 'ARCH=%s\n' "$(uname -m)"
```

**Ожидаемый результат:** `SSH_OK user=<ваш пользователь> host=<имя нового VPS>`; `sudo -v` завершается без ошибки. Повторите вход из каждой нужной сети. SSH-ключ подтверждает право входа, но не исправляет сетевой тайм-аут. ОС и архитектура:

```text
OS=ubuntu VERSION=26.04
ARCH=x86_64
```

## 2. Установить утилиты

**Действие:**

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl python3 iproute2
```

**Где:** SSH нового VPS-моста.

**Проверка:**

```bash
command -v curl python3 ip ss
python3 --version
```

**Ожидаемый результат:** четыре пути к программам и строка `Python 3.<версия>`. Номер версии динамический; ошибок `command not found` нет.

## 3. Проверить путь и HTTPS американского выхода

**Действие:**

```bash
ip route get 162.35.172.6
python3 - <<'PY'
import socket
for port in (8443, 8444):
    with socket.create_connection(('162.35.172.6', port), timeout=5):
        print(f'TCP {port}: connected')
PY
```

Сразу после Python:

```bash
printf 'TCP_CHECK_EXIT=%s\n' "$?"
```

```bash
curl -q -4 --proxy '' --noproxy '*' \
  --resolve rb-gateway-3708079.duckdns.org:8443:162.35.172.6 \
  --connect-timeout 5 --max-time 10 --fail --silent --show-error \
  --output /dev/null --write-out 'HTTP=%{http_code} TCP=%{time_connect} TLS=%{time_appconnect}\n' \
  https://rb-gateway-3708079.duckdns.org:8443/readyz
```

**Где:** SSH нового VPS-моста. Назначения: `162.35.172.6:8443` для TCP/Vision и `162.35.172.6:8444` для XHTTP.

**Проверка:**

```bash
printf 'HTTPS_CHECK_EXIT=%s\n' "$?"
```

**Ожидаемый результат:** маршрут с `dev <интерфейс>` и `src <адрес>`, затем:

```text
TCP 8443: connected
TCP 8444: connected
TCP_CHECK_EXIT=0
```

При тайм-ауте дальше не переходите: нужен доступный путь от моста до выхода. Успех TCP еще не проверяет авторизацию VLESS.

`HTTP=200 TCP=<время больше 0> TLS=<время больше 0>`, затем `HTTPS_CHECK_EXIT=0`. Это проверка панели и сертификата, работа VPN проверяется отдельно.

## 4. Проверить свободные порты и поддержку IPv6

**Действие:**

```bash
sudo ss -ltnp
sudo python3 - <<'PY'
import socket
for port in (443, 8444):
    with socket.socket(socket.AF_INET6, socket.SOCK_STREAM) as probe:
        probe.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
        probe.bind(('::', port))
        print(f'LISTEN_PORT_{port}=FREE')
PY
```

**Где:** SSH нового VPS-моста.

**Проверка:**

```bash
printf 'PORT_CHECK_EXIT=%s\n' "$?"
```

**Ожидаемый результат:**

```text
LISTEN_PORT_443=FREE
LISTEN_PORT_8444=FREE
PORT_CHECK_EXIT=0
```

SSH должен использовать другой порт. TCP 22 зарезервирован установщиком для SSH. Поддержка IPv6 в ядре нужна для dual-stack слушателей даже без публичного IPv6. Если порты заняты или IPv6 отключен, шаг не пройден.

## 5. Установить Docker Engine и Compose из официального репозитория

**Действие:**

```bash
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
bridge_ubuntu_codename=$(. /etc/os-release && printf '%s' "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
bridge_arch=$(dpkg --print-architecture)
sudo tee /etc/apt/sources.list.d/docker.sources > /dev/null <<EOF_DOCKER
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $bridge_ubuntu_codename
Components: stable
Architectures: $bridge_arch
Signed-By: /etc/apt/keyrings/docker.asc
EOF_DOCKER
sudo apt-get update
```

```bash
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
```

**Где:** SSH нового чистого VPS-моста. Файлы: `/etc/apt/keyrings/docker.asc` и `/etc/apt/sources.list.d/docker.sources`. Источник: [официальная инструкция Docker для Ubuntu](https://docs.docker.com/engine/install/ubuntu/).

**Проверка:**

```bash
apt-cache policy docker-ce
```

```bash
sudo systemctl is-active docker
sudo docker version --format 'CLIENT={{.Client.Version}} SERVER={{.Server.Version}}'
sudo docker compose version
```

**Ожидаемый результат:** в `Candidate:` указана версия, а не `(none)`; источник пакетов содержит `download.docker.com/linux/ubuntu`.

`active`, строка `CLIENT=<версия> SERVER=<версия>`, затем `Docker Compose version <версия>`. Все команды завершаются без ошибки.

## 6. Разрешить входные порты

**Действие:** в firewall хостинга разрешите входящие TCP 443 и TCP 8444; сохраните правило своего SSH-порта. На VPS проверьте UFW:

```bash
if command -v ufw > /dev/null; then
  sudo ufw status verbose
else
  printf 'UFW_NOT_INSTALLED\n'
fi
```

Только при `Status: active` добавьте:

```bash
sudo ufw allow 443/tcp
sudo ufw allow 8444/tcp
sudo ufw status verbose
```

**Где:** firewall — личный кабинет хостинга; команды — SSH нового VPS-моста. Установщик firewall не меняет. Не сбрасывайте правила и не включайте неактивный UFW этим порядком.

**Проверка:** в активном UFW найдите `443/tcp` и `8444/tcp`; в кабинете хостинга сверьте сохраненные правила. Внешняя проверка будет после запуска слушателей на шаге 14.

**Ожидаемый результат:** TCP 443/8444 разрешены. В активном UFW — строки `ALLOW`; при неактивном — `Status: inactive`; при отсутствующем — `UFW_NOT_INSTALLED`.

## 7. Открыть панель

**Действие:** откройте `https://rb-gateway-3708079.duckdns.org:8443/`, войдите с 2FA. Если напрямую сайт недоступен, используйте рабочее VPN-подключение для входа и скачивания файлов.

**Где:** браузер вашего компьютера.

**Проверка:** откройте раздел **«Серверы»**.

**Ожидаемый результат:** видны карточки выходных VPS. Мост не меняет адрес панели и загрузки HTTPS-подписки. Уже импортированный VLESS URI не требует загрузки подписки при каждом подключении. Не заменяйте домен HTTPS-подписки на IP моста и не отключайте проверку TLS.

## 8. Сохранить мост у американского выходного VPS

**Действие:** в карточке действующего американского VPS заполните:

| Поле | Значение |
| --- | --- |
| Способ подключения | `Через VPS-мост` |
| Адрес VPS-моста | Публичный IPv4 нового моста |
| Порт TCP/Vision на мосту | `443` |
| Порт XHTTP на мосту | `8444` |

Нажмите **«Сохранить способ подключения»**. Собственные адрес и порты американского выходного VPS не заменяйте: назначения остаются `162.35.172.6:8443` и `162.35.172.6:8444`.

**Где:** браузер → панель → **«Серверы»** → карточка американского выходного VPS.

**Проверка:** сверьте строку **«Сохраненный маршрут»** и кнопку **«Скачать настройку моста»**.

**Ожидаемый результат:** сохранен маршрут через IP нового моста, кнопка скачивания доступна. Сохранение еще не устанавливает пересылку; новые профили уже ведут на мост, который запустится на шаге 13.

## 9. Скачать настройку моста

**Действие:** нажмите **«Скачать настройку моста»**. Сохраните файл как `bridge.json` в папку `Downloads` пользователя Windows.

Для нескольких выходных VPS задайте один и тот же адрес моста и разные входные порты: например, первому `443/8444`, второму `9443/9444`. Скачанный файл объединяет выходы с тем же нормализованным адресом моста. Разные DNS-алиасы одной машины панель не объединяет.

**Где:** браузер → **«Серверы»** → карточка выходного VPS. Путь файла в Git Bash: `$HOME/Downloads/bridge.json`.

**Проверка:** в Git Bash:

```bash
test -s "$HOME/Downloads/bridge.json" && printf 'BRIDGE_CONFIG_FILE_OK\n'
printf 'BRIDGE_CONFIG_PATH=%s\n' "$HOME/Downloads/bridge.json"
```

**Ожидаемый результат:** `BRIDGE_CONFIG_FILE_OK` и полный путь к файлу. Файл содержит назначения без ключей REALITY, UUID устройств и паролей.

## 10. Скачать проверенный выпуск

**Действие:** откройте [GitHub → Actions → CI/CD](https://github.com/rvburov/private-gateway/actions/workflows/ci.yml), выберите успешный запуск нужного коммита. В **Artifacts** скачайте `private-gateway-linux-amd64`. Распакуйте ZIP так, чтобы архив, checksum и папка `install` лежали непосредственно в `$HOME/Downloads/private-gateway-linux-amd64`. Запишите SHA выбранного коммита.

**Где:** браузер и проводник Windows. В Git Bash `$HOME` — домашняя папка пользователя; полный путь покажет проверка.

**Проверка:** в Git Bash:

```bash
artifact="$HOME/Downloads/private-gateway-linux-amd64"
test -s "$artifact/release.tar.gz" &&
  test -s "$artifact/release.tar.gz.sha256" &&
  test -s "$artifact/install/install-bridge.sh" &&
  test -s "$artifact/install/verify-release.py" &&
  printf 'BRIDGE_RELEASE_FILES_OK\n'
printf 'ARTIFACT_PATH=%s\n' "$artifact"
```

**Ожидаемый результат:** `BRIDGE_RELEASE_FILES_OK` и полный путь. Если строки `BRIDGE_RELEASE_FILES_OK` нет, структура распакованной папки неверна или файлы отсутствуют.

## 11. Передать файлы на мост

**Действие:**

```bash
bridge_ip='IP_НОВОГО_VPS'
bridge_user='SSH_ПОЛЬЗОВАТЕЛЬ'
bridge_ssh_port='22'
bridge_key="$HOME/.ssh/private_gateway_bridge_ed25519"
artifact="$HOME/Downloads/private-gateway-linux-amd64"
ssh -i "$bridge_key" -o IdentitiesOnly=yes -p "$bridge_ssh_port" "$bridge_user@$bridge_ip" 'mkdir -p "$HOME/private-gateway-bridge-install"'
scp -i "$bridge_key" -o IdentitiesOnly=yes -P "$bridge_ssh_port" "$artifact/release.tar.gz" "$artifact/release.tar.gz.sha256" \
  "$HOME/Downloads/bridge.json" "$bridge_user@$bridge_ip:private-gateway-bridge-install/"
scp -i "$bridge_key" -o IdentitiesOnly=yes -P "$bridge_ssh_port" -r "$artifact/install" \
  "$bridge_user@$bridge_ip:private-gateway-bridge-install/"
```

**Где:** Git Bash компьютера. Назначение на мосту: `$HOME/private-gateway-bridge-install/` SSH-пользователя.

**Проверка:**

```bash
ssh -i "$bridge_key" -o IdentitiesOnly=yes -p "$bridge_ssh_port" "$bridge_user@$bridge_ip" \
  'test -s "$HOME/private-gateway-bridge-install/bridge.json" && test -s "$HOME/private-gateway-bridge-install/release.tar.gz" && test -s "$HOME/private-gateway-bridge-install/install/install-bridge.sh" && printf "BRIDGE_UPLOAD_OK\n"'
```

**Ожидаемый результат:** `BRIDGE_UPLOAD_OK`. Во время `scp` файлы достигают `100%`; запрос SSH-пароля/пароля ключа при необходимости вводится только в вашем терминале.

## 12. Проверить выпуск и конфигурацию до запуска

**Действие:** в Git Bash подключитесь:

```bash
ssh -i "$bridge_key" -o IdentitiesOnly=yes -p "$bridge_ssh_port" "$bridge_user@$bridge_ip"
```

Затем в SSH:

```bash
cd "$HOME/private-gateway-bridge-install"
sha256sum --check release.tar.gz.sha256
python3 install/verify-release.py --tar release.tar.gz
sudo bash install/install-bridge.sh --archive release.tar.gz --config bridge.json --check
```

**Где:** вход — компьютер; проверка файлов — SSH нового VPS-моста, каталог `$HOME/private-gateway-bridge-install`.

**Проверка:** сразу после установщика:

```bash
printf 'BRIDGE_CHECK_EXIT=%s\n' "$?"
```

**Ожидаемый результат:** `release.tar.gz: OK`; отдельной строкой — SHA из 40 символов, совпадающий с выбранным коммитом Actions. Для каждого назначения — `TCP path to exit OK: <имя>; this does not verify VLESS authentication.`. В конце:

```text
Bridge configuration and outbound TCP paths checked; no bridge was started.
BRIDGE_CHECK_EXIT=0
```

`--check` проверяет выходы, свободные порты и конфигурацию настоящим HAProxy; скачивает закрепленный образ, постоянные слушатели не запускает. При ошибке дальше не переходите.

## 13. Установить мост

**Действие:**

```bash
cd "$HOME/private-gateway-bridge-install"
sudo bash install/install-bridge.sh --archive release.tar.gz --config bridge.json
```

**Где:** SSH нового VPS-моста. Создаются `/etc/private-gateway-bridge/compose.json`, `installed.json` и `configs/`. Установщик отклоняет машину с панелью/выходным агентом, выход на сам мост и пересечения портов. SSH и firewall он не меняет.

**Проверка:**

```bash
printf 'BRIDGE_INSTALL_EXIT=%s\n' "$?"
sudo docker compose -p private-gateway-bridge \
  -f /etc/private-gateway-bridge/compose.json ps --format '{{.Service}} {{.State}} {{.Health}}'
```

**Ожидаемый результат:**

```text
Bridge listeners are running. Import a normal profile and verify the exit IP on your device.
BRIDGE_INSTALL_EXIT=0
```

В списке — `tcp running healthy` и `xhttp running healthy`. Состояние процессов не доказывает авторизацию VLESS. Мост пересылает исходный зашифрованный поток без завершения TLS/REALITY и без второго слоя шифрования. Сайты видят IP выхода; адрес американского VPS виден сети моста.

## 14. Проверить TCP-порты моста с компьютера

**Действие:** выключите на компьютере все VPN и их «Блок при обрыве / Kill Switch». Откройте второе окно Git Bash, SSH моста оставьте открытым:

```bash
bridge_ip='IP_НОВОГО_VPS'
for port in 443 8444; do
  printf 'BRIDGE_PORT=%s\n' "$port"
  curl.exe -q -4 --proxy '' --noproxy '*' --connect-timeout 5 --max-time 7 \
    --verbose --output /dev/null --write-out '\ntcp_connect=%{time_connect}\n' \
    "telnet://$bridge_ip:$port" < /dev/null
done
```

**Где:** Windows Git Bash компьютера. На macOS/Linux замените `curl.exe` на `curl`.

**Проверка:** сверьте результат отдельно под `BRIDGE_PORT=443` и `BRIDGE_PORT=8444`.

**Ожидаемый результат:** для обоих портов — `Connected` либо `Established connection` и `tcp_connect=<время больше 0>`. Последующий `curl: (28) Time-out` допустим: TELNET-проба не выполняет VLESS-обмен. `tcp_connect=0.000000` без строки соединения означает неуспешную проверку.

## 15. Проверить HTTPS через мост

**Действие:**

```bash
curl.exe -q -4 --proxy '' --noproxy '*' \
  --connect-to "rb-gateway-3708079.duckdns.org:8443:$bridge_ip:443" \
  --connect-timeout 5 --max-time 10 --fail --silent --show-error \
  --output /dev/null --write-out 'HTTP=%{http_code} TCP=%{time_connect} TLS=%{time_appconnect}\n' \
  https://rb-gateway-3708079.duckdns.org:8443/readyz
```

**Где:** то же окно Git Bash компьютера; VPN выключены. `--connect-to` меняет адрес соединения только этой команды, сохраняя имя TLS и проверку сертификата.

**Проверка:**

```bash
printf 'BRIDGE_HTTPS_EXIT=%s\n' "$?"
```

**Ожидаемый результат:** `HTTP=200 TCP=<время больше 0> TLS=<время больше 0>`, затем `BRIDGE_HTTPS_EXIT=0`. Браузер и подписка от этой команды автоматически через мост не переключаются.

## 16. Проверить обычные профили TCP/Vision и XHTTP

**Действие:** откройте **«Устройства → устройство американского выходного VPS → Профиль»**. В **«Формат профиля»** выберите **«VLESS · TCP + REALITY + Vision (URI и QR)»**, нажмите **«Скопировать ссылку»**. Импортируйте ссылку в Private Gateway Client или другой совместимый клиент. Включите системный VPN/TUN, чтобы следующая команда тоже шла через VPN. Старый специальный JSON-профиль SOCKS5-цепочки удалите из клиента; обычная ссылка его настройки автоматически не снимает.

После проверки TCP/Vision отключите его. В том же диалоге выберите **«VLESS · XHTTP + REALITY (URI и QR)»**, скопируйте и импортируйте отдельную ссылку, затем подключите ее в системном VPN/TUN-режиме. Для этого нужен клиент с поддержкой XHTTP + REALITY. Выполните ту же проверку отдельно для XHTTP.

**Где:** браузер с панелью → указанный диалог; затем VPN-клиент компьютера. Ссылку с ключом устройства в чат не отправляйте.

**Проверка:** после подключения, Git Bash компьютера:

```bash
curl.exe -q -4 --proxy '' --noproxy '*' --connect-timeout 5 --max-time 10 \
  --fail --silent --show-error --write-out '\nHTTP=%{http_code}\n' https://api.ipify.org/
```

**Ожидаемый результат:**

```text
162.35.172.6
HTTP=200
```

Откройте несколько HTTPS-сайтов. Виден IP американского выхода, а не моста. Одного успешного ответа TCP или `/readyz` для подтверждения работы VLESS недостаточно.

Тот же результат нужен для XHTTP. Это две обычные ссылки с разными входными портами. Автоматическое переключение не добавляется; Hysteria2 и XHTTP H3/UDP этот мост не передает. TCP/Vision и XHTTP работают в отдельных процессах, но сеть и ресурсы VPS у них общие.

## 17. Проверить телефон и нужные сети

**Действие:** импортируйте обе обычные ссылки на телефон и подключите их по очереди. Повторите на компьютере и телефоне через нужные МТС, Теле2 и домашний Wi-Fi.

**Где:** VPN-клиенты устройств; браузер каждого устройства → `https://api.ipify.org/`.

**Проверка:** на Windows повторите команду шага 16 для каждого транспорта. На телефоне откройте `https://api.ipify.org/` и несколько HTTPS-сайтов.

**Ожидаемый результат:** IP `162.35.172.6` и работающие сайты на каждом проверенном сочетании сети, устройства и транспорта. Измерьте скорость и задержку полной цепочки; российское размещение и тариф VPS не гарантируют доступность и прежнюю скорость.

## 18. Собрать диагностику при ошибке

**Действие:**

```bash
sudo docker compose -p private-gateway-bridge -f /etc/private-gateway-bridge/compose.json ps
sudo docker compose -p private-gateway-bridge -f /etc/private-gateway-bridge/compose.json logs --tail 80
sudo ss -ltnp
```

**Где:** SSH VPS-моста. Состояние: `/etc/private-gateway-bridge/installed.json`; предыдущее после успешного изменения: `/etc/private-gateway-bridge/previous.json`.

**Проверка:** сверьте состояния и порты, повторите сетевые проверки шагов 3, 14 и 16.

**Ожидаемый результат:** в рабочем состоянии — оба сервиса `running/healthy`, слушатели 443/8444, реальный профиль дает IP выхода. При ошибке отправьте результаты без VLESS-ссылок, паролей и закрытых ключей. Не удаляйте `/etc/private-gateway-bridge` и не включайте полный журнал пользовательских соединений.

## 19. Необязательно: восстановить прежнюю конфигурацию

**Действие:** после успешного изменения убедитесь, что прежнее состояние существует:

```bash
sudo test -s /etc/private-gateway-bridge/previous.json && printf 'PREVIOUS_CONFIG_EXISTS\n'
```

Только при `PREVIOUS_CONFIG_EXISTS` выполните:

```bash
sudo python3 - <<'PY' > "$HOME/bridge-previous.json"
import json
from pathlib import Path
data = json.loads(Path('/etc/private-gateway-bridge/previous.json').read_text())
print(json.dumps(data['config'], indent=2))
PY
sudo bash "$HOME/private-gateway-bridge-install/install/install-bridge.sh" \
  --archive "$HOME/private-gateway-bridge-install/release.tar.gz" \
  --config "$HOME/bridge-previous.json" --check
```

После успешной проверки запустите:

```bash
sudo bash "$HOME/private-gateway-bridge-install/install/install-bridge.sh" \
  --archive "$HOME/private-gateway-bridge-install/release.tar.gz" \
  --config "$HOME/bridge-previous.json"
```

**Где:** SSH VPS-моста; файл восстановления — `$HOME/bridge-previous.json`. При первой установке прежней конфигурации нет. При ошибке запуска установщик сам пробует откат; сообщение `Automatic bridge rollback failed. Inspect the retained files and containers.` требует разбора сохраненного состояния.

**Проверка:** повторите список контейнеров из шага 13 и подключение с устройства. Если настройки сайта уже изменены, верните там прежние адрес/порты, обновите подписку либо URI.

**Ожидаемый результат:** `tcp running healthy` и `xhttp running healthy` для конфигурации с двумя транспортами; реальное подключение дает IP нужного выходного VPS.
