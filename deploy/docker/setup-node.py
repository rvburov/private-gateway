#!/usr/bin/env python3
"""Создать локальные ключи удалённого VPS и закрытый env без вывода секретов."""

import argparse
import base64
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import socket
import stat
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit
import uuid


class NodeSetupError(ValueError):
    """Ошибка проверенных параметров, которую можно показать без секретов."""


def host(value):
    if not isinstance(value, str) or len(value) > 253:
        raise NodeSetupError('Некорректный адрес VPS или имя REALITY.')
    try:
        address = ipaddress.ip_address(value)
    except ValueError:
        if (not value or any(not re.fullmatch(r'[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?', part)
                             for part in value.split('.')) or re.fullmatch(r'[0-9.]+', value)):
            raise NodeSetupError('Некорректный адрес VPS или имя REALITY.') from None
        return value.lower()
    if address.is_unspecified or address.is_multicast or address.is_loopback or '%' in value:
        raise NodeSetupError('Некорректный адрес VPS или имя REALITY.')
    return str(address)


def settings(node_host, server_name, tcp_port, xhttp_port):
    if (type(tcp_port) is not int or type(xhttp_port) is not int
            or any(not 1 <= number <= 65535 or number == 22 for number in (tcp_port, xhttp_port))
            or tcp_port == xhttp_port):
        raise NodeSetupError('Порты TCP/Vision и XHTTP должны различаться и не занимать SSH 22.')
    name = host(server_name)
    if ':' in name or re.fullmatch(r'[0-9.]+', name):
        raise NodeSetupError('REALITY server name должен быть DNS-именем.')
    return {'node_host': host(node_host), 'server_name': name,
            'tcp_port': tcp_port, 'xhttp_port': xhttp_port}


def private_directory(path, create=False):
    path = Path(path)
    if create and not path.exists() and not path.is_symlink():
        path.mkdir(mode=0o700)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise NodeSetupError('Каталог ключей должен принадлежать root и иметь режим 0700.')
    return path


def private_file(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600:
        raise NodeSetupError('Файл ключей должен принадлежать root и иметь режим 0600.')


def atomic_private(path, content):
    if path.exists() or path.is_symlink():
        private_file(path)
    descriptor, temporary = tempfile.mkstemp(prefix='.' + path.name + '-', dir=path.parent)
    try:
        with os.fdopen(descriptor, 'w') as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def key(value):
    if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z0-9_-]{43}', value):
        raise NodeSetupError('Некорректный локальный ключ X25519.')
    if len(base64.urlsafe_b64decode(value + '=')) != 32:
        raise NodeSetupError('Некорректный локальный ключ X25519.')
    return value


def keypair(xray):
    # Закрытый ключ никогда не передаётся через argv и не повторяется при отказе.
    result = subprocess.run([str(xray), 'x25519'], capture_output=True, text=True, check=False, timeout=30)
    if result.returncode:
        raise NodeSetupError('Xray не смог создать локальную пару ключей.')
    values = dict(line.split(':', 1) for line in result.stdout.splitlines() if ':' in line)
    private = key(values.get('PrivateKey', '').strip())
    public = key(values.get('Password (PublicKey)', values.get('PublicKey', values.get('Password', ''))).strip())
    return {'private': private, 'public': public}


def load(path):
    private_file(path)
    data = json.loads(path.read_text())
    if not isinstance(data, dict) or set(data) != {'version', 'settings', 'tcp', 'xhttp', 'tcp_short_id', 'xhttp_short_id', 'xhttp_path'} or data['version'] != 1:
        raise NodeSetupError('Неизвестный формат сохранённых параметров узла.')
    config = data['settings']
    if not isinstance(config, dict) or set(config) != {'node_host', 'server_name', 'tcp_port', 'xhttp_port'} or settings(**config) != config:
        raise NodeSetupError('Некорректные сохранённые настройки узла.')
    for kind in ('tcp', 'xhttp'):
        pair = data[kind]
        if not isinstance(pair, dict) or set(pair) != {'private', 'public'}:
            raise NodeSetupError('Некорректные сохранённые ключи узла.')
        key(pair['private'])
        key(pair['public'])
    if data['tcp']['private'] == data['xhttp']['private'] or data['tcp']['public'] == data['xhttp']['public']:
        raise NodeSetupError('TCP/Vision и XHTTP должны иметь разные пары ключей.')
    if (not re.fullmatch(r'[0-9a-f]{16}', data['tcp_short_id'])
            or not re.fullmatch(r'[0-9a-f]{16}', data['xhttp_short_id'])
            or data['tcp_short_id'] == data['xhttp_short_id']
            or not re.fullmatch(r'/[0-9a-f]{32}', data['xhttp_path'])):
        raise NodeSetupError('Некорректные сохранённые short ID или путь XHTTP.')
    return data


def initialize(directory, xray, config):
    directory = private_directory(directory, create=True)
    path = directory / 'parameters.json'
    if path.exists() or path.is_symlink():
        data = load(path)
        if data['settings'] != config:
            raise NodeSetupError('Настройки отличаются от сохранённых; существующие ключи не заменены.')
        return data
    tcp, xhttp = keypair(xray), keypair(xray)
    if tcp['private'] == xhttp['private'] or tcp['public'] == xhttp['public']:
        raise NodeSetupError('Xray создал одинаковые пары; конфигурация не записана.')
    short_tcp, short_xhttp = secrets.token_hex(8), secrets.token_hex(8)
    while short_xhttp == short_tcp:
        short_xhttp = secrets.token_hex(8)
    data = {'version': 1, 'settings': config, 'tcp': tcp, 'xhttp': xhttp,
            'tcp_short_id': short_tcp, 'xhttp_short_id': short_xhttp,
            'xhttp_path': '/' + secrets.token_hex(16)}
    atomic_private(path, json.dumps(data, sort_keys=True) + '\n')
    return data


def public_parameters(data):
    config = data['settings']
    return {'NODE_HOST': config['node_host'], 'TCP_PORT': config['tcp_port'],
            'SERVER_NAME': config['server_name'], 'TCP_PUBLIC_KEY': data['tcp']['public'],
            'TCP_SHORT_ID': data['tcp_short_id'], 'XHTTP_PORT': config['xhttp_port'],
            'XHTTP_PUBLIC_KEY': data['xhttp']['public'], 'XHTTP_SHORT_ID': data['xhttp_short_id'],
            'XHTTP_PATH': data['xhttp_path']}


def environment(data, node_host, node_id, control_url):
    config = data['settings']
    if host(node_host) != config['node_host']:
        raise NodeSetupError('Адрес узла отличается от адреса при создании ключей.')
    try:
        identifier = str(uuid.UUID(node_id))
        parsed = urlsplit(control_url)
        parsed_port = parsed.port
    except ValueError:
        raise NodeSetupError('Некорректный ID узла или HTTPS origin панели.') from None
    if identifier != node_id or identifier == str(uuid.UUID(int=0)):
        raise NodeSetupError('Нужен ID узла из панели в формате UUID.')
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password
            or parsed.path or parsed.query or parsed.fragment
            or any(c.isspace() or ord(c) < 32 for c in control_url)
            or (parsed_port is not None and not 1 <= parsed_port <= 65535)):
        raise NodeSetupError('Нужен HTTPS origin панели без пути, / в конце и данных авторизации.')
    host(parsed.hostname)
    return {'GATEWAY_CONTROL_URL': control_url, 'GATEWAY_NODE_ID': identifier,
            'GATEWAY_HOST': config['node_host'], 'GATEWAY_PORT': str(config['tcp_port']),
            'GATEWAY_REALITY_PRIVATE_KEY': data['tcp']['private'],
            'GATEWAY_SERVER_NAME': config['server_name'], 'GATEWAY_SHORT_ID': data['tcp_short_id'],
            'GATEWAY_REALITY_TARGET': config['server_name'] + ':443', 'GATEWAY_XHTTP_ENABLED': '1',
            'GATEWAY_XHTTP_PORT': str(config['xhttp_port']), 'GATEWAY_XHTTP_PRIVATE_KEY': data['xhttp']['private'],
            'GATEWAY_XHTTP_SHORT_ID': data['xhttp_short_id'], 'GATEWAY_XHTTP_PATH': data['xhttp_path'],
            'GATEWAY_XHTTP_H3_ENABLED': '0'}


def write_env(directory, node_host, node_id, control_url, check=False, check_ports=False):
    directory = private_directory(directory)
    data = load(directory / 'parameters.json')
    values = environment(data, node_host, node_id, control_url)
    path = directory / 'node.env'
    if check_ports:
        preflight_ports(values)
    if not check:
        atomic_private(path, ''.join(f'{name}={value}\n' for name, value in values.items()))
    return path



def validate_env(path):
    values = {}
    for line in Path(path).read_text().splitlines():
        if not line or line.startswith('#'):
            continue
        if '=' not in line:
            raise NodeSetupError('Некорректная строка node.env.')
        name, value = line.split('=', 1)
        if not re.fullmatch(r'[A-Z][A-Z0-9_]*', name) or name in values or '\0' in value:
            raise NodeSetupError('Некорректное или повторное имя в node.env.')
        values[name] = value
    required = {'GATEWAY_CONTROL_URL', 'GATEWAY_NODE_ID', 'GATEWAY_HOST', 'GATEWAY_PORT',
                'GATEWAY_REALITY_PRIVATE_KEY', 'GATEWAY_SERVER_NAME', 'GATEWAY_SHORT_ID',
                'GATEWAY_REALITY_TARGET'}
    if not required <= values.keys():
        raise NodeSetupError('В node.env отсутствуют обязательные параметры узла.')
    try:
        tcp_port = int(values['GATEWAY_PORT'])
        xhttp_port = int(values.get('GATEWAY_XHTTP_PORT', '8444'))
    except ValueError:
        raise NodeSetupError('В node.env неверные порты.') from None
    config = settings(values['GATEWAY_HOST'], values['GATEWAY_SERVER_NAME'], tcp_port, xhttp_port)
    key(values['GATEWAY_REALITY_PRIVATE_KEY'])
    if not re.fullmatch(r'[0-9a-fA-F]{16}', values['GATEWAY_SHORT_ID']):
        raise NodeSetupError('В node.env неверный short ID TCP.')
    if values['GATEWAY_REALITY_TARGET'] != config['server_name'] + ':443':
        raise NodeSetupError('REALITY target должен совпадать с server name и портом 443.')
    dummy = {'settings': config, 'tcp': {'private': values['GATEWAY_REALITY_PRIVATE_KEY']},
             'xhttp': {'private': 'not-output'}, 'tcp_short_id': values['GATEWAY_SHORT_ID'],
             'xhttp_short_id': 'not-output', 'xhttp_path': '/not-output'}
    environment(dummy, config['node_host'], values['GATEWAY_NODE_ID'], values['GATEWAY_CONTROL_URL'])
    if values.get('GATEWAY_XHTTP_ENABLED', '0') not in ('0', '1'):
        raise NodeSetupError('В node.env неверный режим XHTTP.')
    if values.get('GATEWAY_XHTTP_ENABLED') == '1':
        xhttp_key = key(values.get('GATEWAY_XHTTP_PRIVATE_KEY', ''))
        sid = values.get('GATEWAY_XHTTP_SHORT_ID', '')
        if (xhttp_key == values['GATEWAY_REALITY_PRIVATE_KEY']
                or not re.fullmatch(r'[0-9a-fA-F]{16}', sid)
                or sid.lower() == values['GATEWAY_SHORT_ID'].lower()
                or not re.fullmatch(r'/[A-Za-z0-9_-]{16,128}', values.get('GATEWAY_XHTTP_PATH', ''))):
            raise NodeSetupError('В node.env неверные или совпадающие параметры XHTTP.')
    if values.get('GATEWAY_XHTTP_H3_ENABLED', '0') != '0':
        raise NodeSetupError('Автоматическая подготовка node.env пока рассчитана на TCP/Vision и XHTTP/REALITY.')
    return values


def preflight_ports(values):
    ports = [int(values['GATEWAY_PORT'])]
    if values.get('GATEWAY_XHTTP_ENABLED') == '1':
        ports.append(int(values['GATEWAY_XHTTP_PORT']))
    for port in ports:
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
                probe.bind(('0.0.0.0', port))
        except OSError:
            raise NodeSetupError('Входной TCP-порт узла занят или недоступен; настройка служб не началась.') from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('initialize', 'environment'))
    parser.add_argument('--state-dir', type=Path, default=Path('/etc/private-gateway-node-setup'))
    parser.add_argument('--xray', type=Path)
    parser.add_argument('--node-host', required=True)
    parser.add_argument('--server-name', default='www.cloudflare.com')
    parser.add_argument('--tcp-port', type=int, default=8443)
    parser.add_argument('--xhttp-port', type=int, default=8444)
    parser.add_argument('--node-id')
    parser.add_argument('--control-url')
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--check-ports', action='store_true')
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise NodeSetupError('Запускайте подготовку локальных ключей через sudo.')
    if args.action == 'initialize':
        if args.check or not args.xray or args.node_id or args.control_url:
            raise NodeSetupError('Для initialize нужен только проверенный Xray и параметры VPS.')
        data = initialize(args.state_dir, args.xray, settings(args.node_host, args.server_name, args.tcp_port, args.xhttp_port))
        for name, value in public_parameters(data).items():
            print(f'{name}={value}')
        print('NODE_PARAMETERS_READY')
    else:
        if not args.node_id or not args.control_url:
            raise NodeSetupError('Нужны ID узла и HTTPS origin панели.')
        path = write_env(args.state_dir, args.node_host, args.node_id, args.control_url, args.check, args.check_ports)
        if not args.check:
            print(path)


if __name__ == '__main__':
    try:
        main()
    except NodeSetupError as error:
        print('Подготовка ключей узла остановлена: ' + str(error), file=sys.stderr)
        raise SystemExit(1) from None
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        # Не выводим stderr Xray, содержимое env и непроверенные строки исключений.
        print('Подготовка ключей узла остановлена. Проверьте параметры, права root:0700/0600 и сохранённый адрес VPS.', file=sys.stderr)
        raise SystemExit(1) from None
