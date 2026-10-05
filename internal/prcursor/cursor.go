// Package prcursor persists ingest-pr's per-provider-per-repo progress
// cursor to a local JSON file, so a repeated run only processes PRs
// completed since the last successful batch (AC-26, AC-28).
package prcursor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cursor records the last successfully-ingested PR completion time for one
// provider+repo pair.
type Cursor struct {
	Provider string    `json:"provider"`
	Repo     string    `json:"repo"`
	Since    time.Time `json:"since"`
}

// Store persists cursors to one JSON file per provider+repo under a base
// directory.
type Store struct {
	dir string
}

// NewStore constructs a Store rooted at dir. dir is created on first Save,
// not by NewStore itself.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) path(provider, repo string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%s__%s.json", sanitize(provider), sanitize(repo)))
}

// File returns the path of the cursor file for provider+repo (for error
// messages that tell the user which file to edit).
func (s *Store) File(provider, repo string) string { return s.path(provider, repo) }

// Load reads the persisted cursor for provider+repo. If the file is
// missing, unreadable, or its content doesn't parse, Load returns
// ok=false rather than an error — a missing/unreadable cursor is the
// normal first-run case (AC-28), and the caller is expected to fall back
// to its configured lookback window, not to fail.
func (s *Store) Load(provider, repo string) (Cursor, bool) {
	data, err := os.ReadFile(s.path(provider, repo))
	if err != nil {
		return Cursor{}, false
	}
	var c Cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return Cursor{}, false
	}
	return c, true
}

// Save persists the cursor atomically: write to a temp file in the same
// directory, then rename over the target, so a crash mid-write never
// leaves a corrupt cursor file in place. Callers must only call Save once
// a repo's whole PR batch has succeeded (AC-26) — Save itself has no
// notion of "batch," it just persists whatever Cursor it's given.
func (s *Store) Save(c Cursor) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create cursor dir: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cursor: %w", err)
	}

	target := s.path(c.Provider, c.Repo)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp cursor file: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("rename cursor into place: %w", err)
	}
	return nil
}

// sanitize makes a provider/repo identifier safe to use as a filename
// component.
func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return r.Replace(s)
}
