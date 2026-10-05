package extraction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/memory/mock"
	"github.com/redfoxius/claude-memory/internal/scrub"
)

// sessionSecretTranscript builds a transcript whose user text and tool output
// carry fake secrets (placeholders, not real credentials).
func sessionSecretTranscript(secrets []string) string {
	var b strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&b, `{"type":"user","cwd":"/w/svc","message":{"role":"user","content":"msg %d"}}`+"\n", i)
	}
	for i, s := range secrets {
		fmt.Fprintf(&b, `{"type":"user","message":{"role":"user","content":"here: %s"}}`+"\n", s)
		fmt.Fprintf(&b, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"out %d %s"}]}}`+"\n", i, s)
	}
	return b.String()
}

func TestProcessSessionScrubsTranscriptBeforeHaiku(t *testing.T) {
	secrets := []string{
		"ghp_" + strings.Repeat("x", 36),
		"postgres://u:p4ssw0rd@h/db",
		"Authorization: Bearer eyJfakeheader.eyJfakepayload.fakesig",
	}
	path := writeTempTranscript(t, sessionSecretTranscript(secrets))
	cfg := Config{MinMessages: 20, CharBudget: 100000, HaikuTimeout: 5 * time.Second}
	runner := singleResponseRunner([]byte(`[]`), nil)

	if _, err := ProcessSession(context.Background(), mock.NewMemoryService(), path, cfg, runner, scrub.NewAdapter(scrub.New())); err != nil {
		t.Fatal(err)
	}
	if len(runner.prompts) == 0 {
		t.Fatal("haiku was not called")
	}
	prompt := runner.prompts[0]
	for _, s := range []string{strings.Repeat("x", 36), "p4ssw0rd", "eyJfakepayload", "fakesig"} {
		if strings.Contains(prompt, s) {
			t.Errorf("secret fragment %q reached the prompt", s)
		}
	}
	if !strings.Contains(prompt, "REDACTED") {
		t.Error("redaction marker missing from prompt")
	}
}

func TestProcessSessionScrubsBeforeToolResultCap(t *testing.T) {
	// A PEM key longer than the per-result cap: capping first would leave an
	// unterminated header+body that no pattern matches.
	pem := "-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat("MIIEfakekeybody", 40) + "\n-----END RSA PRIVATE KEY-----"
	var b strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&b, `{"type":"user","message":{"role":"user","content":"msg %d"}}`+"\n", i)
	}
	body, _ := json.Marshal(pem)
	fmt.Fprintf(&b, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":%s}]}}`+"\n", body)
	path := writeTempTranscript(t, b.String())
	runner := singleResponseRunner([]byte(`[]`), nil)
	cfg := Config{MinMessages: 20, CharBudget: 100000, HaikuTimeout: 5 * time.Second}
	if _, err := ProcessSession(context.Background(), mock.NewMemoryService(), path, cfg, runner, scrub.NewAdapter(scrub.New())); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(runner.prompts[0], "MIIEfakekeybody") {
		t.Error("private key body reached the prompt")
	}
}

func TestProcessSessionNilScrubberKeepsText(t *testing.T) {
	secret := "ghp_" + strings.Repeat("x", 36)
	path := writeTempTranscript(t, sessionSecretTranscript([]string{secret}))
	runner := singleResponseRunner([]byte(`[]`), nil)
	cfg := Config{MinMessages: 20, CharBudget: 100000, HaikuTimeout: 5 * time.Second}
	if _, err := ProcessSession(context.Background(), mock.NewMemoryService(), path, cfg, runner, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.prompts[0], secret) {
		t.Error("nil scrubber must leave text untouched")
	}
}
