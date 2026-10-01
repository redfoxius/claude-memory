package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// clearMemoryEnv unsets every MEMORY_* env var Load() reads, so each test
// starts from a clean slate regardless of what's in the test runner's own
// environment (and restores it after, via t.Setenv's automatic cleanup
// semantics is not applicable to Unsetenv, so we save/restore manually).
func clearMemoryEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"MEMORY_MAX_CONTENT_CHARS",
		"MEMORY_STORE_SIM_UPDATE",
		"MEMORY_STORE_SIM_ASK",
		"MEMORY_PR_INGEST_LOOKBACK",
		"MEMORY_HOOK_SIM_THRESHOLD",
		"MEMORY_EXTRACT_MIN_MESSAGES",
		"MEMORY_CANDIDATE_TTL",
		"MEMORY_HOOK_TIMEOUT",
		"MEMORY_EMBED_MAX_TOKENS",
		"MEMORY_PG_DSN",
		"MEMORY_OLLAMA_URL",
		"MEMORY_OLLAMA_MODEL",
		"MEMORY_PR_INGEST_REPOS",
	}
	for _, k := range keys {
		old, had := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, old)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

// TestLoadDefaults encodes AC-1/AC-15/AC-28/AC-30/AC-32's config-driven
// defaults (spec v0.5 / plan WI-01): with no MEMORY_* env vars set except
// the required MEMORY_PG_DSN, Load() must fall back to exactly these
// documented values.
func TestLoadDefaults(t *testing.T) {
	clearMemoryEnv(t)
	_ = os.Setenv("MEMORY_PG_DSN", "postgres://user:pass@localhost/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"MaxContentChars", cfg.MaxContentChars, 20000},
		{"StoreSimUpdate", cfg.StoreSimUpdate, 0.85},
		{"StoreSimAsk", cfg.StoreSimAsk, 0.65},
		{"HookSimThreshold", cfg.HookSimThreshold, 0.50},
		{"ExtractMinMessages", cfg.ExtractMinMessages, 20},
		{"EmbedMaxTokens", cfg.EmbedMaxTokens, 2048},
		{"HookTimeout", cfg.HookTimeout, 800 * time.Millisecond},
		{"CandidateTTL", cfg.CandidateTTL, 180 * 24 * time.Hour},
		{"PRIngestLookback", cfg.PRIngestLookback, 30 * 24 * time.Hour},
		{"OllamaURL", cfg.OllamaURL, "http://127.0.0.1:11434"},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	if cfg.PRIngestRepos != nil {
		t.Errorf("PRIngestRepos default = %v, want nil/empty", cfg.PRIngestRepos)
	}
}

// TestLoadRequiresPGDSN encodes that MEMORY_PG_DSN has no default (WI-01
// acceptance): Load() must fail, not silently fall back to an empty DSN.
func TestLoadRequiresPGDSN(t *testing.T) {
	clearMemoryEnv(t)

	if _, err := Load(); err == nil {
		t.Fatal("expected Load() to return an error when MEMORY_PG_DSN is unset")
	}
}

// TestLoadParsesPRIngestRepos exercises AC-58's config surface end-to-end
// through Load() (not just the getStringListEnv helper in isolation).
func TestLoadParsesPRIngestRepos(t *testing.T) {
	clearMemoryEnv(t)
	_ = os.Setenv("MEMORY_PG_DSN", "postgres://user:pass@localhost/db")
	_ = os.Setenv("MEMORY_PR_INGEST_REPOS", "/repos/billing-service, /repos/catalog-service")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	want := []string{"/repos/billing-service", "/repos/catalog-service"}
	if !reflect.DeepEqual(cfg.PRIngestRepos, want) {
		t.Errorf("PRIngestRepos = %v, want %v", cfg.PRIngestRepos, want)
	}
}

// TestLoadFromFileRefusesGroupOrWorldReadable encodes the plan WI-01
// acceptance that ~/.config/claude-memory/env (which may hold the Postgres
// DSN with password) must be refused if it's group/world-readable, rather
// than silently loaded.
func TestLoadFromFileRefusesGroupOrWorldReadable(t *testing.T) {
	cases := []struct {
		name string
		mode os.FileMode
	}{
		{"world readable", 0644},
		{"group readable", 0640},
		{"world writable", 0606},
		{"fully open", 0666},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "env")
			if err := os.WriteFile(path, []byte("MEMORY_PG_DSN=postgres://x\n"), tc.mode); err != nil {
				t.Fatalf("failed to write fixture file: %v", err)
			}
			// WriteFile's mode can be masked by umask; force the exact mode.
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatalf("failed to chmod fixture file: %v", err)
			}

			if err := LoadFromFile(path); err == nil {
				t.Errorf("expected LoadFromFile to refuse a %#o file, got nil error", tc.mode)
			}
		})
	}
}

// TestLoadFromFileAcceptsOwnerOnlyFile is the inverse: a properly
// restricted 0600 file must load successfully and populate the
// environment.
func TestLoadFromFileAcceptsOwnerOnlyFile(t *testing.T) {
	clearMemoryEnv(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	content := "MEMORY_HOOK_SIM_THRESHOLD=0.42\n# a comment\n\nMEMORY_OLLAMA_MODEL=bge-m3\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write fixture file: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatalf("failed to chmod fixture file: %v", err)
	}

	if err := LoadFromFile(path); err != nil {
		t.Fatalf("expected a 0600 file to load, got error: %v", err)
	}

	if got := os.Getenv("MEMORY_HOOK_SIM_THRESHOLD"); got != "0.42" {
		t.Errorf("MEMORY_HOOK_SIM_THRESHOLD = %q, want %q", got, "0.42")
	}
	if got := os.Getenv("MEMORY_OLLAMA_MODEL"); got != "bge-m3" {
		t.Errorf("MEMORY_OLLAMA_MODEL = %q, want %q", got, "bge-m3")
	}
}

// TestLoadFromFileMissingFileIsNoop confirms a missing config file (the
// common case before first-time setup) is not an error.
func TestLoadFromFileMissingFileIsNoop(t *testing.T) {
	if err := LoadFromFile(filepath.Join(t.TempDir(), "does-not-exist")); err != nil {
		t.Errorf("expected missing file to be a no-op, got error: %v", err)
	}
}

func TestGetStringListEnv(t *testing.T) {
	testCases := []struct {
		name string
		val  string
		want []string
	}{
		{name: "unset", val: "", want: nil},
		{name: "single", val: "/repos/billing-service", want: []string{"/repos/billing-service"}},
		{
			name: "multiple with spaces",
			val:  "/repos/billing-service, /repos/catalog-service ,/repos/orders-service",
			want: []string{"/repos/billing-service", "/repos/catalog-service", "/repos/orders-service"},
		},
		{name: "trailing comma dropped", val: "/repos/billing-service,", want: []string{"/repos/billing-service"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MEMORY_TEST_LIST", tc.val)
			got := getStringListEnv("MEMORY_TEST_LIST")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("getStringListEnv(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}
