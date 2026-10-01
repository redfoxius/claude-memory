package prcursor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingCursorReturnsNotOK(t *testing.T) {
	store := NewStore(t.TempDir())
	_, ok := store.Load("azuredevops", "billing-service")
	if ok {
		t.Error("expected ok=false for a missing cursor file")
	}
}

func TestLoadCorruptCursorReturnsNotOK(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Write a corrupt (non-JSON) file at the expected path.
	bad := filepath.Join(dir, "azuredevops__billing-service.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatalf("failed to write corrupt cursor fixture: %v", err)
	}

	_, ok := store.Load("azuredevops", "billing-service")
	if ok {
		t.Error("expected ok=false for a corrupt cursor file")
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	store := NewStore(t.TempDir())
	want := Cursor{Provider: "azuredevops", Repo: "billing-service", Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}

	if err := store.Save(want); err != nil {
		t.Fatalf("unexpected error saving cursor: %v", err)
	}

	got, ok := store.Load("azuredevops", "billing-service")
	if !ok {
		t.Fatal("expected ok=true after a successful save")
	}
	if !got.Since.Equal(want.Since) {
		t.Errorf("expected Since %v, got %v", want.Since, got.Since)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Save(Cursor{Provider: "azuredevops", Repo: "billing-service", Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No leftover .tmp file should remain after a successful save.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("unexpected error reading dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("expected no leftover temp file, found %s", e.Name())
		}
	}
}

func TestDifferentReposHaveIndependentCursors(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.Save(Cursor{Provider: "azuredevops", Repo: "billing-service", Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Save(Cursor{Provider: "azuredevops", Repo: "catalog-service", Since: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c1, ok1 := store.Load("azuredevops", "billing-service")
	c2, ok2 := store.Load("azuredevops", "catalog-service")
	if !ok1 || !ok2 {
		t.Fatal("expected both cursors to load")
	}
	if c1.Since.Equal(c2.Since) {
		t.Error("expected independent cursors per repo, got identical Since")
	}
}
