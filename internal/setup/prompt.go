package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrTooManyAttempts is returned by a LinePrompter after three invalid
// answers to one question (AC-10). The engine skips the step it came from
// (whether raised in Configure or while asking for a choice) and goes on.
var ErrTooManyAttempts = errors.New("too many invalid answers")

// MaxPromptAttempts is how many answers one question gets (AC-10).
const MaxPromptAttempts = 3

// LinePrompter is the line-oriented Prompter (AC-10, Design 1): plain text
// over an io.Reader / io.Writer, no TUI. It never touches the terminal
// itself: the TTY adapter in cmd/claude-memory supplies the Interactive
// answer and a no-echo secret reader (golang.org/x/term), tests supply a
// scripted transcript.
//
// Everything it prints goes through the Redactor (the redaction contract in
// ports.go: a step's Detail can reach a question), and a secret is
// registered with the Redactor before Secret returns it (AC-30).
type LinePrompter struct {
	in          *bufio.Reader
	out         io.Writer
	red         *Redactor
	secret      func() (string, error)
	interactive bool
}

var _ Prompter = (*LinePrompter)(nil)

// NewLinePrompter returns a LinePrompter. red may be nil (nothing is
// redacted, for tests); secret reads one line without echo and may be nil
// when Secret is never called; interactive is what Interactive reports.
func NewLinePrompter(in io.Reader, out io.Writer, red *Redactor, secret func() (string, error), interactive bool) *LinePrompter {
	return &LinePrompter{in: bufio.NewReader(in), out: out, red: red, secret: secret, interactive: interactive}
}

// Interactive implements Prompter.
func (p *LinePrompter) Interactive() bool { return p.interactive }

func (p *LinePrompter) printf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	if p.red != nil {
		s = p.red.Redact(s)
	}
	_, _ = io.WriteString(p.out, s)
}

// readLine reads one line without its terminator. End of input with nothing
// read, and a Ctrl-C byte, are ErrInterrupted.
func (p *LinePrompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", ErrInterrupted
	}
	if strings.ContainsRune(line, 0x03) {
		return "", ErrInterrupted
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Select implements Prompter. Answers: the 1-based number, or the option's
// text (case-insensitive); Enter takes def.
func (p *LinePrompter) Select(q string, opts []string, def int) (int, error) {
	if len(opts) == 0 {
		return 0, errors.New("select: no options")
	}
	if def < 0 || def >= len(opts) {
		def = 0
	}
	p.printf("%s\n", q)
	for i, o := range opts {
		mark := " "
		if i == def {
			mark = "*"
		}
		p.printf("  %s%d) %s\n", mark, i+1, o)
	}
	for attempt := 0; attempt < MaxPromptAttempts; attempt++ {
		p.printf("Choice [%d]: ", def+1)
		line, err := p.readLine()
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return def, nil
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(opts) {
			return n - 1, nil
		}
		for i, o := range opts {
			if strings.EqualFold(line, o) {
				return i, nil
			}
		}
		p.printf("Invalid choice %q: enter a number from 1 to %d.\n", line, len(opts))
	}
	return 0, ErrTooManyAttempts
}

// Confirm implements Prompter.
func (p *LinePrompter) Confirm(q string, def bool) (bool, error) {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for attempt := 0; attempt < MaxPromptAttempts; attempt++ {
		p.printf("%s %s ", q, hint)
		line, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		p.printf("Please answer y or n.\n")
	}
	return false, ErrTooManyAttempts
}

// Text implements Prompter. Enter takes def; validate (may be nil) is
// applied to the answer, default included, and a rejection re-asks.
func (p *LinePrompter) Text(q, def string, validate func(string) error) (string, error) {
	for attempt := 0; attempt < MaxPromptAttempts; attempt++ {
		if def != "" {
			p.printf("%s [%s]: ", q, def)
		} else {
			p.printf("%s: ", q)
		}
		line, err := p.readLine()
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			line = def
		}
		if validate != nil {
			if verr := validate(line); verr != nil {
				p.printf("Invalid: %v\n", verr)
				continue
			}
		}
		return line, nil
	}
	return "", ErrTooManyAttempts
}

// Secret implements Prompter: the injected reader gets no echo, and the
// answer is registered with the Redactor before it is returned.
func (p *LinePrompter) Secret(q string) (string, error) {
	if p.secret == nil {
		return "", errors.New("secret: no secret reader configured")
	}
	p.printf("%s: ", q)
	s, err := p.secret()
	p.printf("\n") // the terminal did not echo the Enter
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", ErrInterrupted
		}
		return "", err
	}
	if p.red != nil {
		p.red.Register(s)
	}
	return s, nil
}
