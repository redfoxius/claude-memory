package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/redfoxius/claude-memory/internal/record"
)

func editRec() *record.Record {
	r := mkRec(idA, "ns1", "Original title", record.StatusActive, 0)
	r.Content = "line one\nline two"
	r.Tags = []string{"go", "cache"}
	r.Files = []string{"a/b.go", "c.go:10-12"}
	return r
}

func TestEditFileRoundTrip(t *testing.T) {
	rec := editRec()
	f, err := parseEditFile(renderEditFile(rec))
	if err != nil {
		t.Fatal(err)
	}
	want := editFields{Title: "Original title", Tags: rec.Tags, Files: rec.Files, Content: rec.Content}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("got %+v want %+v", f, want)
	}
	if req, err := diffEdit(rec, f); req != nil || err != nil {
		t.Errorf("unchanged edit: req %+v err %v", req, err)
	}

	// A record with no tags or files round-trips unchanged too.
	bare := mkRec(idB, "ns1", "bare", record.StatusActive, 0)
	f, err = parseEditFile(renderEditFile(bare))
	if err != nil {
		t.Fatal(err)
	}
	if req, err := diffEdit(bare, f); req != nil || err != nil {
		t.Errorf("bare unchanged: req %+v err %v", req, err)
	}
}

func TestParseEditFileErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no separator":  "title: x\ntags: a\nfiles: b\ncontent\n",
		"empty title":   "title:\ntags: a\nfiles: b\n---\ncontent\n",
		"missing title": "tags: a\n---\ncontent\n",
		"empty content": "title: x\ntags: a\nfiles: b\n---\n\n  \n",
		"unknown field": "title: x\nkind: gotcha\n---\ncontent\n",
		"bad line":      "title: x\njunk\n---\ncontent\n",
	} {
		if _, err := parseEditFile([]byte(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A "---" line inside the content is content.
	f, err := parseEditFile([]byte("title: x\n---\nabove\n---\nbelow\n"))
	if err != nil || f.Content != "above\n---\nbelow" {
		t.Errorf("content = %q err %v", f.Content, err)
	}
}

func TestEditRecord(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TMPDIR", t.TempDir())

	// rewrite makes the stub editor replace the file's text.
	rewrite := func(h *harness, f func(string) string) {
		h.editor = func(path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(path, []byte(f(string(data))), 0o600)
		}
	}
	leftovers := func() []string {
		m, _ := filepath.Glob(filepath.Join(os.TempDir(), "claude-memory-edit-*"))
		return m
	}

	t.Run("temp file is 0600 and removed after a successful edit", func(t *testing.T) {
		h := newHarness("", editRec())
		h.editor = func(path string) error {
			st, err := os.Stat(path)
			if err != nil || st.Mode().Perm() != 0o600 {
				t.Errorf("temp file mode %v err %v", st.Mode(), err)
			}
			return os.WriteFile(path, []byte(strings.Replace(string(mustRead(t, path)), "Original", "New", 1)), 0o600)
		}
		if err := runEdit(ctx, h.d, []string{"aaaaaaaa"}); err != nil {
			t.Fatal(err)
		}
		if len(leftovers()) != 0 {
			t.Errorf("temp file kept: %v", leftovers())
		}
	})

	t.Run("only changed fields are sent", func(t *testing.T) {
		cases := map[string]struct {
			from string
			to   string
		}{
			"title":   {from: "Original title", to: "Changed"},
			"content": {from: "line two", to: "line three"},
			"tags":    {from: "go, cache", to: "go, cache, more"},
			"files":   {from: "a/b.go,", to: "a/z.go,"},
		}
		for name, tc := range cases {
			h := newHarness("", editRec())
			rewrite(h, func(s string) string { return strings.Replace(s, tc.from, tc.to, 1) })
			if err := runEdit(ctx, h.d, []string{idA}); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(h.svc.updates) != 1 {
				t.Fatalf("%s: updates = %d", name, len(h.svc.updates))
			}
			u := h.svc.updates[0]
			set := map[string]bool{"title": u.Title != nil, "content": u.Content != nil, "tags": u.Tags != nil, "files": u.Files != nil}
			for field, isSet := range set {
				if isSet != (field == name) {
					t.Errorf("%s edit sent %s=%v (%+v)", name, field, isSet, u)
				}
			}
		}
	})

	t.Run("unchanged writes nothing and removes the file", func(t *testing.T) {
		h := newHarness("", editRec())
		rewrite(h, func(s string) string { return s })
		if err := runEdit(ctx, h.d, []string{idA}); err != nil {
			t.Fatal(err)
		}
		if len(h.svc.updates) != 0 || !strings.Contains(h.out.String(), "no changes") || len(leftovers()) != 0 {
			t.Errorf("updates %d out %q leftovers %v", len(h.svc.updates), h.out.String(), leftovers())
		}
	})

	t.Run("failures write nothing, keep the file and print its path", func(t *testing.T) {
		failures := map[string]func(h *harness){
			"editor exits non-zero": func(h *harness) { h.editor = func(string) error { return errors.New("exit status 1") } },
			"missing separator": func(h *harness) {
				rewrite(h, func(s string) string { return strings.Replace(s, "---\n", "", 1) })
			},
			"empty title": func(h *harness) {
				rewrite(h, func(s string) string { return strings.Replace(s, "Original title", "", 1) })
			},
			"cannot clear tags": func(h *harness) {
				rewrite(h, func(s string) string { return strings.Replace(s, "go, cache", "", 1) })
			},
			"cannot clear files": func(h *harness) {
				rewrite(h, func(s string) string { return strings.Replace(s, "a/b.go, c.go:10-12", "", 1) })
			},
		}
		for name, setup := range failures {
			for _, p := range leftovers() {
				_ = os.Remove(p)
			}
			h := newHarness("", editRec())
			setup(h)
			err := runEdit(ctx, h.d, []string{idA})
			if err == nil || exitCode(err) != 1 {
				t.Errorf("%s: err = %v", name, err)
				continue
			}
			left := leftovers()
			if len(left) != 1 || !strings.Contains(err.Error(), left[0]) {
				t.Errorf("%s: err %q does not name the kept file %v", name, err, left)
			}
			if len(h.svc.updates) != 0 {
				t.Errorf("%s: wrote %d updates", name, len(h.svc.updates))
			}
		}
	})

	t.Run("update failure keeps the file", func(t *testing.T) {
		for _, p := range leftovers() {
			_ = os.Remove(p)
		}
		h := newHarness("", editRec())
		h.svc.failAct = "update"
		rewrite(h, func(s string) string { return strings.Replace(s, "line two", "line 2", 1) })
		if err := runEdit(ctx, h.d, []string{idA}); err == nil || len(leftovers()) != 1 {
			t.Errorf("err %v leftovers %v", err, leftovers())
		}
	})
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
