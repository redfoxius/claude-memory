.PHONY: build test test-integration lint clean cross-build install

# Default target
all: build lint test

# Build the claude-memory binary for the current platform.
build:
	@echo "Building claude-memory..."
	go build -o bin/claude-memory ./cmd/claude-memory

# Test with race detector.
test:
	@echo "Running tests with race detector..."
	go test -race ./...

# Integration tests (build tag "integration"). Either have Docker running
# (testcontainers starts pgvector/pgvector:pg16), or point
# MEMORY_TEST_PG_ADMIN_DSN at a pgvector-enabled Postgres, e.g.
#   MEMORY_TEST_PG_ADMIN_DSN='postgres://postgres@localhost:5432/postgres' make test-integration
# (each test then gets its own throwaway database).
test-integration:
	@echo "Running integration tests..."
	go test -tags integration -race -count=1 ./internal/postgres/...

# Lint with golangci-lint (uses default config when no .golangci.yml exists).
lint:
	@echo "Linting..."
	golangci-lint run ./...

# Cross-build for Darwin ARM64 and Linux AMD64.
cross-build:
	@echo "Cross-building..."
	GOOS=darwin GOARCH=arm64 go build -o bin/claude-memory-darwin-arm64 ./cmd/claude-memory
	GOOS=linux GOARCH=amd64 go build -o bin/claude-memory-linux-amd64 ./cmd/claude-memory

# Clean up build artifacts.
clean:
	@echo "Cleaning..."
	rm -rf bin/

# Build and install the claude-memory binary to ~/.local/bin (user-level,
# no sudo). This is step 1 of integration/INSTALL.md; it never touches
# ~/.claude/, ~/Library/LaunchAgents, or acme/CLAUDE.md itself.
install: build
	@echo "Installing claude-memory to $$HOME/.local/bin..."
	mkdir -p "$$HOME/.local/bin"
	cp bin/claude-memory "$$HOME/.local/bin/claude-memory"
	chmod 755 "$$HOME/.local/bin/claude-memory"
