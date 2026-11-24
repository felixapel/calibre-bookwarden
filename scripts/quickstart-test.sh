#!/bin/sh
# Follows the README's Docker Compose quick start in a temporary directory:
# copies deploy/compose/, writes .env from .env.example by replacing every
# change-me with a test value, starts the stack against the disposable
# Calibre in test/calibre, and requires /readyz and the setup form to answer.
# Also checks that every .env.example variable is documented in a README
# table and that the Unraid template is well-formed XML.
#
#   scripts/quickstart-test.sh [image]
#
# Without an image it builds one from this checkout and removes it after.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
image=${1:-}
# CI jobs share the host Docker daemon, so names carry the run id too.
run=bw-qs-${GITHUB_RUN_ID:-local}-$$
net=$run-calibre
tmp=$(mktemp -d)
built=
reader_password=reader-pass # disposable test container only
start=$(date +%s)
cleanup() {
  if [ -f "$tmp/compose/.env" ]; then
    docker compose --project-directory "$tmp/compose" -p "$run" \
      -f "$tmp/compose/docker-compose.yml" -f "$tmp/compose/quickstart.override.yml" \
      down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  docker rm -f "$run-calibre" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  if [ -n "$built" ]; then docker image rm -f "$built" >/dev/null 2>&1 || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT
# dash runs the EXIT trap on exit only; a caught signal exits through it.
trap 'exit 1' INT TERM HUP
fail() {
  echo "quickstart: $*" >&2
  exit 1
}

template=$root/deploy/unraid/bookwarden.xml
if command -v xmllint >/dev/null 2>&1; then
  xmllint --noout "$template" || fail "the Unraid template is not well-formed XML"
else
  python3 -c 'import sys, xml.etree.ElementTree as e; e.parse(sys.argv[1])' "$template" ||
    fail "the Unraid template is not well-formed XML"
fi

# Every variable a user sets in .env has a row in the README's Quick start
# table or its Environment reference.
awk '/^## /{on = ($0 == "## Quick start" || $0 == "## Environment reference")} on' \
  "$root/README.md" >"$tmp/tables"
sed -n 's/^\([A-Z_][A-Z0-9_]*\)=.*/\1/p' "$root/deploy/compose/.env.example" >"$tmp/names"
while IFS= read -r name; do
  grep -q "^| \`$name\` |" "$tmp/tables" ||
    fail "$name is in .env.example but in neither the README's Quick start table nor its Environment reference"
done <"$tmp/names"

cp -R "$root/deploy/compose" "$tmp/compose"
cd "$tmp/compose"
while IFS= read -r line; do
  case $line in
    '#'*) ;;
    *change-me*)
      name=${line%%=*}
      case $name in
        POSTGRES_PASSWORD) value=quickstart1 ;;
        BOOKWARDEN_CALIBRE_URL) value=http://calibre:8081 ;;
        BOOKWARDEN_CALIBRE_PASSWORD) value=$reader_password ;;
        BOOKWARDEN_ALLOWED_HOSTS) value=bookwarden ;;
        *) fail "$name says change-me but this test has no value for it" ;;
      esac
      line=$name=$value
      ;;
  esac
  printf '%s\n' "$line"
done <.env.example >.env
if grep -v '^#' .env | grep -q change-me; then
  fail "a change-me is left in the .env this test wrote"
fi
# Not a change-me: the test Calibre's account has its own name, and an
# ephemeral loopback port can't collide with anything on the host.
sed -i -e 's/^BOOKWARDEN_CALIBRE_USER=.*/BOOKWARDEN_CALIBRE_USER=reader/' \
  -e 's/^BOOKWARDEN_PORT=.*/BOOKWARDEN_PORT=127.0.0.1:/' .env

if [ -z "$image" ]; then
  image=bookwarden:$run
  docker build -q --build-arg VERSION=quickstart -t "$image" "$root" >/dev/null
  built=$image
fi
# restart: "no", so a run killed before its cleanup leaves no containers that
# come back with the daemon.
cat >quickstart.override.yml <<EOF
services:
  bookwarden:
    image: $image
    restart: "no"
    networks:
      - calibre
  postgres:
    restart: "no"
networks:
  calibre:
    name: $net
    external: true
EOF

docker build -q -t bookwarden-test-calibre:9.16.0 "$root/test/calibre" >/dev/null
docker network create "$net" >/dev/null
docker run -d --name "$run-calibre" --network "$net" --network-alias calibre \
  -e BW_READER_PASSWORD="$reader_password" -e BW_WRITER_PASSWORD=unused-writer-pass \
  bookwarden-test-calibre:9.16.0 >/dev/null
i=0
until docker exec "$run-calibre" curl --silent --fail --output /dev/null --digest \
  -u "reader:$reader_password" http://127.0.0.1:8081/ajax/library-info; do
  i=$((i + 1))
  if [ "$i" -ge 90 ]; then
    docker logs "$run-calibre" >&2
    fail "calibre-server did not come up"
  fi
  sleep 2
done

compose() {
  docker compose -p "$run" -f docker-compose.yml -f quickstart.override.yml "$@"
}
if ! compose up -d --wait --wait-timeout 180; then
  compose logs >&2 || true
  fail "docker compose up --wait failed"
fi
compose logs bookwarden 2>&1 | grep -q '"setup_code"' || fail "no setup code in the log"

# get PATH FILE: the HTTP status of http://bookwarden:8080/PATH, fetched from
# the Calibre container, which shares a network with the service.
get() {
  docker exec "$run-calibre" curl --silent --max-time 10 --output - \
    --write-out '\n%{http_code}' "http://bookwarden:8080$1" >"$2" || true
  tail -n 1 "$2"
}
i=0
until [ "$(get /readyz "$tmp/readyz")" = 200 ]; do
  i=$((i + 1))
  if [ "$i" -ge 30 ]; then
    cat "$tmp/readyz" >&2
    compose logs bookwarden >&2 || true
    fail "/readyz did not answer 200"
  fi
  sleep 2
done
[ "$(get /setup "$tmp/setup")" = 200 ] || fail "/setup did not answer 200"
grep -q 'name="code"' "$tmp/setup" || fail "/setup has no setup form"

echo "quickstart: OK in $(($(date +%s) - start)) s"
