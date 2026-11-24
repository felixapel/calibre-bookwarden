#!/usr/bin/env bash
# Self-contained test for deploy-unraid.sh: stub docker, php, pg_restore, cp, date and
# sleep go first on PATH, log their argv, and return canned answers. Nothing real is touched.
#
#   scripts/deploy-unraid_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SCRIPT="$HERE/deploy-unraid.sh"
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT

DIGEST=sha256:$(printf 'a%.0s' {1..64})
IMAGE="registry.example/org/bookwarden@$DIGEST"
LABEL=abc1234
FAILS=0
RC=0
OUT=""
D=""

make_stubs() {
  local bin=$1
  mkdir -p "$bin"
  cat >"$bin/docker" <<'STUB'
#!/usr/bin/env bash
echo "docker $*" >>"$STUB_LOG"
all="$*"
n=$(cat "$STUB_DIR/php_count" 2>/dev/null || echo 0)
cur=$(cat "$STUB_DIR/cur_c" 2>/dev/null || echo sha256:oldimg)
case $all in
  *" psql "*) [ "${STUB_QUERY_FAIL:-0}" = 1 ] && exit 1 ;;
esac
case $all in
  "login "*)
    echo "login stdin_bytes=$(wc -c)" >>"$STUB_LOG"
    [ "${STUB_LOGIN_FAIL:-0}" = 1 ] && exit 1
    ;;
  "pull "*) [ "${STUB_PULL_FAIL:-0}" = 1 ] && exit 1 ;;
  "rename "*)
    # restoring the old container's name
    if [ "$3" = bookwarden ]; then echo "${STUB_ROLLBACK_IMAGE:-sha256:oldimg}" >"$STUB_DIR/cur_c"; fi
    ;;
  "image inspect"*)
    case $all in
      *"@sha256:"*) echo "${STUB_PULLED_ID:-sha256:newimg}" ;;
      *) echo "${STUB_TPL_ID:-sha256:oldimg}" ;;
    esac
    ;;
  *"from runs"*) echo "${STUB_RUNS:-0}" ;;
  *operation_events*) echo "${STUB_WRITES:-0}" ;;
  *pg_get_userbyid*) echo "${STUB_OWNER:-bwowner}" ;;
  *schema_migrations*)
    [ "${STUB_SCHEMA_FAIL:-0}" = late ] && [ "$n" -ge 1 ] && exit 1
    cat "$STUB_DIR/schema"
    ;;
  *pg_dump*)
    if [ "${STUB_DUMP_FAIL:-0}" = 1 ]; then printf 'PART'; exit 1; fi
    printf 'PGDMP-stub'
    ;;
  *"{{.Image}}"*) echo "$cur" ;;
  *"{{.State.Status}}"*)
    case $cur in
      sha256:oldimg | sha256:other)
        if [ "${STUB_ROLLBACK_HEALTH:-ok}" = bad ] && [ "$n" -ge 1 ]; then echo "running starting"; else echo "running healthy"; fi
        ;;
      *)
        case ${STUB_HEALTH:-ok} in
          ok) echo "running healthy" ;;
          starting) echo "running starting" ;;
          unhealthy) echo "running unhealthy" ;;
          exited) echo "exited nohealthcheck" ;;
          nohealthcheck) echo "running nohealthcheck" ;;
        esac
        ;;
    esac
    ;;
  *"bookwarden version"*) echo "bookwarden ${STUB_VERSION:-v1.2.3 ($LABEL_STUB)}" ;;
  "run --rm --network none --read-only --entrypoint /usr/local/bin/bookwarden "*" version")
    if [ "${STUB_IMAGE_VERSION_FAIL:-0}" = 1 ]; then echo "exec format error"; exit 1; fi
    echo "bookwarden ${STUB_IMAGE_VERSION:-v1.2.3 ($LABEL_STUB)}"
    ;;
esac
exit 0
STUB
  cat >"$bin/php" <<'STUB'
#!/usr/bin/env bash
echo "php $(basename "$1") template=${BW_TEMPLATE:+set}" >>"$STUB_LOG"
n=$(cat "$STUB_DIR/php_count" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" >"$STUB_DIR/php_count"
echo "${STUB_PHP_IMAGE:-sha256:newimg}" >"$STUB_DIR/cur_c"
[ "${STUB_MIGRATE:-0}" = 1 ] && [ "$n" = 1 ] && echo 21 >"$STUB_DIR/schema"
[ "${STUB_PHP_FAIL:-0}" = 1 ] && [ "$n" = 1 ] && exit 1
exit 0
STUB
  cat >"$bin/pg_restore" <<'STUB'
#!/usr/bin/env bash
echo "pg_restore $*" >>"$STUB_LOG"
exit 0
STUB
  cat >"$bin/cp" <<'STUB'
#!/usr/bin/env bash
echo "cp $*" >>"$STUB_LOG"
last=${*: -1}
case ${STUB_CP_FAIL:-} in
  backup) case $last in *.bak-*) exit 1 ;; esac ;;
  restore) case $last in *.bak-*) ;; *) exit 1 ;; esac ;;
esac
exec /usr/bin/cp "$@"
STUB
  cat >"$bin/date" <<'STUB'
#!/usr/bin/env bash
if [ "$*" = "+%Y%m%d-%H%M%S" ]; then echo 20260101-000000; exit 0; fi
exec /usr/bin/date "$@"
STUB
  cat >"$bin/sleep" <<'STUB'
#!/usr/bin/env bash
exec /usr/bin/sleep 0.2
STUB
  chmod +x "$bin"/*
}

# new_case <name>: fresh sandbox.
new_case() {
  D="$ROOT/$1"
  mkdir -p "$D/bin" "$D/stub" "$D/tpl" "$D/dumps"
  make_stubs "$D/bin"
  echo 20 >"$D/stub/schema"
  : >"$D/log"
  printf '<?xml version="1.0"?>\n<Container>\n  <Name>bookwarden</Name>\n  <Repository>old.example/bw:latest</Repository>\n</Container>\n' >"$D/tpl/my-bookwarden.xml"
  cp -p "$D/tpl/my-bookwarden.xml" "$D/template.orig"
}

base_env() {
  echo "PATH=${STUB_PATH:-$D/bin:$PATH}" "STUB_LOG=$D/log" "STUB_DIR=$D/stub" "LABEL_STUB=$LABEL" \
    "BW_PG_USER=testuser" "BW_TEMPLATE=$D/tpl/my-bookwarden.xml" "BW_DUMP_DIR=$D/dumps" \
    "BW_CHANGELOG=$D/changelog" "BW_HEALTH_TIMEOUT=2" "BW_POLL_INTERVAL=1"
}

# run_deploy [VAR=val ...]: runs the script in the sandbox, sets RC and OUT.
run_deploy() {
  local envs
  read -r -a envs <<<"$(base_env)"
  set +e
  OUT=$(env "${envs[@]}" "$@" "$SCRIPT" "$IMAGE" "$LABEL" "test note" 2>&1)
  RC=$?
  set -e
}

check() { # check <description> <status>
  if [ "$2" = 0 ]; then
    echo "ok   - $1"
  else
    echo "FAIL - $1"
    FAILS=$((FAILS + 1))
  fi
}
yes_if() { if "$@"; then echo 0; else echo 1; fi; }

logged() { grep -qE -- "$1" "$D/log"; }
not_logged() { ! grep -qE -- "$1" "$D/log"; }
unchanged_template() { cmp -s "$D/tpl/my-bookwarden.xml" "$D/template.orig"; }
php_runs() { cat "$D/stub/php_count" 2>/dev/null || echo 0; }
rc_is() { [ "$RC" = "$1" ]; }
out_has() { grep -qE -- "$1" <<<"$OUT"; }
not_out_has() { ! grep -qE -- "$1" <<<"$OUT"; }

# assert_order <pattern>...: each pattern matches a log line after the previous match.
assert_order() {
  local pos=0 pat ln
  for pat in "$@"; do
    ln=$(PAT="$pat" awk -v s="$pos" 'NR>s && $0 ~ ENVIRON["PAT"] {print NR; exit}' "$D/log")
    if [ -z "$ln" ]; then
      echo "  order: '$pat' not found after line $pos"
      return 1
    fi
    pos=$ln
  done
}

MUTATING='pg_dump|docker (pull|login|logout|stop|rename|rm|start|run)|^php|^pg_restore|^cp'
BAK='[^ ]+\.bak-pre-abc1234-'

# --- happy path ------------------------------------------------------------
new_case happy
run_deploy
check "happy: exit 0" "$(yes_if rc_is 0)"
[ "$RC" = 0 ] || echo "$OUT"
check "happy: steps run in order (pull, image version, preflight, stop, rename, dump, verify, pin backup, recreate, health, version, rm old)" "$(yes_if assert_order \
  'docker pull -q' "docker run --rm --network none --read-only --entrypoint /usr/local/bin/bookwarden $IMAGE version" 'from runs' 'operation_events' 'schema_migrations' \
  'docker stop -t 40 bookwarden$' 'docker rename bookwarden bookwarden-pre-abc1234$' \
  'pg_dump' '^pg_restore -f /dev/null' "^cp -p [^ ]+my-bookwarden.xml $BAK" '^php recreate.php' \
  'State.Status' 'bookwarden version' 'docker rm bookwarden-pre-abc1234$')"
check "happy: the new container's image id is checked" "$(yes_if assert_order '^php' 'docker inspect -f \{\{.Image\}\} bookwarden$' 'bookwarden version')"
check "happy: recreate reads the template by path" "$(yes_if logged '^php recreate.php template=set')"
check "happy: template pinned to digest" "$(yes_if grep -q "<Repository>$IMAGE</Repository>" "$D/tpl/my-bookwarden.xml")"
backup=$(find "$D/tpl" -name '.my-bookwarden.xml.bak-pre-abc1234-*' | head -1)
check "happy: template backup is the original" "$(yes_if cmp -s "$backup" "$D/template.orig")"
check "happy: backup name carries seconds" "$(yes_if grep -q 'bak-pre-abc1234-20260101-000000' <<<"$backup")"
dump=$(find "$D/dumps" -name 'pre-abc1234-*.dump' | head -1)
check "happy: dump exists with mode 600" "$(yes_if test -n "$dump" -a "$(stat -c %a "$dump")" = 600)"
check "happy: change-log line appended" "$(yes_if grep -q "redeployed with $LABEL" "$D/changelog")"
check "happy: no login without BW_REGISTRY_USER" "$(yes_if not_logged 'docker login')"
check "happy: the old container is not removed before the new one is healthy" "$(yes_if assert_order 'State.Status' 'docker rm bookwarden-pre')"
check "happy: no docker rm of the plain container name" "$(yes_if not_logged 'docker rm bookwarden$')"

# --- registry login: the token arrives through stdin ----------------------------
new_case login
set +e
read -r -a envs <<<"$(base_env)"
OUT=$(echo "s3cret-token" | env "${envs[@]}" BW_REGISTRY_USER=someone "$SCRIPT" "$IMAGE" "$LABEL" 2>&1)
RC=$?
set -e
check "login: exit 0" "$(yes_if rc_is 0)"
check "login: login before pull, logout after" "$(yes_if assert_order 'docker login registry.example .*--password-stdin' 'docker pull' 'docker logout')"
check "login: docker received the token on stdin (13 bytes)" "$(yes_if logged '^login stdin_bytes=13$')"
check "login: token never printed or logged" "$(yes_if test "$(grep -c s3cret-token "$D/log")" = 0 && ! grep -q s3cret-token <<<"$OUT")"
check "login: exactly one logout" "$([ "$(grep -c 'docker logout' "$D/log")" = 1 ] && echo 0 || echo 1)"

new_case loginfail
set +e
read -r -a envs <<<"$(base_env)"
OUT=$(echo "tok" | env "${envs[@]}" BW_REGISTRY_USER=someone STUB_LOGIN_FAIL=1 "$SCRIPT" "$IMAGE" "$LABEL" 2>&1)
RC=$?
set -e
check "login fails: exit 1, nothing pulled or stopped" "$(yes_if test "$RC" = 1 && not_logged 'docker (pull|stop|rename|rm)|pg_dump|^php')"
check "login fails: no logout for a login that never happened" "$(yes_if not_logged 'docker logout')"

# --- pull fails ------------------------------------------------------------------
new_case pullfail
set +e
read -r -a envs <<<"$(base_env)"
OUT=$(echo "tok" | env "${envs[@]}" BW_REGISTRY_USER=someone STUB_PULL_FAIL=1 "$SCRIPT" "$IMAGE" "$LABEL" 2>&1)
RC=$?
set -e
check "pull fails: exit 1" "$(yes_if rc_is 1)"
check "pull fails: nothing stopped, nothing dumped" "$(yes_if not_logged 'docker (stop|rename|rm)|pg_dump|^php|^cp')"
check "pull fails: logout still ran" "$(yes_if logged 'docker logout')"

# --- preflight refusals --------------------------------------------------------
new_case active
run_deploy STUB_RUNS=1
check "active run: exit 3 and message" "$(yes_if test "$RC" = 3 && out_has 'run is active')"
check "active run: nothing mutating ran, template untouched" "$(yes_if not_logged "pg_dump|docker (stop|rename|rm)|^php|^cp" && unchanged_template)"

new_case writing
run_deploy STUB_WRITES=2
check "write in progress: exit 3 and message" "$(yes_if test "$RC" = 3 && out_has 'write is in progress')"
check "write in progress: nothing mutating ran" "$(yes_if not_logged 'pg_dump|docker (stop|rename|rm)|^php|^cp')"

new_case queryfail
run_deploy STUB_QUERY_FAIL=1
check "query failure: exit 3 with 'cannot query'" "$(yes_if test "$RC" = 3 && out_has 'cannot query bookwarden')"
check "query failure: nothing stopped" "$(yes_if not_logged 'docker (stop|rename|rm)|pg_dump|^php')"

new_case tplid
run_deploy STUB_TPL_ID=sha256:somethingelse
check "template points at another image than the running one: exit 3" "$(yes_if test "$RC" = 3 && out_has 'template')"
check "template mismatch: nothing stopped" "$(yes_if not_logged 'docker (stop|rename|rm)|pg_dump|^php')"

new_case exists
: >"$D/dumps/pre-abc1234-20260101-000000.dump"
run_deploy
check "existing dump file: refused with exit 3, nothing stopped" "$(yes_if test "$RC" = 3 && not_logged 'docker (stop|rename|rm)|^php')"

new_case tworepo
printf '<Container>\n<Repository>a</Repository>\n<Repository>b</Repository>\n</Container>\n' >"$D/tpl/my-bookwarden.xml"
cp -p "$D/tpl/my-bookwarden.xml" "$D/template.orig"
run_deploy
check "ambiguous template: refused, container untouched" "$(yes_if test "$RC" = 3 && not_logged 'docker (stop|rename|rm)|^php' && unchanged_template)"

# --- pg_dump fails after the stop ------------------------------------------------
new_case dumpfail
run_deploy STUB_DUMP_FAIL=1
check "pg_dump fails: exit 1" "$(yes_if rc_is 1)"
check "pg_dump fails: no dump file left" "$(yes_if test -z "$(find "$D/dumps" -type f)")"
check "pg_dump fails: old container renamed back and started, no recreate" "$(yes_if assert_order 'docker stop -t 40 bookwarden$' 'docker rename bookwarden bookwarden-pre-abc1234$' 'pg_dump' 'docker rename bookwarden-pre-abc1234 bookwarden$' 'docker start bookwarden$' && test "$(php_runs)" = 0)"
check "pg_dump fails: template untouched, no change-log" "$(yes_if unchanged_template && test ! -e "$D/changelog")"

# --- template backup cp fails ----------------------------------------------------
new_case cpfail
run_deploy STUB_CP_FAIL=backup
check "backup cp fails: exit 1, old container back, no recreate" "$(yes_if test "$RC" = 1 && logged 'docker start bookwarden$' && test "$(php_runs)" = 0)"
check "backup cp fails: template untouched and no 'restored' message" "$(yes_if unchanged_template && not_out_has 'template restored')"

# --- forward recreate fails, rollback succeeds --------------------------------------
new_case phpfail
run_deploy STUB_PHP_FAIL=1
check "recreate fails: exit 1 (rolled back)" "$(yes_if rc_is 1)"
[ "$RC" = 1 ] || echo "$OUT"
check "recreate fails: template restored" "$(yes_if unchanged_template)"
check "recreate fails: rollback order (stop new, schema read, restore template, rm -f new, rename back, start)" "$(yes_if assert_order \
  '^php' 'docker stop -t 40 bookwarden$' 'schema_migrations' "^cp -p ${BAK}[^ ]* [^ ]+my-bookwarden.xml$" \
  'docker rm -f bookwarden$' 'docker rename bookwarden-pre-abc1234 bookwarden$' 'docker start bookwarden$')"
check "recreate fails: PHP ran only once" "$(yes_if test "$(php_runs)" = 1)"

# --- health timeout -> rollback --------------------------------------------------
new_case timeout
run_deploy STUB_HEALTH=starting
check "health timeout: exit 1, template restored" "$(yes_if test "$RC" = 1 && unchanged_template)"
check "health timeout: old container started again without PHP" "$(yes_if test "$(php_runs)" = 1 && logged 'docker start bookwarden$')"
check "health timeout: no database restore attempted" "$(yes_if not_logged 'dropdb|createdb|pg_restore .*--single-transaction')"
check "health timeout: no change-log line" "$(yes_if test ! -e "$D/changelog")"
check "health timeout: the renamed old container is kept until rollback renames it back" "$(yes_if not_logged 'docker rm bookwarden-pre-abc1234$')"

# --- unhealthy / exited / no healthcheck fail at once ---------------------------------
for mode in unhealthy exited nohealthcheck; do
  new_case "fast-$mode"
  start=$SECONDS
  run_deploy STUB_HEALTH=$mode BW_HEALTH_TIMEOUT=30
  elapsed=$((SECONDS - start))
  check "$mode: exit 1, rolled back" "$(yes_if test "$RC" = 1 && unchanged_template)"
  check "$mode: failed at once, not after the timeout (${elapsed}s)" "$(yes_if test "$elapsed" -lt 15)"
done
check "no healthcheck: clear message" "$(yes_if out_has 'no healthcheck')"

# --- schema changed after the failure -> exit 4 -----------------------------------------
new_case schema
run_deploy STUB_HEALTH=starting STUB_MIGRATE=1
check "schema change: exit 4" "$(yes_if rc_is 4)"
[ "$RC" = 4 ] || echo "$OUT"
check "schema change: new container stopped BEFORE the schema is read again" "$(yes_if assert_order '^php' 'docker stop -t 40 bookwarden$' 'schema_migrations')"
check "schema change: prints dropdb, createdb with the original owner, single-transaction restore" "$(yes_if out_has 'docker exec postgresql18 dropdb -U testuser bookwarden' && out_has 'createdb -U testuser -O bwowner bookwarden' && out_has 'pg_restore -U testuser --exit-on-error --single-transaction -d bookwarden < .*/pre-abc1234-.*\.dump')"
check "schema change: does not print --clean" "$(yes_if not_out_has '--clean')"
check "schema change: template restored, DB untouched, old binary not recreated" "$(yes_if unchanged_template && not_logged 'dropdb|createdb|pg_restore .*single' && test "$(php_runs)" = 1)"
check "schema change: old container left renamed, not removed" "$(yes_if not_logged 'docker rm ')"

new_case schemaread
run_deploy STUB_HEALTH=starting STUB_SCHEMA_FAIL=late
check "schema unreadable: exit 4 and says so" "$(yes_if test "$RC" = 4 && out_has 'could not read schema version')"
check "schema unreadable: restore instructions printed" "$(yes_if out_has 'single-transaction')"

# --- rollback itself fails -> exit 5 --------------------------------------------------
new_case rbfail
run_deploy STUB_HEALTH=starting STUB_ROLLBACK_HEALTH=bad
check "rollback unhealthy: exit 5" "$(yes_if rc_is 5)"

new_case rbimage
run_deploy STUB_HEALTH=starting STUB_ROLLBACK_IMAGE=sha256:other
check "rollback image differs from the one before: exit 5" "$(yes_if test "$RC" = 5 && out_has 'image')"

new_case rbcp
run_deploy STUB_HEALTH=starting STUB_CP_FAIL=restore
check "template restore cp fails: exit 5 and no 'template restored' claim" "$(yes_if test "$RC" = 5 && not_out_has 'template restored from' && out_has 'NOT restored')"

# --- the new container runs the wrong image / wrong version ------------------------------
new_case wrongimg
run_deploy STUB_PHP_IMAGE=sha256:wrong
check "new container image differs from the pulled one: exit 1, rolled back" "$(yes_if test "$RC" = 1 && unchanged_template && logged 'docker start bookwarden$')"

new_case badversion
run_deploy STUB_VERSION="v0.0.1 (zzz9999)"
check "version label mismatch: exit 1 and template restored" "$(yes_if test "$RC" = 1 && unchanged_template)"

# --- the pulled image's own version is checked before anything changes -----------------
new_case imgversion
run_deploy STUB_IMAGE_VERSION="v2.0.0 (zzz9999zzz99)"
check "image version lacks the label: exit 3 with the message" "$(yes_if test "$RC" = 3 && out_has "the image's version does not contain the label; nothing was changed")"
check "image version lacks the label: no dump, no template write, no stop" "$(yes_if not_logged 'pg_dump|docker (stop|rename|rm)|^php|^cp' && unchanged_template)"

new_case imgversionfail
run_deploy STUB_IMAGE_VERSION_FAIL=1
check "image version command fails: exit 3, nothing stopped" "$(yes_if test "$RC" = 3 && not_logged 'pg_dump|docker (stop|rename|rm)|^php|^cp' && unchanged_template)"
check "image version command fails: docker's own error is shown" "$(yes_if out_has 'exec format error')"

new_case release
run_deploy STUB_IMAGE_VERSION="v2.0.0 (abc1234def56)" STUB_VERSION="v2.0.0 (abc1234def56)"
check "release image (tag plus revision) with label abc1234: exit 0" "$(yes_if rc_is 0)"

# --- signal during the cutover ---------------------------------------------------------------
new_case signal
read -r -a envs <<<"$(base_env)"
env "${envs[@]}" STUB_HEALTH=starting BW_HEALTH_TIMEOUT=60 "$SCRIPT" "$IMAGE" "$LABEL" >"$D/out" 2>&1 &
pid=$!
for _ in $(seq 1 100); do
  grep -q 'State.Status' "$D/log" && break
  /usr/bin/sleep 0.1
done
kill -TERM "$pid"
set +e
wait "$pid"
RC=$?
set -e
OUT=$(cat "$D/out")
check "SIGTERM mid-cutover: rolled back, exit 1" "$(yes_if test "$RC" = 1 && unchanged_template && logged 'docker start bookwarden$')"

# --- dry run ---------------------------------------------------------------------------------
new_case dry
run_deploy DRY_RUN=1 BW_REGISTRY_USER=someone
check "dry run: exit 0" "$(yes_if rc_is 0)"
[ "$RC" = 0 ] || echo "$OUT"
check "dry run: no mutating stub call" "$(yes_if not_logged "$MUTATING")"
check "dry run: template unchanged, no backup, no dump, no change-log" "$(yes_if unchanged_template && test -z "$(find "$D/tpl" -name '*bak*')" && test -z "$(find "$D/dumps" -type f)" && test ! -e "$D/changelog")"
check "dry run: prints the pull line" "$(yes_if out_has '\[dry-run\] docker pull')"
check "dry run: prints the logout line" "$(yes_if out_has '\[dry-run\] docker logout')"
check "dry run: prints the image version check" "$(yes_if out_has "\[dry-run\] docker run --rm --network none --read-only --entrypoint /usr/local/bin/bookwarden $IMAGE version")"

# --- in-container pg_restore fallback ------------------------------------------------------------
new_case fallback
mkdir -p "$D/min"
for t in env bash cat basename dirname mktemp grep wc sed rm mkdir chmod head cut awk tr stat; do
  src=$(command -v "$t") && ln -sf "$src" "$D/min/$t"
done
rm -f "$D/bin/pg_restore"
for t in docker php cp date sleep; do ln -sf "$D/bin/$t" "$D/min/$t"; done
STUB_PATH="$D/min"
run_deploy
unset STUB_PATH
check "fallback: exit 0 with no pg_restore on PATH" "$(yes_if rc_is 0)"
[ "$RC" = 0 ] || echo "$OUT"
check "fallback: the dump is read through docker exec -i pg_restore -f /dev/null" "$(yes_if logged '^docker exec -i postgresql18 pg_restore -f /dev/null$')"

# --- backup retention -----------------------------------------------------------------------------
# seed_backups: 12 older script dumps and template backups, plus files the pruning must never touch.
seed_backups() {
  local i
  for i in 01 02 03 04 05 06 07 08 09 10 11 12; do
    : >"$D/dumps/pre-old$i-label-202501${i}-120000.dump"
    : >"$D/tpl/.my-bookwarden.xml.bak-pre-old$i-label-202501${i}-120000"
  done
  : >"$D/dumps/pre-v2-20251101-1527.dump"  # hand-made, other timestamp format
  : >"$D/dumps/admin-login.txt"
  : >"$D/tpl/.my-bookwarden.xml.bak-schedule-20251109-144053"
}
count() { find "$1" -maxdepth 1 -name "$2" | wc -l; }

new_case keep
seed_backups
run_deploy BW_KEEP_BACKUPS=3
check "keep 3: exit 0" "$(yes_if rc_is 0)"
[ "$RC" = 0 ] || echo "$OUT"
check "keep 3: three script dumps left" "$(yes_if test "$(count "$D/dumps" 'pre-*-????????-??????.dump')" = 3)"
check "keep 3: this deploy's dump is kept" "$(yes_if test -f "$D/dumps/pre-abc1234-20260101-000000.dump")"
check "keep 3: the newest older dumps are kept" "$(yes_if test -f "$D/dumps/pre-old12-label-20250112-120000.dump" -a -f "$D/dumps/pre-old11-label-20250111-120000.dump")"
check "keep 3: an older dump is removed" "$(yes_if test ! -e "$D/dumps/pre-old10-label-20250110-120000.dump")"
check "keep 3: hand-made dump and other files untouched" "$(yes_if test -f "$D/dumps/pre-v2-20251101-1527.dump" -a -f "$D/dumps/admin-login.txt")"
check "keep 3: three template backups left, this deploy's included" "$(yes_if test "$(count "$D/tpl" '.my-bookwarden.xml.bak-pre-*')" = 3 -a -f "$D/tpl/.my-bookwarden.xml.bak-pre-abc1234-20260101-000000")"
check "keep 3: other template backups untouched" "$(yes_if test -f "$D/tpl/.my-bookwarden.xml.bak-schedule-20251109-144053")"
check "keep 3: removals are logged" "$(yes_if out_has 'removed old backup .*pre-old01-label-20250101-120000.dump')"

new_case keepall
seed_backups
run_deploy BW_KEEP_BACKUPS=0
check "keep 0: exit 0 and nothing removed" "$(yes_if test "$RC" = 0 -a "$(count "$D/dumps" 'pre-*.dump')" = 14 -a "$(count "$D/tpl" '.my-bookwarden.xml.bak-pre-*')" = 13)"

new_case keepdefault
seed_backups
run_deploy
check "default keeps 10 dumps" "$(yes_if test "$RC" = 0 -a "$(count "$D/dumps" 'pre-*-????????-??????.dump')" = 10)"

new_case keepfail
seed_backups
run_deploy BW_KEEP_BACKUPS=1 STUB_HEALTH=unhealthy
check "failed deploy: rolled back (exit 1) and nothing pruned" "$(yes_if test "$RC" = 1 -a "$(count "$D/dumps" 'pre-old*.dump')" = 12 -a "$(count "$D/tpl" '.my-bookwarden.xml.bak-pre-old*')" = 12)"

new_case keepothers
seed_backups
: >"$D/tpl/.my-plex.xml.bak-pre-old01-label-20250101-120000"
: >"$D/tpl/.my-bookwarden-dev.xml.bak-pre-old01-label-20250101-120000"
: >"$D/tpl/my-plex.xml"
ln -s "$D/template.orig" "$D/tpl/.my-bookwarden.xml.bak-pre-link-label-20240101-120000"
run_deploy BW_KEEP_BACKUPS=1
check "other templates and their backups survive" "$(yes_if test "$RC" = 0 -a -f "$D/tpl/.my-plex.xml.bak-pre-old01-label-20250101-120000" -a -f "$D/tpl/.my-bookwarden-dev.xml.bak-pre-old01-label-20250101-120000" -a -f "$D/tpl/my-plex.xml" -a -f "$D/tpl/my-bookwarden.xml")"
check "a symlink named like a backup: the link goes, its target stays" "$(yes_if test ! -L "$D/tpl/.my-bookwarden.xml.bak-pre-link-label-20240101-120000" -a -f "$D/template.orig")"

new_case keepcurrent
: >"$D/dumps/pre-future-label-20270101-120000.dump"
: >"$D/dumps/pre-future-label-20270102-120000.dump"
: >"$D/tpl/.my-bookwarden.xml.bak-pre-future-label-20270101-120000"
run_deploy BW_KEEP_BACKUPS=1
check "newer-named files: this deploy's dump and template backup are still kept" "$(yes_if test "$RC" = 0 -a -f "$D/dumps/pre-abc1234-20260101-000000.dump" -a -f "$D/tpl/.my-bookwarden.xml.bak-pre-abc1234-20260101-000000")"
check "newer-named files: the newest is kept, the one after it removed" "$(yes_if test -f "$D/dumps/pre-future-label-20270102-120000.dump" -a ! -e "$D/dumps/pre-future-label-20270101-120000.dump")"

new_case keepdefaulttpl
seed_backups
run_deploy
check "default keeps 10 template backups" "$(yes_if test "$RC" = 0 -a "$(count "$D/tpl" '.my-bookwarden.xml.bak-pre-*')" = 10)"

new_case keepspace
mkdir -p "$D/dump dir [1]"
for i in 01 02 03; do : >"$D/dump dir [1]/pre-old$i-label-202501${i}-120000.dump"; done
mkdir -p "$D/dump dir 1x"
: >"$D/dump dir 1x/pre-old01-label-20250101-120000.dump"
run_deploy BW_KEEP_BACKUPS=2 "BW_DUMP_DIR=$D/dump dir [1]"
check "dump dir with spaces and brackets: newest 2 kept, sibling dir untouched" "$(yes_if test "$RC" = 0 -a "$(count "$D/dump dir [1]" 'pre-*.dump')" = 2 -a -f "$D/dump dir 1x/pre-old01-label-20250101-120000.dump")"
[ "$RC" = 0 ] || echo "$OUT"

new_case keepdry
seed_backups
run_deploy BW_KEEP_BACKUPS=3 DRY_RUN=1
check "dry run: says what it would keep, removes nothing" "$(yes_if test "$RC" = 0 -a "$(count "$D/dumps" 'pre-old*.dump')" = 12 && out_has 'dry-run\] keep the newest 3')"

new_case keepbad
run_deploy BW_KEEP_BACKUPS=x
check "BW_KEEP_BACKUPS=x: exit 2, nothing touched" "$(yes_if test "$RC" = 2 && not_logged "$MUTATING")"
for v in 00 007 1000000 99999999999999999999 -1 ""; do
  [ -z "$v" ] && continue
  run_deploy "BW_KEEP_BACKUPS=$v"
  check "BW_KEEP_BACKUPS=$v: exit 2" "$(yes_if rc_is 2)"
done
run_deploy "BW_DUMP_DIR=$D/a
b"
check "newline in BW_DUMP_DIR: exit 2, nothing touched" "$(yes_if test "$RC" = 2 && not_logged "$MUTATING")"

# --- usage ----------------------------------------------------------------------------------------
new_case usage
run_deploy BW_PG_USER=
check "missing BW_PG_USER: exit 2" "$(yes_if rc_is 2)"
run_deploy DRY_RUN=2
check "DRY_RUN=2: exit 2" "$(yes_if rc_is 2)"
run_deploy BW_POLL_INTERVAL=abc
check "BW_POLL_INTERVAL=abc: exit 2" "$(yes_if rc_is 2)"
set +e
OUT=$(env "${envs[@]}" "$SCRIPT" "$IMAGE" abc12 2>&1)
RC=$?
set -e
check "label shorter than 7 characters: exit 2" "$(yes_if rc_is 2)"
check "usage errors touch nothing" "$(yes_if not_logged "$MUTATING")"

if [ "$FAILS" = 0 ]; then
  echo "all deploy-unraid checks passed"
else
  echo "$FAILS check(s) failed" >&2
  exit 1
fi
