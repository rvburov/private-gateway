## 1. Проверить уже созданного пользователя

**Действие:** войдите на новый VPS привычным способом через SSH или консоль хостинга.
Если приглашение сейчас `root@...`, а первоначальный сеанс был обычного пользователя,
один раз выполните `exit`, чтобы вернуться к нему. Дальше используйте обычную учетную
запись с `sudo`, например `romanburov` или `ubuntu`. Создавать вторую не требуется.

**Где:** терминал нового VPS-моста, не локальный Git Bash компьютера.

**Проверка:**

```bash
id -un
sudo -v && test "$(sudo -n id -u)" -eq 0 && printf 'SUDO_OK\n'
. /etc/os-release
printf 'OS=%s VERSION=%s ARCH=%s\n' "$ID" "$VERSION_ID" "$(uname -m)"
```

**Ожидаемый результат:** имя существующего пользователя, `SUDO_OK`, затем для
моста с Ubuntu 22.04 — `OS=ubuntu VERSION=22.04 ARCH=x86_64`. Также поддерживаются
Ubuntu 24.04/26.04 amd64. Запишите имя, которое показал `id -un`: это логин SSH,
а не название ОС. Пароль `sudo` — пароль этой учетной записи.

## 2. Подготовить программы на мосту

**Действие:**

```bash
sudo apt-get update &&
sudo apt-get install -y ca-certificates curl python3 iproute2 openssh-server nano

if [ ! -d /run/sshd ]; then
  sudo install -d -o root -g root -m 755 /run/sshd
fi
```

**Где:** тот же терминал нового VPS под обычным пользователем.
Перед каждой административной командой есть `sudo`; выполнять `sudo -i` не нужно.

**Проверка:**

```bash
(
  for bridge_tool in curl python3 ip ss ssh-keygen nano; do
    command -v "$bridge_tool" || exit 1
  done
) && printf 'BRIDGE_TOOLS_OK\n'
python3 --version
```

**Ожидаемый результат:** шесть путей, `BRIDGE_TOOLS_OK` и `Python 3.10.<номер>`
на штатной Ubuntu 22.04. Для установщика нужен новый артефакт с поддержкой Python
3.10; прежние выпуски с `hashlib.file_digest` на этой ОС не подходят.

## 3. Записать данные доступа на компьютере

**Действие:** откройте отдельное локальное окно. Вместо `IP_НОВОГО_VPS` вставьте
IP из панели хостинга; вместо `ИМЯ_ИЗ_ШАГА_1` — результат `id -un` на сервере:

```bash
bridge_ip='IP_НОВОГО_VPS'
bridge_user='ИМЯ_ИЗ_ШАГА_1'
bridge_ssh_port='22'
bridge_key="$HOME/.ssh/private_gateway_bridge_ed25519"
```

Например, при `romanburov@49649` нужно `bridge_user='romanburov'`; при `ubuntu@...`
нужно `bridge_user='ubuntu'`. Если SSH работает на другом порту, укажите его вместо `22`.

**Где:** ваш компьютер — Git Bash Windows или терминал bash macOS/Linux.
Локальные команды следующих шагов выполняйте в этом же окне.

**Проверка:**

```bash
printf 'VPS=%s USER=%s PORT=%s\n' "$bridge_ip" "$bridge_user" "$bridge_ssh_port"
```

**Ожидаемый результат:** реальный IP моста, фактический SSH-пользователь и порт.
Заглушек `IP_НОВОГО_VPS` и `ИМЯ_ИЗ_ШАГА_1` в выводе нет.

## 4. Создать или проверить отдельный SSH-ключ

**Действие:**

```bash
mkdir -p "$HOME/.ssh"
chmod 700 "$HOME/.ssh"
if [ -e "$bridge_key" ] || [ -e "$bridge_key.pub" ]; then
  printf 'KEY_PATH_EXISTS: используйте проверенную существующую пару\n'
else
  ssh-keygen -t ed25519 -a 100 -f "$bridge_key" -C 'private-gateway-bridge'
fi
```

При создании введите новую парольную фразу ключа дважды. Она защищает ключ
на компьютере и отличается от пароля пользователя VPS. При `KEY_PATH_EXISTS`
не перезаписывайте файлы. Если пара знакомая, проверьте ее командами ниже.

**Где:** ваш компьютер. Файлы:
`$HOME/.ssh/private_gateway_bridge_ed25519` и тот же путь с `.pub`.
Закрытый файл остается на компьютере; в VPS и чат передается только открытый `.pub`.

**Проверка:**

```bash
ssh-keygen -lf "$bridge_key.pub"
(set -o pipefail; ssh-keygen -y -f "$bridge_key" | ssh-keygen -lf -)
```

**Ожидаемый результат:** два одинаковых `SHA256:<отпечаток>` типа `ED25519`.
Если одного файла нет или отпечатки отличаются, следующие шаги не выполняйте.

## 5. Установить открытый ключ под текущим пользователем VPS

**Действие — на компьютере:**

```bash
cat "$bridge_key.pub"
```

Скопируйте одну полную строку от `ssh-ed25519` до комментария включительно.

**Действие — в оставленном терминале VPS под пользователем из шага 1:**

```bash
id -un
test "$(id -u)" -ne 0 &&
mkdir -p "$HOME/.ssh" &&
chmod 700 "$HOME/.ssh" &&
touch "$HOME/.ssh/authorized_keys" &&
chmod 600 "$HOME/.ssh/authorized_keys" &&
nano "$HOME/.ssh/authorized_keys"
```

Убедитесь, что `id -un` показывает вашу обычную учетную запись, а не `root`.
В `nano` добавьте открытую строку ключа с новой строки, сохранив прежние ключи.
Сохраните **Ctrl+O → Enter**, выйдите **Ctrl+X**. Эти команды выполняются без
`sudo`, чтобы ключ попал именно текущему пользователю.

**Где:** показ ключа — компьютер; редактирование — новый VPS.
Точный файл для `romanburov`: `/home/romanburov/.ssh/authorized_keys`,
для `ubuntu`: `/home/ubuntu/.ssh/authorized_keys`.

**Проверка — на VPS:**

```bash
stat -c '%U:%G %a %n' "$HOME/.ssh" "$HOME/.ssh/authorized_keys"
ssh-keygen -lf "$HOME/.ssh/authorized_keys"
```

**Ожидаемый результат:** владелец — текущий пользователь; права `.ssh` — `700`,
`authorized_keys` — `600`. Отпечаток добавленного ключа совпадает с шагом 4.
У прежних ключей могут быть дополнительные строки.

## 6. Проверить порт и ключ SSH-сервера

**Действие:**

```bash
sudo /usr/sbin/sshd -t
sudo ss -ltnp
sudo ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

**Где:** оставленный терминал нового VPS, обычный пользователь с `sudo`.

**Проверка:** найдите слушатель SSH-порта с процессом `sshd` или `systemd`;
сверьте с `bridge_ssh_port` из шага 3. Сохраните показанный отпечаток сервера.
Это другой ключ, не локальная пара из шага 4.

**Ожидаемый результат:** `sshd -t` не печатает ошибку; для стандартного порта
есть `LISTEN` на `0.0.0.0:22`, `*:22` или публичном IP с `:22`, а также строка
`256 SHA256:<отпечаток сервера> ... (ED25519)`.

## 7. Проверить новое подключение только ключом

**Действие:** выключите на компьютере все VPN и их «Блок при обрыве / Kill Switch».
Оставьте прежний терминал VPS открытым. В локальном окне выполните:

```bash
ssh -F /dev/null -o ControlPath=none -o IdentitiesOnly=yes \
  -o PreferredAuthentications=publickey -o PasswordAuthentication=no \
  -o KbdInteractiveAuthentication=no \
  -i "$bridge_key" -p "$bridge_ssh_port" "$bridge_user@$bridge_ip" \
  'printf "SSH_KEY_OK user=%s host=%s\n" "$(id -un)" "$(hostname)"'
printf 'KEY_LOGIN_EXIT=%s\n' "$?"
```

При первом подключении сверьте отпечаток сервера с шагом 6 и только при совпадении
ответьте `yes`. Запрос `Enter passphrase for key ...` — локальная парольная фраза
ключа из шага 4; парольный вход пользователя этой проверкой отключен.

**Где:** ваш компьютер, окно с переменными шага 3.

**Проверка:** последние строки команды.

**Ожидаемый результат:** например, для показанного пользователем VPS:

```text
SSH_KEY_OK user=romanburov host=49649
KEY_LOGIN_EXIT=0
```

Имя и hostname должны соответствовать вашему VPS. При тайм-ауте проверяйте
сетевую доступность и firewall; при `Permission denied (publickey)` — учетную
запись, ключ и права файла. Следующий этап — [установка VPS-моста](bridge.md);
нового пользователя создавать не требуется.
