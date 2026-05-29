# Stage 1: Build the React frontend
FROM node:20-slim AS frontend-builder
WORKDIR /webui
COPY webui/package*.json ./
RUN npm install
COPY webui/ ./
RUN npm run build

# Stage 2: Final image
FROM python:3.12-slim

SHELL ["/bin/bash", "-c"]

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    DEBIAN_FRONTEND=noninteractive

WORKDIR /app

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    calibre \
    tesseract-ocr \
    libtesseract-dev \
    ghostscript \
    qpdf \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Install Python dependencies
COPY pyproject.toml README.md /app/
COPY src /app/src
RUN pip install --upgrade pip && \
    pip install ".[dev]"

# Copy frontend build artifacts to a stable location
COPY --from=frontend-builder /webui/dist /app/static
ENV BOOKAUDIT_STATIC_DIR=/app/static

# Create default directories
RUN mkdir -p /library /state /artifacts /config

# Set default environment variables
ENV BOOKAUDIT_LIBRARY_PATH=/library \
    BOOKAUDIT_DB_PATH=/state/bookaudit.db \
    BOOKAUDIT_ARTIFACTS_DIR=/artifacts

# Default entrypoint
ENTRYPOINT ["bookaudit"]

# Default command starts the web server
CMD ["web", "--host", "0.0.0.0", "--port", "8080"]
