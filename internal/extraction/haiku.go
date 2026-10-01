package extraction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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

	// Build the command: claude -p --model haiku --output-format json
	// Arguments are passed as a slice to prevent command injection (A05 / AC-45).
	cmd := exec.CommandContext(runCtx, "claude", "-p", "--model", "haiku", "--output-format", "json")

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

	return stdout.Bytes(), nil
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
