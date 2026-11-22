#!/usr/bin/env bash
# Nightly PostgreSQL backup with retention (spec §7: scheduled backups, tested recovery).
# cron:  15 1 * * *  /opt/btn-ops/deploy/backup.sh >> /var/log/btn-backup.log 2>&1
# Restore test (do this monthly into a scratch DB):
#   createdb btn_ops_restore_test && pg_restore -d btn_ops_restore_test --no-owner /var/backups/btn-ops/<file>.dump
set -euo pipefail
: "${DATABASE_URL:?DATABASE_URL must be set (e.g. set -a; . /opt/btn-ops/backend/.env; set +a)}"
DEST="${BACKUP_DIR:-/var/backups/btn-ops}"
KEEP_DAYS="${BACKUP_KEEP_DAYS:-30}"
mkdir -p "$DEST"
STAMP="$(TZ=Africa/Kigali date +%Y%m%d-%H%M)"
FILE="$DEST/btn_ops-$STAMP.dump"
# DATABASE_URL is a libpq URL (e.g. ?sslmode=require), which pg_dump accepts as-is.
pg_dump --format=custom --no-owner --file="$FILE" "$DATABASE_URL"
pg_restore --list "$FILE" > /dev/null   # verify the archive is readable
find "$DEST" -name 'btn_ops-*.dump' -mtime +"$KEEP_DAYS" -delete
echo "$(date -Is) backup ok: $FILE ($(du -h "$FILE" | cut -f1))"
# Copy off-site, e.g.: rclone copy "$FILE" remote:btn-ops-backups/
