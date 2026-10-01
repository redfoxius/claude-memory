package postgres

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Full-text search for natural-language prompts.
//
// The query is built INSIDE the SQL statement from Postgres' own parser
// (`to_tsvector('simple', prompt)` -> lexemes), never from a Go tokenizer:
// the index was built by that parser, which keeps `db.withtx`,
// `pg_hba.conf`, `100.64.0.0` and `orderservice.cancel` as single lexemes,
// and any other tokenization silently never matches them. Go only
//   - passes the (length-capped) prompt text as a bound parameter,
//   - passes the raw identifier-like tokens it spotted (case matters for
//     camelCase detection, and the SQL lowercases), and
//   - passes the stopword list.

// ftsRelativeFloor is the OR-noise guard: a full-text match with no
// identifier-term hit counts toward ranking only when its flat-weight
// ts_rank is at least this fraction of the best match's. (An absolute floor
// cannot work: for an OR query ts_rank scales with the number of query
// terms. Flat weights keep a title word from outweighing a rare content
// word fivefold.)
const ftsRelativeFloor = 0.25

const (
	// maxFTSWords caps the ordinary OR terms (by position in the prompt).
	maxFTSWords = 32
	// maxFTSIdentifiers caps identifier-like tokens; they are kept
	// regardless of where they sit in a long prompt.
	maxFTSIdentifiers = 16
	// maxFTSPromptBytes bounds the text handed to to_tsvector.
	maxFTSPromptBytes = 8000
)

var ftsStopwordList = strings.Fields(`a an and are as at be but by can do does for from had has have how i if in into is it its me my no not of on or our so than that the their then there these they this to was we were what when where which who why will with would you your should could about after before over under also just use using used via`)

// ftsInputs prepares the bound parameters for the full-text half of a
// search: the prompt text (capped), and the identifier-like raw tokens.
func ftsInputs(text string) (prompt string, ids []string) {
	if len(text) > maxFTSPromptBytes {
		text = strings.ToValidUTF8(text[:maxFTSPromptBytes], "")
	}
	seen := map[string]struct{}{}
	for _, raw := range strings.Fields(text) {
		tok := strings.TrimFunc(raw, func(r rune) bool {
			return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
		})
		if !isIdentifierLike(tok) {
			continue
		}
		if _, dup := seen[strings.ToLower(tok)]; dup {
			continue
		}
		seen[strings.ToLower(tok)] = struct{}{}
		ids = append(ids, tok)
		if len(ids) == maxFTSIdentifiers {
			break
		}
	}
	return text, ids
}

// isIdentifierLike reports whether a raw (case-preserved) token looks like a
// code identifier: snake_case, a letter/digit mix, camelCase/PascalCase, or
// dotted/slashed with a digit or capital. Plain numbers ("2048") and short
// version-ish tokens ("v2") are not: they are common and say little.
func isIdentifierLike(tok string) bool {
	if len(tok) < 3 {
		return false
	}
	var prev rune
	hasLetter, hasDigit, hasUpper, hasSep := false, false, false, false
	for _, r := range tok {
		switch {
		case r == '_':
			return true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsLetter(r):
			hasLetter = true
			if unicode.IsUpper(r) {
				hasUpper = true
				if unicode.IsLower(prev) {
					return true // camelCase / PascalCase
				}
			}
		case r == '.' || r == '/' || r == '-':
			hasSep = true
		}
		prev = r
	}
	if hasLetter && hasDigit {
		return true
	}
	return hasSep && (hasDigit || hasUpper)
}

// ftsQueryCTE returns the `fts_q` CTE: one row with q_all (every usable
// prompt word OR every identifier phrase) and q_id (identifier phrases only),
// either of which is NULL when empty. prompt, ids and stop are the
// placeholder numbers of the bound parameters (text, text[], text[]).
//
// Quoting: lexemes are re-quoted for to_tsquery (backslash and single quote
// escaped), so no prompt content can inject tsquery syntax. Identifier
// tokens go through phraseto_tsquery, which parses them with the same parser
// ("err_gateway_timeout" -> 'err' <-> 'gateway' <-> 'timeout').
func ftsQueryCTE(prompt, ids, stop int) string {
	return fmt.Sprintf(`fts_q AS (
			SELECT
				NULLIF(concat_ws(' | ', w.s, i.s), '')::tsquery AS q_all,
				NULLIF(i.s, '')::tsquery AS q_id
			FROM
				(SELECT string_agg(
						'''' || replace(replace(lexeme, E'\\', E'\\\\'), '''', '''''') || '''', ' | '
					) AS s
				 FROM (
					SELECT lexeme FROM unnest(to_tsvector('simple', $%d::text))
					WHERE length(lexeme) >= 2 AND lexeme <> ALL($%d::text[])
					ORDER BY positions[1]
					LIMIT %d
				 ) wl) w,
				(SELECT string_agg('(' || phraseto_tsquery('simple', t)::text || ')', ' | ') AS s
				 FROM unnest($%d::text[]) t
				 WHERE phraseto_tsquery('simple', t)::text <> '') i
		)`, prompt, stop, maxFTSWords, ids)
}

// sortedStopwords is the stopword list as a bound text[] parameter.
func sortedStopwords() []string {
	out := append([]string(nil), ftsStopwordList...)
	sort.Strings(out)
	return out
}
