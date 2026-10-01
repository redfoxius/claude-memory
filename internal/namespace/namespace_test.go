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
