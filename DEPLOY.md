# Claude Memory Deployment

> **Laptop side: use `claude-memory install`** (and `claude-memory install --upgrade`
> after a new build); `--dry-run` shows the plan first, `claude-memory doctor`
> checks the result. It sets up the env file, database role, schema, Ollama model,
> namespaces, hooks, MCP registration, skills, the CLAUDE.md block and, on macOS,
> the scheduled jobs. The manual steps below (and in `integration/INSTALL.md`) are the
> reference for what it does; the server side (Docker, Postgres, Tailscale) stays manual.

## Topology B (v0.2)

**Laptop** (macOS, arm64): Ollama + `bge-m3` (Homebrew, Metal GPU), `claude-memory` MCP server (stdio), and all CLI subcommands. Embedding and extraction run locally; all data writes reach the server over Tailscale. **Server** (home x86 server, Ubuntu 26.04): Postgres 16 + pgvector only, bound to Tailscale IP, reachable only via password auth (scram-sha-256) and network-restricted to `100.64.0.0/10` CGNAT. No Ollama, no MCP listener on the server. This topology is why we measure acceptable performance on the laptop (12–60× faster than server's AVX-only CPU); see AC-44 baseline below.

## Server Prerequisites

- **Docker CE**: Installed with `data-root` set to `/mnt/data/docker` (in `/etc/docker/daemon.json`: `{"data-root": "/mnt/data/docker", "ip": "127.0.0.1"}`). Restart daemon to apply.
- **Tailscale**: Already running (`tailscale up`). Confirm with `tailscale ip -4` (returns your server's Tailscale IP, e.g., `100.64.0.10`).
- **UFW firewall**: Allow Postgres on Tailscale interface only: `sudo ufw allow in on tailscale0 to any port 5432`. (Host networking means `ufw` rules apply; docker-proxy is not used.)
- **Kernel setting**: `sudo sysctl net.ipv4.ip_nonlocal_bind=1` and persist to `/etc/sysctl.d/60-claude-memory.conf` with:
  ```
  net.ipv4.ip_nonlocal_bind = 1
  ```
  Why: Postgres binds to the Tailscale IP via host networking. If Postgres starts before Tailscale has assigned that IP on bootup, the bind fails silently without this flag. With it, the kernel allows binding to an IP not yet present on any interface; Tailscale assigns it later, and the bind becomes live.
- **Data directories** (owned by root, created once):
  ```bash
  sudo mkdir -p /var/lib/claude-memory/pgdata
  sudo chmod 0700 /var/lib/claude-memory/pgdata
  sudo chown 999:999 /var/lib/claude-memory/pgdata
  
  sudo mkdir -p /mnt/data/backups/claude-memory
  sudo chmod 0755 /mnt/data/backups/claude-memory
  ```
  The `999:999` UID/GID is the postgres user inside the container; full permissions (`0700`) are required.

## Installation

1. **Clone the repo** into `/opt/claude-memory`:
   ```bash
   sudo git clone https://github.com/your-org/claude-memory.git /opt/claude-memory
   sudo chown -R your-user:your-group /opt/claude-memory
   ```

2. **Create `.env`** from the example:
   ```bash
   cd /opt/claude-memory
   cp deploy/.env.example deploy/.env
   chmod 600 deploy/.env
   ```
   Edit `deploy/.env` and fill in:
   - `POSTGRES_BIND_IP`: Run `tailscale ip -4` and copy the IPv4 address (e.g., `100.64.0.10`).
   - `POSTGRES_PASSWORD`: Generate with `openssl rand -hex 32`. Store securely; you will not need it again after initial setup (laptops use the app role instead).
   - `APP_DB_PASSWORD`: Generate with `openssl rand -hex 32`. Store this; you will need it on the laptop.

   Use the hex form: it is URL-safe. A `base64` password can contain `/`, `+` and `=`, and a `/` in the userinfo of the laptop's `postgresql://` DSN ends the host part, so the DSN no longer parses (`claude-memory doctor` reports it as `env.format: fail`; the fix is to URL-encode it, `/` → `%2F`, or rotate to a hex password).

3. **Start the container**:
   ```bash
   cd /opt/claude-memory
   docker compose up -d
   ```

4. **Verify health**:
   ```bash
   docker compose ps
   # Output: claude-memory-postgres should show status "Up ... (healthy)"
   
   ss -tlnp | grep 5432
   # Output: tcp LISTEN  127.0.0.1:5432 and <POSTGRES_BIND_IP>:5432
   # (no 172.x or docker-proxy entries)
   
   docker compose logs postgres | grep "connection authorized"
   # Output: Should show "connection authorized: user=claude_memory database=claude_memory hostaddr=100.x..."
   # (the laptop's Tailscale address)
   ```

## Why Host Networking

The `docker-compose.yml` uses `network_mode: host` instead of the default bridge network. Reason: with bridge networking and published ports, Docker's `docker-proxy` component rewrites the source IP of incoming connections from the laptop (real Tailscale IP `100.x`) to an internal docker bridge range (`172.x`). Postgres's `pg_hba.conf` is configured to accept only the Tailscale CGNAT range `100.64.0.0/10`, so every laptop connection was rejected after source rewriting.

Host networking bypasses docker-proxy entirely, preserving the real client IP (your laptop's Tailscale IP), which `pg_hba.conf` validates correctly. Confirmed 2026-10-01: published ports → rejected connections; host networking → accepted connections.

## Resource Limits & Restart

The container is configured with:
- **Memory**: `512 MiB` limit
- **CPU**: `1.0` (one core)
- **Restart policy**: `unless-stopped` (auto-restart on crash or server reboot)
- **Healthcheck**: `pg_isready -U postgres -d postgres -h 127.0.0.1` every 10s (5s timeout, 5 retries, 20s start grace period)

These limits leave ample headroom for other pre-existing host services on the 7.2 GiB home server. Verify with `docker stats postgres` over a 24-hour window; there should be no OOM-kill or sustained CPU throttle.

## Laptop Side Configuration

1. **Install `libpq`** (for manual `psql` verification):
   ```bash
   brew install libpq
   ```

2. **Create `~/.config/claude-memory/env`** (mode 0600, never committed):
   ```bash
   mkdir -p ~/.config/claude-memory
   cat > ~/.config/claude-memory/env <<'EOF'
   MEMORY_PG_DSN=postgresql://claude_memory:<APP_DB_PASSWORD>@<POSTGRES_BIND_IP>:5432/claude_memory
   MEMORY_OLLAMA_URL=http://127.0.0.1:11434
   MEMORY_EMBED_MAX_TOKENS=2048
   # optional: staleness-check ceilings (hook / MCP server)
   # MEMORY_STALE_TIMEOUT_HOOK=50ms
   # MEMORY_STALE_TIMEOUT=500ms
   EOF
   chmod 600 ~/.config/claude-memory/env
   ```

   The format is plain `KEY=VALUE`, one per line: **no `export`, no quotes**.
   `claude-memory` reads this file itself (it is never shell-sourced): an
   `export KEY=...` line is not read at all, and quotes around a value become
   part of the value. Every subcommand except `doctor`, `version` and
   `namespaces` refuses to start when the file is group/world-readable.

   Replace:
   - `<APP_DB_PASSWORD>`: The password you generated for `APP_DB_PASSWORD` on the server (hex, so it needs no URL-encoding).
   - `<POSTGRES_BIND_IP>`: The server's Tailscale IP (e.g., `100.64.0.10`).

3. **Verify**:
   ```bash
   claude-memory doctor
   # pg.connect / pg.vector / pg.schema should pass; `claude-memory migrate`
   # applies the schema if pg.schema says it is missing or behind.
   ```
   `doctor` is read-only (it never migrates, writes or starts the MCP
   server) and prints a `fix:` line under every failing check; `--json`
   gives the same report for scripts. To check the connection with `psql`
   instead:
   ```bash
   psql "$(sed -n 's/^MEMORY_PG_DSN=//p' ~/.config/claude-memory/env)" -c "SELECT version();"
   # Output: PostgreSQL 16.x ...
   ```
   If this fails, check:
   - Tailscale is up on both laptop and server (`tailscale status`).
   - Password is correct.
   - Server firewall allows 5432 on tailscale0 (`ufw show added`).

## Backups

### Daily Systemd Timer (Server)

The backup runs daily at **04:30 UTC** (plus a random 15-minute jitter to avoid thundering herd). Install the systemd files:

```bash
sudo cp /opt/claude-memory/deploy/systemd/claude-memory-backup.service \
  /etc/systemd/system/

sudo cp /opt/claude-memory/deploy/systemd/claude-memory-backup.timer \
  /etc/systemd/system/

sudo systemctl daemon-reload
sudo systemctl enable claude-memory-backup.timer
sudo systemctl start claude-memory-backup.timer
```

Verify:
```bash
sudo systemctl status claude-memory-backup.timer
sudo systemctl list-timers claude-memory-backup.timer
```

### Manual Backup Run (Server)

```bash
/opt/claude-memory/deploy/backup.sh
# Output: backup ok: /mnt/data/backups/claude-memory/claude_memory-20261001T043015Z.dump (1.2 M)
```

The backup uses `pg_dump -Fc` (custom binary format, ~14-day retention). Backups are stored in `/mnt/data/backups/claude-memory/` with timestamp naming (`claude_memory-YYYYMMDDTHHMMSSZ.dump`).

### Restore from Backup (Server)

To restore from a backup file (e.g., after accidental data loss):

1. **Stop the container**:
   ```bash
   docker compose down
   ```

2. **Drop and recreate the application database**:
   ```bash
   docker compose up -d
   docker exec -u postgres claude-memory-postgres psql -d postgres \
     -c "DROP DATABASE IF EXISTS claude_memory;"
   docker exec -u postgres claude-memory-postgres psql -d postgres \
     -c "CREATE DATABASE claude_memory OWNER claude_memory; REVOKE ALL ON DATABASE claude_memory FROM PUBLIC;"
   docker exec -u postgres claude-memory-postgres psql -d claude_memory \
     -c "CREATE EXTENSION IF NOT EXISTS vector; GRANT ALL ON SCHEMA public TO claude_memory;"
   ```

3. **Restore from the backup file**:
   ```bash
   docker exec -u postgres claude-memory-postgres pg_restore -d claude_memory \
     --no-owner --role=claude_memory \
     < /mnt/data/backups/claude-memory/claude_memory-20261001T043015Z.dump
   ```

4. **Verify**:
   ```bash
   docker exec -u postgres claude-memory-postgres psql -d claude_memory \
     -c "SELECT COUNT(*) FROM record;"
   # Output: Should show the number of records from the backup.
   ```

The flags `--no-owner --role=claude_memory` ensure the restored objects are owned by the application role (not by `postgres`).

## Upgrade

```bash
cd /opt/claude-memory
git pull
docker compose pull
docker compose up -d
```

The container restarts and runs any new migrations on startup (via `docker-entrypoint-initdb.d` on the first start; existing databases are not re-initialized).

### Upgrading the binary across a schema change (namespaces, migration 0002)

`claude-memory` applies its own (idempotent) migrations when `serve`, `seed`, `cleanup`, `ingest-pr` or `extract` start — but **not** the prompt hook, which skips the check to stay inside its latency budget. After installing a new binary, run one of those once before relying on the hook (`claude-memory cleanup` is harmless), or just start Claude Code (the MCP server runs `serve`).

Migration 0002 adds `records.namespace` and backfills every existing record to `work`. **Before** starting Claude Code on the new binary, run `claude-memory namespaces init work='<path>/**'` (or `namespaces add`) on the laptop (see `integration/INSTALL.md` step 2a) so those records are visible from your Acme directories and new facts don't land in `global`; if some already did, use the re-home SQL below. Rolling back to an older binary after migrating requires `ALTER TABLE records ALTER COLUMN namespace SET DEFAULT 'global';` (or restoring the backup), because the old inserts don't supply a namespace.

### Usage events (migration 0003)

Migration 0003 adds the `events` table (ids, enums and numbers only; no record text). After installing the new binary run `claude-memory migrate` once, **before** restarting Claude Code sessions (several `serve` processes starting at once can race on `CREATE TABLE`), to apply it; until then the hook keeps working and its events wait in `~/.local/state/claude-memory/events/spool.jsonl`, which the next `serve`, `extract --run`, `ingest-pr` or `cleanup` drains. `claude-memory stats` prints the report; `cleanup` prunes events older than 365 days and removes stale-cache files older than 7 days. Rolling back to the previous binary is safe (it ignores the table and the spool); to remove the feature entirely, `DROP TABLE events;` and delete `~/.local/state/claude-memory/events`.

**Re-homing records** written to `global` before a mapping existed (records carry their `repo`, so it is one statement per project):

```sql
-- review first
SELECT id, title, repo FROM records WHERE namespace = 'global' AND repo = 'billing-service';
UPDATE records SET namespace = 'work' WHERE namespace = 'global' AND repo IN ('billing-service', 'catalog-service');
```

### Major Postgres Version Upgrade (if needed in the future)

Postgres major-version upgrades (e.g., 16 → 17) require a dump and restore because the on-disk format changes:

```bash
# Backup the current database
/opt/claude-memory/deploy/backup.sh

# Update the image in docker-compose.yml to the new version
# (e.g., pgvector/pgvector:pg17), then:

docker compose down
rm -rf /var/lib/claude-memory/pgdata
docker compose up -d
# Wait for healthy status
docker compose logs -f postgres

# Restore from the backup
docker exec -u postgres claude-memory-postgres pg_restore -d claude_memory \
  --no-owner --role=claude_memory \
  < /mnt/data/backups/claude-memory/claude_memory-YYYYMMDDTHHMMSSZ.dump
```

## Password Rotation

To change the application role password (for the laptop's DSN):

1. **On the server**, generate a new password (hex: URL-safe in the DSN):
   ```bash
   openssl rand -hex 32
   ```

2. **Update Postgres**:
   ```bash
   docker exec -u postgres claude-memory-postgres psql -d postgres \
     -c "ALTER ROLE claude_memory PASSWORD 'new-password';"
   ```

3. **On the laptop**, update `~/.config/claude-memory/env`:
   ```bash
   # Edit ~/.config/claude-memory/env and change the MEMORY_PG_DSN password
   # (plain KEY=VALUE line, no export, no quotes), then:
   claude-memory doctor   # pg.connect must pass
   ```

The superuser (`postgres`) password is not used by any client; if you forget it, it can only be reset by stopping the container and restarting with an environment variable override (advanced recovery; document in internal runbooks if needed).

## Performance Baseline (AC-44)

**Measured 2026-10-01** on the topology this spec mandates:

- **older x86 home server (AVX-only, Ollama `bge-m3` F16)**: 
  - 15 tokens: p50 0.23s
  - 150 tokens: p50 2.3s
  - 600 tokens: p50 12.3s (~17 ms/token, linear)
  - RSS 1434 MiB

- **Laptop M1 Pro (native Ollama, Metal GPU)**: 
  - 15 tokens: 0.02s
  - 150 tokens: 0.045s
  - 600 tokens: 0.21s
  - RSS 673 MB
  - **12–60× faster** than the server across the range.

This disparity is why Topology B runs Ollama (and the MCP server) on the laptop exclusively. The server runs only Postgres, allowing:
- Synchronous read-path hook latency budget (AC-30): p95 < 300 ms on the home Tailscale link.
- Write-path embedding latency (AC-48): p95 < 3 seconds (laptop GPU embedding + tailnet Postgres persistence).

## Security

### Network Isolation (AC-52, AC-54)

- **Postgres binds only to `127.0.0.1` and the server's Tailscale IP** (`100.64.0.10` in this example). No public interface, no `0.0.0.0`.
- **`pg_hba.conf` restricts connection to**:
  - `localhost` (peer auth for docker-exec backups/admin).
  - `127.0.0.1` (healthcheck).
  - `100.64.0.0/10` (Tailscale CGNAT range; laptops only).
  - **Everything else is rejected** (explicit `reject` rule for `0.0.0.0/0` and `::/0`).
- **Authentication**: `scram-sha-256` password auth (salted, hashed; passwords are never stored in plaintext or sent over the wire).
- **Verify network isolation**:
  ```bash
  sudo ss -tlnp | grep 5432
  # Output should show ONLY 127.0.0.1:5432 and 100.64.0.10:5432
  # (no 0.0.0.0, no docker-proxy)
  ```

### Secrets in Environment

- **`.env` file** (`deploy/.env`) is mode `0600` (readable by root and the docker user only). Never committed.
- **Laptop config** (`~/.config/claude-memory/env`) is mode `0600` (readable by the user only).
- **Postgres DSN with password** is read from the env file by `claude-memory` itself (never shell-sourced), and never logged or printed; `claude-memory doctor` masks the password in its output.

### Negative Tests (AC-54, AC-52)

Verify these rejection scenarios to confirm the setup is secure:

1. **Wrong password from laptop**:
   ```bash
   # On laptop, in a test shell:
   export MEMORY_PG_DSN="postgresql://claude_memory:wrong-password@<POSTGRES_BIND_IP>:5432/claude_memory"
   psql "$MEMORY_PG_DSN" -c "SELECT 1;"
   # Expected: "fe_sendauth: no password supplied" or "FATAL: password authentication failed"
   ```

2. **Correct password from a non-Tailscale address** (e.g., the LAN or public IP):
   ```bash
   # On a different machine not on the tailnet:
   psql -h <server-lan-ip> -U claude_memory -d claude_memory
   # Expected: "could not connect to server" (firewall rejects before pg_hba sees it)
   # OR if the port is exposed: "FATAL: pg_hba.conf rejects connection" (pg_hba rule mismatch)
   ```

3. **No password**:
   ```bash
   # On laptop:
   export MEMORY_PG_DSN="postgresql://claude_memory@<POSTGRES_BIND_IP>:5432/claude_memory"
   psql "$MEMORY_PG_DSN" -c "SELECT 1;"
   # Expected: "fe_sendauth: no password supplied" or password prompt (depending on psql version)
   ```

All three should fail cleanly. If they succeed, the server security posture is compromised; re-check `pg_hba.conf`, UFW rules, and Tailscale status.

### Import and migration 0004

`claude-memory import automem|insights` (see `integration/USAGE.md`) needs migration 0004: `records.import_key`, a partial unique index and the `events_source_check_v2` constraint (adds source `import`). The new binary applies it automatically at the next session start (`serve`, `extract --run`, `ingest-pr`, `cleanup` and `migrate` all run the schema); there is no separate step, and it is a no-op on every later start. It takes short exclusive locks on `events` and `records`. The hook never migrates and never touches the new column, so old and new binaries and schemas keep working together in either order. **Rollback = reinstall the old binary**: it ignores the column, the index and the `_v2` constraint and never writes source `import`; there is no down migration. Run `claude-memory import ... --dry-run` first (no database needed). Unreviewed imported candidates are removed by the cleanup TTL and a later import re-creates them, so review them within the candidate TTL (`MEMORY_CANDIDATE_TTL`, default 180 days; the import prints it). Imported candidates are searchable and injected into sessions before review, and the database is shared, so run `--dry-run` first.

Also: (a) `import`, `ls`, `show`, `stats`, `cleanup` and the other long-running commands apply the schema too, so the first command after upgrading applies 0004. (b) A concurrent first start used to be able to fail once on `CREATE`; migrations now run under a Postgres advisory lock on one connection, so starters are serialized. (c) After rolling back to the old binary, imported rows stay as ordinary candidates with source `import`; the old binary ignores the column.

### PR ingest: GitHub and GitLab

`ingest-pr` now also ingests GitHub (`gh`) and GitLab (`glab`) repos (see `integration/USAGE.md`, "PR ingest"). Upgrade steps: `gh auth login` and/or `glab auth login` as the user the job runs as, then `claude-memory install --upgrade` so the jobs' PATH includes the CLIs. The job needs keychain access for `gh`/`glab` from the launchd user agent: run `launchctl kickstart gui/$(id -u)/io.github.claude-memory.ingest-pr` once and check `ingest-pr.log`. GitLab is verified only against fixtures: run `claude-memory ingest-pr --dry-run` first. `namespaces.yaml` may now carry a `pr_ingest` section; a binary older than this version drops it when it re-renders the file (`namespaces add`, `install`), so upgrade every binary that can write the file. No database migration.

**Before replacing the binary, run `claude-memory ingest-pr --dry-run`** with the current environment: `MEMORY_PR_INGEST_REPOS` may include GitHub or GitLab repos that were skipped until now and will be ingested by the first run of the new binary with the first-run lookback (`MEMORY_PR_INGEST_LOOKBACK`, 30 days), i.e. two haiku calls per trusted PR. Also new: Azure review comments now reach the extraction model, and PR text is capped to `MEMORY_MAX_CONTENT_CHARS` (default 20000 characters) after scrubbing, so very long Azure descriptions are cut. Live verification: GitHub is an owner-manual check (fixtures are hand-written), GitLab is fixture-only.

### Management commands

`claude-memory ls|show|rm|edit|promote|review` manage records from the terminal (see `integration/USAGE.md`). They need no schema change or migration; `rm --hard` deletes a row for good and refuses a record that another record's `superseded_by` points at. Short ids (8 hex) resolve only inside the namespace; use the full UUID for `global` records.
