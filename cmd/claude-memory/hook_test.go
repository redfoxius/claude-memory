package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

// fakeHookStore implements memory.Store for hook tests. Only Search is
// exercised by hookCmd's call path (via memory.Service.Search); every
// other method panics if called, so an unexpected call surfaces loudly
// rather than silently returning a zero value.
type fakeHookStore struct {
	searchFn func(ctx context.Context, query string, embedding []float32, repo string, opts memory.SearchOptions) (*memory.SearchResult, error)
}

func (f *fakeHookStore) Search(ctx context.Context, query string, embedding []float32, repo string, opts memory.SearchOptions) (*memory.SearchResult, error) {
	return f.searchFn(ctx, query, embedding, repo, opts)
}
func (f *fakeHookStore) Create(ctx context.Context, r *record.Record) (*record.Record, error) {
	panic("Create should not be called by the hook (read-only path)")
}
func (f *fakeHookStore) Get(ctx context.Context, id string) (*record.Record, error) {
	panic("Get should not be called by the hook (read-only path)")
}
func (f *fakeHookStore) Update(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
	panic("Update should not be called by the hook (read-only path)")
}
func (f *fakeHookStore) FindCandidates(ctx context.Context, embedding []float32, repo string, limit int) ([]*memory.Candidate, error) {
	panic("FindCandidates should not be called by the hook (read-only path)")
}
func (f *fakeHookStore) List(ctx context.Context, filters memory.ListFilters) ([]*record.Record, error) {
	panic("List should not be called by the hook (read-only path)")
}
func (f *fakeHookStore) WithTx(ctx context.Context, fn func(tx memory.TxStore) error) error {
	panic("WithTx should not be called by the hook (read-only path)")
}
func (f *fakeHookStore) DeleteCandidatesByTTL(ctx context.Context, ttlDays int) (int, error) {
	panic("DeleteCandidatesByTTL should not be called by the hook (read-only path)")
}

type fakeHookEmbedder struct {
	embedFn func(ctx context.Context, text string, maxTokens int) ([]float32, error)
}

func (f *fakeHookEmbedder) Embed(ctx context.Context, text string, maxTokens int) ([]float32, error) {
	if f.embedFn != nil {
		return f.embedFn(ctx, text, maxTokens)
	}
	return make([]float32, 1024), nil
}

type fakeHookScrubber struct{}

func (fakeHookScrubber) Scrub(text string) (string, bool) { return text, false }

type fakeHookClock struct{}

func (fakeHookClock) Now() time.Time { return time.Now().UTC() }

func hookTestCfg() *config.Config {
	return &config.Config{
		HookSimThreshold: 0.50,
		HookTimeout:      800 * time.Millisecond,
		EmbedMaxTokens:   2048,
	}
}

// runHookCmd redirects os.Stdin/os.Stdout around a call to hookCmd (which
// talks to the real OS file descriptors, not injected io.Reader/Writer),
// and returns whatever was written to stdout plus hookCmd's own error.
func runHookCmd(t *testing.T, ctx context.Context, cfg *config.Config, svc *memory.Service, stdinJSON string) (string, error) {
	t.Helper()

	origStdin, origStdout := os.Stdin, os.Stdout

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}

	os.Stdin = inR
	os.Stdout = outW
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
	})

	if _, err := inW.WriteString(stdinJSON); err != nil {
		t.Fatalf("failed to write stdin fixture: %v", err)
	}
	if err := inW.Close(); err != nil {
		t.Fatalf("failed to close stdin pipe writer: %v", err)
	}

	outCh := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(outR)
		outCh <- string(data)
	}()

	hookErr := hookCmd(ctx, cfg, svc)

	if err := outW.Close(); err != nil {
		t.Fatalf("failed to close stdout pipe writer: %v", err)
	}
	gotOut := <-outCh

	return gotOut, hookErr
}

func searchRecord(id, title, repo, content string, similarity, score float64, unverified bool) *memory.SearchRecord {
	return &memory.SearchRecord{
		ID:         id,
		Title:      title,
		Repo:       repo,
		Similarity: similarity,
		Score:      score,
		Unverified: unverified,
	}
}

// --- AC-32: threshold compares Similarity, not Score ---

func TestHookCmd_FiltersAndCapsCardsBySimilarity(t *testing.T) {
	cfg := hookTestCfg() // HookSimThreshold = 0.50

	records := []*memory.SearchRecord{
		searchRecord("id-1", "Below threshold (similarity)", "billing-service", "secret content 1", 0.49, 0.99, false),
		searchRecord("id-2", "At threshold", "billing-service", "secret content 2", 0.50, 0.01, false),
		searchRecord("id-3", "Well above threshold A", "billing-service", "secret content 3", 0.80, 0.02, false),
		searchRecord("id-4", "Well above threshold B", "billing-service", "secret content 4", 0.70, 0.03, false),
		searchRecord("id-5", "Well above threshold C (should be capped)", "billing-service", "secret content 5", 0.60, 0.04, false),
	}

	store := &fakeHookStore{
		searchFn: func(ctx context.Context, query string, embedding []float32, repo string, opts memory.SearchOptions) (*memory.SearchResult, error) {
			return &memory.SearchResult{Records: records}, nil
		},
	}
	svc := memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, cfg)

	stdout, err := runHookCmd(t, context.Background(), cfg, svc, `{"prompt":"anything","cwd":"/tmp"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := &hookOutput{}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		t.Fatalf("failed to decode hook stdout as the UserPromptSubmit contract shape: %v\nstdout: %s", err, stdout)
	}

	cardLines := strings.Split(strings.TrimSpace(out.HookSpecificOutput.AdditionalContext), "\n")
	// cardLines[0] is the header line; the rest are one rendered card each.
	gotCards := 0
	if len(cardLines) > 0 && cardLines[0] == additionalContextHeader {
		gotCards = len(cardLines) - 1
	}
	if gotCards > 3 {
		t.Errorf("expected at most 3 cards (AC-46), got %d", gotCards)
	}

	if strings.Contains(out.HookSpecificOutput.AdditionalContext, "id-1") {
		t.Errorf("record below the similarity threshold (0.49 < 0.50) must not be injected")
	}

	if !strings.Contains(out.HookSpecificOutput.AdditionalContext, "id-2") {
		t.Errorf("record exactly at the similarity threshold (0.50) must be injected")
	}
}

// --- AC-46: cards contain only title/repo/id, never content ---

func TestHookCmd_CardsNeverExposeContent(t *testing.T) {
	cfg := hookTestCfg()
	const secretMarker = "DO-NOT-LEAK-THIS-CONTENT-MARKER"

	records := []*memory.SearchRecord{
		searchRecord("id-1", "A relevant record", "billing-service", secretMarker, 0.9, 0.5, false),
	}
	store := &fakeHookStore{
		searchFn: func(ctx context.Context, query string, embedding []float32, repo string, opts memory.SearchOptions) (*memory.SearchResult, error) {
			return &memory.SearchResult{Records: records}, nil
		},
	}
	svc := memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, cfg)

	stdout, err := runHookCmd(t, context.Background(), cfg, svc, `{"prompt":"anything","cwd":"/tmp"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(stdout, secretMarker) {
		t.Errorf("hook stdout must never contain record content, got: %s", stdout)
	}
}

// --- AC-31: silent exit 0, empty stdout on error/timeout ---

func TestHookCmd_SilentExitOnSearchError(t *testing.T) {
	cfg := hookTestCfg()
	store := &fakeHookStore{
		searchFn: func(ctx context.Context, query string, embedding []float32, repo string, opts memory.SearchOptions) (*memory.SearchResult, error) {
			return nil, errors.New("postgres unreachable over tailscale")
		},
	}
	svc := memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, cfg)

	stdout, err := runHookCmd(t, context.Background(), cfg, svc, `{"prompt":"anything","cwd":"/tmp"}`)
	if err != nil {
		t.Fatalf("expected hookCmd to silently return nil on a search error, got: %v", err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected empty stdout on a silent failure (AC-31), got: %q", stdout)
	}
}

func TestHookCmd_SilentExitOnContextTimeout(t *testing.T) {
	cfg := hookTestCfg()
	cfg.HookTimeout = 20 * time.Millisecond

	store := &fakeHookStore{
		searchFn: func(ctx context.Context, query string, embedding []float32, repo string, opts memory.SearchOptions) (*memory.SearchResult, error) {
			// Simulate a slow/unreachable Postgres: block until the caller's
			// context (bounded by MEMORY_HOOK_TIMEOUT) is done, exactly as a
			// real pgx query would behave against a dead connection.
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	svc := memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.HookTimeout)
	defer cancel()

	start := time.Now()
	stdout, err := runHookCmd(t, ctx, cfg, svc, `{"prompt":"anything","cwd":"/tmp"}`)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected hookCmd to silently return nil on timeout, got: %v", err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected empty stdout on timeout (AC-31), got: %q", stdout)
	}
	if elapsed > cfg.HookTimeout+500*time.Millisecond {
		t.Errorf("expected hookCmd to return promptly after the %v timeout, took %v", cfg.HookTimeout, elapsed)
	}
}

func TestHookCmd_MalformedStdinIsSilentNoop(t *testing.T) {
	cfg := hookTestCfg()
	store := &fakeHookStore{
		searchFn: func(ctx context.Context, query string, embedding []float32, repo string, opts memory.SearchOptions) (*memory.SearchResult, error) {
			t.Fatal("Search should never be reached when stdin is malformed")
			return nil, nil
		},
	}
	svc := memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, cfg)

	stdout, err := runHookCmd(t, context.Background(), cfg, svc, `{not valid json`)
	if err != nil {
		t.Fatalf("expected a nil error for malformed stdin, got: %v", err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected empty stdout for malformed stdin, got: %q", stdout)
	}
}

// TestHookCmd_OutputMatchesClaudeCodeUserPromptSubmitContract encodes
// Claude Code's actual UserPromptSubmit hook output contract: either plain
// text, or a JSON object shaped
// {"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"<string>"}}.
// Regression guard: an earlier hookCmd emitted
// {"additionalContext":[{title,repo,id}, ...]}, which Claude Code ignores,
// so cards never reached the session. Do not loosen this assertion.
func TestHookCmd_OutputMatchesClaudeCodeUserPromptSubmitContract(t *testing.T) {
	cfg := hookTestCfg()
	records := []*memory.SearchRecord{
		searchRecord("id-1", "A relevant record", "billing-service", "content", 0.9, 0.5, false),
	}
	store := &fakeHookStore{
		searchFn: func(ctx context.Context, query string, embedding []float32, repo string, opts memory.SearchOptions) (*memory.SearchResult, error) {
			return &memory.SearchResult{Records: records}, nil
		},
	}
	svc := memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, cfg)

	stdout, err := runHookCmd(t, context.Background(), cfg, svc, `{"prompt":"anything","cwd":"/tmp"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var contract struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}

	if err := json.Unmarshal([]byte(stdout), &contract); err != nil {
		t.Fatalf("hook stdout does not even parse as the expected contract shape: %v\nstdout: %s", err, stdout)
	}

	if contract.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Errorf("expected hookSpecificOutput.hookEventName = %q, got %q (full stdout: %s)",
			"UserPromptSubmit", contract.HookSpecificOutput.HookEventName, stdout)
	}
	if contract.HookSpecificOutput.AdditionalContext == "" {
		t.Errorf("expected a non-empty hookSpecificOutput.additionalContext string containing the card(s), got stdout: %s", stdout)
	}
}
