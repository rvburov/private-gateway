## 1. Указать адрес VPS и путь SSH-ключа

**Действие:** подставьте публичный IP нового VPS, SSH-пользователя и порт:

```bash
bridge_ip='IP_НОВОГО_VPS'
bridge_user='SSH_ПОЛЬЗОВАТЕЛЬ'
bridge_ssh_port='22'
bridge_key="$HOME/.ssh/private_gateway_bridge_ed25519"
```

**Где:** ваш компьютер, Windows — Git Bash; macOS/Linux — терминал bash.
Все дальнейшие команды этой инструкции выполняются в этом же локальном окне.
Первый доступ должен уже работать с паролем хостинга либо ранее настроенным ключом.

**Проверка:**

```bash
command -v ssh ssh-keygen
printf 'VPS=%s USER=%s PORT=%s\n' "$bridge_ip" "$bridge_user" "$bridge_ssh_port"
printf 'PRIVATE_KEY_PATH=%s\n' "$bridge_key"
```

**Ожидаемый результат:** пути к `ssh` и `ssh-keygen`, данные нового VPS и
локальный путь `$HOME/.ssh/private_gateway_bridge_ed25519`.
Заглушки `IP_НОВОГО_VPS` и `SSH_ПОЛЬЗОВАТЕЛЬ` должны быть заменены.

## 2. Создать отдельный SSH-ключ

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

В запросах `Enter passphrase` и `Enter same passphrase again` введите одну
и ту же парольную фразу ключа. Ввод скрыт. Это локальная защита закрытого ключа,
а не пароль учетной записи VPS. При `KEY_PATH_EXISTS` используйте существующую
пару после проверки следующим шагом; незнакомые файлы не заменяйте.

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

**Ожидаемый результат:** строка вида
`256 SHA256:<отпечаток> private-gateway-bridge (ED25519)`.
При `KEY_PAIR_MISSING` дальше не переходите. Закрытый файл не копируйте на VPS
и не отправляйте в чат.

## 3. Проверить соответствие открытого и закрытого ключей

**Действие:** получите отпечаток открытого ключа и отпечаток, выведенный из закрытого:

```bash
ssh-keygen -lf "$bridge_key.pub"
(set -o pipefail; ssh-keygen -y -f "$bridge_key" | ssh-keygen -lf -)
```

Если запрошена парольная фраза, введите ее локально.

**Где:** ваш компьютер, тот же Git Bash/терминал.

**Проверка:** сравните части `SHA256:...` в двух строках.

**Ожидаемый результат:** одинаковые отпечатки `SHA256:...` и тип `ED25519`.
Комментарии строк могут различаться. При несовпадении пару не устанавливайте.

## 4. Добавить открытый ключ пользователю на VPS

**Действие:** оставьте уже открытый SSH-сеанс до успешной проверки шага 5.
Из локального терминала передайте только `.pub`:

```bash
cat "$bridge_key.pub" | ssh -p "$bridge_ssh_port" "$bridge_user@$bridge_ip" '
umask 077
bridge_auth_file="$HOME/.ssh/authorized_keys"
mkdir -p "$HOME/.ssh" &&
chmod 700 "$HOME/.ssh" &&
touch "$bridge_auth_file" &&
chmod 600 "$bridge_auth_file" || exit 1

IFS= read -r bridge_public_key || exit 1
case "$bridge_public_key" in
  ssh-ed25519\ *) ;;
  *) exit 1 ;;
esac
printf "%s\n" "$bridge_public_key" | ssh-keygen -lf - > /dev/null || exit 1

if grep -Fqx -- "$bridge_public_key" "$bridge_auth_file"; then
  :
else
  bridge_key_lookup=$?
  [ "$bridge_key_lookup" -eq 1 ] || exit "$bridge_key_lookup"
  printf "\n%s\n" "$bridge_public_key" >> "$bridge_auth_file" || exit 1
fi
printf "SSH_KEY_INSTALLED\n"
'
```

При первом подключении сверьте отпечаток SSH-хоста с консолью хостинга.
Если запрошен пароль VPS, введите его только в своем терминале.

**Где:** команда запускается на вашем компьютере. Ключ добавляется в
`$HOME/.ssh/authorized_keys` выбранного SSH-пользователя:
например, `/root/.ssh/authorized_keys` для `root` или
`/home/ubuntu/.ssh/authorized_keys` для `ubuntu`. Также при необходимости
создается `.ssh` и выставляются права `700` каталогу и `600` файлу.
Старые строки сохраняются;
повторный запуск не добавляет дубликат этой строки.

**Проверка:** сразу после команды:

```bash
printf 'KEY_INSTALL_EXIT=%s\n' "$?"
```

**Ожидаемый результат:**

```text
SSH_KEY_INSTALLED
KEY_INSTALL_EXIT=0
```

Если первый вход еще не работает, добавьте содержимое **открытого** файла `.pub`
через предусмотренную хостингом настройку SSH-ключей/консоль. Затем выполните
проверку шага 5. Закрытый ключ остается на вашем компьютере.

## 5. Проверить новый вход именно этим ключом

**Действие:**

```bash
ssh -F /dev/null \
  -o ControlPath=none \
  -o IdentitiesOnly=yes \
  -o PreferredAuthentications=publickey \
  -o PasswordAuthentication=no \
  -o KbdInteractiveAuthentication=no \
  -i "$bridge_key" -p "$bridge_ssh_port" "$bridge_user@$bridge_ip" \
  'printf "SSH_KEY_OK user=%s host=%s\n" "$(id -un)" "$(hostname)"'
```

**Где:** ваш компьютер, тот же локальный терминал. Используется новое соединение
без пользовательского SSH-конфига и без входа по паролю учетной записи.
Запрос `Enter passphrase for key ...` допустим: он разблокирует ключ локально.

**Проверка:** сразу после команды:

```bash
printf 'KEY_LOGIN_EXIT=%s\n' "$?"
```

**Ожидаемый результат:**

```text
SSH_KEY_OK user=<пользователь VPS> host=<имя VPS>
KEY_LOGIN_EXIT=0
```

При `Permission denied (publickey)` настройка еще не работает. Проверьте
пользователя, порт, выбранную пару и путь `authorized_keys` через прежний доступ.
Параметры проверки описаны в [руководстве OpenSSH](https://man.openbsd.org/ssh.1).

## 6. Подключиться для установки моста

**Действие:**

```bash
ssh -i "$bridge_key" -o IdentitiesOnly=yes \
  -p "$bridge_ssh_port" "$bridge_user@$bridge_ip"
```

**Где:** команда — на вашем компьютере, после входа — SSH VPS-моста.
Для нового локального окна снова задайте переменные из шага 1.

**Проверка — после входа на VPS:**

```bash
printf 'SSH_SESSION_OK user=%s host=%s\n' "$(id -un)" "$(hostname)"
```

**Ожидаемый результат:** `SSH_SESSION_OK user=<пользователь> host=<имя VPS>`.
Продолжайте [установку моста](bridge.md). SSH-ключ управляет доступом к VPS;
ключи REALITY VPN остаются на выходном американском сервере и на мост не копируются.
