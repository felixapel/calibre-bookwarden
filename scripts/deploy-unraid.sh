#!/usr/bin/env bash
# Deploy a pinned Bookwarden image to an Unraid host. Run it ON the Unraid host as root,
# inside tmux or screen (a dropped ssh session must not interrupt a cutover).
#
#   deploy-unraid.sh <image@sha256:digest> <label> [note]
#
# Order: pull by digest -> preflight (the image's own version holds the label, no active run
# or write, template matches the running image) -> stop the old container and rename it to <name>-pre-<label> -> dump the database
# (consistent with the stop) -> pin the Unraid template to the digest -> recreate the container
# through Unraid's own docker-manager code -> wait for health -> check image and version ->
# remove the renamed old container -> remove all but the newest BW_KEEP_BACKUPS of its own dumps
# and template backups.
# On failure after the stop it puts the template back, removes the new container, renames
# the old one back and starts it. It never restores the database by itself.
#
# Environment (all optional unless noted):
#   BW_CONTAINER       container and template name            (bookwarden)
#   BW_TEMPLATE        Unraid user template                   (/boot/config/plugins/dockerMan/templates-user/my-$BW_CONTAINER.xml)
#   BW_PG_CONTAINER    Postgres container                     (postgresql18)
#   BW_PG_USER         Postgres role                          (REQUIRED)
#   BW_DB              database name                          (bookwarden)
#   BW_DUMP_DIR        where pre-deploy dumps go              (/mnt/user/appdata/bookwarden-backups)
#   BW_CHANGELOG       file that gets one appended line       (none)
#   BW_KEEP_BACKUPS    after a successful deploy, keep this many of the script's own
#                      pre-<label>-<timestamp> dumps and template backups; 0 keeps all (10)
#   BW_REGISTRY_USER   registry login; the token is read from stdin (no login when unset)
#   BW_HEALTH_TIMEOUT  seconds to wait for healthy; migrations run first (300)
#   BW_POLL_INTERVAL   whole seconds between health polls     (2)
#   DRY_RUN            1 prints the mutating actions instead of running them; 0 or 1 (0)
#
# Exit codes: 0 deployed; 1 failed and rolled back; 2 usage; 3 preflight refused;
#             4 failed after the schema may have changed (database restore needed, see output);
#             5 failed and the rollback did not complete or did not come up healthy.
set -euo pipefail
umask 077

BW_CONTAINER=${BW_CONTAINER:-bookwarden}
BW_TEMPLATE=${BW_TEMPLATE:-/boot/config/plugins/dockerMan/templates-user/my-${BW_CONTAINER}.xml}
BW_PG_CONTAINER=${BW_PG_CONTAINER:-postgresql18}
BW_DB=${BW_DB:-bookwarden}
BW_DUMP_DIR=${BW_DUMP_DIR:-/mnt/user/appdata/bookwarden-backups}
BW_CHANGELOG=${BW_CHANGELOG:-}
BW_KEEP_BACKUPS=${BW_KEEP_BACKUPS:-10}
BW_REGISTRY_USER=${BW_REGISTRY_USER:-}
BW_HEALTH_TIMEOUT=${BW_HEALTH_TIMEOUT:-300}
BW_POLL_INTERVAL=${BW_POLL_INTERVAL:-2}
DRY_RUN=${DRY_RUN:-0}

log() { printf '%s\n' "$*"; }
die() { log "error: $1" >&2; exit "${2:-1}"; }

if [ $# -lt 2 ] || [ $# -gt 3 ]; then
  die "usage: $0 <image@sha256:digest> <label> [note]" 2
fi
IMAGE=$1
LABEL=$2
NOTE=${3:-}
[ -n "${BW_PG_USER:-}" ] || die "BW_PG_USER is required" 2
[[ $IMAGE =~ ^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$ ]] || die "image must look like repo@sha256:<64 hex>" 2
[[ $LABEL =~ ^[A-Za-z0-9._-]{7,}$ ]] || die "label must be at least 7 characters of letters, digits, . _ -" 2
[[ $BW_HEALTH_TIMEOUT =~ ^[0-9]+$ ]] || die "BW_HEALTH_TIMEOUT must be a whole number of seconds" 2
[[ $BW_POLL_INTERVAL =~ ^[1-9][0-9]*$ ]] || die "BW_POLL_INTERVAL must be a positive whole number of seconds" 2
[[ $DRY_RUN =~ ^[01]$ ]] || die "DRY_RUN must be 0 or 1" 2
# One canonical 0, and at most 6 digits, so no value meant as "keep all" can compare as "keep none".
[[ $BW_KEEP_BACKUPS =~ ^(0|[1-9][0-9]{0,5})$ ]] || die "BW_KEEP_BACKUPS must be 0 (keep all) or a whole number up to 999999" 2
# The backup pruning reads one path per line.
case "$BW_DUMP_DIR$BW_TEMPLATE" in *$'\n'*) die "BW_DUMP_DIR and BW_TEMPLATE must not contain a newline" 2 ;; esac

psql_q() { docker exec "$BW_PG_CONTAINER" psql -U "$BW_PG_USER" -d "$BW_DB" -Atc "$1"; }
schema_version() { psql_q "select coalesce(max(version)::text, 'none') from schema_migrations"; }

TMP=$(mktemp -d)
LOGGED_IN=0
cleanup() {
  if [ "$LOGGED_IN" = 1 ]; then
    docker logout "$REGISTRY" >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

OLD_NAME="${BW_CONTAINER}-pre-${LABEL}"
TS=$(date +%Y%m%d-%H%M%S)
DUMP="$BW_DUMP_DIR/pre-$LABEL-$TS.dump"
TPL_BAK="$(dirname "$BW_TEMPLATE")/.$(basename "$BW_TEMPLATE").bak-pre-$LABEL-$TS"
REGISTRY=${IMAGE%%/*}
BACKED_UP=0
STOP_TRIED=0
RENAMED=0
NEW_CREATED=0

# --- pull ------------------------------------------------------------------
if [ "$DRY_RUN" = 1 ]; then
  [ -z "$BW_REGISTRY_USER" ] || log "[dry-run] docker login $REGISTRY -u <BW_REGISTRY_USER> --password-stdin (token from stdin)"
  log "[dry-run] docker pull -q $IMAGE"
  [ -z "$BW_REGISTRY_USER" ] || log "[dry-run] docker logout $REGISTRY"
  NEW_IMAGE_ID=""
else
  if [ -n "$BW_REGISTRY_USER" ]; then
    docker login "$REGISTRY" -u "$BW_REGISTRY_USER" --password-stdin >/dev/null || die "registry login failed"
    LOGGED_IN=1
  fi
  pull_rc=0
  docker pull -q "$IMAGE" >/dev/null || pull_rc=$?
  if [ "$LOGGED_IN" = 1 ]; then
    docker logout "$REGISTRY" >/dev/null 2>&1 || true
    LOGGED_IN=0
  fi
  [ "$pull_rc" = 0 ] || die "pull failed for $IMAGE"
  NEW_IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$IMAGE") || die "cannot read the id of the pulled image"
fi

# --- preflight -------------------------------------------------------------
# The pulled image must print the label before anything changes: a wrong image is refused
# while the old container still runs. The check after the start stays.
VERSION_CMD=(docker run --rm --network none --read-only --entrypoint /usr/local/bin/bookwarden "$IMAGE" version)
if [ "$DRY_RUN" = 1 ]; then
  log "[dry-run] ${VERSION_CMD[*]}; require $LABEL in its output"
else
  img_ver=$("${VERSION_CMD[@]}" 2>&1) || die "cannot run the image's version command (${img_ver:-no output}); nothing was changed" 3
  log "image version: $img_ver"
  case $img_ver in
    *"$LABEL"*) ;;
    *) die "the image's version does not contain the label; nothing was changed" 3 ;;
  esac
fi
[ -f "$BW_TEMPLATE" ] || die "template not found: $BW_TEMPLATE" 3
[ ! -e "$DUMP" ] || die "dump file already exists: $DUMP" 3
[ ! -e "$TPL_BAK" ] || die "template backup already exists: $TPL_BAK" 3

n_repo=$(grep -o '<Repository>[^<]*</Repository>' "$BW_TEMPLATE" | wc -l)
[ "$n_repo" = 1 ] || die "template has $n_repo <Repository> elements, expected exactly 1" 3
TPL_REPO=$(grep -o '<Repository>[^<]*</Repository>' "$BW_TEMPLATE" | sed 's#</\{0,1\}Repository>##g')

PREV_IMAGE=$(docker inspect -f '{{.Image}}' "$BW_CONTAINER" 2>/dev/null) || die "container $BW_CONTAINER not found" 3
TPL_IMAGE=$(docker image inspect -f '{{.Id}}' "$TPL_REPO" 2>/dev/null) || die "template image $TPL_REPO is not available locally" 3
[ "$TPL_IMAGE" = "$PREV_IMAGE" ] || die "template image $TPL_REPO is not the image $BW_CONTAINER is running; a rollback would start the wrong version" 3

active=$(psql_q "select count(*) from runs where status in ('running','waiting')") || die "cannot query $BW_DB" 3
[ "$active" = 0 ] || die "a run is active ($active); not deploying" 3
writes=$(psql_q "select count(*) from operations o where (select e.state from operation_events e where e.operation_id = o.id order by e.seq desc limit 1) in ('requested','writing','verifying','restoring')") || die "cannot query $BW_DB" 3
[ "$writes" = 0 ] || die "a write is in progress ($writes operation(s)); not deploying" 3
SCHEMA_BEFORE=$(schema_version) || die "cannot query $BW_DB" 3
OWNER=$(psql_q "select pg_catalog.pg_get_userbyid(datdba) from pg_catalog.pg_database where datname = current_database()") || die "cannot query $BW_DB" 3
[ -n "$OWNER" ] || die "cannot read the owner of $BW_DB" 3
log "running image id: $PREV_IMAGE; schema version: $SCHEMA_BEFORE; database owner: $OWNER"

if [ "$DRY_RUN" = 1 ]; then
  log "[dry-run] docker stop -t 40 $BW_CONTAINER; docker rename $BW_CONTAINER $OLD_NAME"
  log "[dry-run] pg_dump -Fc into $DUMP, chmod 600, verify with pg_restore -f /dev/null"
  log "[dry-run] cp -p $BW_TEMPLATE $TPL_BAK; pin <Repository> to $IMAGE"
  log "[dry-run] recreate $BW_CONTAINER through Unraid's template code; wait up to ${BW_HEALTH_TIMEOUT}s for healthy"
  log "[dry-run] check the image id, check '$BW_CONTAINER version' prints $LABEL; docker rm $OLD_NAME; change log"
  if [ "$BW_KEEP_BACKUPS" = 0 ]; then
    log "[dry-run] keep every pre-deploy dump and template backup"
  else
    log "[dry-run] keep the newest $BW_KEEP_BACKUPS pre-<label>-<timestamp> dumps in $BW_DUMP_DIR and template backups of $BW_TEMPLATE; remove older ones"
  fi
  log "dry run finished; nothing was changed"
  exit 0
fi

# --- helpers for the cutover -------------------------------------------------
cat >"$TMP/recreate.php" <<'PHP'
<?php
$docroot = "/usr/local/emhttp";
require_once "$docroot/plugins/dynamix.docker.manager/include/DockerClient.php";
$var = parse_ini_file("/var/local/emhttp/var.ini");
$custom = DockerUtil::custom();
$subnet = DockerUtil::network($custom);
$cpus = DockerUtil::cpus();
[$cmd] = xmlToCommand(getenv("BW_TEMPLATE"));
exec(str_replace("/docker create ", "/docker run -d ", $cmd) . " >/dev/null 2>&1", $o, $rc);
echo "run exit $rc\n";
exit($rc === 0 ? 0 : 1);
PHP

wait_healthy() {
  local deadline=$((SECONDS + BW_HEALTH_TIMEOUT)) st
  while :; do
    st=$(docker inspect -f '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}nohealthcheck{{end}}' "$BW_CONTAINER" 2>/dev/null) || st="missing"
    case $st in
      *" healthy") return 0 ;;
      *" nohealthcheck")
        log "$BW_CONTAINER has no healthcheck, so the deploy cannot be verified"
        return 1
        ;;
      *" unhealthy" | exited* | dead* | missing)
        log "container state: $st"
        return 1
        ;;
    esac
    if [ "$SECONDS" -ge "$deadline" ]; then
      log "timed out after ${BW_HEALTH_TIMEOUT}s (state: $st)"
      return 1
    fi
    sleep "$BW_POLL_INTERVAL" || return 1
  done
}

dump_db() {
  mkdir -p "$BW_DUMP_DIR" || return 1
  if ! docker exec "$BW_PG_CONTAINER" pg_dump -U "$BW_PG_USER" -d "$BW_DB" -Fc >"$DUMP"; then
    rm -f "$DUMP"
    log "pg_dump failed"
    return 1
  fi
  chmod 600 "$DUMP" || return 1
  if [ ! -s "$DUMP" ]; then
    rm -f "$DUMP"
    log "dump is empty"
    return 1
  fi
  # -f /dev/null makes pg_restore read every block, not just the table of contents.
  if command -v pg_restore >/dev/null 2>&1; then
    pg_restore -f /dev/null "$DUMP" >/dev/null || {
      log "dump does not verify: $DUMP"
      return 1
    }
  else
    docker exec -i "$BW_PG_CONTAINER" pg_restore -f /dev/null <"$DUMP" >/dev/null || {
      log "dump does not verify: $DUMP"
      return 1
    }
  fi
  log "dump verified: $DUMP"
}

pin_template() {
  cp -p "$BW_TEMPLATE" "$TPL_BAK" || {
    log "cannot back up the template"
    return 1
  }
  BACKED_UP=1
  # IMAGE is validated above to hold only [A-Za-z0-9._:/@-], so it is safe in sed.
  sed -i "s#<Repository>[^<]*</Repository>#<Repository>$IMAGE</Repository>#" "$BW_TEMPLATE" || return 1
  if [ "$(grep -c "<Repository>$IMAGE</Repository>" "$BW_TEMPLATE")" != 1 ]; then
    log "template pin did not take"
    return 1
  fi
}

cutover() {
  local newid ver
  log "stopping $BW_CONTAINER and renaming it to $OLD_NAME"
  STOP_TRIED=1
  docker stop -t 40 "$BW_CONTAINER" >/dev/null || {
    log "could not stop $BW_CONTAINER"
    return 1
  }
  docker rename "$BW_CONTAINER" "$OLD_NAME" || return 1
  RENAMED=1
  dump_db || return 1
  log "pinning template to $IMAGE (backup: $TPL_BAK)"
  pin_template || return 1
  NEW_CREATED=1
  BW_CONTAINER="$BW_CONTAINER" BW_TEMPLATE="$BW_TEMPLATE" php "$TMP/recreate.php" || {
    log "recreating the container failed"
    return 1
  }
  log "waiting for health (up to ${BW_HEALTH_TIMEOUT}s)"
  wait_healthy || return 1
  newid=$(docker inspect -f '{{.Image}}' "$BW_CONTAINER") || return 1
  if [ "$newid" != "$NEW_IMAGE_ID" ]; then
    log "the new container runs image $newid, not the pulled $NEW_IMAGE_ID"
    return 1
  fi
  ver=$(docker exec "$BW_CONTAINER" bookwarden version 2>&1) || {
    log "version command failed"
    return 1
  }
  log "$ver"
  case $ver in
    *"$LABEL"*) ;;
    *)
      log "version output does not contain label $LABEL"
      return 1
      ;;
  esac
}

print_restore() {
  log "The database was NOT restored. The new container is stopped. To restore the pre-deploy dump"
  log "(this discards everything written since the dump), run:"
  log "  docker exec $BW_PG_CONTAINER dropdb -U $BW_PG_USER $BW_DB"
  log "  docker exec $BW_PG_CONTAINER createdb -U $BW_PG_USER -O $OWNER $BW_DB"
  log "  docker exec -i $BW_PG_CONTAINER pg_restore -U $BW_PG_USER --exit-on-error --single-transaction -d $BW_DB < $DUMP"
  log "Then put the old container back:"
  log "  docker rm $BW_CONTAINER && docker rename $OLD_NAME $BW_CONTAINER && docker start $BW_CONTAINER"
}

rollback() {
  local after changed=0 failed=0 cur
  log "deploy failed; rolling back"

  if [ "$NEW_CREATED" = 1 ]; then
    docker stop -t 40 "$BW_CONTAINER" >/dev/null 2>&1 || log "warning: could not stop the new container"
    if after=$(schema_version) && [ -n "$after" ]; then
      if [ "$after" != "$SCHEMA_BEFORE" ]; then
        log "the schema version changed ($SCHEMA_BEFORE -> $after)"
        changed=1
      fi
    else
      log "could not read schema version; assuming it changed"
      changed=1
    fi
  fi

  if [ "$BACKED_UP" = 1 ]; then
    if cp -p "$TPL_BAK" "$BW_TEMPLATE"; then
      log "template restored from $TPL_BAK"
    else
      log "template NOT restored; copy $TPL_BAK over $BW_TEMPLATE by hand"
      failed=1
    fi
  fi

  if [ "$changed" = 1 ]; then
    log "the previous binary cannot start on a newer schema, so it was not started."
    print_restore
    return 4
  fi

  if [ "$RENAMED" = 1 ]; then
    if docker inspect "$BW_CONTAINER" >/dev/null 2>&1; then
      docker rm -f "$BW_CONTAINER" >/dev/null || {
        log "could not remove the new container"
        return 5
      }
    fi
    docker rename "$OLD_NAME" "$BW_CONTAINER" || {
      log "could not rename $OLD_NAME back to $BW_CONTAINER"
      return 5
    }
  elif [ "$STOP_TRIED" != 1 ]; then
    log "container was not touched"
    return 1
  fi
  docker start "$BW_CONTAINER" >/dev/null || {
    log "could not start the previous container"
    return 5
  }
  wait_healthy || {
    log "the previous container is not healthy; check 'docker logs $BW_CONTAINER'"
    return 5
  }
  cur=$(docker inspect -f '{{.Image}}' "$BW_CONTAINER" 2>/dev/null) || cur=unknown
  if [ "$cur" != "$PREV_IMAGE" ]; then
    log "the container now runs image $cur, not the previous $PREV_IMAGE"
    return 5
  fi
  [ "$failed" = 0 ] || return 5
  log "previous container is healthy again (image $PREV_IMAGE)"
  return 1
}

on_signal() {
  trap '' HUP INT TERM
  log "signal received during the cutover; rolling back"
  local rc=0
  rollback || rc=$?
  exit "$rc"
}

trap on_signal HUP INT TERM
if ! cutover; then
  rc=0
  rollback || rc=$?
  exit "$rc"
fi
trap - HUP INT TERM

# --- done ------------------------------------------------------------------
docker rm "$OLD_NAME" >/dev/null || log "warning: could not remove the old container $OLD_NAME; remove it by hand"
log "schema version: $(schema_version || echo unknown) (was $SCHEMA_BEFORE)"
if [ -n "$BW_CHANGELOG" ]; then
  printf '%s bookwarden redeployed with %s (%s; %s); dump %s\n' "$(date '+%F %T')" "$LABEL" "$IMAGE" "$NOTE" "$DUMP" >>"$BW_CHANGELOG"
fi

# prune_old <dir> <name prefix> <suffix regex>: removes all but the newest $BW_KEEP_BACKUPS files
# named <prefix><label>-YYYYMMDD-HHMMSS<suffix>, newest by that timestamp. Other files, such as
# hand-made dumps, never match. This deploy's own dump and backup are always kept.
prune_old() {
  local dir=$1 prefix=$2 suffix=$3 f name n=0
  [ "$BW_KEEP_BACKUPS" = 0 ] && return 0
  while read -r _ f; do
    n=$((n + 1))
    if [ "$n" -le "$BW_KEEP_BACKUPS" ] || [ "$f" = "$DUMP" ] || [ "$f" = "$TPL_BAK" ]; then continue; fi
    if rm -f -- "$f"; then log "removed old backup $f"; else log "warning: could not remove $f"; fi
  done < <(
    for f in "$dir"/"$prefix"*; do
      name=${f##*/}
      [[ $name =~ ^[A-Za-z0-9._-]+-([0-9]{8}-[0-9]{6})${suffix}$ ]] && printf '%s %s\n' "${BASH_REMATCH[1]}" "$f"
    done | LC_ALL=C sort -r
  )
}
prune_old "$BW_DUMP_DIR" "pre-" '\.dump'
prune_old "$(dirname "$BW_TEMPLATE")" ".$(basename "$BW_TEMPLATE").bak-pre-" ''
log "deployed $LABEL; template backup: $TPL_BAK; dump: $DUMP"
