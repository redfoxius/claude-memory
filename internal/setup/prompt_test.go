package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func newTestPrompter(in string, red *Redactor, secret func() (string, error)) (*LinePrompter, *bytes.Buffer) {
	var out bytes.Buffer
	return NewLinePrompter(strings.NewReader(in), &out, red, secret, true), &out
}

func TestLinePrompterSelect(t *testing.T) {
	t.Parallel()
	opts := []string{"apply", "keep", "skip"}
	cases := []struct {
		name    string
		in      string
		def     int
		want    int
		wantErr error
	}{
		{"enter takes default", "\n", 1, 1, nil},
		{"number", "3\n", 0, 2, nil},
		{"text, any case", "Skip\n", 0, 2, nil},
		{"crlf", "2\r\n", 0, 1, nil},
		{"invalid then valid", "9\nabc\n1\n", 1, 0, nil},
		{"three invalid", "9\n0\nx\n", 1, 0, ErrTooManyAttempts},
		{"eof", "", 1, 0, ErrInterrupted},
		{"ctrl-c byte", "\x03\n", 1, 0, ErrInterrupted},
		{"last line without newline", "2", 0, 1, nil},
	}
	for _, tc := range cases {
		p, out := newTestPrompter(tc.in, nil, nil)
		got, err := p.Select("Pick one", opts, tc.def)
		if !errors.Is(err, tc.wantErr) || (err == nil && got != tc.want) {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", tc.name, got, err, tc.want, tc.wantErr)
		}
		if !strings.Contains(out.String(), "1) apply") || !strings.Contains(out.String(), fmt.Sprintf("Choice [%d]", tc.def+1)) {
			t.Errorf("%s: output lacks numbered options/default:\n%s", tc.name, out)
		}
	}
}

func TestLinePrompterConfirm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		def     bool
		want    bool
		wantErr error
		hint    string
	}{
		{"enter default no", "\n", false, false, nil, "[y/N]"},
		{"enter default yes", "\n", true, true, nil, "[Y/n]"},
		{"yes", "Yes\n", false, true, nil, "[y/N]"},
		{"n", "n\n", true, false, nil, "[Y/n]"},
		{"retry then y", "maybe\ny\n", false, true, nil, "[y/N]"},
		{"three invalid", "a\nb\nc\n", true, false, ErrTooManyAttempts, "[Y/n]"},
		{"eof", "", true, false, ErrInterrupted, "[Y/n]"},
	}
	for _, tc := range cases {
		p, out := newTestPrompter(tc.in, nil, nil)
		got, err := p.Confirm("Proceed?", tc.def)
		if !errors.Is(err, tc.wantErr) || (err == nil && got != tc.want) {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", tc.name, got, err, tc.want, tc.wantErr)
		}
		if !strings.Contains(out.String(), tc.hint) {
			t.Errorf("%s: output lacks %s: %q", tc.name, tc.hint, out)
		}
	}
}

func TestLinePrompterText(t *testing.T) {
	t.Parallel()
	notEmpty := func(s string) error {
		if s == "" {
			return errors.New("must not be empty")
		}
		return nil
	}
	p, out := newTestPrompter("\n", nil, nil)
	if got, err := p.Text("Host", "localhost", nil); err != nil || got != "localhost" {
		t.Errorf("default: (%q, %v)", got, err)
	}
	if !strings.Contains(out.String(), "Host [localhost]: ") {
		t.Errorf("default not shown: %q", out)
	}

	p, out = newTestPrompter("\n  db1  \n", nil, nil)
	if got, err := p.Text("Host", "", notEmpty); err != nil || got != "db1" {
		t.Errorf("validate retry: (%q, %v)", got, err)
	}
	if !strings.Contains(out.String(), "Invalid: must not be empty") {
		t.Errorf("validation error not shown: %q", out)
	}

	p, _ = newTestPrompter("\n\n\n", nil, nil)
	if _, err := p.Text("Host", "", notEmpty); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("3 rejections: %v", err)
	}
	p, _ = newTestPrompter("", nil, nil)
	if _, err := p.Text("Host", "x", nil); !errors.Is(err, ErrInterrupted) {
		t.Errorf("eof: %v", err)
	}
}

func TestLinePrompterSecretRegistersBeforeReturn(t *testing.T) {
	t.Parallel()
	red := NewRedactor()
	const pw = "s3cr3t-Passw0rd"
	p, out := newTestPrompter("", red, func() (string, error) { return pw, nil })
	got, err := p.Secret("Password")
	if err != nil || got != pw {
		t.Fatalf("Secret = (%q, %v)", got, err)
	}
	if red.Redact("connect "+pw) != "connect "+Mask {
		t.Errorf("secret not registered with the Redactor")
	}
	if strings.Contains(out.String(), pw) {
		t.Errorf("secret printed: %q", out)
	}
	if !strings.HasPrefix(out.String(), "Password: ") {
		t.Errorf("question not shown: %q", out)
	}
}

func TestLinePrompterSecretErrors(t *testing.T) {
	t.Parallel()
	p, _ := newTestPrompter("", nil, func() (string, error) { return "", io.EOF })
	if _, err := p.Secret("Password"); !errors.Is(err, ErrInterrupted) {
		t.Errorf("EOF: %v", err)
	}
	boom := errors.New("boom")
	p, _ = newTestPrompter("", nil, func() (string, error) { return "", boom })
	if _, err := p.Secret("Password"); !errors.Is(err, boom) {
		t.Errorf("error passthrough: %v", err)
	}
	p, _ = newTestPrompter("", nil, nil)
	if _, err := p.Secret("Password"); err == nil {
		t.Errorf("nil secret reader must error")
	}
}

// The engine builds the phase-2 Select question from a step's Detail: the
// TTY Prompter must redact it (Prompter contract, Design 17).
func TestLinePrompterRedactsQuestionsAndOptions(t *testing.T) {
	t.Parallel()
	red := NewRedactor()
	red.Register("hunter2-literal")
	const dsn = "postgresql://claude_memory:p%40ssw0rd@db.example:5432/claude_memory"
	detail := "database [absent]: cannot reach " + dsn + " (token hunter2-literal)"

	p, out := newTestPrompter("1\n", red, nil)
	if _, err := p.Select(detail, []string{"apply " + dsn, "keep", "skip"}, 0); err != nil {
		t.Fatal(err)
	}
	p2, out2 := newTestPrompter("y\n", red, nil)
	if _, err := p2.Confirm("use "+dsn+"?", false); err != nil {
		t.Fatal(err)
	}
	p3, out3 := newTestPrompter("\n", red, nil)
	if _, err := p3.Text("DSN "+dsn, dsn, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	p4, out4 := newTestPrompter("", red, func() (string, error) { return "x", nil })
	if _, err := p4.Secret("pw for " + dsn); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]*bytes.Buffer{"select": out, "confirm": out2, "text": out3, "secret": out4} {
		s := o.String()
		for _, leak := range []string{"p%40ssw0rd", "hunter2-literal"} {
			if strings.Contains(s, leak) {
				t.Errorf("%s leaked %q:\n%s", name, leak, s)
			}
		}
		if !strings.Contains(s, "claude_memory:***@db.example") {
			t.Errorf("%s: expected the masked DSN:\n%s", name, s)
		}
	}
}

func TestLinePrompterInteractive(t *testing.T) {
	t.Parallel()
	if NewLinePrompter(strings.NewReader(""), io.Discard, nil, nil, false).Interactive() {
		t.Error("Interactive must report the injected value (false)")
	}
	if !NewLinePrompter(strings.NewReader(""), io.Discard, nil, nil, true).Interactive() {
		t.Error("Interactive must report the injected value (true)")
	}
}

// AC-11: a non-TTY session without --yes exits 2 before Seed or Detect, and
// nothing is read or asked.
func TestEngineNonInteractiveWithoutYesExits2BeforeDetect(t *testing.T) {
	t.Parallel()
	h := newEH(t, false) // FakePrompter fails the test on any prompt
	s := h.step("s", nil, "s/x")
	h.world.Set("s/x", StateAbsent)
	r := h.run(Inputs{}, s)
	wantExit(t, r, ExitUsage)
	if r.Err == nil || !strings.Contains(r.Err.Error(), "non-interactive session: pass --yes") {
		t.Fatalf("err = %v", r.Err)
	}
	if got := h.log.Calls(); len(got) != 0 {
		t.Fatalf("steps ran before the check: %v", got)
	}
	if len(h.fs.Writes()) != 0 {
		t.Fatalf("writes: %v", h.fs.Writes())
	}
}
