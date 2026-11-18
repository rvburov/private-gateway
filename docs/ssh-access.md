## 1. Открыть первоначальную консоль нового VPS

**Действие:** в личном кабинете хостинга откройте карточку **нового VPS-моста**,
затем его консоль: она может называться «Консоль», «VNC» или «Serial console».
Точное название зависит от хостинга. Начальное имя пользователя и способ первого
входа возьмите из карточки VPS или письма хостинга. Это может быть `root`,
`ubuntu` или другой выданный администратор; имя пока не нужно угадывать.

Если консоль требует логин/пароль, используйте выданные хостингом данные.
Если вы вошли не под `root`, выполните:

```bash
sudo -i
```

**Где:** браузер → личный кабинет хостинга → консоль **нового VPS**,
не Git Bash компьютера и не американский выходной сервер.
Оставьте эту консоль открытой до проверки нового SSH-входа.

**Проверка:**

```bash
test "$(id -u)" -eq 0 && printf 'ROOT_OK\n'
. /etc/os-release
printf 'OS=%s VERSION=%s ARCH=%s\n' "$ID" "$VERSION_ID" "$(uname -m)"
```

**Ожидаемый результат:**

```text
ROOT_OK
OS=ubuntu VERSION=26.04 ARCH=x86_64
```

Если нет административного доступа или ОС/архитектура отличаются, следующие
команды пока не выполняйте: сообщите название хостинга и показанную ошибку без
паролей. Консоль хостинга сама по себе не гарантирует вход под `root`.

## 2. Установить программы для учетной записи и SSH

**Действие:**

```bash
apt-get update &&
apt-get install -y sudo adduser openssh-server nano iproute2

if [ ! -d /run/sshd ]; then
  install -d -o root -g root -m 755 /run/sshd
fi
```

**Где:** открытая консоль нового VPS, пользователь `root`.

**Проверка:**

```bash
(
  for bridge_tool in adduser usermod sudo visudo ssh-keygen nano ss; do
    command -v "$bridge_tool" || exit 1
  done
) &&
/usr/sbin/sshd -t &&
printf 'SSH_TOOLS_OK\n'
```

**Ожидаемый результат:** пути к семи программам и `SSH_TOOLS_OK`.
`sshd -t` при успехе ничего не печатает. Установка OpenSSH описана в
[официальной инструкции Ubuntu](https://ubuntu.com/server/docs/how-to/security/openssh-server/).

## 3. Создать пользователя `pgbridge`

**Действие:** создайте на новом VPS обычного пользователя с именем `pgbridge`:

```bash
if getent passwd pgbridge; then
  printf 'USER_ALREADY_EXISTS: сначала проверьте существующую учетную запись\n'
else
  adduser --home /home/pgbridge --shell /bin/bash pgbridge
fi
```

При `USER_ALREADY_EXISTS` новую учетную запись команда не создает: выясните,
ваша ли это ранее созданная учетная запись, прежде чем давать ей права `sudo`.
На чистом новом VPS пользователь создается командой `adduser`:

1. В `New password` задайте **новый пароль пользователя `pgbridge`**.
2. В `Retype new password` повторите его. При вводе символы не видны.
3. В полях имени, комнаты и телефонов нажимайте Enter, оставляя их пустыми.
4. На подтверждение корректности данных ответьте `Y`, затем Enter.

Этот пароль понадобится для `sudo`. Он отличается от парольной фразы SSH-ключа,
которую создадим на компьютере. Настройки парольного SSH-входа здесь не меняются.

**Где:** консоль нового VPS, пользователь `root`. Создаются учетная запись
`pgbridge` и домашняя папка `/home/pgbridge`.
Имя пользователя Windows и логин панели Private Gateway к ней не относятся.

**Проверка:**

```bash
getent passwd pgbridge | cut -d: -f1,6,7
passwd -S pgbridge
```

**Ожидаемый результат:**

```text
pgbridge:/home/pgbridge:/bin/bash
pgbridge P <дата и параметры пароля>
```

Во второй строке сразу после имени должен быть статус `P`, а не `L` или `NP`.
Даты и параметры динамические. Создание пользователя описано в
[руководстве Ubuntu](https://ubuntu.com/server/docs/how-to/security/user-management/).

## 4. Разрешить пользователю административные команды

**Действие:**

```bash
usermod -aG sudo pgbridge
```

**Где:** консоль нового VPS, пользователь `root`.

**Проверка:**

```bash
id -nG pgbridge
visudo -c && sudo -l -U pgbridge
```

**Ожидаемый результат:** среди групп есть `pgbridge` и `sudo`;
`visudo -c` сообщает `parsed OK`, а список разрешений содержит `(ALL : ALL) ALL`
или эквивалентное разрешение административных команд. Порядок групп и язык
сообщений могут различаться. Проверка реального `sudo` будет после нового входа
на шаге 11.

## 5. Записать данные моста на компьютере

**Действие:** откройте Git Bash на Windows или терминал bash на macOS/Linux.
Из карточки нового VPS скопируйте публичный IPv4 вместо `IP_НОВОГО_VPS`:

```bash
bridge_ip='IP_НОВОГО_VPS'
bridge_user='pgbridge'
bridge_ssh_port='22'
bridge_key="$HOME/.ssh/private_gateway_bridge_ed25519"
```

`bridge_user='pgbridge'` оставьте буквально: эту учетную запись создали на шаге 3.
Если у хостинга другой SSH-порт, укажите его вместо `22` и сверьте на шаге 9.

**Где:** **ваш компьютер**, новое локальное окно Git Bash/терминала.
Локальные шаги 5–7 и 10–11 выполняйте в этом окне. Консоль хостинга остается
отдельным окном в браузере.

**Проверка:**

```bash
command -v ssh ssh-keygen
printf 'VPS=%s USER=%s PORT=%s\n' "$bridge_ip" "$bridge_user" "$bridge_ssh_port"
printf 'PRIVATE_KEY_PATH=%s\n' "$bridge_key"
```

**Ожидаемый результат:** пути к `ssh` и `ssh-keygen`, реальный IP нового VPS,
`USER=pgbridge`, нужный SSH-порт и локальный путь к ключу.
`IP_НОВОГО_VPS` должен быть заменен вашим адресом.

## 6. Создать SSH-ключ на компьютере

**Действие:**

```bash
mkdir -p "$HOME/.ssh"
chmod 700 "$HOME/.ssh"
if [ -e "$bridge_key" ] || [ -e "$bridge_key.pub" ]; then
  printf 'KEY_PATH_EXISTS: файлы уже есть; не перезаписывайте их\n'
else
  ssh-keygen -t ed25519 -a 100 -f "$bridge_key" -C 'private-gateway-bridge'
fi
```

В `Enter passphrase` и `Enter same passphrase again` введите одну и ту же новую
парольную фразу ключа. Ввод скрыт. Она защищает закрытый ключ на компьютере;
это не пароль пользователя `pgbridge`. При `KEY_PATH_EXISTS` используйте
существующую пару после проверки на шаге 7; незнакомые файлы не заменяйте.

**Где:** ваш компьютер. Создаются:

- `$HOME/.ssh/private_gateway_bridge_ed25519` — закрытый ключ, остается здесь;
- `$HOME/.ssh/private_gateway_bridge_ed25519.pub` — открытый ключ для VPS.

**Проверка:**

```bash
if [ -s "$bridge_key" ] && [ -s "$bridge_key.pub" ]; then
  ssh-keygen -lf "$bridge_key.pub"
else
  printf 'KEY_PAIR_MISSING\n'
fi
```

**Ожидаемый результат:** `256 SHA256:<отпечаток> private-gateway-bridge (ED25519)`.
При `KEY_PAIR_MISSING` дальше не переходите. Закрытый файл не копируйте на VPS
и не отправляйте в чат.

## 7. Проверить пару и скопировать открытый ключ

**Действие:**

```bash
ssh-keygen -lf "$bridge_key.pub"
(set -o pipefail; ssh-keygen -y -f "$bridge_key" | ssh-keygen -lf -)
```

Если запрошена парольная фраза ключа, введите ее локально. Сравните части
`SHA256:...` в двух строках: они должны совпасть; комментарии могут отличаться.
Затем покажите **только открытый** ключ:

```bash
cat "$bridge_key.pub"
```

Скопируйте целую строку от `ssh-ed25519` до `private-gateway-bridge` включительно.
Перенос отображения длинной строки на экране не должен стать переносом внутри
файла. Сохраните отпечаток `SHA256:...` для следующего шага.

**Где:** ваш компьютер, то же окно Git Bash/терминала.

**Проверка:** два одинаковых отпечатка и одна строка открытого ключа.

**Ожидаемый результат:** отпечатки имеют тип `ED25519`; строка ключа начинается
с `ssh-ed25519`. При несовпадении отпечатков пару не устанавливайте.

## 8. Добавить открытый ключ в учетную запись `pgbridge`

**Действие:** вернитесь в консоль хостинга под `root`:

```bash
install -d -o pgbridge -g pgbridge -m 700 /home/pgbridge/.ssh &&
touch /home/pgbridge/.ssh/authorized_keys &&
nano /home/pgbridge/.ssh/authorized_keys
```

В редактор вставьте скопированную на шаге 7 **одну строку открытого ключа**.
Если строки уже есть, добавьте свою с новой строки, сохранив прежние.
Сохраните: **Ctrl+O → Enter**. Закройте редактор: **Ctrl+X**.
Затем:

```bash
chown pgbridge:pgbridge /home/pgbridge/.ssh/authorized_keys &&
chmod 600 /home/pgbridge/.ssh/authorized_keys
```

**Где:** консоль **нового VPS**, пользователь `root`.
Точный файл: `/home/pgbridge/.ssh/authorized_keys`.
Пользователю `root` или пользователю Windows этот ключ добавлять не нужно.

**Проверка:**

```bash
stat -c '%U:%G %a %n' \
  /home/pgbridge/.ssh \
  /home/pgbridge/.ssh/authorized_keys
ssh-keygen -lf /home/pgbridge/.ssh/authorized_keys
```

**Ожидаемый результат:**

```text
pgbridge:pgbridge 700 /home/pgbridge/.ssh
pgbridge:pgbridge 600 /home/pgbridge/.ssh/authorized_keys
256 SHA256:<отпечаток> private-gateway-bridge (ED25519)
```

Отпечаток добавленного ключа должен совпасть с локальным из шага 7.
Для ранее добавленных ключей могут быть дополнительные строки.

## 9. Проверить порт и отпечаток SSH-сервера

**Действие:**

```bash
/usr/sbin/sshd -t &&
/usr/sbin/sshd -T | sed -n 's/^port /SSH_CONFIG_PORT=/p'
ss -ltnp
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

**Где:** консоль нового VPS, пользователь `root`.
Это открытый ключ **сервера**, отличный от вашего ключа входа.

**Проверка:** найдите в `ss -ltnp` слушатель нужного SSH-порта с процессом
`sshd` или `systemd`, доступный на публичном/всех адресах, а не только `127.0.0.1`.
Сверьте порт с карточкой хостинга и `bridge_ssh_port` на компьютере.
В firewall хостинга и уже действующем firewall VPS должно быть разрешение
входящего TCP на этот порт для вашего компьютера.

**Ожидаемый результат:** при стандартном порте — `SSH_CONFIG_PORT=22`,
строка `LISTEN` с адресом `0.0.0.0:22`, `*:22` или публичным IP с `:22`;
возможны дополнительные IPv6-слушатели. Также выведен
`256 SHA256:<отпечаток сервера> ... (ED25519)`.
Сохраните этот отпечаток для первого подключения с компьютера.

Если слушателя нет или порты расходятся, пришлите вывод без секретов до
изменения настроек SSH. Активный `ssh.socket` может обеспечивать SSH при
неактивном `ssh.service`; один статус службы не доказывает отказ.

## 10. Проверить новый вход именно созданным ключом

**Действие:** на компьютере выключите все VPN и их «Блок при обрыве / Kill Switch»;
проверьте прямой доступ к новому мосту:

```bash
ssh -F /dev/null \
  -o ControlPath=none \
  -o IdentitiesOnly=yes \
  -o PreferredAuthentications=publickey \
  -o PasswordAuthentication=no \
  -o KbdInteractiveAuthentication=no \
  -i "$bridge_key" -p "$bridge_ssh_port" "$bridge_user@$bridge_ip" \
  'printf "SSH_KEY_OK user=%s host=%s\n" "$(id -un)" "$(hostname)"'
printf 'KEY_LOGIN_EXIT=%s\n' "$?"
```

При первом подключении сверьте показанный отпечаток ED25519 с шагом 9 и только
при совпадении ответьте `yes`. Запрос `Enter passphrase for key ...` допустим:
это локальная парольная фраза ключа из шага 6.

**Где:** ваш компьютер, локальное окно с переменными из шага 5.
Используется новое соединение без пользовательского SSH-конфига и входа
по паролю учетной записи. Консоль хостинга оставьте открытой.

**Проверка:** последние две строки команды.

**Ожидаемый результат:**

```text
SSH_KEY_OK user=pgbridge host=<имя нового VPS>
KEY_LOGIN_EXIT=0
```

При `Permission denied (publickey)` проверьте пользователя, выбранную пару,
содержимое и права `authorized_keys` через консоль хостинга.
При сетевом тайм-ауте проверяйте адрес, порт, firewall и доступность нового VPS:
создание SSH-ключа сетевой тайм-аут не исправляет. Параметры проверки описаны
в [руководстве OpenSSH](https://man.openbsd.org/ssh.1).

## 11. Проверить `sudo` после входа и продолжить установку

**Действие:** в локальном окне подключитесь:

```bash
ssh -i "$bridge_key" -o IdentitiesOnly=yes \
  -p "$bridge_ssh_port" "$bridge_user@$bridge_ip"
```

После входа выполните:

```bash
id -un
sudo -k
sudo -v &&
test "$(sudo -n id -u)" -eq 0 &&
printf 'SUDO_OK\n'
```

На `[sudo] password for pgbridge:` введите **пароль пользователя `pgbridge`
из шага 3**. Это не парольная фраза локального ключа. Ввод скрыт.

**Где:** первая команда — ваш компьютер; проверка `sudo` — внутри нового
SSH-сеанса VPS-моста. Для нового локального окна снова задайте переменные шага 5.

**Проверка:** вывод `id -un`, затем результат `sudo`.

**Ожидаемый результат:**

```text
pgbridge
SUDO_OK
```

Продолжайте [установку моста](bridge.md). В ней `bridge_user='pgbridge'`
и `bridge_key="$HOME/.ssh/private_gateway_bridge_ed25519"` уже соответствуют
настроенному входу. SSH-ключ управляет VPS; ключи REALITY VPN остаются
на выходном американском сервере и на мост не копируются.
