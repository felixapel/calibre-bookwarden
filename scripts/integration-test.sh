#!/bin/sh
# Runs a whole check against the disposable Calibre in test/calibre and proves
# it is read-only: a static test binary runs inside the Calibre container,
# reads the library through the content server, stores evidence in a
# throwaway Postgres, and hashes every library file before and after. Then a
# second binary pins Calibre's write behaviour and proves fixing, the stale
# check, readback, restore and undo with the writer account.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
# CI jobs share the host Docker daemon, so names carry the run id too.
run=bw-it-${GITHUB_RUN_ID:-local}-$$
net=$run-net
tmp=$(mktemp -d)
reader_password=reader-pass # disposable test container only
writer_password=writer-pass
cleanup() {
  docker rm -f "$run-calibre" "$run-pg" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

docker build -q -t bookwarden-test-calibre:9.16.0 "$root/test/calibre" >/dev/null
docker network create "$net" >/dev/null
docker run -d --name "$run-pg" --network "$net" --network-alias postgres \
  -e POSTGRES_PASSWORD=integration \
  postgres:18-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873 >/dev/null
docker run -d --name "$run-calibre" --network "$net" \
  -e BW_READER_PASSWORD="$reader_password" -e BW_WRITER_PASSWORD="$writer_password" \
  bookwarden-test-calibre:9.16.0 >/dev/null

(cd "$root" && CGO_ENABLED=0 go test -c -tags calibre -o "$tmp/itest" ./internal/verify/)
(cd "$root" && CGO_ENABLED=0 go test -c -tags calibre -o "$tmp/fixtest" ./internal/fix/)
(cd "$root" && CGO_ENABLED=0 go test -c -tags calibre -o "$tmp/agenttest" ./internal/agent/)

i=0
until docker exec "$run-pg" pg_isready -q -U postgres; do
  i=$((i + 1))
  if [ "$i" -ge 60 ]; then
    docker logs "$run-pg" >&2
    echo "integration: postgres did not come up" >&2
    exit 1
  fi
  sleep 1
done
i=0
until docker exec "$run-calibre" curl --silent --fail --output /dev/null --digest \
  -u "reader:$reader_password" http://127.0.0.1:8081/ajax/library-info; do
  i=$((i + 1))
  if [ "$i" -ge 90 ]; then
    docker logs "$run-calibre" >&2
    echo "integration: calibre-server did not come up" >&2
    exit 1
  fi
  sleep 2
done

docker cp "$tmp/itest" "$run-calibre:/tmp/itest" >/dev/null
docker cp "$tmp/fixtest" "$run-calibre:/tmp/fixtest" >/dev/null
docker cp "$tmp/agenttest" "$run-calibre:/tmp/agenttest" >/dev/null
# run_test BINARY PATTERN NAME...: runs the tests and requires each NAME to
# pass and none to be skipped.
run_test() {
  bin=$1 pattern=$2
  shift 2
  status=0
  docker exec \
    -e BOOKWARDEN_TEST_DATABASE_URL='postgres://postgres:integration@postgres:5432/postgres?sslmode=disable' \
    -e BW_IT_CALIBRE_URL=http://127.0.0.1:8081 \
    -e BW_IT_READER_PASSWORD="$reader_password" \
    -e BW_IT_WRITER_PASSWORD="$writer_password" \
    -e BW_IT_LIBRARY_DIR=/library \
    "$run-calibre" "$bin" -test.run "$pattern" -test.v -test.count=1 \
    >"$tmp/out" 2>&1 || status=$?
  cat "$tmp/out"
  for name in "$@"; do
    if [ "$status" -ne 0 ] || ! grep -q -- "--- PASS: $name " "$tmp/out"; then
      echo "integration: $name did not pass" >&2
      exit 1
    fi
  done
  if grep -q -- '--- SKIP' "$tmp/out"; then
    echo "integration: a test was skipped" >&2
    exit 1
  fi
}
# The read-only proof runs first, on the untouched library.
run_test /tmp/itest '^TestReadOnlyRunOnRealCalibre$' TestReadOnlyRunOnRealCalibre
run_test /tmp/fixtest '^(TestCalibreWriteBehaviour|TestFixesOnRealCalibre|TestCoverOnRealCalibre|TestSeriesAndCoverWrites)$' TestCalibreWriteBehaviour TestFixesOnRealCalibre TestCoverOnRealCalibre TestSeriesAndCoverWrites
run_test /tmp/agenttest '^TestAgentFixingOnRealCalibre$' TestAgentFixingOnRealCalibre
echo "integration: OK"
