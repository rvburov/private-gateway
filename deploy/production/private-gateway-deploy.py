#!/usr/bin/python3 -I
"""Install a bounded, verified release received on stdin through a fixed sudo entry.

Release contents run only as gateway or gateway-browser. This helper never runs
an uploaded installer, shell command, or binary as root. Schema migrations are
not automatically reversible: after a migration failure the API stays stopped.
"""

from __future__ import annotations

import contextlib
import dataclasses
import datetime
import fcntl
import gzip
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

MAX_COMPRESSED = 256 * 1024 * 1024
MAX_EXPANDED = 512 * 1024 * 1024
MAX_FILE = 128 * 1024 * 1024
MAX_MANIFEST = 2 * 1024 * 1024
MAX_MEMBERS = 10000
CHUNK = 1024 * 1024
REQUIRED = frozenset({
    "bin/gateway-api", "bin/gatewayctl", "bin/xray", "web/index.html",
    "browser/Containerfile", "browser/start.sh", "browser/healthcheck.sh",
    "browser/smoke.py", "browser/private-gateway-browser-worker.socket",
    "browser/private-gateway-browser-worker.service", "browser/tmpfiles.conf",
})
BINARIES = frozenset({"bin/gateway-api", "bin/gatewayctl", "bin/xray"})
VERSION = re.compile(r"[0-9a-f]{40}\Z")
DIGEST = re.compile(r"[0-9a-f]{64}\Z")
COMPONENT = re.compile(r"[A-Za-z0-9_@.+-]+\Z")
ENV = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8", "HOME": "/"}


class DeploymentError(Exception):
    pass


def fail(message):
    raise DeploymentError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail("Duplicate key in manifest")
        result[key] = value
    return result


def canonical_path(name, directory=False):
    if directory and name.endswith("/"):
        name = name[:-1]
    parts = name.split("/")
    if (not name or len(name.encode("utf-8")) > 240 or
            any(part in ("", ".", "..") or not COMPONENT.fullmatch(part) for part in parts)):
        fail("Non-canonical archive path")
    if name == "manifest.json" and not directory:
        return name
    if parts[0] not in ("bin", "web", "browser"):
        fail("Unknown release path")
    if directory:
        if parts[0] == "bin" and len(parts) != 1:
            fail("Unexpected executable directory")
    elif len(parts) == 1 or (parts[0] == "bin" and name not in BINARIES):
        fail("Unexpected release file")
    return name


def read_manifest(data):
    try:
        result = json.loads(data, object_pairs_hook=unique_object)
    except (ValueError, UnicodeError):
        fail("Invalid release manifest JSON")
    if not isinstance(result, dict) or set(result) != {"version", "os", "arch", "files"}:
        fail("Invalid release manifest fields")
    if not isinstance(result["version"], str) or not VERSION.fullmatch(result["version"]):
        fail("Release version must be a full lowercase Git commit SHA")
    if result["os"] != "linux" or result["arch"] != "amd64":
        fail("Release must target linux/amd64")
    files = result["files"]
    if not isinstance(files, dict) or not REQUIRED.issubset(files) or len(files) >= MAX_MEMBERS:
        fail("Required release files are missing or file count exceeds limit")
    for name, digest in files.items():
        if not isinstance(name, str) or canonical_path(name) == "manifest.json":
            fail("Invalid manifest payload path")
        if not isinstance(digest, str) or not DIGEST.fullmatch(digest):
            fail("Invalid manifest checksum")
    return result


def bounded_copy(source, target, limit):
    total = 0
    while True:
        block = source.read(min(CHUNK, limit - total + 1))
        if not block:
            return total
        total += len(block)
        if total > limit:
            fail("Release archive exceeds size limit")
        target.write(block)


def exact_read(stream, size):
    data = stream.read(size)
    if len(data) != size:
        fail("Truncated release archive")
    return data


def unpack_verified(stream, destination):
    """Extract into a new private directory; ignore all archive permissions/owners.

    Parse plain tar headers individually rather than allowing hidden PAX/GNU
    extension records, sparse files, links, or implicit extractor behavior.
    """
    destination = Path(destination)
    with tempfile.TemporaryFile() as compressed, tempfile.TemporaryFile() as expanded:
        bounded_copy(stream, compressed, MAX_COMPRESSED)
        compressed.seek(0)
        try:
            with gzip.GzipFile(fileobj=compressed, mode="rb") as gz:
                bounded_copy(gz, expanded, MAX_EXPANDED)
        except (OSError, EOFError) as error:
            raise DeploymentError("Invalid compressed release archive") from error
        expanded.seek(0)
        seen, hashes, manifest_bytes = set(), {}, None
        while True:
            header = exact_read(expanded, 512)
            if header == bytes(512):
                if exact_read(expanded, 512) != bytes(512):
                    fail("Invalid tar end marker")
                while tail := expanded.read(CHUNK):
                    if tail.strip(b"\0"):
                        fail("Trailing data after tar end marker")
                break
            try:
                member = tarfile.TarInfo.frombuf(header, "utf-8", "strict")
            except (tarfile.TarError, ValueError, UnicodeError) as error:
                raise DeploymentError("Invalid tar header") from error
            if member.type not in (tarfile.REGTYPE, tarfile.AREGTYPE, tarfile.DIRTYPE):
                fail("Only plain regular files and directories are allowed")
            directory = member.type == tarfile.DIRTYPE
            name = canonical_path(member.name, directory)
            if name in seen or len(seen) >= MAX_MEMBERS:
                fail("Duplicate archive path or too many members")
            seen.add(name)
            limit = MAX_MANIFEST if name == "manifest.json" else MAX_FILE
            if member.size < 0 or member.size > limit or (directory and member.size != 0):
                fail("Invalid archive member size")
            target = destination / name
            try:
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                if directory:
                    target.mkdir(mode=0o700, exist_ok=True)
                    continue
                fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            except OSError as error:
                raise DeploymentError("Conflicting archive paths") from error
            digest = hashlib.sha256()
            remaining = member.size
            with os.fdopen(fd, "wb") as output:
                while remaining:
                    block = exact_read(expanded, min(CHUNK, remaining))
                    output.write(block)
                    digest.update(block)
                    remaining -= len(block)
                output.flush()
                os.fsync(output.fileno())
            padding = (-member.size) % 512
            if padding and exact_read(expanded, padding).strip(b"\0"):
                fail("Nonzero tar member padding")
            if name == "manifest.json":
                manifest_bytes = target.read_bytes()
            else:
                hashes[name] = digest.hexdigest()
        if manifest_bytes is None:
            fail("Release manifest is missing")
        manifest = read_manifest(manifest_bytes)
        if hashes != manifest["files"]:
            fail("Release payload is missing, unexpected, or has an incorrect checksum")
        for name in BINARIES:
            with (destination / name).open("rb") as executable:
                header = executable.read(20)
            # ELF64 little endian, System V/Linux, x86-64 machine. Never execute
            # scripts or a binary for a different architecture during migration.
            if len(header) < 20 or header[:6] != b"\x7fELF\x02\x01" or header[18:20] != b"\x3e\x00":
                fail("Release executables must be Linux x86-64 ELF binaries")
        return manifest


def trusted_directory(path, mode=0o755, owner=0):
    path = Path(path)
    try:
        path.mkdir(mode=mode)
        os.chmod(path, mode)
    except FileExistsError:
        pass
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
        fail("Deployment directory is not a root-owned protected directory")
    return path


def trusted_file(path, allow_group_read=False, owner=0):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    info = os.fstat(fd)
    mask = 0o027 if allow_group_read else 0o077
    if not stat.S_ISREG(info.st_mode) or info.st_uid != owner or info.st_nlink != 1 or info.st_mode & mask:
        os.close(fd)
        fail("Deployment configuration is not a protected root-owned regular file")
    return os.fdopen(fd, "rb")


def verify_existing(path, manifest, owner=0):
    found = {}
    for directory, folders, files in os.walk(path, followlinks=False):
        for name in folders + files:
            item = Path(directory) / name
            info = item.lstat()
            if info.st_uid != owner or info.st_mode & 0o022 or not (stat.S_ISREG(info.st_mode) or stat.S_ISDIR(info.st_mode)):
                fail("Existing release contains an unsafe filesystem entry")
        for name in files:
            item = Path(directory) / name
            relative = item.relative_to(path).as_posix()
            if relative == "manifest.json":
                if read_manifest(item.read_bytes()) != manifest:
                    fail("Existing release manifest conflicts with uploaded release")
            else:
                with item.open("rb") as source:
                    found[relative] = hashlib.file_digest(source, "sha256").hexdigest()
    if found != manifest["files"] or not (path / "manifest.json").is_file():
        fail("Existing release payload conflicts with uploaded release")


def stage_release(stream, releases, owner=0):
    releases = trusted_directory(releases, owner=owner)
    incoming = Path(tempfile.mkdtemp(prefix=".incoming-", dir=releases))
    try:
        manifest = unpack_verified(stream, incoming)
        release = releases / manifest["version"]
        if release.exists() or release.is_symlink():
            trusted_directory(release, owner=owner)
            verify_existing(release, manifest, owner=owner)
            return release
        for directory, _, files in os.walk(incoming, topdown=False):
            for name in files:
                item = Path(directory) / name
                relative = item.relative_to(incoming).as_posix()
                os.chmod(item, 0o555 if relative in BINARIES or relative in ("browser/start.sh", "browser/healthcheck.sh") else 0o444)
            os.chmod(directory, 0o555)
        os.rename(incoming, release)
        sync_directory(releases)
        return release
    finally:
        if incoming.exists():
            shutil.rmtree(incoming)


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


@dataclasses.dataclass(frozen=True)
class Paths:
    root: Path = Path("/opt/private-gateway")
    environment: Path = Path("/etc/private-gateway/gateway.env")
    backups: Path = Path("/var/backups/private-gateway")
    xray_config: Path = Path("/var/lib/private-gateway/xray.json")
    owner: int = 0

    @property
    def current(self):
        return self.root / "current"

    @property
    def releases(self):
        return self.root / "releases"


class Runtime:
    def command(self, args, *, timeout=120, stdout=subprocess.DEVNULL, env=None):
        try:
            result = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=stdout,
                                    stderr=subprocess.DEVNULL, timeout=timeout, check=False,
                                    env=ENV if env is None else env, cwd="/")
        except (OSError, subprocess.TimeoutExpired) as error:
            raise DeploymentError("Service operation failed or timed out") from error
        if result.returncode:
            # Uploaded application output and subprocess stderr can contain keys.
            # Keep them out of SSH/Actions logs; administrator inspects locally.
            fail("Service operation failed (exit status %d)" % result.returncode)

    def active(self, unit):
        result = subprocess.run(["/usr/bin/systemctl", "is-active", "--quiet", unit],
                                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, timeout=20, env=ENV, cwd="/")
        return result.returncode == 0

    def service(self, action, unit):
        self.command(["/usr/bin/systemctl", action, unit])

    def gateway(self, release, environment, binary, *args):
        # systemd reads the protected file. Credentials never become CLI arguments.
        self.command([
            "/usr/bin/systemd-run", "--quiet", "--wait", "--collect",
            "--property=User=gateway", "--property=Group=gateway",
            "--property=EnvironmentFile=" + str(environment),
            "--property=WorkingDirectory=" + str(release),
            "--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes",
            "--property=ProtectSystem=strict", "--property=ProtectHome=yes",
            "--property=ReadWritePaths=/var/lib/private-gateway",
            "--property=RuntimeMaxSec=270", "--property=TimeoutStopSec=10",
            "--property=UMask=0077", "--property=StandardOutput=null",
            "--property=StandardError=null", "--", str(release / "bin" / binary), *args,
        ], timeout=300)

    def browser_command(self, *args, timeout=120, executable="/usr/bin/podman"):
        worker = pwd.getpwnam("gateway-browser")
        self.command([
            "/usr/sbin/runuser", "-u", "gateway-browser", "--",
            "/usr/bin/env", "-i", "PATH=" + ENV["PATH"], "HOME=" + worker.pw_dir,
            "XDG_RUNTIME_DIR=/run/user/" + str(worker.pw_uid),
            "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + str(worker.pw_uid) + "/bus",
            executable, *args,
        ], timeout=timeout)

    def build_browser(self, release):
        image = "localhost/private-gateway-browser:" + release.name
        self.browser_command("build", "--format", "docker", "--tag", image, "--file",
                             str(release / "browser" / "Containerfile"),
                             str(release / "browser"), timeout=1800)
        # Keep an active browser session intact while testing the new image.
        name = "private-gateway-release-test-" + release.name
        try:
            self.browser_command(
                "run", "--detach", "--pull=never", "--name", name,
                "--user", "10001:10001", "--cap-drop=ALL",
                "--security-opt=no-new-privileges", "--read-only",
                "--memory=1024m", "--memory-swap=1024m", "--cpus=2", "--pids-limit=256",
                "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=256m,mode=1777",
                "--tmpfs", "/run:rw,nosuid,nodev,noexec,size=16m,mode=755",
                "--tmpfs", "/home/browser:rw,nosuid,nodev,noexec,size=128m,mode=1777",
                "--shm-size=256m", "--network=slirp4netns:allow_host_loopback=false",
                "--publish", "127.0.0.1:6081:6080/tcp", image,
            )
            deadline = time.monotonic() + 60
            while True:
                try:
                    self.browser_command("healthcheck", "run", name, timeout=10)
                    self.browser_command(str(release / "browser" / "smoke.py"), "--port", "6081",
                                         timeout=15, executable="/usr/bin/python3")
                    break
                except DeploymentError:
                    if time.monotonic() >= deadline:
                        fail("New browser image failed its health and noVNC checks")
                    time.sleep(1)
        finally:
            self.browser_command("rm", "--force", "--ignore", name)

    def activate_browser(self, release):
        self.browser_command("tag", "localhost/private-gateway-browser:" + release.name,
                             "localhost/private-gateway-browser:0.1")

    def backup_database(self, output):
        self.command([
            "/usr/sbin/runuser", "-u", "postgres", "--", "/usr/bin/pg_dump",
            "--format=custom", "--dbname=gateway", "--no-password",
        ], stdout=output, timeout=300)

    def ready(self):
        # Ignore proxy environment variables for the fixed local health endpoint.
        client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            try:
                with client.open("http://127.0.0.1:8080/readyz", timeout=2) as response:
                    if response.status == 200 and json.loads(response.read(4096)).get("status") == "ready":
                        return
            except (OSError, ValueError, urllib.error.URLError):
                pass
            time.sleep(1)
        fail("Gateway readiness check failed")


class Deployer:
    def __init__(self, paths=None, runtime=None):
        self.paths = paths or Paths()
        self.runtime = runtime or Runtime()

    def previous_release(self):
        current = self.paths.current
        if not current.is_symlink():
            if current.exists():
                fail("Current release must be a symlink")
            return None
        target = Path(os.readlink(current))
        if not target.is_absolute():
            target = current.parent / target
        if target.parent != self.paths.releases or not VERSION.fullmatch(target.name):
            fail("Current symlink points outside releases")
        trusted_directory(target, owner=self.paths.owner)
        return target

    def switch(self, release):
        temporary = self.paths.root / ".current-next"
        if temporary.exists() or temporary.is_symlink():
            temporary.unlink()
        if release is None:
            self.paths.current.unlink(missing_ok=True)
        else:
            os.symlink(str(release), temporary)
            os.replace(temporary, self.paths.current)
        sync_directory(self.paths.root)

    def backup(self, version):
        trusted_directory(self.paths.backups, 0o700, owner=self.paths.owner)
        if self.paths.backups.stat().st_mode & 0o077:
            fail("Backup directory must be accessible only to root")
        timestamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        directory = Path(tempfile.mkdtemp(prefix=timestamp + "-" + version + "-", dir=self.paths.backups))
        database = directory / "database.dump.partial"
        try:
            with database.open("xb") as output:
                os.chmod(database, 0o600)
                self.runtime.backup_database(output)
                output.flush()
                os.fsync(output.fileno())
            if database.stat().st_size < 5:
                fail("Database backup is empty")
            with database.open("rb") as source:
                if source.read(5) != b"PGDMP":
                    fail("Database backup is not a custom-format PostgreSQL dump")
            database.rename(directory / "database.dump")
            with trusted_file(self.paths.environment, allow_group_read=True, owner=self.paths.owner) as source:
                with (directory / "gateway.env").open("xb") as target:
                    os.chmod(target.name, 0o600)
                    shutil.copyfileobj(source, target)
                    target.flush()
                    os.fsync(target.fileno())
            if self.paths.xray_config.exists():
                # Config is legitimately gateway-owned. Copy bytes, never follow a
                # link from the writable application state directory as root.
                fd = os.open(self.paths.xray_config, os.O_RDONLY | os.O_NOFOLLOW)
                with os.fdopen(fd, "rb") as source:
                    info = os.fstat(source.fileno())
                    if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_MANIFEST:
                        fail("Invalid Xray configuration backup source")
                    with (directory / "xray.json").open("xb") as target:
                        os.chmod(target.name, 0o600)
                        bounded_copy(source, target, MAX_MANIFEST)
                        target.flush()
                        os.fsync(target.fileno())
            sync_directory(directory)
            sync_directory(self.paths.backups)
            return directory
        except BaseException:
            shutil.rmtree(directory)
            raise

    def restore_xray(self, backup, was_active, previous):
        old_config = backup / "xray.json"
        if old_config.exists() and previous is not None:
            worker = pwd.getpwnam("gateway")
            fd, name = tempfile.mkstemp(prefix=".restore-", dir=self.paths.xray_config.parent)
            try:
                with os.fdopen(fd, "wb") as output:
                    output.write(old_config.read_bytes())
                    os.fchown(output.fileno(), worker.pw_uid, worker.pw_gid)
                    output.flush()
                    os.fsync(output.fileno())
                os.replace(name, self.paths.xray_config)
                sync_directory(self.paths.xray_config.parent)
            finally:
                with contextlib.suppress(FileNotFoundError):
                    os.unlink(name)
            if was_active:
                self.runtime.service("restart", "private-gateway-xray.service")
        else:
            self.runtime.service("stop", "private-gateway-xray.service")

    def deploy(self, release):
        previous = self.previous_release()
        with trusted_file(self.paths.environment, allow_group_read=True, owner=self.paths.owner):
            pass
        # A failed image build cannot interrupt the current API or data plane.
        self.runtime.build_browser(release)
        xray_was_active = self.runtime.active("private-gateway-xray.service")
        api_was_active = self.runtime.active("gateway-api.service")
        self.runtime.service("stop", "gateway-api.service")
        try:
            backup = self.backup(release.name)
        except BaseException:
            # No migration or switch has happened, so the previous API is safe.
            if api_was_active:
                self.runtime.service("start", "gateway-api.service")
            raise
        print("Database and configuration backup: " + str(backup), flush=True)
        switched = False
        try:
            self.runtime.gateway(release, self.paths.environment, "gateway-api", "migrate")
            self.switch(release)
            switched = True
            self.runtime.service("restart", "gateway-api.service")
            self.runtime.ready()
            self.runtime.gateway(release, self.paths.environment, "gatewayctl", "config", "apply")
            if not self.runtime.active("private-gateway-xray.service"):
                fail("Xray is not active after configuration apply")
            self.runtime.ready()
            # This is the last operation: a failed deployment must not change the
            # image selected for future browser sessions.
            self.runtime.activate_browser(release)
        except BaseException as error:
            recovery = []
            try:
                self.runtime.service("stop", "gateway-api.service")
            except Exception:
                recovery.append("API could not be stopped")
            if switched:
                try:
                    self.switch(previous)
                    self.restore_xray(backup, xray_was_active, previous)
                except Exception:
                    recovery.append("previous Xray could not be restored")
            message = ("Deployment failed; API left stopped because database migrations may have changed the schema. "
                       "Database was NOT restored automatically. Review backup " + str(backup))
            if recovery:
                message += "; manual recovery required: " + "; ".join(recovery)
            raise DeploymentError(message) from error
        print("Deployment verified: " + release.name, flush=True)


@contextlib.contextmanager
def deployment_lock():
    fd = os.open("/run/lock/private-gateway-deploy.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(fd)
        if info.st_uid != 0 or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_mode & 0o077:
            fail("Unsafe deployment lock file")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            fail("Another deployment is already running")
        yield
    finally:
        os.close(fd)


def main(argv=None):
    args = sys.argv[1:] if argv is None else argv
    os.umask(0o077)
    if args not in ([], ["--stage"], ["--check"]):
        fail("Usage: private-gateway-deploy [--stage|--check] < release.tar.gz")
    if args and os.environ.get("SUDO_USER") == "gateway-deploy":
        fail("Deployment user cannot invoke administrative modes")
    if args == ["--check"]:
        with tempfile.TemporaryDirectory(prefix="private-gateway-check-") as directory:
            manifest = unpack_verified(sys.stdin.buffer, Path(directory))
            print(manifest["version"])
        return
    if os.geteuid() != 0:
        fail("Deployment requires the installed root helper")
    paths = Paths()
    # Check each writable ancestor before using fixed paths as root.
    trusted_directory(Path("/opt"))
    trusted_directory(paths.root)
    with deployment_lock():
        release = stage_release(sys.stdin.buffer, paths.releases)
        if args == ["--stage"]:
            print(release)
        else:
            Deployer(paths).deploy(release)


if __name__ == "__main__":
    try:
        main()
    except (DeploymentError, OSError) as error:
        print("Deployment error: " + str(error), file=sys.stderr)
        sys.exit(1)
