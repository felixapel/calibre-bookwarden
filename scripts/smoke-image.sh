#!/bin/sh
# Starts the image with the production hardening flags next to a throwaway
# Postgres, both at once, and checks that bookwarden migrates, prints a setup
# code, runs as UID 10001 and turns healthy.
set -eu
image=$1
run=bw-smoke-$$
net=$run-net
cleanup() {
  docker rm -f "$run-app" "$run-pg" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker image inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$image" | grep -q '^GOMEMLIMIT=' ||
  { echo "smoke: the image sets no GOMEMLIMIT" >&2; exit 1; }
for label in source version revision; do
  value=$(docker image inspect -f "{{index .Config.Labels \"org.opencontainers.image.$label\"}}" "$image")
  [ -n "$value" ] || { echo "smoke: the image has no org.opencontainers.image.$label label" >&2; exit 1; }
done
# The binary must carry the revision the label claims: a wrong -X path is
# ignored by the linker, and the deploy would then refuse every release.
revision=$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image")
if [ "$revision" != unknown ]; then
  short=$(printf '%s' "$revision" | cut -c1-12)
  out=$(docker run --rm --network none --entrypoint /usr/local/bin/bookwarden "$image" version)
  case $out in
    *"($short)"*) ;;
    *) echo "smoke: '$out' does not carry the revision $short" >&2; exit 1 ;;
  esac
fi
docker network create "$net" >/dev/null
docker run -d --name "$run-app" --network "$net" \
  --read-only --tmpfs /tmp:size=1536m,mode=1777 --memory 2g --pids-limit 256 \
  --cap-drop ALL --security-opt no-new-privileges \
  -e BOOKWARDEN_DATABASE_URL='postgres://bookwarden:smoke@postgres:5432/bookwarden?sslmode=disable' \
  -e BOOKWARDEN_CALIBRE_URL=http://calibre:8081 \
  -e BOOKWARDEN_CALIBRE_USER=bookwarden -e BOOKWARDEN_CALIBRE_PASSWORD=unused \
  -e BOOKWARDEN_ALLOWED_HOSTS=localhost \
  "$image" >/dev/null
docker run -d --name "$run-pg" --network "$net" --network-alias postgres \
  -e POSTGRES_USER=bookwarden -e POSTGRES_PASSWORD=smoke -e POSTGRES_DB=bookwarden \
  postgres:18-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873 >/dev/null
i=0
until [ "$(docker inspect --format '{{.State.Health.Status}}' "$run-app")" = healthy ]; do
  i=$((i + 1))
  if [ "$i" -ge 60 ] || [ "$(docker inspect --format '{{.State.Running}}' "$run-app")" != true ]; then
    docker logs "$run-app" >&2
    echo "smoke: bookwarden did not become healthy" >&2
    exit 1
  fi
  sleep 2
done
docker logs "$run-app" 2>&1 | grep -q '"setup_code"' || { echo "smoke: no setup code in the log" >&2; exit 1; }
docker exec "$run-app" bookwarden healthcheck
docker logs "$run-app" 2>&1 | grep -q 'OCR self-check passed' || { docker logs "$run-app" >&2; echo "smoke: the OCR self-check did not pass at startup" >&2; exit 1; }
docker exec "$run-app" bookwarden ocr-check
# The OCR tools run as the same UID; bookwarden's environment holds its
# secrets and must stay out of their reach (ADR-018).
if docker exec "$run-app" cat /proc/1/environ >/dev/null 2>&1; then
  echo "smoke: bookwarden's environment is readable by its own UID" >&2
  exit 1
fi
[ "$(docker exec "$run-app" id -u)" = 10001 ] || { echo "smoke: not running as UID 10001" >&2; exit 1; }
echo "smoke: OK"
