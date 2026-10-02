package setup

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"claude-memory/internal/config"
)

func TestBuildDSNTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                               string
		user, pw, host, port, db, ssl, dsn string
	}{
		{"plain", "claude_memory", "secret", "db.example", "5432", "claude_memory", "prefer",
			"postgresql://claude_memory:secret@db.example:5432/claude_memory?sslmode=prefer"},
		{"dollar", "u", "pa$word", "h", "5432", "d", "disable", "postgresql://u:pa%24word@h:5432/d?sslmode=disable"},
		{"sentinel", "u", sentinelPassword, "h", "5432", "d", "require",
			"postgresql://u:" + pctEncode(sentinelPassword) + "@h:5432/d?sslmode=require"},
		{"ipv6 bracketed in", "u", "p", "[::1]", "5433", "d", "prefer", "postgresql://u:p@[::1]:5433/d?sslmode=prefer"},
		{"ipv6 bare in", "u", "p", "fd7a::1", "5432", "d", "verify-full", "postgresql://u:p@[fd7a::1]:5432/d?sslmode=verify-full"},
		{"ipv4", "u", "p", "127.0.0.1", "5432", "d", "prefer", "postgresql://u:p@127.0.0.1:5432/d?sslmode=prefer"},
		{"db with % / ? space", "u", "p", "h", "5432", "a%b/c?d e", "prefer", "postgresql://u:p@h:5432/a%25b%2Fc%3Fd%20e?sslmode=prefer"},
		{"user with @ :", "a@b:c", "p", "h", "5432", "d", "prefer", "postgresql://a%40b%3Ac:p@h:5432/d?sslmode=prefer"},
		{"no sslmode", "u", "p", "h", "5432", "d", "", "postgresql://u:p@h:5432/d"},
		{"no password", "u", "", "h", "5432", "d", "prefer", "postgresql://u@h:5432/d?sslmode=prefer"},
	}
	for _, c := range cases {
		got, err := BuildDSN(c.user, c.pw, c.host, c.port, c.db, c.ssl)
		if err != nil || got != c.dsn {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.dsn)
			continue
		}
		// Round trip through url.Parse and ParseDBTarget.
		if _, err := url.Parse(got); err != nil {
			t.Errorf("%s: url.Parse: %v", c.name, err)
		}
		tg, err := ParseDBTarget(got)
		if err != nil {
			t.Errorf("%s: ParseDBTarget: %v", c.name, err)
			continue
		}
		if tg.User != c.user || tg.password != c.pw || tg.Name != c.db || tg.Port != c.port || tg.SSLMode != c.ssl {
			t.Errorf("%s: parsed %+v", c.name, tg)
		}
		if tg.DSN() != got {
			t.Errorf("%s: DSN() %q != %q", c.name, tg.DSN(), got)
		}
	}
}

func TestBuildDSNRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, host, port, db, ssl string }{
		{"empty host", "", "5432", "d", "prefer"},
		{"host with path", "h/x", "5432", "d", "prefer"},
		{"host with port", "h:5432", "5432", "d", "prefer"},
		{"host with @", "u@h", "5432", "d", "prefer"},
		{"host with space", "a b", "5432", "d", "prefer"},
		{"socket dir", "/var/run/postgresql", "5432", "d", "prefer"},
		{"bad ipv4", "999.1.1.1", "5432", "d", "prefer"},
		{"unbalanced bracket", "[::1", "5432", "d", "prefer"},
		{"bracketed ipv4", "[127.0.0.1]", "5432", "d", "prefer"},
		{"port zero", "h", "0", "d", "prefer"},
		{"port too big", "h", "65536", "d", "prefer"},
		{"port text", "h", "pg", "d", "prefer"},
		{"port padded", "h", "05432", "d", "prefer"},
		{"empty db", "h", "5432", "", "prefer"},
		{"db control char", "h", "5432", "a\nb", "prefer"},
		{"bad sslmode", "h", "5432", "d", "yes"},
		{"sslmode query injection", "h", "5432", "d", "prefer&x=1"},
	}
	for _, c := range cases {
		if got, err := BuildDSN("u", "p", c.host, c.port, c.db, c.ssl); err == nil {
			t.Errorf("%s: accepted: %q", c.name, got)
		}
		tg := DBTarget{User: "u", Host: c.host, Port: c.port, Name: c.db, SSLMode: c.ssl}.WithPassword("p")
		if tg.DSN() != "" || tg.Validate() == nil {
			t.Errorf("%s: DBTarget accepted", c.name)
		}
	}
}

// Every input path validates: --pg-dsn, Env and the env file all go through
// ParseDBTarget, so an invalid port, sslmode or host is refused on each.
func TestParseDBTargetRefusesInvalidDSNs(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{
		"postgresql://u:p@h:0/d",
		"postgresql://u:p@h:70000/d",
		"postgresql://u:p@h:5432/d?sslmode=bogus",
		"postgresql://u:p@h:5432/",
		"postgresql://u:p@h:5432",
		"postgresql://u:p@h:5432/d?connect_timeout=5",
		"postgresql://u:p@h:5432/a/b",
		"postgresql://u:p@h:5432/d#frag",
		"postgresql://u:p@:5432/d",
		"postgresql://u:p@a b/d",
		"host=h dbname=d",
		"mysql://u:p@h/d",
		"",
	} {
		if tg, err := ParseDBTarget(dsn); err == nil {
			t.Errorf("accepted %q: %+v", dsn, tg)
		}
		if _, ok, err := DBTargetFromFlags(Inputs{PGDSN: dsn}); dsn != "" && (ok || err == nil) {
			t.Errorf("flag accepted %q", dsn)
		}
	}
	// A DSN error never repeats the DSN or its password.
	_, err := ParseDBTarget("postgresql://u:" + sentinelPassword + "@h:99999/d")
	if err == nil || strings.Contains(err.Error(), "S3ntinel") {
		t.Errorf("error leaks: %v", err)
	}
	_, err = ParseDBTarget("postgresql://u:pa/ss@h:5432/d") // unencoded '/'
	if err == nil || strings.Contains(err.Error(), "pa/ss") {
		t.Errorf("error leaks: %v", err)
	}
}

func TestParseDBTargetDefaultsAndForms(t *testing.T) {
	t.Parallel()
	tg, err := ParseDBTarget("postgres://claude_memory:p%2Fw@[::1]/my%20db")
	if err != nil {
		t.Fatal(err)
	}
	if tg.Host != "::1" || tg.Port != "5432" || tg.Name != "my db" || tg.password != "p/w" || tg.SSLMode != "" {
		t.Errorf("%+v", tg)
	}
	// An env DSN without sslmode is the same connection as prefer.
	other := tg
	other.SSLMode = "prefer"
	if !tg.SameConnection(other) {
		t.Error("empty sslmode must equal prefer")
	}
	other.password = "x"
	if tg.SameConnection(other) {
		t.Error("password difference ignored")
	}
}

// The password alphabet round trips through url.Parse, ParseDBTarget and the
// env-file reader with zero findings (AC-20, AC-27, Design 24).
func TestDSNPasswordsRoundTripWithZeroFindings(t *testing.T) {
	t.Parallel()
	pws := []string{sentinelPassword, "pa$word", "$", "@", "/", "%", "p w", "a%2Fb", "%zz", "x'y\"z\\", "ünï", "a:b@c/d?e#f", "`cmd`", "$(x)", "${HOME}"}
	for _, pw := range pws {
		dsn, err := BuildDSN("claude_memory", pw, "h", "5432", "claude_memory", "prefer")
		if err != nil {
			t.Fatalf("%q: %v", pw, err)
		}
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("%q: url.Parse: %v", pw, err)
		}
		if got, _ := u.User.Password(); got != pw {
			t.Errorf("url.Parse password %q != %q", got, pw)
		}
		ef := config.ParseEnvData([]byte("MEMORY_PG_DSN="+dsn+"\n"), 0o600)
		if len(ef.Findings) != 0 || ef.Values["MEMORY_PG_DSN"] != dsn {
			t.Errorf("%q: findings %+v value %q", pw, ef.Findings, ef.Values["MEMORY_PG_DSN"])
		}
		tg, err := ParseDBTarget(dsn)
		if err != nil || tg.password != pw {
			t.Errorf("%q: ParseDBTarget %v %q", pw, err, tg.password)
		}
	}
}

func TestDBTargetNeverPrintsItsPassword(t *testing.T) {
	t.Parallel()
	tg := DBTarget{User: "u", Host: "h", Port: "5432", Name: "d"}.WithPassword(sentinelPassword)
	for _, s := range []string{tg.String(), fmt.Sprint(tg), fmt.Sprintf("%+v", tg), fmt.Sprintf("%#v", tg), fmt.Sprintf("%v", Field[DBTarget]{})} {
		if strings.Contains(s, "S3ntinel") {
			t.Errorf("password printed: %s", s)
		}
	}
}

func TestPGDSNFlagWithPasswordIsRefused(t *testing.T) { // AC-31
	t.Parallel()
	for _, dsn := range []string{"postgresql://u:secret@h/d", "postgresql://u:p%40ss@h/d", "host=h password=x dbname=d", "postgresql://u:" + sentinelPassword + "@h/d"} {
		err := CheckPGDSNFlag(dsn)
		if err == nil || !strings.Contains(err.Error(), "--pg-password-stdin, not argv") {
			t.Errorf("%q: %v", dsn, err)
		}
		if _, _, err := DBTargetFromFlags(Inputs{PGDSN: dsn}); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "S3ntinel") {
			t.Errorf("%q: %v", dsn, err)
		}
	}
	if err := CheckPGDSNFlag("postgresql://u@h:5432/d?sslmode=require"); err != nil {
		t.Errorf("a DSN without a password was refused: %v", err)
	}
	// The stdin password completes the flag DSN (the Seed-side hook).
	tg, ok, err := DBTargetFromFlags(Inputs{PGDSN: "postgresql://u@h:5432/d", PGPassword: "pw-from-stdin"})
	if err != nil || !ok || tg.password != "pw-from-stdin" || tg.Source != SourceFlag {
		t.Errorf("%+v %v %v", tg, ok, err)
	}
	if _, ok, err := DBTargetFromFlags(Inputs{PGPassword: "x"}); ok || err != nil {
		t.Errorf("no --pg-dsn: %v %v", ok, err)
	}
}

func TestRecoverDSN(t *testing.T) {
	t.Parallel()
	good := []struct{ raw, pw string }{
		{"postgresql://claude_memory:ab/cd+ef==@h:5432/claude_memory", "ab/cd+ef=="},
		{"postgresql://u:pa$word@h:5432/d?sslmode=disable", "pa$word"},
		{"postgres://u:a@b@h/d", "a@b"},
		{"postgresql://u:p w@h:5432/d", "p w"},
	}
	for _, c := range good {
		tg, ok := RecoverDSN(c.raw)
		if !ok || tg.password != c.pw {
			t.Errorf("%q: %v %+v", c.raw, ok, tg)
			continue
		}
		// The recovered target is written encoded and reads back clean.
		ef := config.ParseEnvData([]byte("K="+tg.DSN()+"\n"), 0o600)
		if len(ef.Findings) != 0 {
			t.Errorf("%q: re-encoded line has findings %+v", c.raw, ef.Findings)
		}
	}
	for _, raw := range []string{
		"postgresql://u:$(pass show x)@h/d",
		"postgresql://u:a${X}b@h/d",
		"postgresql://u:a\\b@h/d",
		"postgresql://u:a`b@h/d",
		"postgresql://u:p%40ss/x@h/d", // already holds %XX: ambiguous
		"postgresql://u@h/d",          // no password
		"$(cat dsn)",
		"postgresql://u:p@h:99999/d", // bad tail
	} {
		if tg, ok := RecoverDSN(raw); ok {
			t.Errorf("recovered %q: %+v", raw, tg)
		}
	}
}

func TestHostIsLocal(t *testing.T) {
	t.Parallel()
	for h, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "127.1.2.3": true, "::1": true, "[::1]": true, "": true,
		"db.tail1234.ts.net": false, "100.64.0.1": false, "10.0.0.5": false, "example.com": false} {
		if got := HostIsLocal(h); got != want {
			t.Errorf("HostIsLocal(%q) = %v", h, got)
		}
	}
}
