#!/usr/bin/env bash
# Restore drill: prove the latest nightly dump of the Bookwarden database restores and that every
# sealed run in it still verifies. Run it ON the Unraid host as root, away from the nightly dump
# window. The live database is never connected to by the check: the dump is restored into a
# scratch database, checked there, and the scratch database is removed again, also on failure.
#
#   restore-drill.sh
#
# Order: find the newest dump -> refuse a stale or still-growing one -> derive the scratch URL
# (refused if it could reach another database) -> create the scratch database -> pg_restore
# (single transaction) -> `bookwarden verify-seals` against it in a container from the running
# image (verify-seals rewrites the scratch database's projections; the container's filesystem is
# read-only) -> print row counts, fail on too few runs -> after a pass, push to Uptime Kuma ->
# remove the scratch database (on exit, whatever happened).
#
# Environment (all optional unless noted):
#   BW_PG_USER          Postgres superuser that restores                 (REQUIRED)
#   BW_PG_CONTAINER     Postgres container                               (postgresql18)
#   BW_DB               the live database the dump is of                 (bookwarden)
#   BW_DRILL_DB         scratch database name                            (bookwarden_drill)
#   BW_DUMP_ROOT        directory holding postgresql18_YYYYMMDD-HHMM/<BW_DB>.dump nightly dirs
#                       (REQUIRED unless BW_DUMP is given)
#   BW_DUMP             a dump file to use instead of the newest one     (none)
#   BW_MAX_AGE_HOURS    refuse a dump older than this                    (36)
#   BW_MIN_AGE_MINUTES  refuse a dump younger than this (still written)  (15)
#   BW_CONTAINER        running Bookwarden container (image and URL)     (bookwarden)
#   BW_NETWORK          network that reaches Postgres                    (db-net)
#   BW_CHANGELOG        file that gets one appended line                 (none)
#   BW_MIN_RUNS         fail when the restored database has fewer runs   (1)
#   BW_KUMA_URL         Uptime Kuma push URL, called after a pass; it holds a token and is
#                       never printed; a failed push is only a warning   (none)
#
# Exit codes: 0 restored and every run verified; 1 restore or verification failed, or too few
#             runs; 2 usage;
#             3 preflight refused (no dump, stale or growing dump, bad URL, scratch database exists).
set -euo pipefail
umask 077
{ set +x; } 2>/dev/null # the database URL must never reach a trace

BW_PG_CONTAINER=${BW_PG_CONTAINER:-postgresql18}
BW_DB=${BW_DB:-bookwarden}
BW_DRILL_DB=${BW_DRILL_DB:-bookwarden_drill}
BW_DUMP_ROOT=${BW_DUMP_ROOT:-}
BW_DUMP=${BW_DUMP:-}
BW_MAX_AGE_HOURS=${BW_MAX_AGE_HOURS:-36}
BW_MIN_AGE_MINUTES=${BW_MIN_AGE_MINUTES:-15}
BW_CONTAINER=${BW_CONTAINER:-bookwarden}
BW_NETWORK=${BW_NETWORK:-db-net}
BW_CHANGELOG=${BW_CHANGELOG:-}
BW_MIN_RUNS=${BW_MIN_RUNS:-1}
BW_KUMA_URL=${BW_KUMA_URL:-}

log() { printf '%s\n' "$*"; }
die() { log "error: $1" >&2; exit "${2:-1}"; }

[ $# -eq 0 ] || die "usage: $0 (configure with environment variables)" 2
[ -n "${BW_PG_USER:-}" ] || die "BW_PG_USER is required" 2
[ -n "$BW_DUMP_ROOT" ] || [ -n "$BW_DUMP" ] || die "set BW_DUMP_ROOT (nightly dump directory) or BW_DUMP (one dump file)" 2
[[ $BW_DRILL_DB =~ ^[a-z_][a-z0-9_]*$ ]] || die "BW_DRILL_DB must be a plain lower-case identifier" 2
[[ $BW_DB =~ ^[a-z_][a-z0-9_]*$ ]] || die "BW_DB must be a plain lower-case identifier" 2
[ "$BW_DRILL_DB" != "$BW_DB" ] || die "BW_DRILL_DB must differ from BW_DB" 2
[[ $BW_MAX_AGE_HOURS =~ ^[1-9][0-9]*$ ]] || die "BW_MAX_AGE_HOURS must be a positive whole number" 2
[[ $BW_MIN_AGE_MINUTES =~ ^[0-9]+$ ]] || die "BW_MIN_AGE_MINUTES must be a whole number" 2
[[ $BW_MIN_RUNS =~ ^[0-9]+$ ]] || die "BW_MIN_RUNS must be a whole number" 2

psql_on() { docker exec "$BW_PG_CONTAINER" psql -U "$BW_PG_USER" -d "$1" -v ON_ERROR_STOP=1 -Atc "$2"; }

# --- find the dump -----------------------------------------------------------
if [ -z "$BW_DUMP" ]; then
  [ -d "$BW_DUMP_ROOT" ] || die "dump root not found (disk not mounted?): $BW_DUMP_ROOT" 3
  dir=$(find "$BW_DUMP_ROOT" -maxdepth 1 -type d -name 'postgresql18_[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9]' |
    LC_ALL=C sort | tail -n 1) || true
  [ -n "$dir" ] || die "no postgresql18_YYYYMMDD-HHMM directory under $BW_DUMP_ROOT" 3
  BW_DUMP="$dir/$BW_DB.dump"
fi
[ -s "$BW_DUMP" ] || die "dump not found or empty: $BW_DUMP" 3
age_s=$(($(date +%s) - $(stat -c %Y "$BW_DUMP")))
[ "$age_s" -le $((BW_MAX_AGE_HOURS * 3600)) ] || die "dump is $((age_s / 3600))h old (limit ${BW_MAX_AGE_HOURS}h): $BW_DUMP" 3
[ "$age_s" -ge $((BW_MIN_AGE_MINUTES * 60)) ] || die "dump is younger than ${BW_MIN_AGE_MINUTES} min, maybe still being written: $BW_DUMP" 3
age_h=$((age_s / 3600))
log "dump: $BW_DUMP (${age_h}h old, $(stat -c %s "$BW_DUMP") bytes)"

# --- the scratch URL -------------------------------------------------------------
# The live URL comes from the running container with only the database name swapped. A query
# parameter that names a database or service would override the path, so such a URL is refused.
url=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$BW_CONTAINER" | sed -n 's/^BOOKWARDEN_DATABASE_URL=//p') ||
  die "container $BW_CONTAINER not found" 3
[ -n "$url" ] || die "BOOKWARDEN_DATABASE_URL not set on $BW_CONTAINER" 3
base=${url%%\?*}
query=""
[ "$base" = "$url" ] || query=${url#*\?}
case "&$query" in
  *"&dbname="* | *"&database="* | *"&service="*) die "the database URL names a database or service in its query; not drilling" 3 ;;
esac
case $base in
  */"$BW_DB") drill_url="${base%/"$BW_DB"}/$BW_DRILL_DB" ;;
  *) die "the database URL's path is not /$BW_DB" 3 ;;
esac
[ -z "$query" ] || drill_url="$drill_url?$query"
IMAGE=$(docker inspect -f '{{.Image}}' "$BW_CONTAINER") || die "container $BW_CONTAINER not found" 3

exists=$(psql_on postgres "select count(*) from pg_database where datname = '$BW_DRILL_DB'") || die "cannot query $BW_PG_CONTAINER" 3
[ "$exists" = 0 ] || die "scratch database $BW_DRILL_DB already exists; remove it by hand after checking why" 3
OWNER=$(psql_on "$BW_DB" "select pg_catalog.pg_get_userbyid(datdba) from pg_catalog.pg_database where datname = current_database()") ||
  die "cannot read the owner of $BW_DB" 3

TMP=$(mktemp -d)
printf 'BOOKWARDEN_DATABASE_URL=%s\n' "$drill_url" >"$TMP/env"
unset url base query drill_url
CREATED=0
cleanup() {
  trap '' HUP INT TERM
  rm -rf "$TMP"
  if [ "$CREATED" = 1 ]; then
    if docker exec "$BW_PG_CONTAINER" dropdb -U "$BW_PG_USER" --force --if-exists "$BW_DRILL_DB"; then
      log "scratch database $BW_DRILL_DB removed"
    else
      log "warning: could not remove $BW_DRILL_DB; remove it by hand"
    fi
  fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# --- restore -------------------------------------------------------------------
docker exec "$BW_PG_CONTAINER" createdb -U "$BW_PG_USER" -T template0 -O "$OWNER" "$BW_DRILL_DB" || die "createdb failed"
CREATED=1
start=$SECONDS
docker exec -i "$BW_PG_CONTAINER" pg_restore -U "$BW_PG_USER" --single-transaction -d "$BW_DRILL_DB" <"$BW_DUMP" ||
  die "pg_restore failed"
log "restored in $((SECONDS - start))s"

# --- verify seals ----------------------------------------------------------------
start=$SECONDS
verify_rc=0
docker run --rm --read-only --network "$BW_NETWORK" --env-file "$TMP/env" "$IMAGE" verify-seals || verify_rc=$?
rm -f "$TMP/env"
log "verify-seals exit $verify_rc in $((SECONDS - start))s"

counts=$(psql_on "$BW_DRILL_DB" "select (select count(*) from runs) || ' runs, ' || (select count(*) from evidence) || ' sealed books, ' || (select coalesce(max(version), 0) from schema_migrations) || ' schema'") || counts="unknown"
log "drill database: $counts"
# verify-seals passes on a database with nothing in it: an empty or wrong dump must fail.
runs=$(psql_on "$BW_DRILL_DB" "select count(*) from runs") || runs=unknown
[[ $runs =~ ^[0-9]+$ ]] || runs=unknown

result=passed
[ "$verify_rc" = 0 ] && [ "$runs" != unknown ] && [ "$runs" -ge "$BW_MIN_RUNS" ] || result=FAILED
if [ -n "$BW_CHANGELOG" ]; then
  printf '%s bookwarden restore drill %s: %s (dump %sh old); %s\n' "$(date '+%F %T')" "$result" "$BW_DUMP" "$age_h" "$counts" >>"$BW_CHANGELOG"
fi
[ "$verify_rc" = 0 ] || die "seal verification failed on the restored database"
[ "$runs" != unknown ] || die "could not count the runs in the restored database"
[ "$runs" -ge "$BW_MIN_RUNS" ] || die "the restored database has $runs runs, fewer than BW_MIN_RUNS=$BW_MIN_RUNS: an empty or wrong dump"
log "restore drill passed (dump ${age_h}h old)"
if [ -n "$BW_KUMA_URL" ]; then
  # The URL holds the push token: never printed, and curl's own messages could name it. It goes
  # in as a config on stdin, so it isn't in curl's arguments either (ps, bash -x).
  printf 'url = "%s"\n' "$BW_KUMA_URL" | curl -fsS -m 10 -K - >/dev/null 2>&1 || log "warning: the Kuma push failed (curl exit $?)"
fi
