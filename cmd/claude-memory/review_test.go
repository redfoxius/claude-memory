package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/record"
)

// reviewHarness has four candidates, newest first, plus an active record.
func reviewHarness(stdin string) *harness {
	return newHarness(stdin,
		mkRec(idA, "ns1", "first", record.StatusCandidate, 90*time.Minute),
		mkRec(idE, "ns1", "second", record.StatusCandidate, 5*time.Hour),
		mkRec(idC, "ns1", "third", record.StatusCandidate, 3*24*time.Hour),
		mkRec(idD, "ns1", "fourth", record.StatusCandidate, 9*24*time.Hour),
		mkRec(idG, "ns1", "already active", record.StatusActive, time.Hour),
	)
}

func TestReviewNoCandidates(t *testing.T) {
	h := newHarness("", mkRec(idA, "ns1", "active", record.StatusActive, 0))
	if err := runReview(context.Background(), h.d, nil); err != nil || !strings.Contains(h.out.String(), "no candidates") {
		t.Errorf("err %v out %q", err, h.out.String())
	}
}

func TestReviewEveryKey(t *testing.T) {
	h := reviewHarness("a\nd\n\nx\ny\ns\n")
	if err := runReview(context.Background(), h.d, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.svc.updates) != 1 || h.svc.updates[0].ID != idA || *h.svc.updates[0].Status != record.StatusActive {
		t.Errorf("approve: %+v", h.svc.updates)
	}
	if len(h.svc.deprecates) != 1 || h.svc.deprecates[0].ID != idE || h.svc.deprecates[0].Reason != "rejected in review" {
		t.Errorf("deprecate: %+v", h.svc.deprecates)
	}
	if len(h.svc.deletes) != 1 || h.svc.deletes[0] != idC {
		t.Errorf("delete: %v", h.svc.deletes)
	}
	got := h.out.String()
	for _, want := range []string{"[1/4] aaaaaaaa", "[2/4] eeeeeeee", "[3/4] cccccccc", "[4/4] dddddddd",
		"[a]pprove [e]dit [d]eprecate [x]delete [s]kip [v]iew [q]uit > ",
		"approved 1, edited 0, deprecated 1, deleted 1, skipped 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	// Newest first.
	if strings.Index(got, "first") > strings.Index(got, "second") || strings.Index(got, "second") > strings.Index(got, "third") {
		t.Error("records not shown newest first")
	}
}

func TestReviewDeprecateReasonAndQuit(t *testing.T) {
	h := reviewHarness("d\n  too vague  \nq\n")
	if err := runReview(context.Background(), h.d, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.svc.deprecates) != 1 || h.svc.deprecates[0].Reason != "too vague" {
		t.Errorf("deprecates %+v", h.svc.deprecates)
	}
	if strings.Contains(h.out.String(), "[3/4]") || !strings.Contains(h.out.String(), "approved 0, edited 0, deprecated 1, deleted 0, skipped 0") {
		t.Errorf("q did not stop / summary wrong:\n%s", h.out.String())
	}
}

func TestReviewEnterSkipsAndUnknownReprompts(t *testing.T) {
	h := reviewHarness("zz\n\nS\nq\n")
	if err := runReview(context.Background(), h.d, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "unknown key") || !strings.Contains(h.out.String(), "skipped 2") {
		t.Errorf("output:\n%s", h.out.String())
	}
	if n := strings.Count(h.out.String(), "[1/4]"); n != 1 {
		t.Errorf("record 1 shown %d times after an unknown key", n)
	}
}

func TestReviewFailedActionStaysOnRecord(t *testing.T) {
	for _, tc := range []struct{ name, script, failAct string }{
		{"approve", "a\ns\n", "update"},
		{"deprecate", "d\n\ns\n", "deprecate"},
	} {
		h := reviewHarness(tc.script)
		h.svc.failAct = tc.failAct
		if err := runReview(context.Background(), h.d, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(h.err.String(), tc.failAct+" failed") {
			t.Errorf("%s: error not printed: %q", tc.name, h.err.String())
		}
		// The failed action neither counts nor advances: record 1 is shown once and
		// the next "s" is answered on it.
		if !strings.Contains(h.out.String(), "approved 0, edited 0, deprecated 0, deleted 0, skipped 1") ||
			strings.Count(h.out.String(), "[1/4]") != 1 || strings.Count(h.out.String(), reviewPrompt) != 3 {
			t.Errorf("%s: output:\n%s", tc.name, h.out.String())
		}
	}
}

func TestReviewDeleteRefusedStays(t *testing.T) {
	h := reviewHarness("x\ny\ns\n")
	h.svc.deleteErr = &memory.ErrReferenced{IDs: []string{idD}}
	if err := runReview(context.Background(), h.d, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.err.String(), "use [d]eprecate instead") || !strings.Contains(h.err.String(), "dddddddd") {
		t.Errorf("stderr: %q", h.err.String())
	}
	if !strings.Contains(h.out.String(), "deleted 0, skipped 1") || strings.Count(h.out.String(), "[1/4]") != 1 {
		t.Errorf("output:\n%s", h.out.String())
	}
}

func TestReviewDeleteDeclined(t *testing.T) {
	h := reviewHarness("x\nn\nq\n")
	if err := runReview(context.Background(), h.d, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.svc.deletes) != 0 || !strings.Contains(h.out.String(), "not deleted") {
		t.Errorf("deletes %v out %q", h.svc.deletes, h.out.String())
	}
}

func TestReviewEOF(t *testing.T) {
	cases := map[string]struct {
		script string
		want   string
	}{
		"at the main prompt":  {"", "approved 0, edited 0, deprecated 0, deleted 0, skipped 0"},
		"after a skip":        {"s\n", "skipped 1"},
		"at the reason":       {"d\n", "deprecated 0"},
		"at the delete [y/N]": {"x\n", "deleted 0"},
		"unterminated line":   {"a", "approved 1"}, // a last line without newline still counts
	}
	for name, tc := range cases {
		h := reviewHarness(tc.script)
		if err := runReview(context.Background(), h.d, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(h.out.String(), tc.want) {
			t.Errorf("%s: output lacks %q:\n%s", name, tc.want, h.out.String())
		}
		if name == "at the reason" || name == "at the delete [y/N]" {
			if len(h.svc.deprecates)+len(h.svc.deletes)+len(h.svc.updates) != 0 {
				t.Errorf("%s: an aborted action wrote", name)
			}
			if strings.Contains(h.out.String(), "[2/4]") {
				t.Errorf("%s: kept going after EOF", name)
			}
		}
	}
}

func TestReviewEditReshowsRecord(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := reviewHarness("e\nq\n")
	h.editor = func(path string) error {
		return os.WriteFile(path, []byte("title: edited title\ntags:\nfiles:\n---\nbody of first\n"), 0o600)
	}
	if err := runReview(context.Background(), h.d, nil); err != nil {
		t.Fatal(err)
	}
	got := h.out.String()
	if strings.Count(got, "[1/4]") != 2 || !strings.Contains(got, "edited title") || !strings.Contains(got, "edited 1,") {
		t.Errorf("output:\n%s", got)
	}
	if len(h.svc.updates) != 1 || h.svc.updates[0].Title == nil {
		t.Errorf("updates %+v", h.svc.updates)
	}
}

func TestReviewEditFailureStays(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	h := reviewHarness("e\nq\n")
	h.editor = func(string) error { return os.ErrPermission }
	if err := runReview(context.Background(), h.d, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.err.String(), "editor failed") || strings.Count(h.out.String(), "[1/4]") != 1 {
		t.Errorf("stderr %q out:\n%s", h.err.String(), h.out.String())
	}
}

func TestReviewViewAndPreview(t *testing.T) {
	long := mkRec(idA, "ns1", "long one", record.StatusCandidate, time.Hour)
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, "content line "+string(rune('a'+i-1)))
	}
	long.Content = strings.Join(lines, "\n")
	h := newHarness("v\nq\n", long)
	if err := runReview(context.Background(), h.d, nil); err != nil {
		t.Fatal(err)
	}
	got := h.out.String()
	first := got[:strings.Index(got, reviewPrompt)]
	if strings.Contains(first, "content line p") || !strings.Contains(first, "content line o") || !strings.Contains(first, "... 5 more lines") {
		t.Errorf("preview is not the first 15 lines:\n%s", first)
	}
	if !strings.Contains(got[len(first):], "content line t") {
		t.Errorf("v did not print the full content:\n%s", got)
	}
	if strings.Count(got, reviewPrompt) != 2 {
		t.Errorf("v should prompt again; prompts = %d", strings.Count(got, reviewPrompt))
	}
}

func TestReviewBlockContents(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	rec := mkRec(idA, "ns1", "the title", record.StatusCandidate, 90*time.Minute)
	rec.Tags, rec.Files, rec.CommitSHA, rec.SeenCount, rec.UsedCount = []string{"x"}, []string{"a.go"}, &sha, 2, 1
	h := newHarness("q\n", rec, mkRec(idE, "ns1", "near twin", record.StatusActive, 0))
	h.svc.checkout = &memory.Checkout{Repo: "svc", Dir: "/w/svc"}
	h.svc.hint = &memory.StaleHint{Commits: 4}
	h.svc.similar = map[string][]*memory.Candidate{idA: {{ID: idE, Title: "near twin", Similarity: 0.873}}}
	if err := runReview(context.Background(), h.d, nil); err != nil {
		t.Fatal(err)
	}
	got := h.out.String()
	for _, want := range []string{"[1/1] aaaaaaaa  pattern  repo=svc  source=session  age=1h  seen=2 used=1",
		"stale: 4 commits", "tags: x", "files: a.go", "similar:", "eeeeeeee  active     0.87  near twin"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}

	h = newHarness("q\n", mkRec(idA, "ns1", "no vec", record.StatusCandidate, 0))
	h.svc.noEmbed = map[string]bool{idA: true}
	if err := runReview(context.Background(), h.d, nil); err != nil || !strings.Contains(h.out.String(), "similar: unavailable") {
		t.Errorf("err %v out %q", err, h.out.String())
	}
}

func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{30 * time.Second: "0m", 59 * time.Minute: "59m", 90 * time.Minute: "1h", 47 * time.Hour: "47h", 49 * time.Hour: "2d"} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}
