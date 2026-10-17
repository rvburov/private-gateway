#!/usr/bin/env bash
# Установить закреплённый официальный выпуск Xray, не меняя системные пакеты.
# Хеши SHA-256 ниже взяты из опубликованных файлов .zip.dgst этого выпуска.
set -euo pipefail

version=26.3.27
destination="${1:-/workspace/.toolchains/xray}"
if [[ "$(uname -s)" != Linux ]]; then
  printf '%s\n' 'This installer supports Linux only.' >&2
  exit 1
fi
case "$(uname -m)" in
  x86_64|amd64)
    archive=Xray-linux-64.zip
    digest=23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae
    ;;
  aarch64|arm64)
    archive=Xray-linux-arm64-v8a.zip
    digest=4d30283ae614e3057f730f67cd088a42be6fdf91f8639d82cb69e48cde80413c
    ;;
  *)
    printf 'Unsupported CPU architecture: %s\n' "$(uname -m)" >&2
    exit 1
    ;;
esac
for prerequisite in curl sha256sum python3; do
  command -v "$prerequisite" >/dev/null || {
    printf 'Required command is missing: %s\n' "$prerequisite" >&2
    exit 1
  }
done

temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
  "https://github.com/XTLS/Xray-core/releases/download/v${version}/${archive}" \
  --output "${temporary}/${archive}"
printf '%s  %s\n' "$digest" "${temporary}/${archive}" | sha256sum --check --status
# Распаковать только исполняемый файл: созданная конфигурация не использует geodata.
python3 - "${temporary}/${archive}" "$temporary" <<'PY'
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1]) as archive:
    archive.extract("xray", sys.argv[2])
PY
mkdir -p "$destination"
install -m 0755 "$temporary/xray" "$destination/xray"
"$destination/xray" version
