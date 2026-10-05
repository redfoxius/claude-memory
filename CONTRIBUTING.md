# Contributing

This is a personal project that I use every day. Issues and pull requests are
welcome, but I can't promise a response time. For anything bigger than a small
fix, open an issue first so we can agree on the approach. Features start from a
spec under `docs/specs/<feature>/`, and the existing ones show the format.

Unit tests need only Go (the version in `go.mod`): `make test` runs
`go test -race ./...`. `make lint` runs `golangci-lint`. CI runs both the unit
and the integration suites on every push and pull request.

`make test-integration` runs the tests tagged `integration` (Postgres store,
migrations, namespace isolation, the write path through the service). It needs
either a running Docker, so that testcontainers can start
`pgvector/pgvector:pg16`, or `MEMORY_TEST_PG_ADMIN_DSN` pointing at a
pgvector-enabled Postgres, for example
`MEMORY_TEST_PG_ADMIN_DSN='postgres://postgres@localhost:5432/postgres' make test-integration`.
Each test then creates its own throwaway database.

`make eval` runs the retrieval eval against real services: Ollama with `bge-m3`
(`MEMORY_OLLAMA_URL`, default `http://127.0.0.1:11434`) and the Postgres from
`~/.config/claude-memory/env`. Its fixtures live in a separate `eval`
namespace. It rewrites `docs/specs/memory-mvp/eval-results.md`, so
`git diff` after a run shows any regression. Use
`make eval EVAL_OUTPUT=/tmp/eval.md` to write the report somewhere else.
Please include the eval diff in pull requests that touch search, embedding or
thresholds.
