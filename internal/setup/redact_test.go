package setup

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// sentinelPassword is the AC-30 sentinel: every output path is grepped for
// it and its percent-encoded forms.
const sentinelPassword = "S3ntinel-pw-$@:/x"

func TestRedactorUserinfoPattern(t *testing.T) {
	t.Parallel()
	r := NewRedactor()
	cases := []struct{ in, want string }{
		{"postgresql://claude_memory:secret@host:5432/claude_memory",
			"postgresql://claude_memory:***@host:5432/claude_memory"},
		{"MEMORY_PG_DSN=postgres://u:p%40ss@h/db?sslmode=prefer",
			"MEMORY_PG_DSN=postgres://u:***@h/db?sslmode=prefer"},
		// Unencoded base64 password with '/' and '+'.
		{"dial postgres://u:ab/c+d==@100.1.2.3:5432/db: timeout",
			"dial postgres://u:***@100.1.2.3:5432/db: timeout"},
		// Short password: still masked by the pattern.
		{"postgres://u:pw@h/db", "postgres://u:***@h/db"},
		// Empty password.
		{"postgres://u:@h/db", "postgres://u:***@h/db"},
		// No password: nothing to mask.
		{"postgres://u@h/db", "postgres://u@h/db"},
		{"http://127.0.0.1:11434/api/version", "http://127.0.0.1:11434/api/version"},
		// Two URLs on one line.
		{"a postgres://u:one@h/db b postgresql://v:two@k/db",
			"a postgres://u:***@h/db b postgresql://v:***@k/db"},
		{"no url here", "no url here"},
		// '@' inside the password: anchor on the last '@' before the first '/'.
		{"dial postgres://u:p@ss$w@h:5432/db: refused", "dial postgres://u:***@h:5432/db: refused"},
		// A later '@' in the path or query is not the userinfo end.
		{"postgres://u:pw@h/db?x=a@b", "postgres://u:***@h/db?x=a@b"},
		// The installer's own pctEncode form.
		{"postgresql://u:" + pctEncode(sentinelPassword) + "@h/db", "postgresql://u:***@h/db"},
	}
	for _, tc := range cases {
		if got := r.Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactorMinLength(t *testing.T) {
	t.Parallel()
	cases := []struct {
		secret     string
		registered bool
	}{
		{"", false},
		{"pw", false},
		{"1234567", false}, // 7: below the minimum
		{"12345678", true}, // 8: the minimum
		{"a-much-longer-secret", true},
	}
	for _, tc := range cases {
		r := NewRedactor()
		if got := r.Register(tc.secret); got != tc.registered {
			t.Errorf("Register(%q) = %v, want %v", tc.secret, got, tc.registered)
		}
		text := "value=" + tc.secret + " pw in a sentence"
		got := r.Redact(text)
		if tc.registered && strings.Contains(got, tc.secret) {
			t.Errorf("registered secret %q not masked: %q", tc.secret, got)
		}
		if !tc.registered && got != text {
			t.Errorf("short secret %q must not change unrelated text: %q", tc.secret, got)
		}
	}
}

func TestRedactorEscapedForms(t *testing.T) {
	t.Parallel()
	r := NewRedactor()
	if !r.Register(sentinelPassword) {
		t.Fatal("sentinel not registered")
	}
	userinfo := strings.TrimPrefix(url.UserPassword("", sentinelPassword).String(), ":")
	forms := []string{
		sentinelPassword,
		url.QueryEscape(sentinelPassword),
		url.PathEscape(sentinelPassword),
		userinfo,
		pctEncode(sentinelPassword),
	}
	for _, f := range forms {
		out := r.Redact("before " + f + " after")
		if out != "before *** after" {
			t.Errorf("form %q: got %q", f, out)
		}
	}
	// A DSN built the way install builds it (url.UserPassword).
	dsn := (&url.URL{Scheme: "postgresql", User: url.UserPassword("claude_memory", sentinelPassword),
		Host: "h:5432", Path: "/claude_memory"}).String()
	out := r.Redact("connect " + dsn)
	assertNoSentinel(t, out)
	if !strings.Contains(out, "claude_memory:***@h:5432") {
		t.Errorf("DSN not redacted as expected: %q", out)
	}
	if got := r.RedactError(errors.New("auth failed for " + sentinelPassword)); got != "auth failed for ***" {
		t.Errorf("RedactError = %q", got)
	}
	if got := r.RedactError(nil); got != "" {
		t.Errorf("RedactError(nil) = %q", got)
	}
}

func TestRedactorLongestFirst(t *testing.T) {
	t.Parallel()
	r := NewRedactor()
	r.Register("secret-abcdef")
	r.Register("secret-abcdef-longer")
	if got := r.Redact("x secret-abcdef-longer y"); got != "x *** y" {
		t.Errorf("got %q", got)
	}
}

func TestRedactorWriter(t *testing.T) {
	t.Parallel()
	r := NewRedactor()
	var buf bytes.Buffer
	w := r.Writer(&buf)
	// Registered before use (AC-30): the secret is known before any output.
	r.Register(sentinelPassword)
	msg := fmt.Sprintf("password %s and dsn postgres://u:short@h/db\n", sentinelPassword)
	n, err := fmt.Fprint(w, msg)
	if err != nil || n != len(msg) {
		t.Fatalf("Fprint = %d, %v; want %d, nil", n, err, len(msg))
	}
	if got, want := buf.String(), "password *** and dsn postgres://u:***@h/db\n"; got != want {
		t.Errorf("writer output %q, want %q", got, want)
	}
}

func TestRedactorConcurrent(t *testing.T) {
	t.Parallel()
	r := NewRedactor()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			r.Register(fmt.Sprintf("concurrent-secret-%d", i))
		}(i)
		go func() {
			defer wg.Done()
			_ = r.Redact("concurrent-secret-1 postgres://u:p@h/db")
		}()
	}
	wg.Wait()
	if got := r.Redact("concurrent-secret-7"); got != Mask {
		t.Errorf("got %q", got)
	}
}

// assertNoSentinel fails when s contains the AC-30 sentinel password or any
// of its percent-encoded forms. Shared by every sentinel test in the package.
func assertNoSentinel(t testing.TB, s string) {
	t.Helper()
	forms := []string{
		sentinelPassword,
		url.QueryEscape(sentinelPassword),
		url.PathEscape(sentinelPassword),
		strings.TrimPrefix(url.UserPassword("", sentinelPassword).String(), ":"),
		pctEncode(sentinelPassword),
	}
	for _, f := range forms {
		if strings.Contains(s, f) {
			t.Errorf("output contains the sentinel password (form %q): %q", f, s)
		}
	}
}

func TestRedactorRegistersPctEncodedForm(t *testing.T) {
	t.Parallel()
	r := NewRedactor()
	r.Register("pa$$word-x")
	// '$' stays raw in url.UserPassword but is %24 in pctEncode; both are masked.
	for _, in := range []string{"pa$$word-x", "pa%24%24word-x"} {
		if got := r.Redact("v=" + in + ";"); got != "v=***;" {
			t.Errorf("Redact(%q) = %q", in, got)
		}
	}
}
