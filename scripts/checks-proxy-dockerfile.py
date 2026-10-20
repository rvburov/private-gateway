#!/usr/bin/env python3
"""Собрать временный Dockerfile с CA прокси и одним слоем для облачного Docker VFS."""

import pathlib
import shlex
import sys


def instructions(source):
    result, pending = [], ""
    for line in source.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        pending += line.strip()
        if pending.endswith("\\"):
            pending = pending[:-1] + " "
            continue
        kind, _, value = pending.partition(" ")
        result.append((kind, value))
        pending = ""
    if pending:
        raise ValueError("unfinished Dockerfile instruction")
    return result


def prepare(source):
    prefix, metadata, commands = [], [], []
    user, workdir, stage, seen_run = "root", "/", 0, False
    for kind, value in instructions(source):
        if kind == "FROM":
            if stage >= 2 or seen_run:
                raise ValueError("unsupported Dockerfile stages")
            prefix.append(f"FROM {value}")
            stage += 1
        elif kind == "ARG":
            prefix.append(f"ARG {value}")
        elif kind == "RUN":
            seen_run = True
            command = value
            if user != "root":
                command = "runuser -u " + shlex.quote(user) + " -- /bin/sh -ec " + shlex.quote(value)
            commands.append(command)
        elif kind == "COPY":
            parts, source_root, owner = shlex.split(value), "/run/checks-context", None
            while parts and parts[0].startswith("--"):
                option = parts.pop(0)
                if option == "--from=node":
                    source_root = "/run/checks-node"
                elif option.startswith("--chown="):
                    owner = option.removeprefix("--chown=")
                else:
                    raise ValueError("unsupported COPY option")
            if len(parts) < 2:
                raise ValueError("COPY requires sources and destination")
            destination = pathlib.PurePosixPath(parts[-1])
            if not destination.is_absolute():
                destination = pathlib.PurePosixPath(workdir) / destination
            sources = [str(pathlib.PurePosixPath(source_root) / part.lstrip("/")) for part in parts[:-1]]
            directory = destination if len(sources) > 1 or parts[-1].endswith("/") or parts[-1] == "." else destination.parent
            commands.append("mkdir -p " + shlex.quote(str(directory)))
            # COPY . . накладывает файлы, сохраняя установленные зависимости в рабочем каталоге.
            if parts[:-1] == ["."]:
                sources = [source_root + "/."]
            commands.append("cp -a " + " ".join(shlex.quote(x) for x in sources) + " " + shlex.quote(str(destination)))
            if owner:
                commands.append("chown -R " + shlex.quote(owner) + " " + shlex.quote(str(destination)))
        elif kind == "USER":
            user = value
        elif kind == "WORKDIR":
            if not value.startswith("/"):
                raise ValueError("WORKDIR must be absolute")
            workdir = value
            commands.extend(["mkdir -p " + shlex.quote(workdir), "chown " + shlex.quote(user) + " " + shlex.quote(workdir), "cd " + shlex.quote(workdir)])
        elif kind in ("ENV", "CMD"):
            metadata.append(f"{kind} {value}")
        else:
            raise ValueError("unsupported Dockerfile instruction: " + kind)
    if stage != 2 or not seen_run:
        raise ValueError("expected Node and Go stages with setup commands")
    # В VFS каждый обычный слой копирует весь Go toolchain. Один RUN сохраняет тот же порядок
    # установки, но требует места только для одной полной копии файловой системы.
    setup = [
        "set -eu",
        "export CURL_CA_BUNDLE=/run/secrets/proxy_ca SSL_CERT_FILE=/run/secrets/proxy_ca NODE_EXTRA_CA_CERTS=/run/secrets/proxy_ca",
        *commands,
    ]
    run = (
        "RUN --mount=type=secret,id=proxy_ca,required=true,mode=0444 "
        "--mount=type=bind,target=/run/checks-context "
        "--mount=type=bind,from=node,target=/run/checks-node "
        + "; \\\n    ".join(setup)
    )
    return "\n".join([*prefix, run, "USER " + user, "WORKDIR " + workdir, *metadata, ""])


if __name__ == "__main__":
    sys.stdout.write(prepare(pathlib.Path(sys.argv[1]).read_text()))
