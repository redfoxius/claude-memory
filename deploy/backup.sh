#!/usr/bin/env bash
# Daily logical backup of the claude-memory database, 14-day retention.
# Run on the server by systemd/claude-memory-backup.timer.
set -euo pipefail

CONTAINER="${CONTAINER:-claude-memory-postgres}"
DB="${APP_DB_NAME:-claude_memory}"
DIR="${BACKUP_DIR:-/mnt/data/backups/claude-memory}"
KEEP_DAYS="${KEEP_DAYS:-14}"

mountpoint -q /mnt/data || { echo "/mnt/data is not mounted, skipping backup" >&2; exit 1; }
mkdir -p "$DIR"

ts="$(date -u +%Y%m%dT%H%M%SZ)"
tmp="$DIR/.${DB}-${ts}.dump.partial"
out="$DIR/${DB}-${ts}.dump"

docker exec -u postgres "$CONTAINER" pg_dump -Fc -d "$DB" > "$tmp"
mv "$tmp" "$out"
chmod 600 "$out"

find "$DIR" -maxdepth 1 -name "${DB}-*.dump" -mtime +"$KEEP_DAYS" -delete
find "$DIR" -maxdepth 1 -name ".${DB}-*.dump.partial" -mtime +1 -delete

echo "backup ok: $out ($(du -h "$out" | cut -f1))"
