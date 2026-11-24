#!/bin/sh
# Builds the disposable Calibre image, starts it and checks digest login.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
name=bw-calibre-smoke-$$
reader_password=reader-pass # disposable test container only
info=$(mktemp)
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -f "$info"' EXIT
docker build -q -t bookwarden-test-calibre:9.16.0 "$here" >/dev/null
docker run -d --rm --name "$name" -p 127.0.0.1::8081 \
  -e BW_READER_PASSWORD="$reader_password" -e BW_WRITER_PASSWORD=writer-pass \
  bookwarden-test-calibre:9.16.0 >/dev/null
port=$(docker port "$name" 8081/tcp | head -n 1 | sed 's/.*://')
i=0
until curl --silent --fail --digest -u "reader:$reader_password" \
  "http://127.0.0.1:$port/ajax/library-info" >"$info"; do
  i=$((i + 1))
  if [ "$i" -ge 90 ]; then
    docker logs "$name" >&2
    echo "calibre-server did not come up" >&2
    exit 1
  fi
  sleep 2
done
grep -q '"default_library"' "$info"
anonymous=$(curl --silent --output /dev/null --write-out '%{http_code}' \
  "http://127.0.0.1:$port/ajax/library-info")
[ "$anonymous" = 401 ] || { echo "anonymous request got $anonymous, want 401" >&2; exit 1; }
echo "calibre test server OK on port $port"
