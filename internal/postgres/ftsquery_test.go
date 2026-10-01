package postgres

import "testing"

func TestBuildFTSQueries(t *testing.T) {
	cases := []struct{ in, all, ids string }{
		{"", "", ""},
		{"the and of", "", ""},
		{"fix the foo_bar bug", "fix | foo_bar | bug", "foo_bar"},
		{"why does ErrNoRows happen in prod", "errnorows | happen | prod", "errnorows"},
		{"ErrNoRows ErrNoRows", "errnorows", "errnorows"},
		{"x'); DROP TABLE records; --", "drop | table | records", ""},
		{"a | b & !c <-> d:*", "", ""},
		{"why does pgx_pool_acquire hang", "pgx_pool_acquire | hang", "pgx_pool_acquire"},
		{"retry 3 times on HTTP500", "retry | times | http500", "http500"},
		{"OrderService.Cancel retries", "orderservice | cancel | retries", "orderservice"},
		{"ERR_NULL_DEREF in handler", "err_null_deref | handler", "err_null_deref"},
		{"plain words only here", "plain | words | only | here", ""},
	}
	for _, c := range cases {
		all, ids := buildFTSQueries(c.in)
		if all != c.all || ids != c.ids {
			t.Errorf("buildFTSQueries(%q) = (%q, %q), want (%q, %q)", c.in, all, ids, c.all, c.ids)
		}
	}
}

func TestBuildFTSQueriesCapsTerms(t *testing.T) {
	var in string
	for i := 0; i < 100; i++ {
		in += "word" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + " "
	}
	all, _ := buildFTSQueries(in)
	if n := len(splitOR(all)); n != maxFTSTerms {
		t.Errorf("terms = %d, want %d", n, maxFTSTerms)
	}
}

func splitOR(s string) []string {
	var out []string
	cur := ""
	for _, part := range s {
		if part == '|' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(part)
	}
	return append(out, cur)
}
