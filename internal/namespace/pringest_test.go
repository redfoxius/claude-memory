package namespace

import (
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
