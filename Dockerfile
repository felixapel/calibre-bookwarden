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
COPY pyproject.toml uv.lock README.md ./
COPY src ./src
RUN --mount=type=cache,target=/root/.cache/uv uv sync --frozen --no-dev --no-editable

FROM python:3.12.13-slim-bookworm@sha256:8a7e7cc04fd3e2bd787f7f24e22d5d119aa590d429b50c95dfe12b3abe52f48b AS runtime
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    DEBIAN_FRONTEND=noninteractive \
    PATH=/opt/venv/bin:$PATH \
    BOOKAUDIT_REPOSITORY_ROOT=/app \
    BOOKAUDIT_STATIC_DIR=/app/static \
    BOOKAUDIT_LIBRARY_PATH=/library \
    BOOKAUDIT_DB_PATH=/state/bookaudit.db \
    BOOKAUDIT_ARTIFACTS_DIR=/artifacts \
    HOME=/tmp/bookaudit-home \
    XDG_CACHE_HOME=/tmp/bookaudit-home/.cache \
    XDG_CONFIG_HOME=/tmp/bookaudit-home/.config
WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
      calibre \
      curl \
      ghostscript \
      qpdf \
      tesseract-ocr \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 bookaudit \
    && useradd --uid 10001 --gid bookaudit --no-create-home --shell /usr/sbin/nologin bookaudit \
    && mkdir -p /library /state /artifacts /config \
    && chown -R bookaudit:bookaudit /state /artifacts

COPY --from=python-builder /opt/venv /opt/venv
COPY --from=frontend-builder /webui/dist /app/static
COPY alembic.ini /app/alembic.ini
COPY migrations /app/migrations
COPY config /app/config

USER 10001:10001
ENTRYPOINT ["bookaudit"]
CMD ["web", "--host", "0.0.0.0", "--port", "8080"]
