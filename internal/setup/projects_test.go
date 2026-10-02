package setup

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestDecodeProjectName(t *testing.T) {
	h := newS23(t)
	for _, d := range []string{"work/Block-strike", "work/acme/billing-service", "work/a/b", "work/a-b", "work/c/d", "plain"} {
		h.write(filepath.Join(h.root, d, ".keep"), "", 0o600)
	}
	dash := func(rel string) string {
		return "-" + replaceSlashes(filepath.Join(h.root, rel)[1:])
	}
	for _, c := range []struct {
		rel  string
		want string
		ok   bool
	}{
		{"work/Block-strike", "work/Block-strike", true}, // literal hyphen
		{"work/acme/billing-service", "work/acme/billing-service", true},
		{"work/a/b", "work/a-b", true}, // both exist: the longest join wins (documented)
		{"work/c/d", "work/c/d", true},
		{"work/a-b", "work/a-b", true},
		{"work/missing", "", false},
		{"work/Block-strike-nope", "", false},
	} {
		got, ok := DecodeProjectName(h.fs, h.root, dash(c.rel))
		want := ""
		if c.ok {
			want = filepath.Join(h.root, c.want)
		}
		if ok != c.ok || got != want {
			t.Errorf("%s: got %q %v, want %q %v", c.rel, got, ok, want, c.ok)
		}
	}
	for _, bad := range []string{"", "-", "noprefix", "-nonexistent-dir", "-etc-passwd"} {
		if got, ok := DecodeProjectName(h.fs, h.root, bad); ok {
			t.Errorf("%q decoded to %q", bad, got)
		}
	}
}

func replaceSlashes(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c == '/' {
			b[i] = '-'
		}
	}
	return string(b)
}

func TestProjectDirsAndSuggestions(t *testing.T) {
	h := newS23(t)
	for _, d := range []string{"work/acme/x", "work/acme/y-z", "src/game"} {
		h.write(filepath.Join(h.root, d, ".keep"), "", 0o600)
	}
	proj := filepath.Join(h.p.ClaudeDir, "projects")
	for _, rel := range []string{"work/acme/x", "work/acme/y-z", "src/game", "gone/away"} {
		h.write(filepath.Join(proj, "-"+replaceSlashes(filepath.Join(h.root, rel)[1:]), "s.jsonl"), "{}", 0o600)
	}
	h.write(filepath.Join(proj, "stray-file"), "x", 0o600)
	got := ProjectDirs(h.fs, h.root, proj)
	want := []string{
		filepath.Join(h.root, "src/game"),
		filepath.Join(h.root, "work/acme/x"),
		filepath.Join(h.root, "work/acme/y-z"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectDirs = %v, want %v", got, want)
	}
	sug := SuggestParentDirs(got, h.p.Home, 5)
	wantSug := []string{filepath.Join(h.root, "work/acme"), filepath.Join(h.root, "src")}
	if !reflect.DeepEqual(sug, wantSug) {
		t.Errorf("suggestions = %v, want %v", sug, wantSug)
	}
	if got := SuggestParentDirs([]string{h.p.Home + "/p", "/top"}, h.p.Home, 5); len(got) != 0 {
		t.Errorf("home/root parents suggested: %v", got)
	}
	if got := ProjectDirs(h.fs, h.root, filepath.Join(h.root, "nope")); got != nil {
		t.Errorf("missing dir: %v", got)
	}
}
