#!/usr/bin/env bash
# Runs once, on first start with an empty data directory.
# Creates the application role and database and enables pgvector by running
# app-role.psql, the one SQL source shared with `claude-memory install`
# (bootstrap.sql). The .psql extension keeps the Postgres image's entrypoint
# from running that file a second time on its own.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
  -v app_db="$APP_DB_NAME" -v app_user="$APP_DB_USER" -v app_pw="$APP_DB_PASSWORD" \
  -f "$here/app-role.psql"
