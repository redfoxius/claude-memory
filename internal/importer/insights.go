package importer

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/redfoxius/claude-memory/internal/record"
)

// insightsKinds maps a lower-cased `## ` section name to a record kind.
var insightsKinds = map[string]record.Kind{
	"what works":               record.KindPattern,
	"codebase patterns":        record.KindPattern,
	"what doesn't work":        record.KindGotcha,
	"recurring errors & fixes": record.KindGotcha,
	"tool & library notes":     record.KindGotcha,
}

// Entry is one parsed INSIGHTS.md entry.
type Entry struct {
	Section  string
	Kind     record.Kind
	Date     string
	Text     string   // entry text without the date, continuation lines joined by "\n"
	Title    string   // first sentence, at most 160 runes
	FileRefs []string // raw backticked file-like tokens, e.g. "src/a.ts:63-68"
	Line     int      // 1-based line of the bullet
}

var (
	entryRe   = regexp.MustCompile(`^- (\d{4}-\d{2}-\d{2})\s*(?:—|–|-)\s*(.*)$`)
	sentence  = regexp.MustCompile(`[.!?](\s|$)`)
	backtick  = regexp.MustCompile("`([^`\\s]+)`")
	fileToken = regexp.MustCompile(`^[\w@+~./-]+?(:\d+(-\d+)?)?$`)
)

// ParseInsights parses the entries of an INSIGHTS.md. Entries under a section
// that has no kind mapping (Open Questions, Session Notes, anything else) are
// returned as Skips with reason `section <name>`. Skip.Origin is "line N".
func ParseInsights(data []byte) ([]Entry, []Skip) {
	var entries []Entry
	var skips []Skip
	section := ""
	var cur *Entry
	var body []string

	flush := func() {
		if cur == nil {
			return
		}
		cur.Text = strings.TrimSpace(strings.Join(body, "\n"))
		kind, ok := insightsKinds[normalizeSection(cur.Section)]
		switch {
		case !ok:
			skips = append(skips, Skip{Origin: lineOrigin(cur.Line), Reason: "section " + cur.Section})
		case cur.Text == "":
			skips = append(skips, Skip{Origin: lineOrigin(cur.Line), Reason: "empty entry"})
		default:
			cur.Kind = kind
			cur.Title = truncateRunes(firstSentence(cur.Text), maxTitleRunes)
			cur.FileRefs = fileRefs(cur.Text)
			entries = append(entries, *cur)
		}
		cur, body = nil, nil
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			section = strings.TrimSpace(line[3:])
		case entryRe.MatchString(line):
			flush()
			m := entryRe.FindStringSubmatch(line)
			cur = &Entry{Section: section, Date: m[1], Line: i + 1}
			body = []string{m[2]}
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			// A top-level bullet that is not a plain dated entry (undated,
			// bold date, other shape).
			flush()
			skips = append(skips, Skip{Origin: lineOrigin(i + 1), Reason: "unrecognized entry"})
		case cur != nil && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && strings.TrimSpace(line) != "":
			body = append(body, strings.TrimSpace(line))
		default:
			flush()
		}
	}
	flush()
	return entries, skips
}

func lineOrigin(n int) string { return "line " + strconv.Itoa(n) }

func normalizeSection(s string) string {
	s = strings.ReplaceAll(s, "’", "'")
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// firstSentence is the entry text up to its first sentence end, whitespace
// collapsed.
func firstSentence(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	if loc := sentence.FindStringIndex(flat); loc != nil {
		return flat[:loc[0]+1]
	}
	return flat
}

// fileRefs returns the backticked tokens that look like `path` or
// `path:N[-M]` (they must contain a "." or "/").
func fileRefs(text string) []string {
	var out []string
	for _, m := range backtick.FindAllStringSubmatch(text, -1) {
		tok := m[1]
		if !fileToken.MatchString(tok) || !strings.ContainsAny(tok, "./") {
			continue
		}
		out = append(out, tok)
	}
	return out
}
