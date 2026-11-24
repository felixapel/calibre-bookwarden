#!/usr/bin/env bash
# Self-contained test for restore-drill.sh: a stub docker goes first on PATH, logs its argv and
# returns canned answers. Dumps are files in a temp directory. Nothing real is touched.
#
#   scripts/restore-drill_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SCRIPT="$HERE/restore-drill.sh"
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
FAILS=0

mkdir -p "$ROOT/bin"
cat >"$ROOT/bin/docker" <<'STUB'
#!/usr/bin/env bash
echo "docker $*" >>"$STUB_LOG"
case "$*" in
  *"from pg_database where datname = "*) echo "${STUB_EXISTS:-0}" ;;
  *"pg_get_userbyid"*) echo bookwarden ;;
  *"sealed books"*) echo "16 runs, 51000 sealed books, 22 schema" ;;
  *"select count(*) from runs"*) [ -z "${STUB_RUNS_FAIL:-}" ] || exit 1; echo "${STUB_RUNS:-16}" ;;
  "inspect -f {{.Image}} "*) echo "sha256:runningimage" ;;
  "inspect -f {{range .Config.Env}}"*)
    printf 'TZ=Europe/Berlin\nBOOKWARDEN_DATABASE_URL=%s\n' "${STUB_URL:-postgres://bookwarden:s3cret@postgresql18:5432/bookwarden?sslmode=disable}" ;;
  *" createdb "*) exit "${STUB_CREATEDB_RC:-0}" ;;
  *" pg_restore "*) cat >/dev/null; exit "${STUB_RESTORE_RC:-0}" ;;
  "run "*)
    for a in "$@"; do case $prev in --env-file) cat "$a" >>"$STUB_LOG.env" ;; esac; prev=$a; done
    exit "${STUB_VERIFY_RC:-0}" ;;
  *" dropdb "*) ;;
esac
exit 0
STUB
chmod +x "$ROOT/bin/docker"
cat >"$ROOT/bin/curl" <<'STUB'
#!/usr/bin/env bash
echo "curl $*" >>"$STUB_LOG.curl"
cat >>"$STUB_LOG.curl.cfg"
exit "${STUB_CURL_RC:-0}"
STUB
chmod +x "$ROOT/bin/curl"

run_case() { # name expected_rc env...
  local name=$1 want=$2
  shift 2
  local d="$ROOT/$name"
  mkdir -p "$d/dumps/postgresql18_20261107-0230" "$d/dumps/postgresql18_20261108-0230"
  mkdir -p "$d/dumps/postgresql18_manual" "$d/tmp"
  printf 'old' >"$d/dumps/postgresql18_20261107-0230/bookwarden.dump"
  printf 'new' >"$d/dumps/postgresql18_20261108-0230/bookwarden.dump"
  printf 'odd' >"$d/dumps/postgresql18_manual/bookwarden.dump"
  touch -d '1 hour ago' "$d"/dumps/*/bookwarden.dump
  : >"$d/log"
  local rc=0
  env PATH="$ROOT/bin:$PATH" TMPDIR="$d/tmp" STUB_LOG="$d/log" BW_PG_USER=postgres BW_DUMP_ROOT="$d/dumps" BW_CHANGELOG="$d/changes" "$@" \
    bash "$SCRIPT" >"$d/out" 2>&1 || rc=$?
  if [ "$rc" != "$want" ]; then
    echo "FAIL $name: exit $rc, want $want"; cat "$d/out"; FAILS=$((FAILS + 1))
  fi
  D=$d
}
expect() { # description command...
  local what=$1
  shift
  if ! "$@"; then echo "FAIL $what"; FAILS=$((FAILS + 1)); fi
}
has() { grep -q -- "$2" "$1"; }
hasnt() { ! grep -q -- "$2" "$1"; }

run_case ok 0
expect "ok: restores the newest dated dump, not a stray directory" has "$D/out" "postgresql18_20261108-0230/bookwarden.dump"
expect "ok: creates the scratch database from template0, owned like the live one" has "$D/log" "createdb -U postgres -T template0 -O bookwarden bookwarden_drill"
expect "ok: restores in one transaction" has "$D/log" "pg_restore -U postgres --single-transaction -d bookwarden_drill"
expect "ok: verify-seals runs the running container's exact image" has "$D/log" "sha256:runningimage verify-seals"
expect "ok: verify-seals runs read-only on db-net" has "$D/log" "run --rm --read-only --network db-net --env-file"
expect "ok: the URL points at the scratch database" has "$D/log.env" "@postgresql18:5432/bookwarden_drill?sslmode=disable"
expect "ok: only the URL goes into the env file" hasnt "$D/log.env" "TZ="
expect "ok: the password is never printed" hasnt "$D/out" "s3cret"
expect "ok: scratch database removed" has "$D/log" "dropdb -U postgres --force --if-exists bookwarden_drill"
expect "ok: no temp files left" test -z "$(ls -A "$D/tmp")"
expect "ok: change log line" has "$D/changes" "restore drill passed"
expect "ok: the change log line gives the dump's age" has "$D/changes" "dump 1h old"
expect "ok: the change log line gives the row counts" has "$D/changes" "16 runs, 51000 sealed books, 22 schema"
expect "ok: the pass line gives the dump's age" has "$D/out" "restore drill passed (dump 1h old)"
expect "ok: no Kuma push without BW_KUMA_URL" test ! -e "$D/log.curl"

run_case no_runs 1 STUB_RUNS=0
expect "no runs: says so" has "$D/out" "0 runs, fewer than BW_MIN_RUNS=1"
expect "no runs: change log says FAILED" has "$D/changes" "restore drill FAILED"
expect "no runs: scratch database still removed" has "$D/log" "dropdb"

run_case min_runs 1 STUB_RUNS=16 BW_MIN_RUNS=20
expect "min runs: says so" has "$D/out" "16 runs, fewer than BW_MIN_RUNS=20"

run_case runs_query_fails 1 STUB_RUNS_FAIL=1
expect "runs query fails: says the count failed" has "$D/out" "could not count the runs"
expect "runs query fails: not reported as 0 runs" hasnt "$D/out" "has 0 runs"
expect "runs query fails: change log says FAILED" has "$D/changes" "restore drill FAILED"

run_case bad_min_runs 2 BW_MIN_RUNS=x
expect "bad min runs: refused" has "$D/out" "BW_MIN_RUNS must be"

kuma=https://kuma.example/api/push/K6SECRETPUSHTOKEN
run_case kuma 0 "BW_KUMA_URL=$kuma"
expect "kuma: pushed once after a pass" test "$(grep -c . "$D/log.curl" 2>/dev/null)" = 1
expect "kuma: with a timeout, failing on HTTP errors, config on stdin" has "$D/log.curl" "curl -fsS -m 10 -K -"
expect "kuma: the URL goes in on stdin" has "$D/log.curl.cfg" "url = \"$kuma\""
expect "kuma: the URL is not in curl's arguments" hasnt "$D/log.curl" "K6SECRETPUSHTOKEN"
expect "kuma: the URL never reaches the output" hasnt "$D/out" "K6SECRETPUSHTOKEN"
expect "kuma: nor the change log" hasnt "$D/changes" "K6SECRETPUSHTOKEN"

run_case kuma_fails 0 "BW_KUMA_URL=$kuma" STUB_CURL_RC=7
expect "kuma fails: a warning, still a pass" has "$D/out" "warning: the Kuma push failed"
expect "kuma fails: the URL never reaches the output" hasnt "$D/out" "K6SECRETPUSHTOKEN"

run_case kuma_after_failure 1 "BW_KUMA_URL=$kuma" STUB_VERIFY_RC=1
expect "kuma: no push after a failed drill" test ! -e "$D/log.curl"

run_case kuma_no_runs 1 "BW_KUMA_URL=$kuma" STUB_RUNS=0
expect "kuma: no push after a drill with too few runs" test ! -e "$D/log.curl"

printf 'young' >"$ROOT/young.dump"
printf 'aged' >"$ROOT/aged.dump"
touch -d '3 hours ago' "$ROOT/aged.dump"
run_case stale 3 BW_MAX_AGE_HOURS=1 "BW_DUMP=$ROOT/aged.dump"
expect "stale: refused before creating anything" hasnt "$D/log" "createdb"
expect "stale: says how old" has "$D/out" "dump is 3h old (limit 1h)"

run_case young 3 "BW_DUMP=$ROOT/young.dump"
expect "young: refused" has "$D/out" "still being written"
expect "young: nothing created" hasnt "$D/log" "createdb"

printf '' >"$ROOT/empty.dump"
touch -d '1 hour ago' "$ROOT/empty.dump"
run_case empty 3 "BW_DUMP=$ROOT/empty.dump"
expect "empty: nothing created" hasnt "$D/log" "createdb"

run_case no_root 3 "BW_DUMP_ROOT=$ROOT/not-mounted"
expect "no root: says why" has "$D/out" "dump root not found"

run_case no_query 0 "STUB_URL=postgres://bookwarden:s3cret@postgresql18:5432/bookwarden"
expect "no query: URL points at the scratch database" has "$D/log.env" "@postgresql18:5432/bookwarden_drill\$"

for q in "sslmode=disable&dbname=bookwarden" "database=bookwarden" "service=prod"; do
  run_case "dbname_${q%%=*}" 3 "STUB_URL=postgres://bookwarden:s3cret@postgresql18:5432/bookwarden?$q"
  expect "query $q: refused before creating anything" hasnt "$D/log" "createdb"
done

run_case other_path 3 "STUB_URL=postgres://u:p@pg/live?application_name=x/bookwarden"
expect "other path: refused before creating anything" hasnt "$D/log" "createdb"

run_case createdb_fails 1 STUB_CREATEDB_RC=1
expect "createdb fails: never drops a database it didn't create" hasnt "$D/log" "dropdb"

run_case exists 3 STUB_EXISTS=1
expect "exists: refused" has "$D/out" "already exists"
expect "exists: never removes a database it didn't create" hasnt "$D/log" "dropdb"

run_case restore_fails 1 STUB_RESTORE_RC=1
expect "restore fails: scratch database still removed" has "$D/log" "dropdb"
expect "restore fails: verify-seals not run" hasnt "$D/log" "verify-seals"

run_case verify_fails 1 STUB_VERIFY_RC=1
expect "verify fails: scratch database still removed" has "$D/log" "dropdb"
expect "verify fails: change log says FAILED" has "$D/changes" "restore drill FAILED"

run_case same_db 2 BW_DRILL_DB=bookwarden
expect "same db: refused" has "$D/out" "must differ"

run_case no_source 2 BW_DUMP_ROOT=
expect "no dump source: says what to set" has "$D/out" "set BW_DUMP_ROOT"

run_case bad_name 2 "BW_DRILL_DB=drill;x"
expect "bad name: refused" has "$D/out" "plain lower-case identifier"

run_case explicit_dump 0 "BW_DUMP=$ROOT/ok/dumps/postgresql18_20261107-0230/bookwarden.dump"
expect "explicit dump: used" has "$D/out" "postgresql18_20261107-0230/bookwarden.dump"

if [ "$FAILS" -ne 0 ]; then
  echo "restore-drill_test: $FAILS failure(s)"
  exit 1
fi
echo "restore-drill_test: OK"
