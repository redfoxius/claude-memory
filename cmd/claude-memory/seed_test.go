package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/record"
)

// fakeSeedStorer is a fake seedStorer for unit tests, never touching
// Postgres/Ollama.
type fakeSeedStorer struct {
	calls []*memory.StoreRequest
	err   error
	// idPrefix is used to synthesize a distinct id per call.
	nextID int
}

func (f *fakeSeedStorer) Store(_ context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	f.nextID++
	return &memory.StoreResponse{
		ID:       "id-" + string(rune('0'+f.nextID)),
		Decision: memory.ActionAdd,
	}, nil
}

func TestLoadSeedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "facts.yaml")
	content := `
facts:
  - kind: gotcha
    title: "Example gotcha"
    content: "Some content"
    repo: "claude-memory"
    tags: [a, b]
  - kind: convention
    title: "Example convention"
    content: "Other content"
    repo: "*"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	facts, err := loadSeedFile(path)
	if err != nil {
		t.Fatalf("loadSeedFile: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
	if facts[0].Kind != "gotcha" || facts[0].Repo != "claude-memory" {
		t.Errorf("unexpected first fact: %+v", facts[0])
	}
	if len(facts[0].Tags) != 2 {
		t.Errorf("expected 2 tags, got %v", facts[0].Tags)
	}
}

func TestLoadSeedFileRejectsInvalidKind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "facts.yaml")
	content := `
facts:
  - kind: not-a-real-kind
    title: "Bad"
    content: "Bad content"
    repo: "*"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadSeedFile(path); err == nil {
		t.Fatal("expected an error for an invalid kind, got nil")
	}
}

func TestLoadSeedFileRejectsMissingFields(t *testing.T) {
	cases := []string{
		`facts:
  - kind: gotcha
    content: "no title"
    repo: "*"
`,
		`facts:
  - kind: gotcha
    title: "no content"
    repo: "*"
`,
		`facts:
  - kind: gotcha
    title: "no repo"
    content: "x"
`,
	}

	for _, c := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "facts.yaml")
		if err := os.WriteFile(path, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSeedFile(path); err == nil {
			t.Errorf("expected an error for case %q, got nil", c)
		}
	}
}

func TestLoadSeedFileRealFixture(t *testing.T) {
	// Exercise the actual seed/facts.yaml shipped with the repo (WI-17
	// acceptance: a handful of facts, each with kind/title/content/repo/tags).
	path := filepath.Join("..", "..", "seed", "facts.yaml")
	facts, err := loadSeedFile(path)
	if err != nil {
		t.Fatalf("loadSeedFile(seed/facts.yaml): %v", err)
	}
	if len(facts) < 5 || len(facts) > 20 {
		t.Errorf("got %d facts, want 5-20", len(facts))
	}
	for i, f := range facts {
		if len(f.Tags) == 0 {
			t.Errorf("fact %d (%q) has no tags", i, f.Title)
		}
	}
}

func TestSeedAllStoresEachFactInline(t *testing.T) {
	facts := []seedFact{
		{Kind: "gotcha", Title: "Fact A", Content: "Content A", Repo: "claude-memory", Tags: []string{"x"}},
		{Kind: "convention", Title: "Fact B", Content: "Content B", Repo: "*", Tags: []string{"y"}},
	}

	fake := &fakeSeedStorer{}
	var out bytes.Buffer
	if err := seedAll(context.Background(), fake, facts, &out); err != nil {
		t.Fatalf("seedAll: %v", err)
	}

	if len(fake.calls) != 2 {
		t.Fatalf("got %d Store calls, want 2", len(fake.calls))
	}
	for _, call := range fake.calls {
		if call.Source != record.SourceInline {
			t.Errorf("expected Source=inline, got %q", call.Source)
		}
	}

	output := out.String()
	if !strings.Contains(output, "Fact A") || !strings.Contains(output, "Fact B") {
		t.Errorf("expected output to mention both facts, got: %s", output)
	}
}

func TestSeedAllReportsErrorsWithoutAborting(t *testing.T) {
	facts := []seedFact{
		{Kind: "gotcha", Title: "Fact A", Content: "Content A", Repo: "claude-memory"},
		{Kind: "gotcha", Title: "Fact B", Content: "Content B", Repo: "claude-memory"},
	}

	fake := &fakeSeedStorer{err: context.DeadlineExceeded}
	var out bytes.Buffer
	if err := seedAll(context.Background(), fake, facts, &out); err != nil {
		t.Fatalf("seedAll: %v", err)
	}

	if len(fake.calls) != 2 {
		t.Fatalf("expected both facts attempted despite errors, got %d calls", len(fake.calls))
	}
	if !strings.Contains(out.String(), "ERROR") {
		t.Errorf("expected ERROR lines in output, got: %s", out.String())
	}
}
