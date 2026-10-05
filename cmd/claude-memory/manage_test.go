package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/record"
)

// fakeMgmt is an in-memory mgmtService.
type fakeMgmt struct {
	ns        string
	recs      []*record.Record // all namespaces; ListRecords filters to ns
	checkout  *memory.Checkout
	hint      *memory.StaleHint
	similar   map[string][]*memory.Candidate
	noEmbed   map[string]bool
	deleteErr error
	failAct   string // "update", "deprecate": that call fails

	updates    []*memory.UpdateRequest
	deprecates []*memory.DeprecateRequest
	deletes    []string
}

func (f *fakeMgmt) Namespace() string { return f.ns }

func (f *fakeMgmt) find(id string) *record.Record {
	for _, r := range f.recs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (f *fakeMgmt) ListRecords(_ context.Context, fl memory.ListFilters) ([]*record.Record, error) {
	var out []*record.Record
	for _, r := range f.recs {
		if r.Namespace != f.ns || (fl.Status != nil && r.Status != *fl.Status) ||
			(fl.Kind != nil && r.Kind != *fl.Kind) || (fl.Repo != nil && r.Repo != *fl.Repo) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeMgmt) GetRecord(_ context.Context, id string) (*record.Record, error) {
	if r := f.find(id); r != nil && (r.Namespace == f.ns || r.Namespace == record.GlobalNamespace) {
		return r, nil
	}
	return nil, memory.ErrNotFound
}

func (f *fakeMgmt) UpdateRecord(_ context.Context, req *memory.UpdateRequest) (*record.Record, error) {
	if f.failAct == "update" {
		return nil, errors.New("update failed")
	}
	f.updates = append(f.updates, req)
	r := f.find(req.ID)
	if req.Status != nil {
		r.Status = *req.Status
	}
	if req.Title != nil {
		r.Title = *req.Title
	}
	if req.Content != nil {
		r.Content = *req.Content
	}
	return r, nil
}

func (f *fakeMgmt) DeprecateRecord(_ context.Context, req *memory.DeprecateRequest) (*record.Record, error) {
	if f.failAct == "deprecate" {
		return nil, errors.New("deprecate failed")
	}
	f.deprecates = append(f.deprecates, req)
	r := f.find(req.ID)
	r.Status = record.StatusDeprecated
	return r, nil
}

func (f *fakeMgmt) DeleteRecord(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletes = append(f.deletes, id)
	return nil
}

func (f *fakeMgmt) Similar(_ context.Context, id string, _ int) ([]*memory.Candidate, error) {
	if f.noEmbed[id] {
		return nil, memory.ErrNoEmbedding
	}
	return f.similar[id], nil
}

func (f *fakeMgmt) StaleHint(context.Context, *record.Record) *memory.StaleHint { return f.hint }

func (f *fakeMgmt) Checkout() (memory.Checkout, bool) {
	if f.checkout == nil {
		return memory.Checkout{}, false
	}
	return *f.checkout, true
}

var mgmtNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func mkRec(id, ns, title string, st record.Status, ago time.Duration) *record.Record {
	return &record.Record{
		ID: id, Namespace: ns, Kind: record.KindPattern, Title: title, Content: "body of " + title,
		Repo: "svc", Status: st, Source: record.SourceSession, Confidence: 0.5,
		CreatedAt: mgmtNow.Add(-ago), UpdatedAt: mgmtNow.Add(-ago),
	}
}

const (
	idA = "aaaaaaaa-1111-4111-8111-111111111111"
	idB = "aaaaaaaa-2222-4222-8222-222222222222"
	idC = "cccccccc-3333-4333-8333-333333333333"
	idD = "dddddddd-4444-4444-8444-444444444444"
	idG = "99999999-5555-4555-8555-555555555555"
	idE = "eeeeeeee-6666-4666-8666-666666666666"
)

// harness wires a fakeMgmt into mgmtDeps with captured output.
type harness struct {
	svc      *fakeMgmt
	out, err bytes.Buffer
	d        mgmtDeps
	opened   []string // ns values Open was called with
	warned   []bool
	editor   func(path string) error
}

func newHarness(stdin string, recs ...*record.Record) *harness {
	h := &harness{svc: &fakeMgmt{ns: "ns1", recs: recs}}
	h.d = mgmtDeps{
		Open: func(ns string, warn bool) (mgmtService, func(), error) {
			h.opened = append(h.opened, ns)
			h.warned = append(h.warned, warn)
			if ns != "" {
				h.svc.ns = ns
			}
			return h.svc, func() {}, nil
		},
		Stdin:  strings.NewReader(stdin),
		Out:    &h.out,
		Err:    &h.err,
		Editor: func(path string) error { return h.editor(path) },
		Now:    func() time.Time { return mgmtNow },
	}
	return h
}

func TestResolveID(t *testing.T) {
	svc := &fakeMgmt{ns: "ns1", recs: []*record.Record{
		mkRec(idA, "ns1", "a", record.StatusActive, 0),
		mkRec(idB, "ns1", "b", record.StatusDeprecated, 0),
		mkRec(idC, "ns1", "c", record.StatusCandidate, 0),
		mkRec(idG, "global", "g", record.StatusActive, 0),
		mkRec(idD, "other", "d", record.StatusActive, 0),
	}}
	cases := []struct {
		arg     string
		wantID  string
		errPart string
	}{
		{"cccccccc", idC, ""},
		{"CCCCCCCC", idC, ""}, // case-insensitive
		{idC, idC, ""},
		{"aaaaaaaa", "", "ambiguous"},
		{idB, idB, ""}, // full uuid of a deprecated record
		{"bbbbbbbb", "", "not found"},
		{"99999999", "", "not found"}, // global: full UUID only
		{idG, idG, ""},
		{"dddddddd", "", "not found"}, // other namespace
		{idD, "", "not found"},
		{"ccccccc", "", "invalid id"},   // 7 hex is not a prefix
		{"ccccccccc", "", "invalid id"}, // 9 hex is not a prefix
		{"zzzzzzzz", "", "invalid id"},
	}
	for _, tc := range cases {
		rec, err := resolveID(context.Background(), svc, tc.arg)
		if tc.errPart != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errPart) {
				t.Errorf("%q: err = %v, want %q", tc.arg, err, tc.errPart)
			}
			continue
		}
		if err != nil || rec.ID != tc.wantID {
			t.Errorf("%q: rec = %v err = %v, want %s", tc.arg, rec, err, tc.wantID)
		}
	}
	// The ambiguity error lists both matches.
	_, err := resolveID(context.Background(), svc, "aaaaaaaa")
	if !strings.Contains(err.Error(), idA) || !strings.Contains(err.Error(), idB) {
		t.Errorf("ambiguity error lacks matches: %v", err)
	}
}

func TestNamespaceFlag(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid namespace is a usage error and opens nothing", func(t *testing.T) {
		for _, run := range []func(context.Context, mgmtDeps, []string) error{runLs, runShow, runRm, runEdit, runPromote, runReview} {
			h := newHarness("")
			err := run(ctx, h.d, []string{"--namespace", "Bad Name", idA})
			if exitCode(err) != 2 {
				t.Errorf("exit = %d (err %v), want 2", exitCode(err), err)
			}
			if len(h.opened) != 0 {
				t.Error("Open was called for an invalid namespace")
			}
		}
	})

	t.Run("passed to Open; ls and show do not warn about the fallback", func(t *testing.T) {
		h := newHarness("", mkRec(idC, "other", "c", record.StatusCandidate, 0))
		if err := runLs(ctx, h.d, []string{"--namespace", "other"}); err != nil {
			t.Fatal(err)
		}
		if err := runShow(ctx, h.d, []string{"--namespace=other", "cccccccc"}); err != nil {
			t.Fatal(err)
		}
		if err := runPromote(ctx, h.d, []string{"--namespace", "other", "cccccccc"}); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(h.opened, []string{"other", "other", "other"}) || !slices.Equal(h.warned, []bool{false, false, true}) {
			t.Errorf("opened %v warned %v", h.opened, h.warned)
		}
	})

	t.Run("mutating commands refuse a record of another namespace", func(t *testing.T) {
		for name, run := range map[string]func(context.Context, mgmtDeps, []string) error{
			"rm": runRm, "edit": runEdit, "promote": runPromote,
		} {
			h := newHarness("", mkRec(idG, "global", "g", record.StatusCandidate, 0))
			err := run(ctx, h.d, []string{idG})
			if err == nil || !strings.Contains(err.Error(), "record is in namespace global; use --namespace global") {
				t.Errorf("%s: err = %v", name, err)
			}
			if len(h.svc.updates)+len(h.svc.deprecates)+len(h.svc.deletes) != 0 {
				t.Errorf("%s wrote to a foreign record", name)
			}
		}
	})
}

func lsRecs() []*record.Record {
	return []*record.Record{
		mkRec(idA, "ns1", "newest candidate", record.StatusCandidate, time.Hour),
		mkRec(idB, "ns1", "an active one", record.StatusActive, 2*time.Hour),
		mkRec(idC, "ns1", "a deprecated one", record.StatusDeprecated, 3*time.Hour),
		mkRec(idD, "ns1", strings.Repeat("long", 40), record.StatusActive, 4*time.Hour),
	}
}

func TestLs(t *testing.T) {
	ctx := context.Background()

	t.Run("default drops deprecated, one line per record, title truncated", func(t *testing.T) {
		h := newHarness("", lsRecs()...)
		if err := runLs(ctx, h.d, nil); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(h.out.String()), "\n")
		if len(lines) != 3 {
			t.Fatalf("lines = %d:\n%s", len(lines), h.out.String())
		}
		if !strings.HasPrefix(lines[0], "aaaaaaaa  candidate") || !strings.Contains(lines[0], "2026-10-02") ||
			!strings.HasSuffix(lines[0], "newest candidate") {
			t.Errorf("line 0 = %q", lines[0])
		}
		if strings.Contains(h.out.String(), "cccccccc") {
			t.Error("deprecated record listed by default")
		}
		last := lines[2]
		title := last[strings.LastIndex(last, "  ")+2:]
		if n := len([]rune(title)); n != 80 || !strings.HasSuffix(title, "…") {
			t.Errorf("title = %q (%d runes)", title, n)
		}
	})

	t.Run("filters and limit", func(t *testing.T) {
		h := newHarness("", lsRecs()...)
		if err := runLs(ctx, h.d, []string{"--status", "deprecated"}); err != nil {
			t.Fatal(err)
		}
		if got := h.out.String(); !strings.Contains(got, "cccccccc") || strings.Count(got, "\n") != 1 {
			t.Errorf("--status deprecated:\n%s", got)
		}
		h = newHarness("", lsRecs()...)
		if err := runLs(ctx, h.d, []string{"--limit", "1"}); err != nil {
			t.Fatal(err)
		}
		if strings.Count(h.out.String(), "\n") != 1 {
			t.Errorf("--limit 1:\n%s", h.out.String())
		}
		for _, bad := range [][]string{{"--status", "x"}, {"--kind", "x"}, {"--limit", "-1"}, {"extra"}} {
			if err := runLs(ctx, newHarness("").d, bad); exitCode(err) != 2 {
				t.Errorf("%v: exit %d", bad, exitCode(err))
			}
		}
	})

	t.Run("empty", func(t *testing.T) {
		h := newHarness("")
		if err := runLs(ctx, h.d, nil); err != nil || !strings.Contains(h.out.String(), "no records") {
			t.Errorf("out %q err %v", h.out.String(), err)
		}
	})
}

func TestShow(t *testing.T) {
	ctx := context.Background()
	sha := "0123456789abcdef0123456789abcdef01234567"
	rec := mkRec(idA, "ns1", "the title", record.StatusActive, time.Hour)
	rec.Tags, rec.Files, rec.CommitSHA = []string{"t1", "t2"}, []string{"a/b.go"}, &sha

	t.Run("text has every field and an unchecked line without a checkout", func(t *testing.T) {
		h := newHarness("", rec)
		if err := runShow(ctx, h.d, []string{"aaaaaaaa"}); err != nil {
			t.Fatal(err)
		}
		got := h.out.String()
		for _, want := range []string{"id:          " + idA, "kind:        pattern", "status:      active", "source:      session",
			"repo:        svc", "namespace:   ns1", "tags:        t1, t2", "files:       a/b.go", "commit_sha:  " + sha,
			"ticket:      -", "confidence:  0.50", "seen/used:   0/0", "deprecation: -", "superseded:  -",
			"\nunchecked\n", "body of the title"} {
			if !strings.Contains(got, want) {
				t.Errorf("output lacks %q:\n%s", want, got)
			}
		}
	})

	t.Run("stale line", func(t *testing.T) {
		cases := []struct {
			name string
			co   *memory.Checkout
			hint *memory.StaleHint
			sha  *string
			want string
		}{
			{"stale with count", &memory.Checkout{Repo: "svc", Dir: "/w/svc"}, &memory.StaleHint{Commits: 3}, &sha, "stale: 3 commits"},
			{"stale unknown count", &memory.Checkout{Repo: "svc", Dir: "/w/svc"}, &memory.StaleHint{}, &sha, "\nstale\n"},
			{"fresh", &memory.Checkout{Repo: "svc", Dir: "/w/svc"}, nil, &sha, "\nfresh\n"},
			{"other repo", &memory.Checkout{Repo: "other", Dir: "/w/other"}, &memory.StaleHint{Commits: 3}, &sha, "\nunchecked\n"},
			{"no baseline", &memory.Checkout{Repo: "svc", Dir: "/w/svc"}, &memory.StaleHint{Commits: 3}, nil, "\nunchecked\n"},
		}
		for _, tc := range cases {
			r := *rec
			r.CommitSHA = tc.sha
			h := newHarness("", &r)
			h.svc.checkout, h.svc.hint = tc.co, tc.hint
			if err := runShow(ctx, h.d, []string{"aaaaaaaa"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(h.out.String(), tc.want) {
				t.Errorf("%s: output lacks %q:\n%s", tc.name, tc.want, h.out.String())
			}
		}
	})

	t.Run("a global record by full UUID", func(t *testing.T) {
		h := newHarness("", mkRec(idG, "global", "g", record.StatusActive, 0))
		if err := runShow(ctx, h.d, []string{idG}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("not found and missing id", func(t *testing.T) {
		h := newHarness("")
		if err := runShow(ctx, h.d, []string{"cccccccc"}); err == nil || exitCode(err) != 1 {
			t.Errorf("err = %v", err)
		}
		if err := runShow(ctx, h.d, nil); exitCode(err) != 2 {
			t.Errorf("no id: exit %d", exitCode(err))
		}
	})
}

func TestRm(t *testing.T) {
	ctx := context.Background()
	mk := func(stdin string) *harness {
		return newHarness(stdin, mkRec(idA, "ns1", "doomed", record.StatusActive, 0), mkRec(idB, "ns1", "gone", record.StatusDeprecated, 0))
	}

	t.Run("deprecates by default with the default reason", func(t *testing.T) {
		h := mk("")
		if err := runRm(ctx, h.d, []string{idA}); err != nil {
			t.Fatal(err)
		}
		if len(h.svc.deprecates) != 1 || h.svc.deprecates[0].Reason != "removed via CLI" || len(h.svc.deletes) != 0 {
			t.Errorf("deprecates %+v deletes %v", h.svc.deprecates, h.svc.deletes)
		}
	})
	t.Run("--reason, flag after the id", func(t *testing.T) {
		h := mk("")
		if err := runRm(ctx, h.d, []string{idA, "--reason", "outdated"}); err != nil {
			t.Fatal(err)
		}
		if h.svc.deprecates[0].Reason != "outdated" {
			t.Errorf("reason = %q", h.svc.deprecates[0].Reason)
		}
	})
	t.Run("already deprecated exits 0 without a write", func(t *testing.T) {
		h := mk("")
		if err := runRm(ctx, h.d, []string{idB}); err != nil || !strings.Contains(h.out.String(), "already deprecated") {
			t.Fatalf("err %v out %q", err, h.out.String())
		}
		if len(h.svc.deprecates) != 0 {
			t.Error("wrote to a deprecated record")
		}
	})

	t.Run("--hard prompts; only y deletes", func(t *testing.T) {
		for in, wantDel := range map[string]bool{"y\n": true, "yes\n": true, "Y\n": true, "n\n": false, "\n": false, "": false, "x\n": false} {
			h := mk(in)
			if err := runRm(ctx, h.d, []string{idA, "--hard"}); err != nil {
				t.Fatalf("%q: %v", in, err)
			}
			if got := len(h.svc.deletes) == 1; got != wantDel {
				t.Errorf("input %q: deleted = %v, want %v", in, got, wantDel)
			}
			if !strings.Contains(h.out.String(), `delete aaaaaaaa "doomed" permanently? [y/N]`) {
				t.Errorf("prompt missing: %q", h.out.String())
			}
			if !wantDel && !strings.Contains(h.out.String(), "not deleted") {
				t.Errorf("input %q: no 'not deleted' line: %q", in, h.out.String())
			}
			if len(h.svc.deprecates) != 0 {
				t.Error("--hard also deprecated")
			}
		}
	})
	t.Run("--hard --yes skips the prompt", func(t *testing.T) {
		h := mk("")
		if err := runRm(ctx, h.d, []string{"--hard", "--yes", idA}); err != nil {
			t.Fatal(err)
		}
		if len(h.svc.deletes) != 1 || strings.Contains(h.out.String(), "[y/N]") {
			t.Errorf("deletes %v out %q", h.svc.deletes, h.out.String())
		}
	})
	t.Run("--yes alone is a usage error", func(t *testing.T) {
		if err := runRm(ctx, mk("").d, []string{"--yes", idA}); exitCode(err) != 2 {
			t.Errorf("exit %d", exitCode(err))
		}
	})
	t.Run("referenced record is refused and points at deprecate", func(t *testing.T) {
		h := mk("y\n")
		h.svc.deleteErr = &memory.ErrReferenced{IDs: []string{idC}}
		err := runRm(ctx, h.d, []string{idA, "--hard"})
		if err == nil || !strings.Contains(err.Error(), "cccccccc") || !strings.Contains(err.Error(), "deprecate") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("delete failure is returned", func(t *testing.T) {
		h := mk("")
		h.svc.deleteErr = errors.New("db down")
		if err := runRm(ctx, h.d, []string{idA, "--hard", "--yes"}); err == nil || !strings.Contains(err.Error(), "db down") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestPromote(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		status  record.Status
		wantOut string
		wantErr string
		wantUpd int
	}{
		{record.StatusCandidate, "promoted", "", 1},
		{record.StatusActive, "already active", "", 0},
		{record.StatusDeprecated, "", "cannot promote a deprecated record", 0},
	}
	for _, tc := range cases {
		h := newHarness("", mkRec(idA, "ns1", "r", tc.status, 0))
		err := runPromote(ctx, h.d, []string{"aaaaaaaa"})
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || exitCode(err) != 1 {
				t.Errorf("%s: err = %v", tc.status, err)
			}
		} else if err != nil || !strings.Contains(h.out.String(), tc.wantOut) {
			t.Errorf("%s: err %v out %q", tc.status, err, h.out.String())
		}
		if len(h.svc.updates) != tc.wantUpd {
			t.Errorf("%s: updates = %d", tc.status, len(h.svc.updates))
		}
		if tc.wantUpd == 1 && (h.svc.updates[0].Status == nil || *h.svc.updates[0].Status != record.StatusActive) {
			t.Errorf("update = %+v", h.svc.updates[0])
		}
	}
}

func TestEditorCommand(t *testing.T) {
	cmd := editorCommand("code -w", "/tmp/a b.md")
	want := []string{"sh", "-c", `code -w "$1"`, "sh", "/tmp/a b.md"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("args = %q, want %q", cmd.Args, want)
	}
	env := map[string]string{"VISUAL": "v", "EDITOR": "e"}
	for _, tc := range []struct{ visual, editor, want string }{{"v", "e", "v"}, {"", "e", "e"}, {"", "", "vi"}} {
		env["VISUAL"], env["EDITOR"] = tc.visual, tc.editor
		got := editorProgram(func(k string) string { return env[k] })
		if got != tc.want {
			t.Errorf("VISUAL=%q EDITOR=%q -> %q, want %q", tc.visual, tc.editor, got, tc.want)
		}
	}
}
