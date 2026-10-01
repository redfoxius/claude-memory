#!/usr/bin/env bash
# Runs once, on first start with an empty data directory.
# Creates the application role and database and enables pgvector.
# The app role is not a superuser; the extension is created here because
# pgvector is not a trusted extension.
set -euo pipefail

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
  -v app_db="$APP_DB_NAME" -v app_user="$APP_DB_USER" -v app_pw="$APP_DB_PASSWORD" <<'SQL'
CREATE ROLE :"app_user" LOGIN PASSWORD :'app_pw';
CREATE DATABASE :"app_db" OWNER :"app_user";
REVOKE ALL ON DATABASE :"app_db" FROM PUBLIC;
SQL

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$APP_DB_NAME" \
  -v app_user="$APP_DB_USER" <<'SQL'
CREATE EXTENSION IF NOT EXISTS vector;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT ALL ON SCHEMA public TO :"app_user";
SQL
