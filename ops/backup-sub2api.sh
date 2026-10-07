#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
case "${1:-}" in
  --help|-h)
    printf '%s\n' 'Usage: backup-sub2api.sh' 'Back up PostgreSQL, indexed images, configuration and the exact running release.' 'Validate before publishing latest.tar.gz; retain successful backups for seven days.'
    exit 0 ;;
  '') ;;
  *) printf 'Unknown argument: %s\n' "$1" >&2; exit 2 ;;
esac
DEPLOY_DIR=/opt/sub2api-deploy
BACKUP_ROOT=/home/fengyong/backups/sub2api-scheduled
SCRIPT_DIR=$(dirname "$(readlink -f "$0")")
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
DEST="$BACKUP_ROOT/$STAMP"
ARCHIVE="$DEST.tar.gz"
LOG="$BACKUP_ROOT/backup.log"
log(){ printf '%s %s\n' "$(date -Is)" "$*" | tee -a "$LOG"; }
exec 9>"$DEPLOY_DIR/.sub2api-backup.lock"
flock -n 9 || { printf 'Another backup is running\n' >&2; exit 1; }
exec 8>"$DEPLOY_DIR/.sub2api-deploy.lock"
flock -sn 8 || { printf 'A release is being switched; retry backup later\n' >&2; exit 1; }
mkdir -p "$BACKUP_ROOT"
test ! -e "$ARCHIVE"
mkdir -m 700 "$DEST"
PUBLISHED=0
cleanup(){
  status=$?
  trap - EXIT
  rm -rf -- "$DEST"
  rm -f -- "$ARCHIVE.partial" "$BACKUP_ROOT/latest.tar.gz.next"
  if [ "$status" -ne 0 ]; then
    if [ "$PUBLISHED" = 1 ]; then
      log 'ERROR backup published successfully, but post-publication maintenance failed'
    else
      log 'ERROR backup failed; previous latest preserved'
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
log "START backup dest=$DEST"
cd "$DEPLOY_DIR"
docker compose --env-file .env -f docker-compose.yml ps >/dev/null
cp -a .env docker-compose.yml "$DEST/"
if [ -f data/config.yaml ]; then cp -a data/config.yaml "$DEST/config.yaml"; fi
mkdir -p "$DEST/ops/lib"
for script in backup-sub2api.sh guarded-docker.py probe.py verify-backup.py README.md; do
  cp -a "$SCRIPT_DIR/$script" "$DEST/ops/"
done
cp -a "$SCRIPT_DIR/lib/backup_state.py" "$DEST/ops/lib/"
docker exec sub2api-postgres psql -U sub2api -d sub2api -XAt -v ON_ERROR_STOP=1 \
  -c "select key||chr(9)||coalesce(value,chr(60)||chr(110)||chr(117)||chr(108)||chr(108)||chr(62)) from settings order by key;" > "$DEST/settings.tsv"
curl -fsS --max-time 10 http://127.0.0.1:8080/api/v1/settings/public > "$DEST/public-settings.json"
# Redis is supplemental cache state, separate from the DB/image snapshot.
if docker exec sub2api-redis redis-cli SAVE >/dev/null 2>&1; then
  docker cp sub2api-redis:/data/dump.rdb "$DEST/redis-dump.rdb"
else
  log 'WARN Redis SAVE unavailable; database and image snapshot remain required'
fi
if ! docker cp sub2api-redis:/data/appendonlydir "$DEST/redis-appendonlydir" >/dev/null 2>&1; then
  log 'WARN Redis AOF copy unavailable'
fi
docker inspect sub2api sub2api-postgres sub2api-redis > "$DEST/docker-inspect.json"
python3 "$SCRIPT_DIR/lib/backup_state.py" "$DEPLOY_DIR" "$DEST"
tar -C "$BACKUP_ROOT" -czf "$ARCHIVE.partial" "$STAMP"
python3 "$SCRIPT_DIR/verify-backup.py" "$ARCHIVE.partial"
mv "$ARCHIVE.partial" "$ARCHIVE"
sha256sum "$ARCHIVE" > "$ARCHIVE.sha256"
ln -s "$ARCHIVE" "$BACKUP_ROOT/latest.tar.gz.next"
mv -Tf "$BACKUP_ROOT/latest.tar.gz.next" "$BACKUP_ROOT/latest.tar.gz"
PUBLISHED=1
find "$BACKUP_ROOT" -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.tar.gz.sha256' \) -mtime +7 -delete
log "OK backup archive=$ARCHIVE bytes=$(stat -c %s "$ARCHIVE")"
