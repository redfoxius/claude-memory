package postgres

import "fmt"

// tsvectorExprFromParts builds the tsvector_content expression from three
// already-SQL-safe weight-input expressions (title/tags/content, in that
// order) — each must be either a bound placeholder ($N) or a safe column
// reference, never a spliced literal value. See the security skill's
// OWASP A05 (Injection) guidance.
func tsvectorExprFromParts(titleExpr, tagsExpr, contentExpr string) string {
	return fmt.Sprintf(
		"setweight(to_tsvector('simple', %s), 'A') || "+
			"setweight(to_tsvector('simple', %s), 'B') || "+
			"setweight(to_tsvector('simple', %s), 'C')",
		titleExpr, tagsExpr, contentExpr,
	)
}

// tsvectorExpr returns a parameterized SQL expression that computes the
// weighted tsvector_content value from a record's title, tags, and content.
// It binds three placeholders starting at argStart (title, tags-joined,
// content, in that order) instead of splicing the values into the SQL text.
// Callers MUST append exactly those three values (title,
// strings.Join(tags, " "), content) to their args slice, immediately after
// the existing positional arguments, so the placeholder numbers line up.
//
// Shared by Store.Create (store.go) and txStoreImpl.Create (advisory_lock.go)
// so the non-tx and tx write paths can't diverge on how the tsvector is
// built.
func tsvectorExpr(argStart int) string {
	return tsvectorExprFromParts(
		fmt.Sprintf("$%d", argStart),
		fmt.Sprintf("$%d", argStart+1),
		fmt.Sprintf("$%d", argStart+2),
	)
}

// tsvectorUpdateExpr returns the tsvector_content recompute expression for
// an UPDATE where title/tags/content may each be either a NEW bound value
// (if that field is part of this update) or the row's EXISTING column (if
// not). Pass the placeholder number used for that field's new value in
// titleArg/tagsArg/contentArg, or 0 if that field isn't being changed by
// this call.
//
// Why a bare column reference is correct for an unchanged field: within one
// UPDATE statement, every SET expression (including this one) is evaluated
// against the row's pre-statement snapshot — a bare `title`/`tags`/`content`
// column reference here always yields the OLD value, which is exactly the
// current, correct value for a field this call isn't touching. For a field
// that IS being updated, the matching column's own `col = $N` SET clause
// updates the stored column, and this expression must use that same NEW
// value ($N) — not the stale column reference — so the recomputed
// tsvector reflects what the row will actually contain after the update
// (AC-5/AC-8: exact-identifier search must find new content, not old).
//
// This must never read a changed field via a correlated subquery against
// `records` — that subquery runs against the same pre-statement snapshot
// and would silently reintroduce the stale-tsvector bug this helper fixes.
//
// Shared by Store.Update (store.go) and txStoreImpl.Update
// (advisory_lock.go) so the non-tx and tx paths can't diverge.
func tsvectorUpdateExpr(titleArg, tagsArg, contentArg int) string {
	titleExpr := "COALESCE(title, '')"
	if titleArg > 0 {
		titleExpr = fmt.Sprintf("$%d", titleArg)
	}

	tagsExpr := "COALESCE(array_to_string(tags, ' '), '')"
	if tagsArg > 0 {
		tagsExpr = fmt.Sprintf("$%d", tagsArg)
	}

	contentExpr := "COALESCE(content, '')"
	if contentArg > 0 {
		contentExpr = fmt.Sprintf("$%d", contentArg)
	}

	return tsvectorExprFromParts(titleExpr, tagsExpr, contentExpr)
}
