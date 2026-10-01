package postgres

import (
	"strings"
	"unicode"
)

// minFTSRank is the ts_rank floor for a full-text match to count in hybrid
// RRF. With OR semantics a long natural-language prompt matches any record
// sharing a single common word; weak matches (rank below this floor) are
// noise and would displace vector-ranked results.
const minFTSRank = 0.05

// maxFTSTerms caps the number of OR terms so a very long prompt cannot
// produce an unbounded tsquery.
const maxFTSTerms = 32

var ftsStopwords = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by can do does for from had has have how i if in into is it its me my no not of on or our so than that the their then there these they this to was we were what when where which who why will with would you your should could about after before over under also just use using used via`) {
		ftsStopwords[w] = struct{}{}
	}
}

// buildORTSQuery turns free text into a to_tsquery('simple', …) string with
// OR semantics over distinct non-stopword terms, e.g. "fix the foo_bar bug"
// → "fix | foo_bar | bug". Every term is reduced to letters, digits and
// underscores, so the result contains only safe tsquery syntax (no
// operators, quotes or prefix markers) and is still passed as a bound
// parameter. Returns "" when no usable term remains (caller skips FTS).
func buildORTSQuery(text string) string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	})
	seen := make(map[string]struct{}, len(fields))
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, "_")
		if len(f) < 2 {
			continue
		}
		if _, stop := ftsStopwords[f]; stop {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		terms = append(terms, f)
		if len(terms) == maxFTSTerms {
			break
		}
	}
	return strings.Join(terms, " | ")
}
