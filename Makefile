.PHONY: build test test-integration lint clean cross-build install

# Default target
all: build lint test

# Version stamped into the binary (`claude-memory version`). LDFLAGS is the
# single definition used by build, install and cross-build (and by a future
# release workflow).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Build the claude-memory binary for the current platform.
build:
	@echo "Building claude-memory..."
	go build -ldflags "$(LDFLAGS)" -o bin/claude-memory ./cmd/claude-memory

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
	go test -tags integration -race -count=1 ./internal/postgres/... ./cmd/claude-memory/...

# Lint with golangci-lint (uses default config when no .golangci.yml exists).
lint:
	@echo "Linting..."
	golangci-lint run ./...

# Cross-build for macOS and Linux, arm64 and amd64.
cross-build:
	@echo "Cross-building..."
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/claude-memory-darwin-arm64 ./cmd/claude-memory
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/claude-memory-darwin-amd64 ./cmd/claude-memory
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/claude-memory-linux-amd64 ./cmd/claude-memory
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/claude-memory-linux-arm64 ./cmd/claude-memory

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
