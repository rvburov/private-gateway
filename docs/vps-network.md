# Сеть VPS: восстановление и профилактика

Для VPS-моста, управляемого выходного VPN-сервера и центральной панели нужны
одинаковые проверки сети хоста. Установка Docker или здоровый контейнер
не подтверждают наличие внешнего IPv4 и маршрута. Инцидент и подтверждённые
результаты от 2026-11-19 записаны в [истории ошибок](troubleshooting.md#потеря-ipv4-на-vps-мосте-после-зависания-системы--2026-11-19).

## 1. До установки сохранить параметры сети

**Где:** консоль хостинга noVNC либо SSH нового VPS, не терминал компьютера.
Сначала убедитесь, что можете войти в noVNC независимо от SSH и VPN.

**Действие и проверка:**

```bash
date -u '+UTC=%Y-%m-%dT%H:%M:%SZ'
uptime
uname -r
ip -brief link
ip -4 -brief address
ip -6 -brief address
ip -4 route
ip -6 route
sudo netplan get
```

**Ожидаемый результат:** внешний интерфейс имеет выданный адрес, есть нужный
маршрут по умолчанию. Сверьте в кабинете провайдера IP, префикс, шлюз,
MAC, IPv6, DNS и поддержку ручной настройки IPv4. Наличие публичного адреса
в кабинете не означает, что он назначен интерфейсу внутри VPS: возможны NAT,
адрес `/32` и шлюз вне подсети. Эти схемы требуют своих настроек.

**Резервная копия — на том же VPS с Netplan:**

```bash
sudo bash -eu <<'SH'
pg_backup=$(mktemp -d /root/private-gateway-network.XXXXXX)
chmod 700 "$pg_backup"
cp -a /etc/netplan "$pg_backup/netplan"
for pg_path in /run/netplan /lib/netplan; do
  [ ! -d "$pg_path" ] || cp -a "$pg_path" "$pg_backup/$(basename "$(dirname "$pg_path")")-netplan"
done
netplan get > "$pg_backup/netplan-merged.yaml"
ip -4 address > "$pg_backup/ipv4.txt"
ip -6 address > "$pg_backup/ipv6.txt"
ip -4 route > "$pg_backup/routes4.txt"
ip -6 route > "$pg_backup/routes6.txt"
printf 'NETWORK_BACKUP=%s\n' "$pg_backup"
SH
```

**Успех:** `NETWORK_BACKUP=/root/private-gateway-network.…`. Сохраните этот
точный путь. Если `netplan` отсутствует, сначала определите используемый
сетевой менеджер; следующие Netplan-команды к нему не относятся.

DHCP сам по себе не ошибка. Переводить каждый новый VPS на статику не нужно.
Ручной IPv4 применяют только с разрешёнными провайдером параметрами, сохраняя
IPv6, DNS, MTU и другие действующие маршруты. Установщики проекта эти параметры
не определяют и автоматически сеть хоста не меняют.

## 2. При потере SSH сначала проверить сеть хоста

**Где:** noVNC проблемного VPS.

```bash
ip -4 -brief address
ip -4 route
systemctl is-active systemd-networkd docker
systemctl show -p Id -p ActiveState -p UnitFileState ssh.service ssh.socket
sudo ss -ltnp 'sport = :22'
sudo journalctl --utc -u systemd-networkd -n 60 --no-pager
sudo docker ps --format '{{.Names}} {{.Status}}'
```

**Ожидаемый результат:** адрес и маршрут присутствуют, используемые службы
активны, SSH слушает нужный порт, контейнеры запущены. SSH может запускаться
через `ssh.service` или `ssh.socket` — подробнее в шаге 6. Если SSH использует
другой порт, подставьте его. Статус `active` у SSH и `healthy` у контейнеров при пустом
IPv4-интерфейсе не подтверждают внешнюю связь. Тайм-аут SSH до получения
баннера не проверяет SSH-ключ и не доказывает блокировку провайдером.

Если адрес пропал, восстановление требует выданных этому VPS параметров.
Не меняйте профили REALITY, SNI и порты приложения вместо проверки сети.
Повторные `networkctl renew` и `reconfigure` в инциденте 2026-11-19 адрес
не восстановили; их повторение не заменяет диагностику DHCP.

## 3. Восстановление IPv4 нашего моста

**Только для VPS `87.199.131.176`, Ubuntu 22.04, интерфейс `enp3s0`,
MAC `fa:16:3e:a7:b8:47`. На новом VPS эти значения не использовать.**
На работающем мосте после подтверждённой перезагрузки повторять восстановление
не нужно. Этот раздел — инструкция на случай повторной потери адреса.

### 3.1. Временно вернуть адрес и маршрут

**Где:** noVNC моста. Сначала подтвердите, что IPv4 и маршрут отсутствуют,
а IP в кабинете хостинга остался прежним.

```bash
sudo bash -eu <<'SH'
test "$(cat /sys/class/net/enp3s0/address)" = 'fa:16:3e:a7:b8:47'
ip address add 87.199.131.176/25 dev enp3s0
ip route add default via 87.199.131.129 dev enp3s0
ip -4 route get 162.35.172.6
SH
```

**Успех:** `162.35.172.6 via 87.199.131.129 dev enp3s0 src 87.199.131.176 …`.
`File exists` означает, что объект уже есть; сначала посмотрите состояние,
не удаляйте действующий адрес или маршрут. Команды `ip` не сохраняют настройку
после перезагрузки.

### 3.2. Сохранить постоянный IPv4

**Где:** noVNC моста. Резервная копия из шага 1 должна уже существовать.

Файлы провайдера `/etc/netplan/50-cloud-init.yaml` и
`/etc/netplan/99-dhcp-all-interfaces.yaml` сохраняются. В данном случае
`enp3s0` — также идентификатор устройства в Netplan. На другом VPS он может
отличаться от имени интерфейса.

**Действие:** создать **один** дополнительный файл
`/etc/netplan/99-private-gateway-static-ipv4.yaml`:

```bash
sudo bash -eu <<'SH'
test "$(cat /sys/class/net/enp3s0/address)" = 'fa:16:3e:a7:b8:47'
pg_file=/etc/netplan/99-private-gateway-static-ipv4.yaml
if [ -e "$pg_file" ]; then
  printf 'STOP: файл уже существует; сначала проверьте его содержимое\n'
  exit 1
fi
umask 077
cat > "$pg_file" <<'YAML'
network:
  version: 2
  ethernets:
    enp3s0:
      dhcp4: false
      addresses:
        - 87.199.131.176/25
      routes:
        - to: 0.0.0.0/0
          via: 87.199.131.129
YAML
if ! netplan generate; then
  rm -- "$pg_file"
  netplan generate
  exit 1
fi
printf 'NETPLAN_GENERATE_OK\n'
SH
```

**Успех:** `NETPLAN_GENERATE_OK`. Это проверка генерации конфигурации,
а не применение к работающей сети. Проверьте объединённый результат:

```bash
sudo netplan get ethernets.enp3s0
sudo ls -l /etc/netplan/99-private-gateway-static-ipv4*.yaml
```

**Ожидается:** `dhcp4: false`, IPv4 `87.199.131.176/25`, прежний IPv6
`2a00:5da0:2005:1::d3d/64`, прежние DNS, IPv6-маршрут через
`2a00:5da0:2005:1::1` и новый IPv4-маршрут. В списке — ровно один файл
дополнительной настройки. Не создавайте копии с суффиксами `.XXXX.yaml`:
Netplan читает все такие YAML. В инциденте одинаковые лишние копии были
сверены и перенесены в `/root/netplan-cleanup.0c8E10/`, а не удалены вслепую.

Netplan объединяет файлы по имени: списки адресов и маршрутов дополняются,
скаляр `dhcp4` заменяется для **того же идентификатора**. Поэтому проверяют
весь объединённый документ, а не только новый файл. Правила описаны в
[Netplan generate](https://netplan.readthedocs.io/en/0.106/netplan-generate/).

**Применение — в noVNC моста:**

```bash
sudo netplan try --timeout 120
```

Пока команда ждёт подтверждения, откройте **второй SSH-сеанс с компьютера**
и выполните там:

```bash
ip -4 -brief address show dev enp3s0
ip -6 -brief address show dev enp3s0
ip -4 route get 162.35.172.6
```

**Успех:** IPv4 и IPv6 прежние, маршрут содержит правильный шлюз и исходящий
адрес. Тогда вернитесь в noVNC и подтвердите `netplan try` клавишей Enter.
Без успешной проверки подтверждать не надо. После отмены/тайм-аута проверьте
реальную сеть и конфигурацию на диске: автоматический откат имеет
[известные ограничения](https://netplan.readthedocs.io/en/0.106/netplan-try/).

**Если новый файл оказался неверным:** в noVNC перенесите только созданный
на этом шаге файл за пределы `/etc/netplan` в сохранённую резервную папку,
затем повторите `sudo netplan generate`. Например, сначала укажите настоящий
путь из `NETWORK_BACKUP`, затем:

```bash
pg_backup='/root/private-gateway-network.ПУТЬ_ИЗ_ВЫВОДА'
sudo test -d "$pg_backup/netplan" &&
sudo mv -- /etc/netplan/99-private-gateway-static-ipv4.yaml \
  "$pg_backup/rejected-static-ipv4.yaml" &&
sudo netplan generate
```

**Успех:** генерация без ошибки и отсутствие отклонённого файла в
`/etc/netplan`. Возврат файлов не восстанавливает пропавшую DHCP-аренду;
проверяйте фактическую сеть через noVNC. Не перезагружайте VPS с непроверенным
содержимым `/etc/netplan`.

## 4. На новом VPS выбрать подходящую настройку

**Где:** кабинет хостинга и консоль нового VPS; до изменения сети.

1. Сверьте адрес, префикс, шлюз и разрешение статической настройки с данными
   провайдера. Для DHCP, NAT, `/32`, шлюза вне подсети, нескольких интерфейсов
   или другого сетевого менеджера используйте соответствующую схему провайдера.
2. Выполните шаг 1. Найдите **идентификатор Netplan**, фактический интерфейс
   и действующие настройки IPv6/DNS. Не копируйте адреса из шага 3.
3. Если допустима статика, подготовьте дополнительную настройку того же
   устройства с параметрами **нового VPS**. Сохраните IPv6, DNS и нужные
   маршруты; проверьте итог `netplan get` и `netplan generate`.
4. Примените через `netplan try` при открытом noVNC и проверьте второй SSH-сеанс.
5. Если DHCP обязателен или работает штатно, оставьте его. Сбор метрик,
   проверка обновлений и контроль после перезагрузки нужны при обоих способах.

**Ожидаемый результат:** новый VPS доступен по SSH, сохраняет назначенные
ему адреса и достигает своего назначения: мост — выходного VPS, выход —
сайта управления и интернета. Статика устраняет зависимость адреса от DHCP,
но не предотвращает зависание ядра или остановку виртуальной машины.

## 5. Включить историю CPU, памяти и диска

**Где:** сам VPS, SSH или noVNC. Проверено на нашем мосте Ubuntu 22.04 с
`sysstat 12.5.2`. Для другой поддерживаемой Ubuntu сначала проверьте наличие
пакета и перечисленных systemd-служб. Это отдельная настройка эксплуатации,
не автоматический этап `setup-vps.sh`.

**Установка без общего обновления пакетов:**

```bash
sudo env NEEDRESTART_MODE=l DEBIAN_FRONTEND=noninteractive \
  apt-get install -y --no-install-recommends --no-upgrade --no-remove sysstat
systemctl cat sysstat.service sysstat-collect.timer sysstat-summary.timer
```

**Успех:** `sysstat` установлен, показаны определения трёх служб/таймеров.
Если пакет недоступен или службы другие, сначала выясните отличие ОС.
`NEEDRESTART_MODE=l` оставляет `needrestart` в режиме перечисления служб;
не соглашайтесь на произвольный перезапуск сетевых служб ради этой настройки.

**Сохранить настройки и задать сбор раз в минуту:**

```bash
sudo /usr/bin/python3 - <<'PY'
from pathlib import Path
import re
import shutil
import tempfile

backup = Path(tempfile.mkdtemp(prefix="private-gateway-sysstat-", dir="/root"))
settings = {
    Path("/etc/default/sysstat"): {"ENABLED": '"true"'},
    Path("/etc/sysstat/sysstat"): {
        "HISTORY": "14", "COMPRESSAFTER": "7", "SADC_OPTIONS": '"-S DISK"',
    },
}
dropin = Path("/etc/systemd/system/sysstat-collect.timer.d/private-gateway.conf")
for path in (*settings, dropin):
    if path.exists():
        shutil.copy2(path, backup / str(path).lstrip("/").replace("/", "_"))
for path, values in settings.items():
    content = path.read_text()
    for key, value in values.items():
        pattern = rf"^{re.escape(key)}=.*$"
        replacement = f"{key}={value}"
        if re.search(pattern, content, re.M):
            content = re.sub(pattern, lambda match: replacement, content, flags=re.M)
        else:
            content = content.rstrip() + "\n" + replacement + "\n"
    path.write_text(content)
dropin.parent.mkdir(parents=True, exist_ok=True)
dropin.write_text("[Timer]\nOnCalendar=\nOnCalendar=*-*-* *:*:00\nAccuracySec=1s\n")
print(f"SYSSTAT_BACKUP={backup}")
PY
```

**Успех:** `SYSSTAT_BACKUP=/root/private-gateway-sysstat-…`. Сохраняются
остальные строки настроек. Пустой `OnCalendar=` сбрасывает прежнее расписание.
Затем на том же VPS:

```bash
sudo systemctl daemon-reload &&
sudo systemctl enable --now sysstat.service sysstat-collect.timer sysstat-summary.timer &&
sudo systemctl restart sysstat-collect.timer &&
sudo systemctl start sysstat-collect.service
systemctl is-active sysstat.service sysstat-collect.timer sysstat-summary.timer
systemctl is-enabled sysstat.service sysstat-collect.timer sysstat-summary.timer
systemctl list-timers --no-pager 'sysstat-*'
```

**Успех:** три строки `active`, три `enabled`; следующий сбор не дальше
примерно минуты. Данные — `/var/log/sysstat/saDD`, отчёты — `sarDD`.
`HISTORY=14` сохраняет историю примерно за две недели, `COMPRESSAFTER=7`
сжимает старые файлы. `SADC_OPTIONS` меняет набор данных только для новых
суточных файлов: при отсутствии дисковых данных в старом файле не удаляйте
историю, проверьте следующий новый файл.
[Описание параметров sysstat](https://manpages.ubuntu.com/manpages/jammy/man5/sysstat.5.html).

**Проверка после двух сборов — на VPS:**

```bash
sudo systemctl start sysstat-collect.service
sleep 2
sudo systemctl start sysstat-collect.service
LC_ALL=C TZ=UTC sar -u ALL
LC_ALL=C TZ=UTC sar -r ALL
LC_ALL=C TZ=UTC sar -d
LC_ALL=C TZ=UTC sar -q
```

**Успех:** появились строки измерений CPU (включая `%steal` и `%iowait`),
памяти и очереди задач, а для нового файла с `-S DISK` — также диска.
Если старый суточный файл создан без дисковой активности, отсутствие
дисковых строк до создания нового файла не означает отказ таймера.
Заголовок суточного файла может содержать
ядро, работавшее при создании файла; текущее ядро проверяют `uname -r`.
Краткий нормальный замер не доказывает отсутствия прошлого зависания.

## 6. Проверить автозапуск и принять сервер после перезагрузки

**Где:** VPS до плановой перезагрузки; noVNC должен быть доступен.

```bash
uname -r
ls -lh /boot/vmlinuz-* /boot/initrd.img-*
systemctl is-active docker
systemctl is-enabled docker
systemctl show -p Id -p ActiveState -p UnitFileState ssh.service ssh.socket
sudo docker ps --format '{{.Names}} {{.Status}}'
```

Сверьте установленное поддерживаемое ядро с выбранным загрузчиком и ожидаемой
версией; одного сообщения «System restart required» для этого недостаточно.
Не задавайте ядро `203` на другом VPS по номеру из нашего инцидента.

**Проверка политики — на VPS.** Для моста имена ниже; для другой роли сначала
возьмите реальные имена из `docker ps` и укажите контейнеры этой установки:

```bash
sudo docker inspect --format \
  '{{.Name}} restart={{.HostConfig.RestartPolicy.Name}} running={{.State.Running}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
  private-gateway-bridge-tcp-1 private-gateway-bridge-xhttp-1
```

**Успех для моста:** обе строки содержат `restart=unless-stopped` либо
`always`, `running=true`, `health=healthy`; Docker — `active` и `enabled`.
Для SSH подтверждены `active` и `enabled` у `ssh.service` **либо** у
`ssh.socket`, а также новое успешное внешнее SSH-подключение. На новых Ubuntu
SSH штатно может запускаться через сокет; выключенный автозапуск самой
`ssh.service` в этой схеме не является ошибкой. Существующий способ запуска
сохраняют. [Описание Canonical](https://discourse.ubuntu.com/t/sshd-now-uses-socket-based-activation-ubuntu-22-10-and-later/30189/1).

`on-failure` не обеспечивает запуск после перезапуска Docker. При
`unless-stopped` контейнер, остановленный вручную, сам не стартует после
перезагрузки. Один `unhealthy` также не запускает автоматический перезапуск
контейнера, пока его процесс работает. [Политики Docker](https://docs.docker.com/engine/containers/start-containers-automatically/).

**Действие — в noVNC, после успешных проверок:**

```bash
sudo systemctl reboot
```

**После нового входа на VPS:** повторите проверки адресов/маршрутов, служб,
контейнеров и таймеров. `uname -r` должен совпасть с ожидаемым новым ядром.
Не перезапускайте исправный VPS повторно ради старого заголовка `sar`.

**Последняя проверка — на компьютере/телефоне:** включите нужный профиль
в режиме VPN всей системы/TUN и проверьте сайты и IP по шагу 6 или 10
[мастера настройки](setup-vps.md). При успехе виден IP **выходного** VPS,
не моста и не домашней сети. Каждый транспорт проверяют отдельно.
Автозапуск сервера не включает VPN-клиент на устройстве и не переключает
TCP/Vision на XHTTP автоматически.

## 7. Если зависание повторится

**Где:** noVNC или доступный SSH проблемного VPS. Сохраните данные до
перезапуска, если это позволяет состояние машины:

```bash
pg_report="$HOME/pg-network-repeat-$(date -u +%Y%m%dT%H%M%SZ).txt"
(umask 077; {
  date -u '+UTC=%Y-%m-%dT%H:%M:%SZ'
  uptime
  uname -r
  ip -4 -brief address
  ip -4 route
  systemctl --failed --no-pager
  sudo journalctl --utc -k --since '-30 min' --no-pager
  sudo journalctl --utc --since '-30 min' --no-pager \
    -u systemd-networkd -u systemd-logind -u systemd-udevd \
    -u systemd-resolved -u systemd-timesyncd -u qemu-guest-agent
  LC_ALL=C TZ=UTC sar -u ALL
  LC_ALL=C TZ=UTC sar -r ALL
  LC_ALL=C TZ=UTC sar -d
  LC_ALL=C TZ=UTC sar -q
} > "$pg_report" 2>&1)
printf 'REPORT=%s\n' "$pg_report"
```

**Успех:** непустой TXT по показанному пути. Если инцидент был до последней
перезагрузки, отдельно запросите журнал предыдущей загрузки (`journalctl -b -1`),
если он сохранён. Время инцидента сверяйте в UTC; МСК = UTC+3. Сохраните
соответствующий суточный файл из `/var/log/sysstat` и новый crash/core локально;
сырой core в чат не отправляйте. Отсутствие записей не доказывает, что сбоя
не было: журнал мог не сохраниться.

Одновременные watchdog нескольких служб и kernel soft lockup требуют
проверки всей виртуальной машины. `%steal`, I/O и журналы помогают сузить
причину; сами по себе не устанавливают виновника. Не отключайте watchdog
и не удаляйте виртуальный CD-ROM только из-за его упоминания в стеке.

