package setup

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/redfoxius/claude-memory/deploy"
)

// Tests for WI-S2-4b rendering: the bootstrap.sql golden (AC-64), the
// password and name rules (B-3) and the generated-password helper (AC-22).

// bootstrapSentinel is a password inside the bootstrap alphabet. The
// AC-30 sentinel S3ntinel-pw-$@:/x is deliberately outside it.
const bootstrapSentinel = "S3ntinel-pw_.~x9"

func TestRenderBootstrapGolden(t *testing.T) {
	t.Parallel()
	body := readTestdata(t, "testdata/bootstrap/body.psql")
	out, err := RenderBootstrap(body, "cm_app", "cm_db", bootstrapSentinel)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "testdata/bootstrap/sentinel.golden", out)
	again, _ := RenderBootstrap(body, "cm_app", "cm_db", bootstrapSentinel)
	if !bytes.Equal(out, again) {
		t.Error("render is not deterministic")
	}
}

// The embedded app-role.psql is appended verbatim after exactly three \set
// lines.
func TestRenderBootstrapEmbeddedBodyVerbatim(t *testing.T) {
	t.Parallel()
	body, err := fs.ReadFile(deploy.FS, deploy.InitDBAppRolePSQL)
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderBootstrap(body, "claude_memory", "claude_memory", bootstrapSentinel)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(out), "\n", 4)
	want := []string{`\set app_user 'claude_memory'`, `\set app_db 'claude_memory'`, `\set app_pw '` + bootstrapSentinel + `'`}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i+1, lines[i], w)
		}
	}
	if lines[3] != string(body) {
		t.Error("the body is not appended verbatim")
	}
}

func TestRenderBootstrapRefusesUnsafeValues(t *testing.T) {
	t.Parallel()
	body := []byte("SELECT 1;\n")
	passwords := []string{
		"pass'word1", `pass\word1`, "pass`id`1", "pass$word1", "pass word1", "password1\n", "pass\nword1", "pass\x00word1",
		"pa\"ssword1", "pass;word1", "pass:word1", "pass/word1", "pass@word1", "pässword1",
		"short", "", strings.Repeat("a", 257), bootstrapSentinel + "\n",
		"S3ntinel-pw-$@:/x",
	}
	for _, pw := range passwords {
		out, err := RenderBootstrap(body, "cm_app", "cm_db", pw)
		if err == nil || out != nil {
			t.Errorf("password %q was rendered", pw)
			continue
		}
		if pw != "" && len(pw) >= 8 && strings.Contains(err.Error(), pw) {
			t.Errorf("the error echoes the password %q: %v", pw, err)
		}
		if err.Error() != BootstrapPasswordRefusal {
			t.Errorf("password %q: error %q", pw, err)
		}
	}
	if !strings.Contains(BootstrapPasswordRefusal, "let the installer generate one") ||
		!strings.Contains(BootstrapPasswordRefusal, "deploy/initdb/app-role.psql") {
		t.Errorf("refusal text: %s", BootstrapPasswordRefusal)
	}
	for _, name := range []string{"", "Bad", "a-b", "1x", "x\n", "a b", `a"b`, "a;b", "a'b", "a`b", `a\b`, "ü", strings.Repeat("a", 64), "a.b"} {
		if out, err := RenderBootstrap(body, name, "cm_db", bootstrapSentinel); err == nil || out != nil {
			t.Errorf("role name %q was rendered", name)
		}
		if out, err := RenderBootstrap(body, "cm_app", name, bootstrapSentinel); err == nil || out != nil {
			t.Errorf("database name %q was rendered", name)
		}
	}
	// The edges of the rules are accepted.
	for _, pw := range []string{"abcdefgh", "A-b_c.d~e9", strings.Repeat("a", 256)} {
		if _, err := RenderBootstrap(body, "_x", strings.Repeat("a", 63), pw); err != nil {
			t.Errorf("password %q refused: %v", pw, err)
		}
	}
}

// No rendered output can contain a backtick, a backslash outside the \set
// meta-commands, or a quote beyond the three argument pairs, whatever passes
// validation.
func TestRenderBootstrapNoShellOrEscapeBytes(t *testing.T) {
	t.Parallel()
	out, err := RenderBootstrap([]byte(""), "u", "d", "A-b_c.d~e9")
	if err != nil {
		t.Fatal(err)
	}
	head := string(out)
	if strings.ContainsAny(head, "`$\"") || strings.Count(head, `\`) != 3 || strings.Count(head, "'") != 6 {
		t.Errorf("unexpected bytes in %q", head)
	}
}

func TestGeneratePassword(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader(bytes.Repeat([]byte{0xfb}, 64)) // 0xfb... maps to '-' and '_' in base64url
	a, err := GeneratePassword(src)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GeneratePassword(src)
	if len(a) != 43 || len(b) != 43 {
		t.Fatalf("length %d/%d, want 43 (32 bytes, no padding)", len(a), len(b))
	}
	if err := ValidateBootstrapPassword(a); err != nil {
		t.Errorf("generated password is not in the bootstrap alphabet: %v", err)
	}
	for _, c := range []byte(a) {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			t.Errorf("byte %q outside [A-Za-z0-9_-]", c)
		}
	}
	// A short read is an error, not a weak password.
	if pw, err := GeneratePassword(bytes.NewReader([]byte{1, 2, 3})); err == nil || pw != "" {
		t.Errorf("short read: %q, %v", pw, err)
	}
	if _, err := GeneratePassword(errReader{}); err == nil {
		t.Error("reader error ignored")
	}
	// The default source is crypto/rand: two draws differ.
	x, _ := GeneratePassword(nil)
	y, _ := GeneratePassword(nil)
	if x == y || len(x) != 43 {
		t.Errorf("default source: %q %q", x, y)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestBootstrapCommand(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ os, path, want string }{
		{OSLinux, "/home/u/.local/state/claude-memory/bootstrap.sql",
			"sudo -u postgres psql -v ON_ERROR_STOP=1 -d postgres -f - < /home/u/.local/state/claude-memory/bootstrap.sql"},
		{OSDarwin, "/Users/u/.local/state/claude-memory/bootstrap.sql",
			"psql -v ON_ERROR_STOP=1 -d postgres -f /Users/u/.local/state/claude-memory/bootstrap.sql"},
		{OSLinux, "/home/my user/it's/bootstrap.sql",
			`sudo -u postgres psql -v ON_ERROR_STOP=1 -d postgres -f - < '/home/my user/it'\''s/bootstrap.sql'`},
	} {
		if got := BootstrapCommand(c.os, c.path); got != c.want {
			t.Errorf("%s: %q, want %q", c.os, got, c.want)
		}
	}
}
