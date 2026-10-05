package extraction

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/redfoxius/claude-memory/internal/memory/mock"
)

type replaceScrubber struct{ from, to string }

func (r replaceScrubber) Scrub(s string) (string, bool) {
	return strings.ReplaceAll(s, r.from, r.to), strings.Contains(s, r.from)
}

func TestBuildPRDocumentSectionsAndNoCommentsHeaderWhenEmpty(t *testing.T) {
	doc := buildPRDocument(PRInput{Title: "T", Description: "D", URL: "U"}, nil, 0)
	if doc != "PR Title: T\n\nPR Description:\nD\n\nPR URL: U" {
		t.Errorf("doc = %q", doc)
	}
	doc = buildPRDocument(PRInput{Title: "T", URL: "U", ReviewComments: []string{"one", " ", "two"}}, nil, 0)
	if !strings.HasSuffix(doc, "\n\nComments:\n- one\n- two") {
		t.Errorf("doc = %q", doc)
	}
}

func TestBuildPRDocumentCapsCommentCountAndLength(t *testing.T) {
	var cs []string
	for i := 0; i < 60; i++ {
		cs = append(cs, "c")
	}
	doc := buildPRDocument(PRInput{ReviewComments: cs}, nil, 0)
	if got := strings.Count(doc, "\n- "); got != 50 {
		t.Errorf("comments kept = %d, want 50", got)
	}
	doc = buildPRDocument(PRInput{ReviewComments: []string{strings.Repeat("я", 1500)}}, nil, 0)
	line := doc[strings.LastIndex(doc, "\n- ")+3:]
	if utf8.RuneCountInString(line) != 1000 {
		t.Errorf("comment runes = %d, want 1000", utf8.RuneCountInString(line))
	}
}

func TestBuildPRDocumentScrubsBeforeCapping(t *testing.T) {
	// The secret straddles rune 1000; capping first would leave a prefix the
	// scrubber's pattern no longer matches.
	secret := "SECRETTOKEN-ABCDEF"
	c := strings.Repeat("x", 995) + secret
	doc := buildPRDocument(PRInput{Title: "t " + secret, Description: secret, ReviewComments: []string{c}}, replaceScrubber{secret, "[R]"}, 0)
	if strings.Contains(doc, "SECRETTOKEN") {
		t.Errorf("secret (or a fragment) survived:\n%s", doc)
	}
	if !strings.Contains(doc, strings.Repeat("x", 995)+"[R]") {
		t.Error("scrubbed comment should be kept whole after scrubbing")
	}
}

func TestBuildPRDocumentTruncatesHeadFirstAtRuneBoundary(t *testing.T) {
	doc := buildPRDocument(PRInput{Title: "ЗАГОЛОВОК", Description: strings.Repeat("ж", 100), ReviewComments: []string{"tail comment"}}, nil, 25)
	if !utf8.ValidString(doc) || utf8.RuneCountInString(doc) != 25 {
		t.Fatalf("doc invalid or wrong size (%d runes): %q", utf8.RuneCountInString(doc), doc)
	}
	if !strings.HasPrefix(doc, "PR Title: ЗАГОЛОВОК") {
		t.Errorf("head must survive: %q", doc)
	}
	if strings.Contains(doc, "tail comment") {
		t.Error("tail must be cut")
	}
}

func TestProcessPRSendsScrubbedDocumentAndKeepsCommitSHA(t *testing.T) {
	svc := mock.NewMemoryService()
	runner := &FakeHaikuRunner{responses: []haikuResponse{
		{output: []byte(`[{"kind":"convention","title":"c","content":"Learned from a PR."}]`)},
		{output: []byte(`{"action":"ADD"}`)},
	}}
	pr := PRInput{Title: "t", Description: "has SECRETTOKEN-1", Repo: "r", URL: "u", CommitSHA: "abc123",
		ReviewComments: []string{"comment SECRETTOKEN-2"}}
	if _, err := ProcessPR(context.Background(), svc, pr, Config{HaikuTimeout: time.Second}, runner, replaceScrubber{"SECRETTOKEN", "[R]"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.prompts) == 0 || strings.Contains(runner.prompts[0], "SECRETTOKEN") || !strings.Contains(runner.prompts[0], "comment [R]-2") {
		t.Errorf("prompt not scrubbed / comments missing:\n%v", runner.prompts)
	}
	stored := svc.GetStoredRecords()
	if len(stored) != 1 || stored[0].CommitSHA == nil || *stored[0].CommitSHA != "abc123" {
		t.Errorf("stored = %+v", stored)
	}
}
