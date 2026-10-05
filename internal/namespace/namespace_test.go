package namespace

import (
	"os"
	"path/filepath"
	"strings"
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
  - namespace: work
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
		"/work/acme/billing-service": "work",
		"/work/acme/infra/tf":        "acme-infra", // most specific wins
		"/work/acme":                 "work",       // ** matches zero segments
		"/src/pet-game":              "pet-game",
		"/src/pet-game/server/x":     "pet-game",
		"/src/other-game":            "pet-game",
		"/elsewhere/thing":           "scratch",
		"/work/acmetwo/x":            "scratch",
		"/work/acme/../acme/y":       "work",
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
	if err := Init(p, "", []Rule{{Namespace: "work", Paths: []string{"/work/acme/**"}}}, false); err != nil {
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

func TestMarshalMatchesSave(t *testing.T) {
	c := &Config{Default: Fallback, Namespaces: []Rule{}}
	if err := c.Add("work", "~/work/acme/**"); err != nil {
		t.Fatal(err)
	}
	if err := c.Add("work", "~/work/acme/**", "~/src/x"); err != nil { // duplicate glob skipped
		t.Fatal(err)
	}
	got, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "n.yaml")
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	disk, _ := os.ReadFile(p)
	if string(got) != string(disk) {
		t.Errorf("Marshal != Save output:\n%s\n---\n%s", got, disk)
	}
	if !strings.HasPrefix(string(got), "# claude-memory namespaces.") {
		t.Errorf("no header: %s", got)
	}
	back, err := Parse(got, p, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Namespaces) != 1 || len(back.Namespaces[0].Paths) != 2 {
		t.Errorf("round trip: %+v", back.Namespaces)
	}
}

func TestConfigAddValidates(t *testing.T) {
	c := &Config{}
	if err := c.Add("Bad Name", "/x"); err == nil {
		t.Error("invalid name accepted")
	}
	if err := c.Add("ok"); err == nil {
		t.Error("no globs accepted")
	}
}

func TestAddRejectsInvalidGlobs(t *testing.T) {
	for _, g := range []string{"/work/[unclosed/**", "/src/a[", "", "  ", "/x/\\"} {
		c := &Config{}
		if err := c.Add("ok", g); err == nil {
			t.Errorf("Add accepted glob %q", g)
		}
		if len(c.Namespaces) != 0 {
			t.Errorf("rejected glob %q still created a namespace", g)
		}
	}
	// A bad glob among good ones rejects the whole call.
	c := &Config{}
	if err := c.Add("ok", "/good/**", "/bad/["); err == nil || len(c.Namespaces) != 0 {
		t.Errorf("mixed globs: err=%v namespaces=%v", err, c.Namespaces)
	}
	for _, g := range []string{"/work/acme/**", "~/src/pet-game", "$PWD/**", "/src/*-game", "/a/[bc]/**"} {
		if err := (&Config{}).Add("ok", g); err != nil {
			t.Errorf("Add rejected valid glob %q: %v", g, err)
		}
	}
}

func TestInitAndAddRejectInvalidGlobsWithoutWriting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "namespaces.yaml")
	err := Init(p, "", []Rule{{Namespace: "x", Paths: []string{"/a/["}}}, false)
	if err == nil || !strings.Contains(err.Error(), "invalid path glob") {
		t.Fatalf("Init err = %v, want invalid path glob", err)
	}
	if _, statErr := os.Stat(p); statErr == nil {
		t.Error("Init wrote a file despite the invalid glob")
	}
	if err := Init(p, "Bad Default", nil, false); err == nil {
		t.Error("Init accepted an invalid default")
	}
	if err := Init(p, "", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := Add(p, "x", "/a/["); err == nil {
		t.Error("Add accepted an invalid glob")
	}
}
