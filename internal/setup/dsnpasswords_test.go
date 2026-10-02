package setup

import (
	"slices"
	"testing"
)

func TestDSNPasswords(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, dsn string
		want      []string // order-insensitive
	}{
		{"url plain", "postgresql://u:secret@h:5432/db", []string{"secret"}},
		{"url encoded", "postgresql://u:p%40ss%24@h/db", []string{"p@ss$", "p%40ss%24"}},
		{"url no password", "postgresql://u@h/db", nil},
		{"url empty password", "postgresql://u:@h/db", nil},
		{"url unencoded at (parse fails)", "postgresql://u:p@ss@h:5432/db", []string{"p@ss"}},
		{"url '@' in query is not userinfo", "postgresql://u:pw@h/db?x=a@b", []string{"pw"}},
		{"url pctEncode sentinel", "postgresql://u:" + pctEncode(sentinelPassword) + "@h/db", []string{sentinelPassword, pctEncode(sentinelPassword)}},
		{"kv plain", "host=h user=u password=secret dbname=d", []string{"secret"}},
		{"kv quoted with spaces", "host=h password='a b c' dbname=d", []string{"a b c"}},
		{"kv quoted with escapes", `host=h password='it\'s a \\ b' dbname=d`, []string{`it's a \ b`, `it\'s a \\ b`}},
		{"kv spaces around =", "password = 'x y' host=h", []string{"x y"}},
		{"kv password not first", "host=h dbname=d user=u password=last", []string{"last"}},
		{"kv no password", "host=h dbname=d", nil},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		got := dsnPasswords(tc.dsn)
		if len(got) != len(tc.want) {
			t.Errorf("%s: dsnPasswords = %q, want %q", tc.name, got, tc.want)
			continue
		}
		for _, w := range tc.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: dsnPasswords = %q, missing %q", tc.name, got, w)
			}
		}
	}
}
