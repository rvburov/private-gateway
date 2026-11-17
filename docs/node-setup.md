# Добавление нового выходного VPS с нуля

Эта инструкция добавляет **полноценный выходной VPN-сервер** к уже работающей
панели Private Gateway. Он получает собственные устройства и VPN-ссылки; сайты
видят его IP. На новом сервере устанавливаются Xray и агент, центральный сайт и
база остаются на основном VPS. Для входного VPS, который только передает
соединения на существующий выход, используйте [инструкцию моста](bridge.md).

Команды ниже рассчитаны на **чистый Ubuntu 26.04 amd64**, пользователя с `sudo`
и два транспорта: TCP/Vision на TCP 8443 и XHTTP/REALITY на TCP 8444.
H3/UDP в этом порядке не включается. Не выполняйте установку на действующем
американском сервере панели или на машине, уже работающей мостом.

## 1. Войти на новый VPS

**На вашем компьютере, Windows — Git Bash; macOS/Linux — терминал bash:**

```bash
node_ip='IP_НОВОГО_VPS'
node_user='SSH_ПОЛЬЗОВАТЕЛЬ'
node_ssh_port='22'
ssh -p "$node_ssh_port" "$node_user@$node_ip"
```

Подставьте данные хостинга. Сверьте отпечаток SSH-хоста с его консолью.
Для первого входа и загрузки выпуска можно использовать работающее VPN-соединение.
Если нужен прямой вход из МТС, Теле2 или домашней сети, отдельно проверьте его
с выключенными VPN и их блоком при обрыве. Ключ SSH не устраняет сетевой тайм-аут.

**Далее в открытом SSH на новом VPS:**

```bash
cat /etc/os-release
uname -m
```

Нужны Ubuntu 26.04 и архитектура `x86_64`. Доступ к панели и свободные
TCP 8443/8444 проверяются ниже после установки сетевых утилит.

## 2. Установить Docker и подготовить порты

**В SSH нового чистого VPS:**

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
sudo docker version
sudo docker compose version
sudo ss -ltnp
curl -q -4 --proxy '' --noproxy '*' --connect-timeout 5 --max-time 10 \
  --fail --silent --show-error --output /dev/null --write-out 'PANEL_HTTP=%{http_code}\n' \
  https://rb-gateway-3708079.duckdns.org:8443/readyz
```

Нужны свободные TCP 8443/8444 и `PANEL_HTTP=200` с действующим TLS-сертификатом.
Агент должен иметь доступ к этой панели с нового VPS. Если панель недоступна,
регистрацию пока не запускайте.

Источник: [официальная установка Docker для Ubuntu](https://docs.docker.com/engine/install/ubuntu/).
Если на VPS уже установлен Docker или работают другие службы, сначала проверьте
их состояние. Команды рассчитаны на новую машину и не предлагают переустановку
действующего стека.

В firewall хостинга разрешите **входящие TCP 8443 и 8444**, сохранив SSH-порт.
Исходящие HTTPS к панели, доступ к REALITY target и сайтам должны работать.
Этот узел публикует порты через Docker: правила UFW сами по себе не являются
ограничением опубликованных портов. Для ограничения источников используйте
сетевой firewall хостинга либо согласованные правила Docker. Не очищайте
iptables/nftables и не закрывайте SSH. Установка не требует публичного порта БД,
API агента или отдельного сайта на этом VPS.

## 3. Скачать проверенный выпуск и передать его на VPS

**В браузере на вашем компьютере** откройте
[Private Gateway → Actions → CI/CD](https://github.com/rvburov/private-gateway/actions/workflows/ci.yml).
Выберите успешный запуск нужного коммита, в **Artifacts** скачайте
`private-gateway-linux-amd64` и распакуйте ZIP. В одной папке должны лежать
`release.tar.gz`, `release.tar.gz.sha256` и каталог `install/` с
`install-node.sh` и `verify-release.py`.

**В новом локальном Git Bash на Windows** (не внутри SSH), измените путь к
распакованной папке и данные VPS:

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
ssh -p "$node_ssh_port" "$node_user@$node_ip"
```

**В SSH нового VPS:**

```bash
cd "$HOME/private-gateway-node-install"
sha256sum --check release.tar.gz.sha256
python3 install/verify-release.py --tar release.tar.gz
```

Ожидаются `release.tar.gz: OK` и SHA коммита выбранного успешного выпуска.
Все файлы берите из одного артефакта. При ошибке контрольной суммы остановитесь.

## 4. Создать отдельные ключи и параметры двух транспортов

**В том же SSH нового VPS:**

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
PY
```

Вывод содержит только открытые параметры для сайта. В закрепленном Xray
открытый ключ подписан `Password (PublicKey)`; это значение для поля открытого
ключа REALITY, а не пароль панели. `Hash32` для этого поля не подходит.
Закрытые ключи остались в файлах `tcp.keys` и `xhttp.keys` с закрытыми правами;
не печатайте их и не отправляйте в чат. Этот шаг выполняется **один раз**:
при повторе файлы не перезаписываются. При разрыве SSH продолжайте с готовыми
файлами, не создавайте новые ключи для уже добавленного узла.

## 5. Заполнить форму на сайте

**В браузере** откройте `https://rb-gateway-3708079.duckdns.org:8443/`, войдите
с 2FA, перейдите **Серверы → Добавить VPS**. Для входа используйте пока рабочее
подключение. Заполните:

| Поле сайта | Что указать |
| --- | --- |
| Название | Любое понятное имя, например «Второй выходной VPS» |
| Адрес VPS | Публичный IP нового выходного VPS, без `https://` и без порта |
| Порт TCP/Vision | `8443` |
| Server name REALITY | `www.cloudflare.com` для этого примера; такой же target задается ниже |
| Открытый ключ REALITY | Значение `TCP_PUBLIC_KEY` из шага 4 |
| Short ID | Значение `TCP_SHORT_ID` |

Откройте **Дополнительные транспорты VPS**, включите
**XHTTP + REALITY (HTTP/2)** и заполните:

| Поле сайта | Что указать |
| --- | --- |
| Порт XHTTP | `8444` |
| Открытый ключ XHTTP | Значение `XHTTP_PUBLIC_KEY` |
| Short ID XHTTP | Значение `XHTTP_SHORT_ID` |
| Путь XHTTP | Значение `XHTTP_PATH`, начиная с `/` |

**XHTTP + TLS (HTTP/3, UDP)** оставьте выключенным для этого порядка установки.
Нажмите **Сохранить VPS**. В окне **Подключить агент VPS** сохраните локально
**Идентификатор VPS** и **Код подключения**. Код действует 15 минут и вводится
только в скрытый запрос установщика. Не отправляйте его в чат и не вставляйте
в командную строку. Если он истечет, получите новый через **Переподключить агент**.

## 6. Подготовить закрытый файл настроек на новом VPS

**Вернитесь в SSH нового VPS**. Команда перенесет закрытые ключи из локальных
файлов в файл `root:root 0600`, не выводя их. Она не перезаписывает существующий
файл:

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

В редакторе замените **только** `ID_ИЗ_ПАНЕЛИ` на идентификатор из шага 5,
`IP_НОВОГО_VPS` на адрес из формы. Сохраните `Ctrl+O`, Enter, выйдите `Ctrl+X`.
Если выбрали другой Server name, одинаково измените `GATEWAY_SERVER_NAME`
и домен в `GATEWAY_REALITY_TARGET`, сохранив `:443`. Его доступность и
пригодность TLS проверяются отдельно; название крупного сайта не гарантирует
работу из любой сети. Адрес панели — точный HTTPS origin **без `/` на конце**.

## 7. Установить агент, применить конфигурацию и проверить процессы

**В SSH нового VPS:**

```bash
cd "$HOME/private-gateway-node-install"
sudo bash install/install-node.sh --archive release.tar.gz --env-file /root/private-gateway-node.env
```

На запрос **Одноразовый токен узла:** вставьте код из окна сайта и нажмите Enter;
ввод скрыт. Дождитесь завершения команды. Первое сообщение о запущенном агенте
еще не подтверждает готовность VPN.

**На сайте → Серверы → карточка нового VPS → Применить.** Дождитесь связи
агента. Затем **в SSH того же VPS**:

```bash
sudo bash install/install-node.sh --resume --archive release.tar.gz
sudo docker ps --filter label=com.docker.compose.project=private-gateway-node \
  --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
sudo ss -ltnp
```

Нужны работающие `node-agent`, `xray` и `xray-xhttp`. В панели
**Примененная / нужная версия** должны совпадать, сообщение об ошибке агента
должно отсутствовать. При еще не примененной конфигурации дождитесь агента и
повторите `--resume`; заново добавлять VPS не нужно.

Если код истек **до регистрации**, получите новый через **Переподключить агент**
и повторите `--resume`: при отсутствии сохраненного токена будет скрытый запрос.
После успешной регистрации повтор не просит код. Не создавайте новый код для
уже работающего агента без необходимости: это отдельная операция его переподключения.

## 8. Создать устройство и проверить настоящую VPN-ссылку

**На сайте:** в карточке нового VPS оставьте способ подключения
**Напрямую к VPS**. Откройте **Устройства → Добавить устройство**, введите
**Название устройства**, в поле **Сервер подключения** выберите новый выходной
VPS и нажмите **Добавить устройство**.
Затем снова **Серверы → карточка этого VPS → Применить**, дождитесь совпадения
примененной и нужной версий. В **Устройствах** откройте **Профиль**, выберите
TCP/Vision и скопируйте URI или QR. Импортируйте в совместимый VPN-клиент.
Отдельно повторите с XHTTP.

Для первичной проверки портов **на Windows в Git Bash**, отключив все VPN и их
блок при обрыве:

```bash
node_ip='IP_НОВОГО_VPS'
for port in 8443 8444; do
  curl.exe -q -4 --proxy '' --noproxy '*' --connect-timeout 5 --max-time 7 \
    --verbose --output /dev/null --write-out '\ntcp_connect=%{time_connect}\n' \
    "telnet://$node_ip:$port" < /dev/null
done
```

На macOS/Linux замените `curl.exe` на `curl`. Строка `Connected` или
`Established connection` и ненулевое `tcp_connect` подтверждают TCP-соединение.
Последующий общий тайм-аут TELNET не доказывает отказ VPN: эта проба не говорит
на VLESS/REALITY. Удаленный узел не размещает панель, поэтому `/readyz` на его
VPN-порту не является проверкой этого узла.

Затем включите **только проверяемый** VPN-профиль, откройте несколько HTTPS-сайтов
и проверьте внешний IP. Ожидается IP нового выходного VPS. Повторите TCP/Vision
и XHTTP отдельно на ПК и телефоне и в нужных сетях. Здоровые контейнеры и
совпавшая ревизия не заменяют эту проверку.

Чтобы подключаться к этому выходу через мост, используйте [следующую инструкцию](bridge.md):
на выходном VPS остается эта установка, мост готовится на другой машине.
Обновления выходного узла не происходят просто от сохранения карточки на сайте:
`--resume` проверяет уже установленный выпуск. Процедуру смены выпуска нужно
планировать отдельно с сохранением ключей и состояния агента.
