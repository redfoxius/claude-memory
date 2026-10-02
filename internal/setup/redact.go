package setup

import (
	"io"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// MinSecretLen is the shortest secret the Redactor masks as a literal
// (AC-30). A shorter secret could mangle unrelated text; it is still masked
// inside URLs by the userinfo pattern.
const MinSecretLen = 8

// Mask replaces every redacted secret in output.
const Mask = "***"

// userinfoRe matches the password part of `scheme://user:password@`. It
// anchors on the last '@' before the first '/' (so a password holding '@'
// is masked whole); when the password itself holds a '/' (unencoded base64)
// it falls back to the first '@'. Whitespace ends the match either way.
var userinfoRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^:@/\s]*):(?:[^\s/]*@|[^@\s]*@)`)

// Redactor is the single output sink's filter (AC-30): every byte the
// feature prints, in text and JSON, passes through it. It always masks the
// password of `scheme://user:password@` URLs by pattern, and masks
// registered secret literals (and their URL-escaped forms) when they are at
// least MinSecretLen long. Secrets are registered before they are used —
// the prompt adapter registers a password as it returns it — so no error
// path can print one first. Safe for concurrent use.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string // literal forms, longest first
}

// NewRedactor returns a Redactor with no registered secrets.
func NewRedactor() *Redactor { return &Redactor{} }

// Register adds secret (and its url.QueryEscape, url.PathEscape and
// URL-userinfo and pctEncode forms) to the literals to mask. It returns false, and
// registers nothing, when the secret is shorter than MinSecretLen.
func (r *Redactor) Register(secret string) bool {
	if len(secret) < MinSecretLen {
		return false
	}
	forms := []string{
		secret,
		url.QueryEscape(secret),
		url.PathEscape(secret),
		strings.TrimPrefix(url.UserPassword("", secret).String(), ":"),
		pctEncode(secret), // the form dsn.go writes into the env file (Design 24)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range forms {
		if !slices.Contains(r.secrets, f) {
			r.secrets = append(r.secrets, f)
		}
	}
	// Longest first, so a secret that contains another is masked whole.
	sort.SliceStable(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
	return true
}

// Redact returns s with every URL password and registered secret masked.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, Mask)
	}
	r.mu.RUnlock()
	return userinfoRe.ReplaceAllString(s, "${1}:"+Mask+"@")
}

// RedactError returns err's message redacted, or "" for a nil error.
func (r *Redactor) RedactError(err error) string {
	if err == nil {
		return ""
	}
	return r.Redact(err.Error())
}

// Writer returns an io.Writer that redacts each Write before passing it to
// w. Callers write whole messages (one fmt.Fprint* call each), so a secret
// is never split across two writes. The returned byte count is len(p) on
// success, as io.Writer requires, even though fewer or more bytes reached w.
func (r *Redactor) Writer(w io.Writer) io.Writer { return &redactWriter{r: r, w: w} }

type redactWriter struct {
	r *Redactor
	w io.Writer
}

func (rw *redactWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(rw.w, rw.r.Redact(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}
