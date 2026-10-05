package setup

import (
	"net/url"
	"strings"
	"testing"

	"github.com/redfoxius/claude-memory/internal/config"
)

func TestPctEncode(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", ""},
		{"abcXYZ019-._~", "abcXYZ019-._~"},
		{"pa$word", "pa%24word"},
		{"a b", "a%20b"},
		{"$&'()*+,;=:@/?#%", "%24%26%27%28%29%2A%2B%2C%3B%3D%3A%40%2F%3F%23%25"},
		{"é", "%C3%A9"},
	}
	for _, tc := range cases {
		if got := pctEncode(tc.in); got != tc.want {
			t.Errorf("pctEncode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPctEncodeRoundTrip: the encoded password survives url.Parse and the
// env-file parser flags nothing (Design 24, B-2).
func TestPctEncodeRoundTrip(t *testing.T) {
	t.Parallel()
	for _, pw := range []string{sentinelPassword, "pa$word", "p w", "a%2Fb", "x'y\"z\\", "ünï", "plain"} {
		dsn := "postgresql://" + pctEncode("claude_memory") + ":" + pctEncode(pw) + "@h:5432/" + pctEncode("db") + "?sslmode=disable"
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("%q: url.Parse: %v", pw, err)
		}
		if got, _ := u.User.Password(); got != pw {
			t.Errorf("round trip of %q gave %q", pw, got)
		}
		ef := config.ParseEnvData([]byte("MEMORY_PG_DSN="+dsn+"\n"), 0o600)
		if len(ef.Findings) != 0 {
			t.Errorf("env line for %q has findings: %+v", pw, ef.Findings)
		}
		if ef.Values["MEMORY_PG_DSN"] != dsn {
			t.Errorf("env value for %q = %q, want %q", pw, ef.Values["MEMORY_PG_DSN"], dsn)
		}
	}
}

// Only known libpq keywords are echoed in the unsupported-option error; any
// other key (possibly a pasted secret) is not, and a repeated sslmode is refused.
func TestParseDBTargetUnsupportedOptionEcho(t *testing.T) {
	base := "postgresql://u:p@h:5432/db"
	for q, wantEcho := range map[string]string{
		"?connect_timeout=5":              "connect_timeout",
		"?application_name=x":             "application_name",
		"?Hunter2secret":                  "",
		"?Hunter2secret=1":                "",
		"?sslmode=disable&abc=1":          "",
		"?sslmode=disable&sslrootcert=/x": "sslrootcert",
	} {
		_, err := ParseDBTarget(base + q)
		if err == nil {
			t.Errorf("%s accepted", q)
			continue
		}
		if strings.Contains(err.Error(), "Hunter2secret") || strings.Contains(err.Error(), "abc") {
			t.Errorf("%s: key echoed: %v", q, err)
		}
		if wantEcho != "" && !strings.Contains(err.Error(), wantEcho) {
			t.Errorf("%s: known key not named: %v", q, err)
		}
	}
	if _, err := ParseDBTarget(base + "?sslmode=disable&sslmode=require"); err == nil {
		t.Error("repeated sslmode accepted")
	}
}
