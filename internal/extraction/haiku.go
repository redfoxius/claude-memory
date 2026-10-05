package extraction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// HaikuRunner is a port for invoking the claude haiku model via subprocess.
// The concrete implementation calls `claude -p --model haiku --output-format json`.
type HaikuRunner interface {
	// Run invokes haiku with the given prompt and returns the JSON output.
	// Returns ErrHaikuFailed if the subprocess fails, times out, or returns non-JSON.
	Run(ctx context.Context, prompt string) ([]byte, error)
}

// CLIHaikuRunner implements HaikuRunner via `claude -p` subprocess.
type CLIHaikuRunner struct {
	timeout time.Duration
	// bin is the executable to run; empty means "claude". Tests point it at a
	// stub script.
	bin string
}

// NewCLIHaikuRunner creates a subprocess-based haiku runner with the given timeout.
func NewCLIHaikuRunner(timeout time.Duration) *CLIHaikuRunner {
	return &CLIHaikuRunner{timeout: timeout}
}

// Run invokes `claude -p --model haiku --output-format json` with the prompt on stdin.
// Arguments are passed as a slice (never shell-concatenated) to prevent injection.
// Returns ErrHaikuFailed if the subprocess fails, times out, or returns unexpected output.
func (r *CLIHaikuRunner) Run(ctx context.Context, prompt string) ([]byte, error) {
	// Create a context with timeout.
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	bin := r.bin
	if bin == "" {
		bin = "claude"
	}

	// Build the command: claude -p --model haiku --output-format json
	// --no-session-persistence (one-shot calls must not litter session history).
	// Arguments are passed as a slice to prevent command injection (A05 / AC-45).
	cmd := exec.CommandContext(runCtx, bin, "-p", "--model", "haiku", "--output-format", "json", "--no-session-persistence")

	// Provide the prompt on stdin.
	cmd.Stdin = bytes.NewBufferString(prompt)

	// Capture stdout and stderr.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the command.
	err := cmd.Run()
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%w: haiku timed out", ErrHaikuFailed)
		}
		return nil, fmt.Errorf("%w: haiku failed: %v (stderr: %s)", ErrHaikuFailed, err, stderr.String())
	}

	return unwrapHaikuEnvelope(stdout.Bytes())
}

// haikuEnvelope is the object `claude -p --output-format json` prints: the
// model's reply lands as text in Result, alongside usage/session metadata.
type haikuEnvelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// maxEnvelopeErrLen caps how much of an error result is echoed into errors/logs.
const maxEnvelopeErrLen = 300

// unwrapHaikuEnvelope extracts the bare JSON the model produced from the CLI
// envelope, stripping an optional markdown code fence. Output that is not an
// envelope (a bare array, or an object without a "type" field) passes through
// unchanged. An error envelope returns ErrHaikuFailed.
func unwrapHaikuEnvelope(stdout []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return stdout, nil
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, fmt.Errorf("%w: haiku output is not valid JSON: %v", ErrHaikuFailed, err)
	}
	if _, ok := probe["type"]; !ok {
		return stdout, nil
	}

	var env haikuEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return nil, fmt.Errorf("%w: unreadable haiku envelope: %v", ErrHaikuFailed, err)
	}
	if env.IsError || (env.Subtype != "" && env.Subtype != "success") {
		return nil, fmt.Errorf("%w: haiku reported %q: %s", ErrHaikuFailed, env.Subtype, truncateRunes(env.Result, maxEnvelopeErrLen))
	}

	return extractJSONPayload(env.Result), nil
}

// extractJSONPayload strips a markdown code fence from s; failing that it
// falls back to the span from the first '['/'{' to the last ']'/'}'. If no
// JSON-looking span exists, the trimmed text is returned and the caller's
// parser reports the problem.
func extractJSONPayload(s string) []byte {
	s = strings.TrimSpace(s)

	if strings.HasPrefix(s, "```") {
		body := strings.TrimPrefix(s, "```")
		if nl := strings.IndexByte(body, '\n'); nl >= 0 {
			body = body[nl+1:] // drop the info string ("json")
		}
		if end := strings.LastIndex(body, "```"); end >= 0 {
			body = body[:end]
		}
		return []byte(strings.TrimSpace(body))
	}

	start := strings.IndexAny(s, "[{")
	end := strings.LastIndexAny(s, "]}")
	if start >= 0 && end > start {
		return []byte(s[start : end+1])
	}
	return []byte(s)
}

// ParseHaikuOutput parses the haiku subprocess output as JSON.
// Returns ErrHaikuFailed if the output is not valid JSON.
func ParseHaikuOutput(data []byte) ([]json.RawMessage, error) {
	var candidates []json.RawMessage
	if err := json.Unmarshal(data, &candidates); err != nil {
		return nil, fmt.Errorf("%w: failed to parse haiku output as JSON: %v", ErrHaikuFailed, err)
	}
	return candidates, nil
}
