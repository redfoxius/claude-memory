// Package config provides environment-driven configuration for claude-memory.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all tunable settings for the claude-memory service.
// Values are read from environment variables, with documented defaults.
// The Postgres DSN is read from the environment or a local config file,
// never logged or persisted in a way that could expose the password.
type Config struct {
	// MaxContentChars is the maximum size of a record's content in characters.
	// Default: 20000.
	// Env: MEMORY_MAX_CONTENT_CHARS
	MaxContentChars int

	// StoreSimUpdate is the similarity threshold for the store write path:
	// at or above this score, an existing record is updated rather than added.
	// Range: 0.0-1.0.
	// Default: 0.85 (tuned on bge-m3: near-duplicates measured 0.79–0.88, 2026-10-01).
	// Env: MEMORY_STORE_SIM_UPDATE
	StoreSimUpdate float64

	// StoreSimAsk is the similarity threshold for the store write path:
	// between StoreSimAsk and StoreSimUpdate, top candidates are returned
	// to the caller for judgment.
	// Range: 0.0-1.0.
	// Default: 0.65 (tuned on bge-m3, 2026-10-01).
	// Env: MEMORY_STORE_SIM_ASK
	StoreSimAsk float64

	// PRIngestLookback is the default time window for PR ingest when no cursor exists.
	// Default: 30 days.
	// Env: MEMORY_PR_INGEST_LOOKBACK (format: Go duration string, e.g. "720h")
	PRIngestLookback time.Duration

	// HookSimThreshold is the similarity threshold for the read-path hook:
	// results at or above this score are injected into the session.
	// Range: 0.0-1.0.
	// Default: 0.50 (tuned on bge-m3: relevant 0.45–0.72, unrelated ≤0.42, 2026-10-01).
	// Env: MEMORY_HOOK_SIM_THRESHOLD
	HookSimThreshold float64

	// ExtractMinMessages is the minimum message count in a session to trigger extraction.
	// Sessions with fewer messages and no code changes are skipped.
	// Default: 20.
	// Env: MEMORY_EXTRACT_MIN_MESSAGES
	ExtractMinMessages int

	// CandidateTTL is the lifetime of candidate records before cleanup deletion.
	// Active and deprecated records are never deleted by cleanup.
	// Default: 180 days.
	// Env: MEMORY_CANDIDATE_TTL (format: Go duration string, e.g. "4320h")
	CandidateTTL time.Duration

	// HookTimeout is the hard timeout for the UserPromptSubmit hook's
	// embedding and database query.
	// Default: 800ms.
	// Env: MEMORY_HOOK_TIMEOUT (format: Go duration string, e.g. "800ms")
	HookTimeout time.Duration

	// EmbedMaxTokens is the maximum number of tokens to send to the embedding provider.
	// Input beyond this limit is truncated from the end.
	// Default: 2048.
	// Env: MEMORY_EMBED_MAX_TOKENS
	EmbedMaxTokens int

	// PGDSN is the Postgres connection string (host:port, database, user, password).
	// No default; required.
	// Env: MEMORY_PG_DSN
	// Never logged. Should be read from environment or a local, untracked config file
	// at ~/.config/claude-memory/env (mode 0600).
	PGDSN string

	// OllamaURL is the base URL of the Ollama embedding server.
	// Default: http://127.0.0.1:11434
	// Env: MEMORY_OLLAMA_URL
	OllamaURL string

	// OllamaModel is the name of the Ollama model to use for embeddings.
	// Default: bge-m3
	// Env: MEMORY_OLLAMA_MODEL
	OllamaModel string

	// PRIngestRepos is the list of local repo directories (or root
	// directories scanned one level deep for git repos) that `claude-memory
	// ingest-pr` ingests PRs from (AC-58). Default: empty (no repos
	// configured; ingest-pr has nothing to do).
	// Env: MEMORY_PR_INGEST_REPOS (comma-separated absolute paths)
	PRIngestRepos []string
}

// Load reads configuration from environment variables, with fallback defaults.
// It returns an error if any required value is missing or malformed.
func Load() (*Config, error) {
	c := &Config{
		MaxContentChars:     getIntEnv("MEMORY_MAX_CONTENT_CHARS", 20000),
		StoreSimUpdate:      getFloatEnv("MEMORY_STORE_SIM_UPDATE", 0.85),
		StoreSimAsk:         getFloatEnv("MEMORY_STORE_SIM_ASK", 0.65),
		PRIngestLookback:    getDurationEnv("MEMORY_PR_INGEST_LOOKBACK", 30*24*time.Hour),
		HookSimThreshold:    getFloatEnv("MEMORY_HOOK_SIM_THRESHOLD", 0.50),
		ExtractMinMessages:  getIntEnv("MEMORY_EXTRACT_MIN_MESSAGES", 20),
		CandidateTTL:        getDurationEnv("MEMORY_CANDIDATE_TTL", 180*24*time.Hour),
		HookTimeout:         getDurationEnv("MEMORY_HOOK_TIMEOUT", 800*time.Millisecond),
		EmbedMaxTokens:      getIntEnv("MEMORY_EMBED_MAX_TOKENS", 2048),
		OllamaURL:           getStringEnv("MEMORY_OLLAMA_URL", "http://127.0.0.1:11434"),
		OllamaModel:         getStringEnv("MEMORY_OLLAMA_MODEL", "bge-m3"),
		PRIngestRepos:       getStringListEnv("MEMORY_PR_INGEST_REPOS"),
	}

	// PGDSN is required and never has a default.
	dsn := os.Getenv("MEMORY_PG_DSN")
	if dsn == "" {
		return nil, fmt.Errorf("MEMORY_PG_DSN is required but not set")
	}
	c.PGDSN = dsn

	return c, nil
}

// getStringEnv reads a string environment variable with a default fallback.
func getStringEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// getStringListEnv reads a comma-separated environment variable into a
// slice of trimmed, non-empty strings. An unset or empty variable returns
// an empty (nil) slice.
func getStringListEnv(key string) []string {
	val := os.Getenv(key)
	if val == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// getIntEnv reads an integer environment variable with a default fallback.
// If the value is set but not a valid integer, it returns the default.
func getIntEnv(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return i
}

// getFloatEnv reads a float64 environment variable with a default fallback.
// If the value is set but not a valid float, it returns the default.
func getFloatEnv(key string, defaultVal float64) float64 {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return defaultVal
	}
	return f
}

// getDurationEnv reads a duration environment variable with a default fallback.
// If the value is set but not a valid duration string, it returns the default.
func getDurationEnv(key string, defaultVal time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return defaultVal
	}
	return d
}

// LoadFromFile attempts to source environment variables from a local config file.
// The file is expected to be in KEY=VALUE format (one per line).
// It returns an error if the file is group/world-readable (permission check).
// This is a no-op if the file doesn't exist.
func LoadFromFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		// File doesn't exist or can't be read; silently skip.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat config file: %w", err)
	}

	// Check permissions: must not be readable by group or world (mode 0600).
	mode := info.Mode().Perm()
	if mode&0077 != 0 {
		return fmt.Errorf("config file %s must have mode 0600, not %#o", path, mode)
	}

	// Read and parse the file.
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		// Set in environment if not already set.
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val) // os.Setenv always succeeds
		}
	}

	return nil
}
