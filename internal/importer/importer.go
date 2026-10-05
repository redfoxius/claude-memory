// Package importer turns existing knowledge files (Claude Code auto-memory
// and INSIGHTS.md logs) into import items. It parses and maps only: reading
// goes through consumer-side ports, and storing is the caller's job
// (memory.Service.Store). It imports no infrastructure package.
package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/redfoxius/claude-memory/internal/record"
)

// maxTitleRunes bounds an imported title.
const maxTitleRunes = 160

// Item is one record to import.
type Item struct {
	Kind    record.Kind
	Title   string
	Content string
	Repo    string
	Home    string // the directory whose namespace the item belongs to
	Files   []string
	Tags    []string
	Key     string // import key, see ImportKey
	Origin  string // where it came from, for reports (never file content)
}

// Skip is one input that produced no item. Reason is a fixed string, never
// file content.
type Skip struct {
	Origin string
	Reason string
}

// ImportKey is `<kind>:` plus the first 32 hex chars of
// sha256(locator + "\0" + whitespace-collapsed raw text).
func ImportKey(kind, locator, raw string) string {
	sum := sha256.Sum256([]byte(locator + "\x00" + strings.Join(strings.Fields(raw), " ")))
	return kind + ":" + hex.EncodeToString(sum[:])[:32]
}

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
