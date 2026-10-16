# Toolchain for `bash scripts/dev.sh test`: the same Go, Node.js, Python, Xray and sqlc on a Windows PC,
# a Linux host and in CI. scripts/dev.sh builds it from the content of the next commit, so ignored
# local files such as .env never reach the image and Windows checkouts cannot leak CRLF into scripts.
FROM docker.io/library/node:22.23.3-trixie-slim@sha256:154ba2f4d6fec323d28e4f4bb86bba4677f1223391a1979cf521304e03a98dfa AS node

FROM docker.io/library/golang:1.26.9-trixie@sha256:f06b7122f888680048afb247c4e5f49c0069eb6c4c1ccae0f3f4834527a8d22c

RUN apt-get update \
  && apt-get install -y --no-install-recommends python3 \
  && rm -rf /var/lib/apt/lists/*

# Node.js 22 as in CI, from the official image built on the same Debian release.
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
  && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# Pinned Xray, sqlc and actionlint releases, verified by SHA-256 like scripts/setup-cloud.sh.
COPY scripts/install-xray.sh /usr/local/share/private-gateway/install-xray.sh
RUN bash /usr/local/share/private-gateway/install-xray.sh /opt/xray
ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) sqlc=468aecee071bfe55e97fcbcac52ea0208eeca444f67736f3b8f0f3d6a106132e \
             actionlint=8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8 ;; \
      arm64) sqlc=dd9ab43b022ba3b3402054f99d7ae6e5efea33c949e869c3c66b214415e0c82d \
             actionlint=325e971b6ba9bfa504672e29be93c24981eeb1c07576d730e9f7c8805afff0c6 ;; \
      *) echo "Unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
  && curl -fsSL --proto '=https' --tlsv1.2 -o /tmp/sqlc.tar.gz \
    "https://github.com/sqlc-dev/sqlc/releases/download/v1.30.0/sqlc_1.30.0_linux_${TARGETARCH}.tar.gz" \
  && curl -fsSL --proto '=https' --tlsv1.2 -o /tmp/actionlint.tar.gz \
    "https://github.com/rhysd/actionlint/releases/download/v1.7.12/actionlint_1.7.12_linux_${TARGETARCH}.tar.gz" \
  && printf '%s  %s\n' "$sqlc" /tmp/sqlc.tar.gz "$actionlint" /tmp/actionlint.tar.gz | sha256sum -c - \
  && tar -xzf /tmp/sqlc.tar.gz -C /usr/local/bin sqlc \
  && tar -xzf /tmp/actionlint.tar.gz -C /usr/local/bin actionlint \
  && rm /tmp/sqlc.tar.gz /tmp/actionlint.tar.gz

# CI runs the checks as an unprivileged user, and so does this image.
RUN useradd --create-home --uid 10001 checks
USER checks
WORKDIR /home/checks/src
# The Go build cache is a shared Docker volume (deploy/checks/compose.yml); this directory gives
# a fresh volume the right owner.
RUN mkdir -p /home/checks/.cache/go-build

# Dependency layers stay cached until go.sum or package-lock.json changes.
COPY --chown=checks:checks go.mod go.sum ./
RUN go mod download
COPY --chown=checks:checks web/package.json web/package-lock.json web/
RUN cd web && npm ci --no-audit --no-fund && rm -rf /home/checks/.npm
COPY --chown=checks:checks . .

ENV PRIVATE_GATEWAY_REAL_XRAY_BINARY=/opt/xray/xray
CMD ["bash", "scripts/checks.sh"]
