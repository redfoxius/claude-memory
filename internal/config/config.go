// Package config provides environment-driven configuration for claude-memory.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
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

	// StaleTimeoutHook is the ceiling for the hook's staleness checks
	// (they also stop at the hook's overall latency budget).
	// Default: 50ms. Env: MEMORY_STALE_TIMEOUT_HOOK
	StaleTimeoutHook time.Duration

	// StaleTimeout is the ceiling for staleness checks in the MCP server.
	// Default: 500ms. Env: MEMORY_STALE_TIMEOUT
	StaleTimeout time.Duration

	// Namespace is the record namespace this process reads and writes. It is
	// not read by Load: the composition root resolves it per invocation
	// (MEMORY_NAMESPACE override, else namespaces.yaml by project directory)
	// and sets it before building the service.
	Namespace string
}

// NamespaceEnv is the environment variable that explicitly overrides the
// namespace resolved from namespaces.yaml (e.g. set in a repo's
// .claude/settings.json env).
const NamespaceEnv = "MEMORY_NAMESPACE"

// Load reads configuration from environment variables, with fallback defaults.
// It returns an error if any required value is missing or malformed.
func Load() (*Config, error) {
	c := &Config{
		MaxContentChars:    getIntEnv("MEMORY_MAX_CONTENT_CHARS", 20000),
		StoreSimUpdate:     getFloatEnv("MEMORY_STORE_SIM_UPDATE", 0.85),
		StoreSimAsk:        getFloatEnv("MEMORY_STORE_SIM_ASK", 0.65),
		PRIngestLookback:   getDurationEnv("MEMORY_PR_INGEST_LOOKBACK", 30*24*time.Hour),
		HookSimThreshold:   getFloatEnv("MEMORY_HOOK_SIM_THRESHOLD", 0.50),
		ExtractMinMessages: getIntEnv("MEMORY_EXTRACT_MIN_MESSAGES", 20),
		CandidateTTL:       getDurationEnv("MEMORY_CANDIDATE_TTL", 180*24*time.Hour),
		HookTimeout:        getDurationEnv("MEMORY_HOOK_TIMEOUT", 800*time.Millisecond),
		EmbedMaxTokens:     getIntEnv("MEMORY_EMBED_MAX_TOKENS", 2048),
		OllamaURL:          getStringEnv("MEMORY_OLLAMA_URL", "http://127.0.0.1:11434"),
		OllamaModel:        getStringEnv("MEMORY_OLLAMA_MODEL", "bge-m3"),
		PRIngestRepos:      getStringListEnv("MEMORY_PR_INGEST_REPOS"),
		StaleTimeoutHook:   getDurationEnv("MEMORY_STALE_TIMEOUT_HOOK", 50*time.Millisecond),
		StaleTimeout:       getDurationEnv("MEMORY_STALE_TIMEOUT", 500*time.Millisecond),
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
// The file is expected to be in KEY=VALUE format (one per line); it is parsed
// by ParseEnvFile. Every KEY=VALUE pair is set in the environment unless the
// variable is already set (non-empty), so the process environment wins.
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
	if mode&0o077 != 0 {
		return fmt.Errorf("config file %s must have mode 0600, not %#o", path, mode)
	}

	ef, err := ParseEnvFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // removed in between
		}
		return err
	}

	for _, e := range ef.entries {
		// Set in environment if not already set.
		if os.Getenv(e.key) == "" {
			_ = os.Setenv(e.key, e.value) // os.Setenv always succeeds
		}
	}

	return nil
}

// FindingKind names one env-file format problem (spec AC-27).
type FindingKind string

// Env-file finding kinds.
const (
	// FindingExportPrefix: a `export KEY=VALUE` line (DEPLOY.md's old form).
	// The line is rejected: its key is not in Values, and LoadFromFile does
	// not set it. Convertible to the plain form.
	FindingExportPrefix FindingKind = "export-prefix"
	// FindingQuotedWholeValue: quotes wrap the entire value (`KEY="v"`). The
	// value is kept verbatim, quotes included, as LoadFromFile always did.
	// Convertible: the quoted text has no character that needs the shell.
	FindingQuotedWholeValue FindingKind = "quoted-whole-value"
	// FindingUnparseableValue: anything else the plain format cannot represent
	// safely: `$(...)`, `${...}`, `$VAR`, backticks, backslashes, unbalanced
	// or partial quotes, or an invalid key. Never converted.
	FindingUnparseableValue FindingKind = "unparseable-value"
	// FindingDuplicate: a key set more than once. The first non-empty value
	// wins, as in LoadFromFile.
	FindingDuplicate FindingKind = "duplicate"
	// FindingNoEquals: a non-comment line without '='. Ignored.
	FindingNoEquals FindingKind = "no-equals"
	// FindingGroupWorldReadable: the file mode has group or world bits;
	// LoadFromFile refuses such a file.
	FindingGroupWorldReadable FindingKind = "group-world-readable"
)

// Finding is one format problem in an env file.
type Finding struct {
	Line   int // 1-based line number; 0 for file-level findings
	Kind   FindingKind
	Key    string // the line's key, when it has one
	Detail string
}

// EnvFile is a parsed env file (spec AC-27).
type EnvFile struct {
	// Values maps each accepted key to the value LoadFromFile would set from
	// this file into an environment where it is unset.
	Values map[string]string
	// Order lists the keys of Values in order of first appearance.
	Order []string
	// Mode is the file's permission bits.
	Mode fs.FileMode
	// Findings lists every format problem, in line order (file-level first).
	Findings []Finding

	entries []envEntry // every accepted KEY=VALUE line, in order, duplicates included
}

type envEntry struct {
	line       int
	key, value string
}

// envKeyRe is a valid environment variable name.
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// shellExpansionRe matches text a shell would expand: $(...), ${...}, $VAR.
var shellExpansionRe = regexp.MustCompile(`\$[A-Za-z_({]`)

// ParseEnvFile reads and parses the KEY=VALUE env file at path without
// touching the process environment (spec AC-27). Blank lines and lines
// starting with '#' are skipped; keys and values are trimmed of surrounding
// whitespace; the value is everything after the first '='. A missing file
// returns an error wrapping fs.ErrNotExist. Wrong permissions are reported
// as a finding, not an error: refusing to load such a file is LoadFromFile's
// job.
func ParseEnvFile(path string) (*EnvFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat config file: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	return ParseEnvData(data, info.Mode().Perm()), nil
}

// ParseEnvData parses env-file content read by the caller (e.g. through
// internal/setup's FS port) with the file's permission bits: ParseEnvFile
// without the I/O.
func ParseEnvData(data []byte, mode fs.FileMode) *EnvFile {
	ef := parseEnv(string(data))
	ef.Mode = mode.Perm()
	if ef.Mode&0o077 != 0 {
		ef.Findings = append([]Finding{{
			Kind:   FindingGroupWorldReadable,
			Detail: fmt.Sprintf("mode %#o has group/world bits; the file must be 0600", ef.Mode),
		}}, ef.Findings...)
	}
	return ef
}

// parseEnv parses env-file content (everything but the mode).
func parseEnv(data string) *EnvFile {
	ef := &EnvFile{Values: map[string]string{}}
	seen := map[string]bool{}
	for i, raw := range strings.Split(data, "\n") {
		lineNo := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		exported := false
		if rest, ok := cutExport(line); ok {
			exported = true
			line = rest
		}

		k, v, ok := strings.Cut(line, "=")
		if !ok {
			ef.Findings = append(ef.Findings, Finding{Line: lineNo, Kind: FindingNoEquals,
				Detail: "line has no '='; ignored"})
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)

		if exported {
			ef.Findings = append(ef.Findings, Finding{Line: lineNo, Kind: FindingExportPrefix, Key: key,
				Detail: "`export` prefix: the line is ignored; write KEY=VALUE"})
			if kind, detail := classifyValue(val); kind != "" {
				ef.Findings = append(ef.Findings, Finding{Line: lineNo, Kind: kind, Key: key, Detail: detail})
			}
			continue
		}
		if !envKeyRe.MatchString(key) {
			ef.Findings = append(ef.Findings, Finding{Line: lineNo, Kind: FindingUnparseableValue, Key: key,
				Detail: fmt.Sprintf("invalid key %q; the line is ignored", key)})
			continue
		}
		if kind, detail := classifyValue(val); kind != "" {
			ef.Findings = append(ef.Findings, Finding{Line: lineNo, Kind: kind, Key: key, Detail: detail})
		}
		if seen[key] {
			ef.Findings = append(ef.Findings, Finding{Line: lineNo, Kind: FindingDuplicate, Key: key,
				Detail: "key set more than once; the first non-empty value is used"})
		} else {
			seen[key] = true
			ef.Order = append(ef.Order, key)
		}
		if ef.Values[key] == "" {
			ef.Values[key] = val
		}
		ef.entries = append(ef.entries, envEntry{line: lineNo, key: key, value: val})
	}
	return ef
}

// cutExport strips a leading `export` keyword (followed by a space or tab).
func cutExport(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "export")
	if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return line, false
	}
	return strings.TrimSpace(rest), true
}

// classifyValue reports whether a value needs a finding: quotes wrapping the
// whole value (convertible), or anything a shell would interpret that the
// plain format reads literally (unparseable).
func classifyValue(v string) (FindingKind, string) {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		q := v[0]
		inner := v[1 : len(v)-1]
		switch {
		case strings.IndexByte(inner, q) >= 0:
			return FindingUnparseableValue, "quotes inside a quoted value; fix it by hand"
		case q == '"' && strings.ContainsAny(inner, "\\`$"):
			return FindingUnparseableValue, "double-quoted value with $, ` or \\; fix it by hand"
		}
		return FindingQuotedWholeValue, "quotes wrap the whole value and are kept as part of it; remove them"
	}
	switch {
	case strings.ContainsAny(v, "\\`"):
		return FindingUnparseableValue, "value contains a backslash or backtick; fix it by hand"
	case shellExpansionRe.MatchString(v):
		return FindingUnparseableValue, "value looks like shell expansion ($VAR, ${...}, $(...)), which is not performed"
	case strings.Count(v, `"`)%2 == 1 || strings.Count(v, "'")%2 == 1:
		return FindingUnparseableValue, "unbalanced quotes; fix it by hand"
	case strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'"):
		return FindingUnparseableValue, "partially quoted value; fix it by hand"
	}
	return "", ""
}
