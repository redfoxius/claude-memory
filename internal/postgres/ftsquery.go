package postgres

import (
	"strings"
	"unicode"
)

// ftsRelativeFloor is the OR-noise guard: a full-text match with no
// identifier-like term counts toward ranking only when its ts_rank is at
// least this fraction of the best match's. (An absolute ts_rank floor cannot
// work: for an OR query ts_rank is scaled by the number of query terms, so
// the same single-word match scores 0.12 against a 1-term query and 0.02
// against a 6-term one.)
const ftsRelativeFloor = 0.25

// maxFTSTerms caps the number of OR terms so a very long prompt cannot
// produce an unbounded tsquery.
const maxFTSTerms = 32

var ftsStopwords = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by can do does for from had has have how i if in into is it its me my no not of on or our so than that the their then there these they this to was we were what when where which who why will with would you your should could about after before over under also just use using used via`) {
		ftsStopwords[w] = struct{}{}
	}
}

// isIdentifierLike reports whether a raw (case-preserved) token looks like a
// code identifier — snake_case, contains a digit, or camelCase/PascalCase.
// Such terms are rare by construction, so a match on one is always kept.
func isIdentifierLike(tok string) bool {
	var prev rune
	for _, r := range tok {
		if r == '_' || unicode.IsDigit(r) {
			return true
		}
		if unicode.IsUpper(r) && unicode.IsLower(prev) {
			return true
		}
		prev = r
	}
	return false
}

// buildFTSQueries turns free text into two to_tsquery('simple', …) strings
// with OR semantics: all is every distinct non-stopword term, ids only the
// identifier-like ones. Terms are reduced to letters, digits and
// underscores, so the strings contain only safe tsquery syntax (no
// operators, quotes or prefix markers); they are still passed as bound
// parameters. all is "" when no usable term remains (callers skip full-text);
// ids is "" when there is no identifier-like term.
//
// Example: "why does ErrNoRows happen in fix_it" →
// all="errnorows | happen | fix_it" (stopwords dropped), ids="errnorows | fix_it".
func buildFTSQueries(text string) (all, ids string) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	})
	seen := make(map[string]struct{}, len(fields))
	var allTerms, idTerms []string
	for _, f := range fields {
		f = strings.Trim(f, "_")
		term := strings.ToLower(f)
		if len(term) < 2 {
			continue
		}
		if _, stop := ftsStopwords[term]; stop {
			continue
		}
		if _, dup := seen[term]; dup {
			continue
		}
		seen[term] = struct{}{}
		allTerms = append(allTerms, term)
		if isIdentifierLike(f) {
			idTerms = append(idTerms, term)
		}
		if len(allTerms) == maxFTSTerms {
			break
		}
	}
	return strings.Join(allTerms, " | "), strings.Join(idTerms, " | ")
}

// nullable returns nil for an empty string so a bound parameter is SQL NULL
// (to_tsquery(NULL) matches nothing).
func nullable(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
