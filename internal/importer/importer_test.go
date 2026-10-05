package importer

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/redfoxius/claude-memory/internal/record"
)

// osFS is the real filesystem as the FS port (tests use temp dirs only).
type osFS struct{}

func (osFS) ReadDir(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) }
func (osFS) ReadFile(p string) ([]byte, error)       { return os.ReadFile(p) }
func (osFS) Lstat(p string) (fs.FileInfo, error)     { return os.Lstat(p) }
func (osFS) EvalSymlinks(p string) (string, error)   { return filepath.EvalSymlinks(p) }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImportKey(t *testing.T) {
	a := ImportKey("automem", "p/f.md", "hello   world\n")
	if a != ImportKey("automem", "p/f.md", "hello world") {
		t.Error("key must ignore whitespace differences")
	}
	if a == ImportKey("automem", "p/g.md", "hello world") || a == ImportKey("insights", "p/f.md", "hello world") {
		t.Error("key must depend on locator and kind")
	}
	if !strings.HasPrefix(a, "automem:") || len(a) != len("automem:")+32 {
		t.Errorf("key = %q", a)
	}
}

func TestParseAutoMemory(t *testing.T) {
	doc := func(front, body string) []byte { return []byte("---\n" + front + "\n---\n" + body) }
	cases := []struct {
		name     string
		file     string
		data     []byte
		wantKind record.Kind
		wantSkip string
		title    string
		content  string
	}{
		{"feedback", "a.md", doc("name: a\ndescription: Use X\nmetadata:\n  type: feedback", "Body text\n"), record.KindConvention, "", "Use X", "Body text"},
		{"project top-level type", "a.md", doc("description: D\ntype: project", "B"), record.KindDecision, "", "D", "B"},
		{"reference", "a.md", doc("description: D\nmetadata:\n  type: reference", "B"), record.KindPattern, "", "D", "B"},
		{"empty body uses description", "a.md", doc("description: Only desc\nmetadata:\n  type: reference", ""), record.KindPattern, "", "Only desc", "Only desc"},
		{"user skipped", "a.md", doc("description: D\nmetadata:\n  type: user", "B"), "", "type user", "", ""},
		{"unknown type", "a.md", doc("description: D\nmetadata:\n  type: weird", "B"), "", "unknown type", "", ""},
		{"missing type", "a.md", doc("description: D", "B"), "", "unknown type", "", ""},
		{"no description", "a.md", doc("name: x\nmetadata:\n  type: feedback", "B"), "", "no description", "", ""},
		{"no frontmatter", "a.md", []byte("just text"), "", "no frontmatter", "", ""},
		{"unterminated frontmatter", "a.md", []byte("---\ndescription: D\n"), "", "no frontmatter", "", ""},
		{"bad yaml", "a.md", doc("description: [unclosed", "B"), "", "invalid frontmatter", "", ""},
		{"MEMORY.md", "MEMORY.md", doc("description: D\ntype: feedback", "B"), "", "index file", "", ""},
		{"claude.md any case", "Claude.MD", doc("description: D\ntype: feedback", "B"), "", "index file", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, skip := ParseAutoMemory(tc.file, tc.data)
			if tc.wantSkip != "" {
				if skip == nil || skip.Reason != tc.wantSkip {
					t.Fatalf("skip = %+v, want %q", skip, tc.wantSkip)
				}
				return
			}
			if skip != nil {
				t.Fatalf("unexpected skip %+v", skip)
			}
			if item.Kind != tc.wantKind || item.Title != tc.title || item.Content != tc.content {
				t.Errorf("item = %+v", item)
			}
			if !slices.Equal(item.Tags, []string{"imported", "auto-memory"}) {
				t.Errorf("tags = %v", item.Tags)
			}
		})
	}
}

func TestParseAutoMemory_TitleTruncatedAndNoContentInSkip(t *testing.T) {
	long := strings.Repeat("я", 300)
	item, _ := ParseAutoMemory("a.md", []byte("---\ndescription: "+long+"\ntype: feedback\n---\nB"))
	if n := len([]rune(item.Title)); n != 160 {
		t.Errorf("title runes = %d", n)
	}
	_, skip := ParseAutoMemory("a.md", []byte("---\ndescription: SECRET-TOKEN\ntype: user\n---\nSECRET-BODY"))
	if skip == nil || strings.Contains(skip.Reason+skip.Origin, "SECRET") {
		t.Errorf("skip leaks content: %+v", skip)
	}
}

const insightsFixture = `# Insights

## What Works
- 2026-01-02 — Use the outbox pattern. It keeps writes atomic, see ` + "`src/a.ts:63-68`" + ` and ` + "`README.md`" + `.
  Second line of the entry with ` + "`not a file`" + ` and ` + "`word`" + `.
- 2026-01-03 – En dash entry without a period

- 2026-01-04 - Hyphen entry. More.

## What Doesn't Work
- 2026-02-01 — Never do X.

## Open Questions
- 2026-03-01 — Should we Y?

## Session Notes
- 2026-03-02 — Worked on Z.

## Tool & Library Notes
- 2026-04-01 — Lib quirk! Details.
- not a dated bullet
`

func TestParseInsights(t *testing.T) {
	entries, skips := ParseInsights([]byte(insightsFixture))
	if len(entries) != 5 {
		t.Fatalf("entries = %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Kind != record.KindPattern || e.Date != "2026-01-02" || e.Title != "Use the outbox pattern." {
		t.Errorf("entry 0 = %+v", e)
	}
	if !strings.Contains(e.Text, "Second line") || strings.Contains(e.Text, "2026-01-02") {
		t.Errorf("text = %q", e.Text)
	}
	if !slices.Equal(e.FileRefs, []string{"src/a.ts:63-68", "README.md"}) {
		t.Errorf("refs = %v", e.FileRefs)
	}
	if entries[1].Title != "En dash entry without a period" || entries[2].Title != "Hyphen entry." {
		t.Errorf("titles = %q / %q", entries[1].Title, entries[2].Title)
	}
	if entries[3].Kind != record.KindGotcha || entries[4].Kind != record.KindGotcha || entries[4].Title != "Lib quirk!" {
		t.Errorf("gotcha entries = %+v / %+v", entries[3], entries[4])
	}
	var reasons []string
	for _, s := range skips {
		reasons = append(reasons, s.Reason)
	}
	if !slices.Equal(reasons, []string{"section Open Questions", "section Session Notes", "unrecognized entry"}) {
		t.Errorf("skip reasons = %v", reasons)
	}
}

func fixedEnv(ns map[string]string, top func(string) (string, bool), decode map[string]string) Env {
	return Env{
		FS:          osFS{},
		NamespaceOf: func(dir string) string { return ns[dir] },
		Toplevel:    top,
		Decode: func(name string) (string, bool) {
			d, ok := decode[name]
			return d, ok
		},
	}
}

func TestDiscoverAutoMem(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	gitHome := filepath.Join(root, "work", "repo-a")
	plainHome := filepath.Join(root, "work", "plain")
	otherHome := filepath.Join(root, "other", "x")
	ok := "---\ndescription: %s\ntype: feedback\n---\nbody\n"
	mk := func(proj, name, desc string) {
		write(t, filepath.Join(projects, proj, "memory", name), strings.Replace(ok, "%s", desc, 1))
	}
	mk("-git", "one.md", "Git one")
	mk("-git", "MEMORY.md", "index")
	mk("-git", "notes.txt", "ignored")
	write(t, filepath.Join(projects, "-git", "memory", "user.md"), "---\ndescription: U\ntype: user\n---\nb")
	mk("-plain", "p.md", "Plain one")
	mk("-other", "o.md", "Other one")
	mk("-gone", "g.md", "Undecodable")
	// A symlinked .md and a nested dir are never imported.
	if err := os.Symlink(filepath.Join(projects, "-git", "memory", "one.md"), filepath.Join(projects, "-git", "memory", "link.md")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{gitHome, plainHome, otherHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := fixedEnv(
		map[string]string{gitHome: "work", plainHome: "work", otherHome: "pet"},
		func(dir string) (string, bool) { return gitHome, dir == gitHome },
		map[string]string{"-git": gitHome, "-plain": plainHome, "-other": otherHome},
	)

	items, skips, err := DiscoverAutoMem(env, projects, "work")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Item{}
	for _, it := range items {
		got[it.Origin] = it
	}
	if len(items) != 2 || got["-git/one.md"].Repo != "repo-a" || got["-plain/p.md"].Repo != "*" {
		t.Fatalf("items = %+v", items)
	}
	if it := got["-git/one.md"]; it.Home != gitHome || !strings.HasPrefix(it.Key, "automem:") || it.Kind != record.KindConvention {
		t.Errorf("item = %+v", it)
	}
	reasons := map[string]string{}
	for _, s := range skips {
		reasons[s.Origin] = s.Reason
	}
	want := map[string]string{
		"-git/MEMORY.md": "index file",
		"-git/link.md":   "not a regular file",
		"-git/user.md":   "type user",
		"-other/o.md":    "other namespace (pet)",
		"-gone":          "undecodable project dir",
	}
	for k, v := range want {
		if reasons[k] != v {
			t.Errorf("skip %s = %q, want %q (all: %v)", k, reasons[k], v, reasons)
		}
	}
}

func TestDiscoverInsights(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	top := filepath.Join(root, "repo")
	outside := filepath.Join(root, "secret.txt")
	write(t, outside, "secret")
	write(t, filepath.Join(top, "src", "a.ts"), "x")
	write(t, filepath.Join(top, "README.md"), "x")
	write(t, filepath.Join(top, "mod", "lib.go"), "x")
	write(t, filepath.Join(top, "node_modules", "INSIGHTS.md"), "## What Works\n- 2026-01-01 — skipped dir.\n")
	if err := os.Symlink(outside, filepath.Join(top, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	insights := "## What Works\n- 2026-01-02 — Real entry. Refs: `src/a.ts:63-68` `src/a.ts` `mod/lib.go` `README.md:1` `../secret.txt` `leak.txt` `/etc/passwd` `missing.go` `src/a.ts:63-68`.\n" +
		"## Open Questions\n- 2026-01-03 — Q?\n"
	write(t, filepath.Join(top, "mod", "INSIGHTS.md"), insights)
	write(t, filepath.Join(root, "plain", "insights.md"), "## What Works\n- 2026-05-05 — Non git entry. `a.go`\n")

	env := fixedEnv(
		map[string]string{top: "work", filepath.Join(root, "plain"): "work"},
		func(dir string) (string, bool) { return top, strings.HasPrefix(dir, top) },
		nil,
	)
	items, skips, err := DiscoverInsights(env, []string{top}, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v (node_modules must be skipped)", items)
	}
	it := items[0]
	wantFiles := []string{"src/a.ts:63-68", "src/a.ts", "mod/lib.go", "README.md:1"}
	if !slices.Equal(it.Files, wantFiles) {
		t.Errorf("files = %v, want %v", it.Files, wantFiles)
	}
	if it.Repo != "repo" || it.Home != top || it.Kind != record.KindPattern || !strings.HasPrefix(it.Key, "insights:") {
		t.Errorf("item = %+v", it)
	}
	if !strings.HasSuffix(it.Content, "(imported from mod/INSIGHTS.md, 2026-01-02)") {
		t.Errorf("content = %q", it.Content)
	}
	if len(skips) != 1 || skips[0].Reason != "section Open Questions" || skips[0].Origin != "mod/INSIGHTS.md:4" {
		t.Errorf("skips = %+v", skips)
	}

	// A file outside any checkout: repo "*", absolute locator, no files.
	plain := filepath.Join(root, "plain", "insights.md")
	envPlain := fixedEnv(map[string]string{filepath.Join(root, "plain"): "work"},
		func(string) (string, bool) { return "", false }, nil)
	items, _, err = DiscoverInsights(envPlain, []string{plain}, "work")
	if err != nil || len(items) != 1 {
		t.Fatalf("plain: %v %+v", err, items)
	}
	if items[0].Repo != "*" || len(items[0].Files) != 0 || !strings.Contains(items[0].Content, "(imported from "+plain+",") {
		t.Errorf("plain item = %+v", items[0])
	}

	// Another namespace is skipped as a whole.
	envOther := fixedEnv(map[string]string{top: "pet"}, env.Toplevel, nil)
	items, skips, _ = DiscoverInsights(envOther, []string{top}, "work")
	if len(items) != 0 || len(skips) != 1 || skips[0].Reason != "other namespace (pet)" {
		t.Errorf("other ns: %+v %+v", items, skips)
	}
}

func TestDiscoverRejectsNonUTF8AndNUL(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	mem := filepath.Join(projects, "-enc", "memory")
	write(t, filepath.Join(mem, "nul.md"), "---\ndescription: D\ntype: feedback\n---\nb\x00c")
	write(t, filepath.Join(mem, "bad.md"), "---\ndescription: D\ntype: feedback\n---\nb\xff\xfe")
	env := fixedEnv(map[string]string{root: "ns"}, func(string) (string, bool) { return "", false }, map[string]string{"-enc": root})
	items, skips, err := DiscoverAutoMem(env, projects, "ns")
	if err != nil || len(items) != 0 {
		t.Fatalf("items %+v err %v", items, err)
	}
	got := map[string]string{}
	for _, s := range skips {
		got[s.Origin] = s.Reason
	}
	if got["-enc/nul.md"] != "contains NUL" || got["-enc/bad.md"] != "not UTF-8" {
		t.Errorf("skips = %v", got)
	}

	ins := filepath.Join(root, "INSIGHTS.md")
	write(t, ins, "## What Works\n- 2026-01-01 \u2014 ok\xff\n")
	_, skips, _ = DiscoverInsights(env, []string{ins}, "ns")
	if len(skips) != 1 || skips[0].Reason != "not UTF-8" {
		t.Errorf("insights skips = %+v", skips)
	}
}

func TestDiscoverAutoMemDoesNotFollowSymlinkedMemoryDir(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	real := filepath.Join(root, "elsewhere")
	write(t, filepath.Join(real, "a.md"), "---\ndescription: D\ntype: feedback\n---\nb")
	if err := os.MkdirAll(filepath.Join(projects, "-enc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(projects, "-enc", "memory")); err != nil {
		t.Fatal(err)
	}
	env := fixedEnv(map[string]string{root: "ns"}, func(string) (string, bool) { return "", false }, map[string]string{"-enc": root})
	items, skips, _ := DiscoverAutoMem(env, projects, "ns")
	if len(items) != 0 || len(skips) != 0 {
		t.Errorf("followed a symlinked memory dir: %+v %+v", items, skips)
	}
}

func TestDiscoverInsightsKeyStableAcrossPathSpelling(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	top := filepath.Join(root, "repo")
	write(t, filepath.Join(top, "INSIGHTS.md"), "## What Works\n- 2026-01-02 \u2014 Same entry.\n")
	link := filepath.Join(root, "link")
	if err := os.Symlink(top, link); err != nil {
		t.Fatal(err)
	}
	env := fixedEnv(map[string]string{top: "ns"}, func(dir string) (string, bool) {
		real, _ := filepath.EvalSymlinks(dir)
		return top, strings.HasPrefix(real, top)
	}, nil)
	a, _, err := DiscoverInsights(env, []string{top}, "ns")
	if err != nil || len(a) != 1 {
		t.Fatalf("%v %+v", err, a)
	}
	b, _, err := DiscoverInsights(env, []string{link}, "ns")
	if err != nil || len(b) != 1 {
		t.Fatalf("%v %+v", err, b)
	}
	if a[0].Key != b[0].Key || a[0].Content != b[0].Content {
		t.Errorf("key/content differ across spellings: %q vs %q", a[0].Key, b[0].Key)
	}
}

func TestParseInsightsUnrecognizedBullets(t *testing.T) {
	_, skips := ParseInsights([]byte("## What Works\n- **2026-01-02** \u2014 bold\n- plain\n"))
	if len(skips) != 2 || skips[0].Reason != "unrecognized entry" {
		t.Errorf("skips = %+v", skips)
	}
}
