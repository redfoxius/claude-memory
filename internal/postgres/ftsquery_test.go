package postgres

import "testing"

func TestBuildORTSQuery(t *testing.T) {
	cases := map[string]string{
		"":                               "",
		"the and of":                     "",
		"fix the foo_bar bug":            "fix | foo_bar | bug",
		"ErrNoRows ErrNoRows":            "errnorows",
		"x'); DROP TABLE records; --":    "drop | table | records",
		"a | b & !c <-> d:*":             "",
		"why does pgx_pool_acquire hang": "pgx_pool_acquire | hang",
	}
	for in, want := range cases {
		if got := buildORTSQuery(in); got != want {
			t.Errorf("buildORTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
