package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/extraction"
	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/scrub"
	"github.com/redfoxius/claude-memory/internal/transcript"
)

// defaultHaikuTimeout bounds a single `claude -p` subprocess call made by
// extraction. Not configurable via MEMORY_* env (unlike the thresholds in
// internal/config): it's an internal subprocess budget, not a tunable the
// plan calls out as a documented default.
const defaultHaikuTimeout = 60 * time.Second

// subprocessGuard reports whether this process was started from inside one of
// claude-memory's own `claude -p` calls (extraction.SubprocessEnv is set). The
// hook and extract entry points check it first, before any DB/Ollama setup.
func subprocessGuard() bool {
	return os.Getenv(extraction.SubprocessEnv) != ""
}

// sessionEndInput matches Claude Code's SessionEnd hook stdin JSON contract.
type sessionEndInput struct {
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	SessionID      string `json:"session_id"`
}

// cmdExtract dispatches the "extract" subcommand's two modes:
//   - `claude-memory extract` (no --run): the SessionEnd hook entry point.
//     Reads the hook's stdin JSON, launches a fully detached background
//     re-exec of itself in --run mode, and returns immediately (AC-21).
//   - `claude-memory extract --run <transcript_path>`: the actual extraction
//     work, run by the detached process. Applies gating (AC-22) and calls
//     extraction.ProcessSession with the real `claude -p` haiku runner.
func cmdExtract(cfg *config.Config) error {
	if subprocessGuard() {
		return nil // transcript of an internal `claude -p` call: nothing to extract
	}
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	runPath := fs.String("run", "", "internal: run extraction synchronously against this transcript path")
	if err := fs.Parse(flag.Args()[1:]); err != nil {
		return fmt.Errorf("parse extract flags: %w", err)
	}

	if *runPath != "" {
		return runExtract(cfg, *runPath)
	}

	executable, err := os.Executable()
	if err != nil {
		// Can't find our own binary path to re-exec; log and exit 0 rather
		// than fail the hook (extraction is best-effort, never blocking).
		slog.Debug("extract: could not resolve own executable path", "error", err)
		return nil
	}

	logPath := filepath.Join(stateDir(), "extract.log")
	return extractCmd(os.Stdin, executable, logPath)
}

// extractCmd reads the SessionEnd hook's stdin JSON and launches a detached
// background re-exec (`<executable> extract --run <transcript_path>`),
// returning without waiting for it (AC-21: hook-added latency measurably
// under 100ms). Malformed stdin or a missing transcript_path is treated as
// "nothing to extract", not an error — the hook must never fail the
// session-end flow.
func extractCmd(stdin io.Reader, executable string, logPath string) error {
	input := &sessionEndInput{}
	if err := json.NewDecoder(stdin).Decode(input); err != nil {
		slog.Debug("extract: failed to parse SessionEnd stdin", "error", err)
		return nil
	}
	if input.TranscriptPath == "" {
		return nil
	}

	if err := launchDetached(executable, logPath, "extract", "--run", input.TranscriptPath); err != nil {
		slog.Debug("extract: failed to launch detached extraction", "error", err)
		return nil
	}
	return nil
}

// launchDetached starts executable with args as a fully detached background
// process: a new session (Setsid, so it survives the parent's exit and
// isn't in the parent's process group), stdin from /dev/null, stdout/stderr
// appended to logPath (falling back to /dev/null if the log file can't be
// opened). It calls Start(), never Wait() — the caller must return
// immediately without blocking on the child.
func launchDetached(executable, logPath string, args ...string) error {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}

	logFile := devnull
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err == nil {
			if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				logFile = f
			}
		}
	}

	cmd := exec.Command(executable, args...)
	cmd.Stdin = devnull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start detached process: %w", err)
	}
	// Intentionally not Wait()-ing: this process (the hook) is about to
	// return/exit; the detached child is reparented once we do, so no
	// zombie is left behind, and we never delay the hook on its completion.
	return nil
}

// sessionScrubber is the redactor handed to extraction.ProcessSession so
// transcript text is scrubbed before it is sent to haiku.
func sessionScrubber() memory.Scrubber { return scrub.NewAdapter(scrub.New()) }

// runExtract implements the --run mode: gating (AC-22, inside ProcessSession)
// and extraction against the real claude-memory service and the real
// `claude -p` haiku runner (nil runner -> extraction.ProcessSession
// constructs extraction.NewCLIHaikuRunner itself).
func runExtract(cfg *config.Config, transcriptPath string) error {
	ctx := context.Background()

	svc, sink, cleanup, err := buildServiceWithEvents(ctx, cfg)
	if err != nil {
		// Best-effort background job: log and exit 0, never crash noisily.
		slog.Error("extract --run: failed to build service", "error", err)
		return nil
	}
	defer cleanup()
	drainSpool(ctx, sink, 30*time.Second)

	// The session's own working directory decides the namespace, repo and
	// commit-baseline checkout; if the transcript can't be read here,
	// ProcessSession reports it below.
	extractionRepo := ""
	if tr, perr := transcript.Parse(transcriptPath, transcript.Config{CharBudget: cfg.MaxContentChars}); perr == nil && tr.Cwd != "" {
		svc, extractionRepo = scopeSessionService(ctx, svc, newCodeHistory(), cfg, tr.Cwd)
	} else {
		// No cwd to map: the namespace falls back to the default, so say so.
		slog.Warn("extract --run: transcript has no cwd; using the fallback namespace", "transcript", transcriptPath)
		svc = svc.WithNamespace(resolveNamespace(""))
	}

	extractionCfg := extraction.Config{
		MinMessages:  cfg.ExtractMinMessages,
		CharBudget:   cfg.MaxContentChars,
		HaikuTimeout: defaultHaikuTimeout,
		Repo:         extractionRepo,
	}

	result, err := extraction.ProcessSession(ctx, svc, transcriptPath, extractionCfg, nil, sessionScrubber())
	if err != nil {
		slog.Error("extract --run failed", "transcript", transcriptPath, "error", err)
		return nil
	}

	slog.Info("extract --run completed",
		"transcript", transcriptPath,
		"processed", result.RecordsProcessed,
		"stored", result.RecordsStored,
		"skipped_reason", result.SkippedReason,
		"errors", result.Errors)
	return nil
}

// stateDir returns the base directory for claude-memory's runtime state
// (logs), creating no directory itself — callers create it on demand.
// Default: ~/.local/state/claude-memory.
func stateDir() string {
	home := os.Getenv("HOME")
	return filepath.Join(home, ".local", "state", "claude-memory")
}

// scopeSessionService scopes svc to a session's working directory: its
// namespace, and — when cwd is inside a git checkout — the checkout, so the
// repo is the checkout's top-level name (not basename(cwd), a sub-directory
// name for sessions started below the top level) and commit baselines can be
// stamped. HEAD is deliberately not pinned: extraction can run for minutes
// and a commit landing meanwhile must not be stamped with a pre-commit sha.
// repo is "" outside a checkout (extraction then falls back to its own
// inference).
func scopeSessionService(ctx context.Context, svc *memory.Service, history memory.CodeHistory, cfg *config.Config, cwd string) (*memory.Service, string) {
	ns := resolveNamespace(cwd)
	warnIfFallback(cwd, ns)
	return scopeCheckout(ctx, svc.WithNamespace(ns), history, cfg, cwd)
}

// scopeCheckout is the checkout half of scopeSessionService: it leaves the
// namespace alone (the management CLI resolves that itself, without the
// fallback warning).
func scopeCheckout(ctx context.Context, svc *memory.Service, history memory.CodeHistory, cfg *config.Config, cwd string) (*memory.Service, string) {
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	co, _, ok, err := history.Resolve(rctx, cwd)
	if err != nil || !ok {
		return svc, ""
	}
	return svc.WithCheckout(co).WithCodeHistory(history, cfg.StaleTimeout), co.Repo
}
