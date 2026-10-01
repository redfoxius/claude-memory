# Ollama setup (laptop-local embedding provider)

Topology B runs Ollama + `bge-m3` natively on the laptop only (never on
the server) — see `DEPLOY.md` and spec AC-44. This is the one piece of
`claude-memory`'s stack that is not this repo's own code; it is a
Homebrew service you install and keep warm.

## Install

```bash
brew install ollama
brew services start ollama
```

`brew services start` installs a launchd `LaunchAgent` at
`~/Library/LaunchAgents/homebrew.mxcl.ollama.plist` that keeps `ollama
serve` running and restarts it on login/crash.

## Keep the model loaded

Nothing to configure. `claude-memory` sends `"keep_alive": -1` with every
`/api/embed` request (`internal/ollama/embedder.go`), so after the first call
the model stays loaded until Ollama restarts — the hook never pays the
cold-start cost. (Editing `OLLAMA_KEEP_ALIVE` into
`~/Library/LaunchAgents/homebrew.mxcl.ollama.plist` does not persist:
`brew services restart` and `brew upgrade` regenerate that plist.)

To warm it right after a reboot instead of on the first prompt:

```bash
curl -s http://127.0.0.1:11434/api/embed \
  -d '{"model":"bge-m3","input":"warmup","keep_alive":-1}' > /dev/null
ollama ps   # UNTIL: Forever
```

## Pull the model

```bash
ollama pull bge-m3
```

## Health check

```bash
curl -sS http://127.0.0.1:11434/api/embed \
  -d '{"model":"bge-m3","input":"health check","truncate":true,"options":{"num_ctx":2048,"num_batch":2048}}' \
  | python3 -m json.tool | head -5
# Expect a JSON body with an "embeddings" array of one 1024-length vector.
```

If this fails:
- `brew services list` — confirm `ollama` shows `started`.
- `ollama list` — confirm `bge-m3` is pulled.
- `tail -f ~/Library/Logs/Homebrew/ollama/ollama.log` (or `brew services
  info ollama` for the actual log path on your machine).
