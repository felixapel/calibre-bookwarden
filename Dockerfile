# syntax=docker/dockerfile:1.7
FROM node:20.19.4-bookworm-slim@sha256:6db5e436948af8f0244488a1f658c2c8e55a3ae51ca2e1686ed042be8f25f70a AS frontend-builder
WORKDIR /webui
COPY webui/package.json webui/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY webui/ ./
RUN npm run build

FROM ghcr.io/astral-sh/uv:0.11.15@sha256:e590846f4776907b254ac0f44b5b380347af5d90d668138ca7938d1b0c2f98d3 AS uv

FROM python:3.12.13-slim-bookworm@sha256:8a7e7cc04fd3e2bd787f7f24e22d5d119aa590d429b50c95dfe12b3abe52f48b AS python-builder
COPY --from=uv /uv /usr/local/bin/uv
ENV UV_PROJECT_ENVIRONMENT=/opt/venv \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy
WORKDIR /build
COPY pyproject.toml uv.lock README.md LICENSE ./
COPY src ./src
RUN --mount=type=cache,target=/root/.cache/uv uv sync --frozen --no-dev --no-editable

FROM python-builder AS python-builder-legacy
# Some optional SDKs embed public test keys in their source. Do not compile the
# legacy-only closure into opaque bytecode that secret scanners cannot
# contextualize; the runtime also has PYTHONDONTWRITEBYTECODE enabled.
ENV UV_COMPILE_BYTECODE=0
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --no-editable --extra legacy --extra mcp --extra ingest

FROM python:3.12.13-slim-bookworm@sha256:8a7e7cc04fd3e2bd787f7f24e22d5d119aa590d429b50c95dfe12b3abe52f48b AS runtime-common
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    DEBIAN_FRONTEND=noninteractive \
    PATH=/opt/venv/bin:/usr/local/bin:$PATH \
    BOOKAUDIT_REPOSITORY_ROOT=/app \
    BOOKAUDIT_STATIC_DIR=/app/static \
    BOOKAUDIT_LIBRARY_PATH=/library \
    BOOKAUDIT_ARTIFACTS_DIR=/artifacts \
    HOME=/tmp/bookaudit-home \
    XDG_CACHE_HOME=/tmp/bookaudit-home/.cache \
    XDG_CONFIG_HOME=/tmp/bookaudit-home/.config
WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      ghostscript \
      qpdf \
      tesseract-ocr \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 bookaudit \
    && useradd --uid 10001 --gid bookaudit --no-create-home --shell /usr/sbin/nologin bookaudit \
    && mkdir -p /library /artifacts /config /app/scripts \
    && chown -R bookaudit:bookaudit /artifacts

COPY --from=frontend-builder /webui/dist /app/static
COPY alembic.ini /app/alembic.ini
COPY migrations /app/migrations
COPY config /app/config
COPY LICENSE /app/LICENSE

# Build the reviewed edge with patched Go and dependency versions while the
# upstream Caddy image catches up. The runtime contains only Caddy, trust roots,
# and media types; it has no third-party proxy plugins.
FROM golang@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS caddy-edge-builder
WORKDIR /build
COPY deploy/caddy/module/go.mod deploy/caddy/module/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download \
    && CGO_ENABLED=0 go build -mod=readonly -trimpath \
      -ldflags='-s -w -X github.com/caddyserver/caddy/v2.CustomVersion=v2.11.4-bookaudit-patched' \
      -o /usr/local/bin/caddy github.com/caddyserver/caddy/v2/cmd/caddy

FROM alpine@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS caddy-edge
ARG BOOKAUDIT_BUILD_REVISION=unknown
LABEL org.opencontainers.image.revision="$BOOKAUDIT_BUILD_REVISION"
RUN apk add --no-cache ca-certificates mailcap
COPY --from=caddy-edge-builder /usr/local/bin/caddy /usr/bin/caddy
ENTRYPOINT ["caddy"]
CMD ["run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]

# Certificate B remains an explicit, separately built image. It retains the
# compatibility CLI and Calibre binary but is never selected by default.
FROM runtime-common AS writer
ARG BOOKAUDIT_BUILD_REVISION=unknown
LABEL org.opencontainers.image.revision="$BOOKAUDIT_BUILD_REVISION"
ARG CALIBRE_VERSION=9.11.0
ARG CALIBRE_X86_64_SHA512=4b2250124e73b907dc84f30d413e095193735ffe3f933793a7d021885efbb37b2a92254e36c17e0a52c555729a7cd67c5229a1b62b9968baf61764463aeea47e
ENV PATH=/opt/calibre:$PATH
RUN apt-get update && apt-get install -y --no-install-recommends \
      libegl1 \
      libglx0 \
      libopengl0 \
      libxkbcommon0 \
      xz-utils \
    && rm -rf /var/lib/apt/lists/* \
    && curl --fail --location --proto '=https' --tlsv1.2 \
      "https://download.calibre-ebook.com/${CALIBRE_VERSION}/calibre-${CALIBRE_VERSION}-x86_64.txz" \
      --output /tmp/calibre.txz \
    && echo "${CALIBRE_X86_64_SHA512}  /tmp/calibre.txz" | sha512sum --check --strict \
    && mkdir -p /opt/calibre \
    && tar --extract --xz --file /tmp/calibre.txz --directory /opt/calibre \
    && rm /tmp/calibre.txz \
    && /opt/calibre/calibredb --version 2>&1 | grep -F "calibre ${CALIBRE_VERSION%.*}" \
    && mkdir -p /writer-artifacts /lab-library /lab-auth /credentials \
    && chown -R bookaudit:bookaudit /writer-artifacts /lab-library /lab-auth /credentials
COPY --from=python-builder-legacy /opt/venv /opt/venv
COPY --chmod=0755 scripts/calibredb_readonly_wrapper.py /usr/local/bin/calibredb
COPY --chmod=0755 scripts/disposable_calibre_fixture.py /app/scripts/disposable_calibre_fixture.py
USER 10001:10001
ENTRYPOINT ["bookaudit"]
CMD ["writer"]

# This is deliberately the final/default target: app and verifier contain no
# Calibre binary, LLM SDK, vector client, MCP server, watcher, or legacy WebUI.
FROM runtime-common AS certificate-a
ARG BOOKAUDIT_BUILD_REVISION=unknown
LABEL org.opencontainers.image.revision="$BOOKAUDIT_BUILD_REVISION"
COPY --from=python-builder /opt/venv /opt/venv
USER 10001:10001
ENTRYPOINT ["bookaudit-certificate-a"]
CMD ["web", "--host", "0.0.0.0", "--port", "8080"]
