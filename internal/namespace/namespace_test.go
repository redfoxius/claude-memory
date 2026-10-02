package namespace

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "namespaces.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolve(t *testing.T) {
	c, err := Load(write(t, `
default: scratch
namespaces:
  - namespace: acme
    paths: ["/work/acme/**"]
  - namespace: acme-infra
    paths: ["/work/acme/infra/**"]
  - namespace: pet-game
    paths: ["/src/pet-game", "/src/pet-game/**", "/src/*-game"]
`))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/work/acme/billing-service":   "acme",
		"/work/acme/infra/tf":     "acme-infra", // most specific wins
		"/work/acme":              "acme",       // ** matches zero segments
		"/src/pet-game":              "pet-game",
		"/src/pet-game/server/x":     "pet-game",
		"/src/other-game":            "pet-game",
		"/elsewhere/thing":           "scratch",
		"/work/acmetwo/x":         "scratch",
		"/work/acme/../acme/y": "acme",
	}
	for dir, want := range cases {
		if got := c.Resolve(dir); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestMissingFileFallsBack(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Resolve("/any"); got != Fallback {
		t.Errorf("got %q, want %q", got, Fallback)
	}
}

func TestTildeExpansion(t *testing.T) {
	c, err := Load(write(t, "namespaces:\n  - namespace: a\n    paths: [\"~/proj/**\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	c.home = "/home/u"
	if got := c.Resolve("/home/u/proj/x"); got != "a" {
		t.Errorf("got %q", got)
	}
}

func TestInvalidNamesRejected(t *testing.T) {
	if _, err := Load(write(t, "namespaces:\n  - namespace: Bad Name\n    paths: [\"/x\"]\n")); err == nil {
		t.Error("expected error for invalid namespace name")
	}
	c := &Config{}
	if _, err := c.ForDir("../x", "/d"); err == nil {
		t.Error("expected error for invalid override")
	}
}

func TestOverrideWins(t *testing.T) {
	c := &Config{Default: "d"}
	got, err := c.ForDir("pet-game", "/d")
	if err != nil || got != "pet-game" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestInitAddAndRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "namespaces.yaml")
	if err := Init(p, "", []Rule{{Namespace: "acme", Paths: []string{"/work/acme/**"}}}, false); err != nil {
		t.Fatal(err)
	}
	if err := Init(p, "", nil, false); err == nil {
		t.Error("Init must refuse to overwrite without force")
	}
	if err := Add(p, "pet-game", "/src/pet-game/**"); err != nil {
		t.Fatal(err)
	}
	if err := Add(p, "pet-game", "/src/pet-game/**", "/src/pg2"); err != nil { // dedups
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Default != Fallback {
		t.Errorf("default = %q, want %q", c.Default, Fallback)
	}
	if got := c.Resolve("/src/pg2"); got != "pet-game" {
		t.Errorf("Resolve = %q", got)
	}
	if n := len(c.Namespaces[1].Paths); n != 2 {
		t.Errorf("pet-game paths = %d, want 2 (dedup)", n)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	if err := Add(p, "Bad Name", "/x"); err == nil {
		t.Error("expected invalid name error")
	}
	if err := Init(p, "global", nil, true); err != nil {
		t.Errorf("force init: %v", err)
	}
}

func TestExplainAndTieBreak(t *testing.T) {
	c := &Config{Default: "d", Namespaces: []Rule{
		{Namespace: "wide", Paths: []string{"/p/**"}},
		{Namespace: "exact", Paths: []string{"/p"}},
	}}
	if ns, why := c.Explain("/p"); ns != "exact" || why != "rule /p" {
		t.Errorf("Explain(/p) = %q, %q", ns, why)
	}
	if ns, _ := c.Explain("/p/x"); ns != "wide" {
		t.Errorf("Explain(/p/x) = %q", ns)
	}
	if ns, why := c.Explain("/q"); ns != "d" || why != WhyDefault {
		t.Errorf("Explain(/q) = %q, %q", ns, why)
	}
	if ns, why := (&Config{}).Explain("/q"); ns != Fallback || why != WhyFallback {
		t.Errorf("empty Explain = %q, %q", ns, why)
	}
}
