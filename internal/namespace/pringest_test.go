package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parse(t *testing.T, body string) *Config {
	t.Helper()
	c, err := Parse([]byte(body), "ns.yaml", "/home/u")
	if err != nil {
		t.Fatalf("Parse must never fail because of pr_ingest: %v", err)
	}
	return c
}

func TestPRIngestParseValid(t *testing.T) {
	c := parse(t, `namespaces:
  - namespace: sandbox
    paths: ["/s/**"]
    pr_ingest: {enabled: false}
  - namespace: corp
    paths: ["/c/**"]
    pr_ingest: {provider: gitlab, enabled: true, future_key: 1}
`)
	if len(c.PRIngestProblems) != 0 {
		t.Fatalf("problems: %+v", c.PRIngestProblems)
	}
	p, prob := c.PRIngestFor("sandbox")
	if prob != "" || p.IsEnabled() {
		t.Errorf("sandbox: %+v %q", p, prob)
	}
	p, _ = c.PRIngestFor("corp")
	if p.Provider != "gitlab" || !p.IsEnabled() {
		t.Errorf("corp: %+v", p)
	}
	p, prob = c.PRIngestFor("nothing")
	if prob != "" || !p.IsEnabled() || p.Provider != "" {
		t.Errorf("no section must mean enabled/no override: %+v", p)
	}
}

func TestPRIngestProblemsNeverFailParseAndResolveWorks(t *testing.T) {
	for name, body := range map[string]string{
		"unknown provider": "pr_ingest: {provider: bitbucket}",
		"enabled maybe":    "pr_ingest: {enabled: maybe}",
		"enabled list":     "pr_ingest: {enabled: [1]}",
		"provider map":     "pr_ingest: {provider: {a: b}}",
		"scalar":           "pr_ingest: 5",
		"sequence":         "pr_ingest: [a, b]",
	} {
		t.Run(name, func(t *testing.T) {
			c := parse(t, "namespaces:\n  - namespace: bad\n    paths: [\"/b/**\"]\n    "+body+"\n  - namespace: ok\n    paths: [\"/o/**\"]\n")
			if len(c.PRIngestProblems) != 1 || c.PRIngestProblems[0].Namespace != "bad" {
				t.Fatalf("problems = %+v", c.PRIngestProblems)
			}
			if got := c.Resolve("/b/x"); got != "bad" {
				t.Errorf("Resolve broke: %q", got)
			}
			if _, prob := c.PRIngestFor("bad"); prob == "" {
				t.Error("PRIngestFor must report the problem")
			}
		})
	}
}

func TestPRIngestConflictBetweenRules(t *testing.T) {
	c := parse(t, `namespaces:
  - namespace: a
    paths: ["/1/**"]
    pr_ingest: {enabled: false}
  - namespace: a
    paths: ["/2/**"]
    pr_ingest: {enabled: true}
`)
	if len(c.PRIngestProblems) != 1 || !strings.Contains(c.PRIngestProblems[0].Reason, "differs") {
		t.Fatalf("problems = %+v", c.PRIngestProblems)
	}
	// Identical sections (or one section only) are fine.
	c = parse(t, `namespaces:
  - namespace: a
    paths: ["/1/**"]
    pr_ingest: {enabled: false}
  - namespace: a
    paths: ["/2/**"]
    pr_ingest: {enabled: false}
  - namespace: a
    paths: ["/3/**"]
`)
	if len(c.PRIngestProblems) != 0 {
		t.Errorf("problems = %+v", c.PRIngestProblems)
	}
}

func TestPRIngestSurvivesMarshalRoundTrip(t *testing.T) {
	c := parse(t, `namespaces:
  - namespace: sandbox
    paths: ["/s/**"]
    pr_ingest: {enabled: false, provider: github}
  - namespace: plain
    paths: ["/p/**"]
`)
	out, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "pr_ingest:") || strings.Count(string(out), "pr_ingest:") != 1 {
		t.Fatalf("pr_ingest must be emitted exactly for the rule that has it:\n%s", out)
	}
	c2, err := Parse(out, "x", "/h")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := c2.PRIngestFor("sandbox")
	if p.IsEnabled() || p.Provider != "github" {
		t.Errorf("round trip lost it: %+v", p)
	}
	out2, _ := Marshal(c2)
	if string(out) != string(out2) {
		t.Errorf("not stable:\n%s\n---\n%s", out, out2)
	}
}

func TestAddKeepsPRIngest(t *testing.T) {
	c := parse(t, "namespaces:\n  - namespace: s\n    paths: [\"/s\"]\n    pr_ingest: {enabled: false}\n")
	if err := c.Add("s", "/s2"); err != nil {
		t.Fatal(err)
	}
	if p, _ := c.PRIngestFor("s"); p.IsEnabled() {
		t.Error("Add dropped pr_ingest")
	}
}

func TestMalformedPRIngestIsNeverRewritten(t *testing.T) {
	for name, pr := range map[string]string{
		"false":         "pr_ingest: false",
		"enabled maybe": "pr_ingest: {enabled: maybe}",
		"provider list": "pr_ingest: {provider: [gitlab]}",
		"unknown":       "pr_ingest: {provider: bitbucket}",
	} {
		t.Run(name, func(t *testing.T) {
			body := "namespaces:\n  - namespace: s\n    paths: [\"/s\"]\n    " + pr + "\n"
			path := filepath.Join(t.TempDir(), "ns.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			// Before: skipped by ingest-pr (problem reported).
			c, _ := Load(path)
			if _, prob := c.PRIngestFor("s"); prob == "" {
				t.Fatal("must be a problem before")
			}
			if _, err := Marshal(c); err == nil {
				t.Error("Marshal must refuse")
			}
			for _, op := range []func() error{
				func() error { return Add(path, "s", "/more") },
				func() error { return Add(path, "other", "/o") },
				func() error { return Save(path, c) },
			} {
				err := op()
				if err == nil || !strings.Contains(err.Error(), "fix pr_ingest in "+path+" first") {
					t.Errorf("want clear refusal, got %v", err)
				}
			}
			if got, _ := os.ReadFile(path); string(got) != body {
				t.Errorf("file changed:\n%s", got)
			}
			// After (nothing written): still skipped.
			c2, _ := Load(path)
			if _, prob := c2.PRIngestFor("s"); prob == "" {
				t.Error("must stay skipped after the refused rewrite")
			}
		})
	}
}

func TestValidPRIngestStillRoundTripsThroughAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ns.yaml")
	_ = os.WriteFile(path, []byte("namespaces:\n  - namespace: s\n    paths: [\"/s\"]\n    pr_ingest: {enabled: false}\n"), 0o600)
	if err := Add(path, "s", "/more"); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(path)
	if p, prob := c.PRIngestFor("s"); prob != "" || p.IsEnabled() {
		t.Errorf("lost opt-out: %+v %q", p, prob)
	}
}
