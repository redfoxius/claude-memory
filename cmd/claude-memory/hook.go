package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"claude-memory/internal/config"
	"claude-memory/internal/memory"
)

// userPromptSubmitInput matches Claude Code's UserPromptSubmit hook JSON format.
type userPromptSubmitInput struct {
	Prompt string `json:"prompt"`
	CWD    string `json:"cwd"`
}

// hookOutput is the top-level envelope Claude Code's UserPromptSubmit hook
// contract expects: {"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"<string>"}}.
type hookOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

// hookSpecificOutput matches Claude Code's expected hook output format:
// additionalContext is a single rendered string, not a structured list.
type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// card is a memory search result card with only title, repo, and id.
type card struct {
	Title string `json:"title"`
	Repo  string `json:"repo"`
	ID    string `json:"id"`
}

// additionalContextHeader prefixes the rendered card list so Claude treats
// the lines below as hints to verify (via memory_get), not as established
// fact.
const additionalContextHeader = "Possible relevant memories below (unverified hints — confirm with memory_get before relying on them):"

// renderAdditionalContext renders up to 3 cards as the single string
// Claude Code's UserPromptSubmit contract expects, one card per line:
// "- [memory] <title> (repo: <repo>, id: <id>)", prefixed by a short
// header line. Returns "" when there are no cards, signaling the caller to
// emit no output at all.
func renderAdditionalContext(cards []card) string {
	if len(cards) == 0 {
		return ""
	}

	lines := make([]string, 0, len(cards)+1)
	lines = append(lines, additionalContextHeader)
	for _, c := range cards {
		lines = append(lines, fmt.Sprintf("- [memory] %s (repo: %s, id: %s)", c.Title, c.Repo, c.ID))
	}
	return strings.Join(lines, "\n")
}

// hookCmd implements the UserPromptSubmit hook (AC-30, AC-31, AC-32, AC-46, AC-56).
// It reads stdin for Claude Code's UserPromptSubmit JSON, searches memory with the prompt,
// and outputs additional context (at most 3 cards with title/repo/id) where Similarity >= threshold.
// On error or timeout, it exits 0 with no output (silent failure per AC-31).
func hookCmd(ctx context.Context, cfg *config.Config, svc *memory.Service) error {
	// Read JSON from stdin.
	input := &userPromptSubmitInput{}
	if err := json.NewDecoder(os.Stdin).Decode(input); err != nil {
		// Silent failure: malformed input is not an error to report.
		slog.DebugContext(ctx, "failed to parse stdin", "error", err)
		return nil
	}

	// Derive repo from cwd: repo = basename of git toplevel, or basename of cwd if not in a git repo.
	repo := deriveRepo(ctx, input.CWD)

	// Scope the search to the namespace of the prompt's project directory.
	svc = svc.WithNamespace(resolveNamespace(input.CWD))

	// Perform search with the full prompt (model-side truncation via AC-56).
	// Use nil embedding to let the service compute it (AC-56).
	searchReq := &memory.SearchRequest{
		Query: input.Prompt,
		Repo:  repo,
		Limit: 3, // At most 3 cards per AC-46.
	}

	searchResult, err := svc.Search(ctx, searchReq)
	if err != nil {
		// Silent failure: search error is not reported (AC-31).
		slog.DebugContext(ctx, "search failed", "error", err)
		return nil
	}

	// Filter results by similarity threshold (compare Similarity, not Score, per AC-32).
	var cards []card
	for _, rec := range searchResult.Records {
		if rec.Similarity >= cfg.HookSimThreshold {
			cards = append(cards, card{
				Title: rec.Title,
				Repo:  rec.Repo,
				ID:    rec.ID,
			})
		}
	}

	// Cap at 3 cards.
	if len(cards) > 3 {
		cards = cards[:3]
	}

	additionalContext := renderAdditionalContext(cards)
	if additionalContext == "" {
		// No cards above threshold: no output at all (AC-31/AC-46).
		return nil
	}

	output := &hookOutput{
		HookSpecificOutput: hookSpecificOutput{
			HookEventName:     "UserPromptSubmit",
			AdditionalContext: additionalContext,
		},
	}

	// Marshal to JSON and output to stdout.
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		slog.DebugContext(ctx, "failed to encode output", "error", err)
		return nil
	}

	return nil
}

// deriveRepo derives the repo name from cwd:
// - First tries to get the git toplevel of cwd, takes its basename.
// - Falls back to the basename of cwd if not in a git repo.
func deriveRepo(ctx context.Context, cwd string) string {
	// Try git rev-parse --show-toplevel to get the repo root.
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd

	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		// Successfully got git root; return its basename.
		root := filepath.Clean(string(output)[:len(output)-1]) // Trim newline.
		return filepath.Base(root)
	}

	// Fall back to basename of cwd.
	return filepath.Base(cwd)
}
