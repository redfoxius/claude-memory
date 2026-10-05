package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/memory"
)

// userPromptSubmitInput matches Claude Code's UserPromptSubmit hook JSON format.
type userPromptSubmitInput struct {
	Prompt    string `json:"prompt"`
	CWD       string `json:"cwd"`
	SessionID string `json:"session_id"`
}

// hookTotalBudget is the hook's latency target (MVP AC-30 p95); the stale
// check must finish before hookTotalBudget - hookReserve of the hook's life,
// leaving the reserve for rendering and output.
const (
	hookTotalBudget = 300 * time.Millisecond
	hookReserve     = 20 * time.Millisecond
)

// hookDeps are the adapters the hook needs, built in main.go (the
// composition root) so hook.go never constructs one.
type hookDeps struct {
	// History returns the git adapter for a session (wrapped in that
	// session's verdict cache). nil disables staleness checking.
	History func(sessionID string) memory.CodeHistory

	// Events receives one card_injected event per card, after the output is
	// written. It must be a local, append-only sink (the spool): the hook
	// never writes events to Postgres. nil disables events.
	Events memory.EventSink

	// Classify maps a failed search to its error class for the reliability
	// event. nil classifies everything as internal.
	Classify func(error) memory.ErrorClass
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
	// Stale is set when the record's files changed since it was recorded.
	Stale *memory.StaleHint `json:"-"`
	// Event inputs (never rendered).
	Similarity   float64 `json:"-"`
	StaleChecked bool    `json:"-"`
}

// staleSuffix is the fixed text appended to a stale card (AC-5).
func staleSuffix(h *memory.StaleHint) string {
	const base = " ⚠ code changed since this was recorded"
	switch {
	case h.Commits <= 0:
		return base
	case h.Commits == 1:
		return base + " (1 commit)"
	case h.Commits >= 100:
		return base + " (100+ commits)"
	default:
		return fmt.Sprintf("%s (%d commits)", base, h.Commits)
	}
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
		line := fmt.Sprintf("- [memory] %s (repo: %s, id: %s)", c.Title, c.Repo, c.ID)
		if c.Stale != nil {
			line += staleSuffix(c.Stale)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// runHook reads the hook's stdin first (pure local work), then builds the
// service and runs hookCmd. Reading first lets a service-build failure still
// be attributed to a namespace and session: the only network step of build is
// the Postgres ping, so every build failure is recorded as db_unavailable.
// Output and exit code are unchanged: any failure exits 0 with no output
// (silent failure per AC-31).
func runHook(ctx context.Context, cfg *config.Config, stdin io.Reader, build func(context.Context) (*memory.Service, func(), error), deps hookDeps, hookStart time.Time) error {
	input := &userPromptSubmitInput{}
	if err := json.NewDecoder(stdin).Decode(input); err != nil {
		// Silent failure: malformed input is not an error to report.
		slog.DebugContext(ctx, "failed to parse stdin", "error", err)
		return nil
	}

	svc, cleanup, err := build(ctx)
	if err != nil {
		slog.DebugContext(ctx, "failed to build service", "error", err)
		spoolHookEvents(ctx, deps, reliabilityEvent(resolveNamespaceQuiet(input.CWD), input.SessionID, time.Now(),
			memory.OutcomeError, memory.ErrClassDBUnavailable))
		return nil
	}
	defer cleanup()
	return hookCmd(ctx, cfg, svc, input, deps, hookStart)
}

// reliabilityEvent builds the hook's one search_called event per run: enums
// and ids only.
func reliabilityEvent(namespace, sessionID string, now time.Time, outcome memory.FeedbackOutcome, class memory.ErrorClass) memory.Event {
	if !sessionIDRe.MatchString(sessionID) {
		sessionID = ""
	}
	e := memory.NewEvent(now, namespace, memory.EventSearchCalled)
	e.Via, e.Outcome, e.ErrorClass, e.SessionID = memory.ViaHook, outcome, class, sessionID
	return e
}

// spoolHookEvents appends evs in one write; a failure stays at Debug.
func spoolHookEvents(ctx context.Context, deps hookDeps, evs ...memory.Event) {
	if deps.Events == nil {
		return
	}
	if err := deps.Events.Append(ctx, evs...); err != nil {
		slog.DebugContext(ctx, "failed to spool events", "error", err)
	}
}

// hookCmd implements the UserPromptSubmit hook (AC-30, AC-31, AC-32, AC-46, AC-56).
// It searches memory with the already decoded prompt,
// and outputs additional context (at most 3 cards with title/repo/id) where Similarity >= threshold.
// On error or timeout, it exits 0 with no output (silent failure per AC-31).
// Every run ends with one spool append: the search_called reliability event,
// plus the card events when cards were rendered.
func hookCmd(ctx context.Context, cfg *config.Config, svc *memory.Service, input *userPromptSubmitInput, deps hookDeps, hookStart time.Time) error {

	// Scope the search to the namespace of the prompt's project directory.
	svc = svc.WithNamespace(resolveNamespace(input.CWD))

	// One git call resolves the checkout (repo = basename of its top level)
	// and HEAD; with no card found, no further git process runs. Outside a
	// checkout the repo is the cwd's basename.
	repo := filepath.Base(input.CWD)
	if deps.History != nil {
		h := deps.History(input.SessionID)
		if co, head, ok, err := h.Resolve(ctx, input.CWD); err == nil && ok {
			repo = co.Repo
			svc = svc.WithCheckout(co).
				WithPinnedHead(head).
				WithCodeHistory(h, cfg.StaleTimeoutHook).
				WithStaleDeadline(hookStart.Add(hookTotalBudget - hookReserve))
		}
	}

	// Perform search with the full prompt (model-side truncation via AC-56).
	// Use nil embedding to let the service compute it (AC-56).
	searchReq := &memory.SearchRequest{
		Query: input.Prompt,
		Repo:  repo,
		Limit: 3, // At most 3 cards per AC-46.
		// Only results that can become cards are staleness-checked.
		MinSimilarity: cfg.HookSimThreshold,
	}

	searchResult, err := svc.Search(ctx, searchReq)
	if err != nil {
		// Silent failure: search error is not reported (AC-31).
		slog.DebugContext(ctx, "search failed", "error", err)
		if memory.IsCanceled(err) {
			return nil // not a service failure: no reliability event
		}
		class := memory.ClassifyError(err, nil)
		if deps.Classify != nil {
			class = deps.Classify(err)
		}
		spoolHookEvents(ctx, deps, reliabilityEvent(svc.Namespace(), input.SessionID, time.Now(), memory.OutcomeError, class))
		return nil
	}
	outcome := memory.OutcomeOK
	if searchResult.Degraded {
		outcome = memory.OutcomeDegraded
	}

	// Filter results by similarity threshold (compare Similarity, not Score, per AC-32).
	var cards []card
	for _, rec := range searchResult.Records {
		if rec.Similarity >= cfg.HookSimThreshold {
			cards = append(cards, card{
				Title: rec.Title,
				Repo:  rec.Repo,
				ID:    rec.ID,
				Stale: rec.Stale,

				Similarity:   rec.Similarity,
				StaleChecked: rec.StaleChecked,
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
		spoolHookEvents(ctx, deps, reliabilityEvent(svc.Namespace(), input.SessionID, time.Now(), outcome, ""))
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
		// The cards were not delivered: record the search only.
		spoolHookEvents(ctx, deps, reliabilityEvent(svc.Namespace(), input.SessionID, time.Now(), outcome, ""))
		return nil
	}

	// After the output, so a failed spool write can never change what Claude
	// Code receives (AC-22, AC-23). One append for the card events and the
	// reliability event; the append is sub-millisecond but still part of the
	// hook's wall time.
	now := time.Now()
	evs := cardEvents(cards, svc.Namespace(), input.SessionID, now)
	evs = append(evs, reliabilityEvent(svc.Namespace(), input.SessionID, now, outcome, ""))
	spoolHookEvents(ctx, deps, evs...)

	return nil
}

// cardEvents builds one card_injected event per card: ids, similarity and the
// stale tri-state (nil = unchecked, commits nil = unknown), never any text.
func cardEvents(cards []card, namespace, sessionID string, now time.Time) []memory.Event {
	if !sessionIDRe.MatchString(sessionID) {
		sessionID = ""
	}
	evs := make([]memory.Event, 0, len(cards))
	for _, c := range cards {
		e := memory.NewEvent(now, namespace, memory.EventCardInjected)
		e.RecordID = c.ID
		sim := c.Similarity
		e.Similarity = &sim
		e.SessionID = sessionID
		if c.Stale != nil {
			t := true
			e.Stale = &t
			if c.Stale.Commits > 0 {
				n := c.Stale.Commits
				e.StaleCommits = &n
			}
		} else if c.StaleChecked {
			f := false
			e.Stale = &f
		}
		evs = append(evs, e)
	}
	return evs
}
