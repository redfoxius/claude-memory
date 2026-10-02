package setup

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// pctEncode percent-encodes every byte of s outside the RFC 3986 unreserved
// set (A-Za-z0-9-._~) as %XX, so `$ & ' ( ) * + , ; = : @ / ? # %` and
// spaces are all escaped (plan Design 24, B-2). url.UserPassword leaves
// sub-delims such as `$` alone, and config.classifyValue then flags the
// installer's own env file; this form is safe in a DSN user, password and
// database name. Bytes are encoded individually, so multi-byte UTF-8 becomes
// one %XX per byte.
func pctEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

// Defaults of the database prompts (AC-20).
const (
	DefaultPGPort     = "5432"
	DefaultPGDB       = "claude_memory"
	DefaultPGUser     = "claude_memory"
	DefaultSSLMode    = "prefer"
	pgPasswordFlagMsg = "pass the password via the prompt or --pg-password-stdin, not argv"
)

// SSLModes are the sslmode values a DSN may carry (libpq).
var SSLModes = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// NormalizeHost validates a DSN host (plan Design 24): a hostname, an IPv4
// address or an IPv6 address, bracketed or not, and nothing else (no path,
// port, userinfo or socket directory). It returns the host in the form
// DBTarget stores: IPv6 without brackets.
func NormalizeHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return "", errors.New("host is empty")
	}
	if strings.HasPrefix(h, "[") {
		if !strings.HasSuffix(h, "]") {
			return "", errors.New("host: unbalanced brackets")
		}
		h = h[1 : len(h)-1]
		if ip := net.ParseIP(h); ip == nil || ip.To4() != nil || strings.Contains(h, "%") {
			return "", errors.New("host: not an IPv6 address")
		}
		return h, nil
	}
	if strings.Contains(h, ":") {
		if ip := net.ParseIP(h); ip != nil && ip.To4() == nil && !strings.Contains(h, "%") {
			return h, nil
		}
		return "", errors.New("host: contains ':' (put an IPv6 address in brackets; the port is a separate field)")
	}
	if len(h) > 253 || !hostnameRe.MatchString(h) {
		return "", errors.New("host: not a hostname or an IP address")
	}
	// An all-numeric last label means an IPv4 literal, which must be valid.
	last := h[strings.LastIndex(h, ".")+1:]
	if _, err := strconv.Atoi(last); err == nil {
		if ip := net.ParseIP(h); ip == nil || ip.To4() == nil {
			return "", errors.New("host: not a valid IPv4 address")
		}
	}
	return h, nil
}

// ValidatePort accepts 1-65535 as plain digits.
func ValidatePort(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
		return errors.New("port: want a number from 1 to 65535")
	}
	return nil
}

// ValidateDBName accepts any non-empty name without control characters; the
// stricter role/database rule applies only on the create path (WI-S2-4b).
func ValidateDBName(n string) error {
	if n == "" {
		return errors.New("database name is empty")
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return errors.New("database name has a control character")
		}
	}
	return nil
}

// ValidateSSLMode accepts the libpq sslmode values.
func ValidateSSLMode(m string) error {
	for _, v := range SSLModes {
		if m == v {
			return nil
		}
	}
	return fmt.Errorf("sslmode: want one of %s", strings.Join(SSLModes, ", "))
}

// Validate checks host, port, database name and sslmode ("" sslmode is
// accepted: the DSN then omits it and the driver default, prefer, applies).
// It runs on every source of a DBTarget (Design 24). Errors never include
// the password.
func (t DBTarget) Validate() error {
	if _, err := NormalizeHost(t.Host); err != nil {
		return err
	}
	if err := ValidatePort(t.Port); err != nil {
		return err
	}
	if err := ValidateDBName(t.Name); err != nil {
		return err
	}
	if t.SSLMode != "" {
		return ValidateSSLMode(t.SSLMode)
	}
	return nil
}

// BuildDSN builds the postgresql:// URL of Design 24: user, password and
// database name are percent-encoded byte by byte outside RFC 3986 unreserved
// (never url.UserPassword, which leaves `$` alone), so the written env line
// never trips the AC-27 unparseable-value rule. host, port, database and
// sslmode are validated; an empty user omits the userinfo, an empty password
// omits ":password", and an empty sslmode omits the query.
func BuildDSN(user, password, host, port, db, sslmode string) (string, error) {
	t := DBTarget{Host: host, Port: port, Name: db, User: user, SSLMode: sslmode, password: secret(password)}
	if err := t.Validate(); err != nil {
		return "", err
	}
	h, _ := NormalizeHost(host)
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	var b strings.Builder
	b.WriteString("postgresql://")
	if user != "" {
		b.WriteString(pctEncode(user))
		if password != "" {
			b.WriteString(":" + pctEncode(password))
		}
		b.WriteString("@")
	}
	b.WriteString(h + ":" + port + "/" + pctEncode(db))
	if sslmode != "" {
		b.WriteString("?sslmode=" + sslmode)
	}
	return b.String(), nil
}

// DSN returns the target's DSN, or "" when Validate fails (nothing invalid
// is ever written).
func (t DBTarget) DSN() string {
	d, err := BuildDSN(t.User, string(t.password), t.Host, t.Port, t.Name, t.SSLMode)
	if err != nil {
		return ""
	}
	return d
}

// WithPassword returns a copy of t holding pw.
func (t DBTarget) WithPassword(pw string) DBTarget {
	t.password = secret(pw)
	return t
}

// HasPassword reports whether a password is held.
func (t DBTarget) HasPassword() bool { return t.password != "" }

// SameConnection reports whether o reaches the same database as t with the
// same credentials: host, port, name, user, password and effective sslmode
// (an empty sslmode is the driver default, prefer). Mode and Source do not
// count, nor does the DSN's spelling.
func (t DBTarget) SameConnection(o DBTarget) bool {
	eff := func(m string) string {
		if m == "" {
			return DefaultSSLMode
		}
		return m
	}
	return t.Host == o.Host && t.Port == o.Port && t.Name == o.Name && t.User == o.User &&
		t.password == o.password && eff(t.SSLMode) == eff(o.SSLMode)
}

// String is the target without its password, safe to print (and what %v of a
// DBTarget shows, instead of the unexported password field).
func (t DBTarget) String() string {
	d, err := BuildDSN(t.User, "", t.Host, firstNonEmpty(t.Port, DefaultPGPort), firstNonEmpty(t.Name, "-"), t.SSLMode)
	if err != nil {
		return "(invalid database target)"
	}
	return d
}

// GoString keeps %#v from printing the password.
func (t DBTarget) GoString() string { return "DBTarget(" + t.String() + ")" }

// ParseDBTarget parses a postgresql:// (or postgres://) URL into a validated
// DBTarget: a missing port becomes 5432, an empty database is an error. Only
// the sslmode query option is supported; any other one is refused rather
// than silently dropped from the rewritten file. Errors never carry the DSN.
func ParseDBTarget(dsn string) (DBTarget, error) {
	dsn = strings.TrimSpace(dsn)
	if !strings.HasPrefix(dsn, "postgresql://") && !strings.HasPrefix(dsn, "postgres://") {
		return DBTarget{}, errors.New("DSN: want a postgresql:// URL")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return DBTarget{}, errors.New("DSN does not parse as a URL (encode special characters in the password as %XX)")
	}
	if u.Fragment != "" || u.Opaque != "" {
		return DBTarget{}, errors.New("DSN: unexpected fragment")
	}
	t := DBTarget{User: u.User.Username(), Port: u.Port(), SSLMode: u.Query().Get("sslmode")}
	pw, _ := u.User.Password()
	t.password = secret(pw)
	for k := range u.Query() {
		if k != "sslmode" {
			return DBTarget{}, fmt.Errorf("DSN option %q is not supported by install (only sslmode)", k)
		}
	}
	host := u.Hostname()
	if strings.Contains(u.Host, "[") && !strings.Contains(host, ":") {
		return DBTarget{}, errors.New("host: not an IPv6 address")
	}
	if t.Host, err = NormalizeHost(host); err != nil {
		return DBTarget{}, err
	}
	if t.Port == "" {
		t.Port = DefaultPGPort
	}
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	if strings.Contains(path, "/") {
		return DBTarget{}, errors.New("database name: '/' must be encoded as %2F (or the password holds an unencoded '/')")
	}
	if t.Name, err = url.PathUnescape(path); err != nil {
		return DBTarget{}, errors.New("database name: bad percent-encoding")
	}
	if err := t.Validate(); err != nil {
		return DBTarget{}, err
	}
	return t, nil
}

var hexEscapeRe = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

// RecoverDSN parses a DSN whose password (or user) was written without
// percent-encoding, e.g. a base64 password with '/' or `pa$word` (AC-28): the
// userinfo ends at the last '@', the user at the first ':'. It refuses text
// that looks like shell syntax (quotes, backslash, backtick, `$(`, `${`) and
// a password that already holds %XX sequences, which is ambiguous; those
// stay as the user wrote them. The result is a normal DBTarget, so DSN()
// re-encodes it.
func RecoverDSN(raw string) (DBTarget, bool) {
	raw = strings.TrimSpace(raw)
	var scheme string
	switch {
	case strings.HasPrefix(raw, "postgresql://"):
		scheme = "postgresql://"
	case strings.HasPrefix(raw, "postgres://"):
		scheme = "postgres://"
	default:
		return DBTarget{}, false
	}
	rest := raw[len(scheme):]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return DBTarget{}, false
	}
	user, pw, ok := strings.Cut(rest[:at], ":")
	if !ok || pw == "" || strings.ContainsAny(user, "/?#") {
		return DBTarget{}, false
	}
	if strings.ContainsAny(rest[:at], "\\`\"'") || strings.Contains(rest[:at], "$(") ||
		strings.Contains(rest[:at], "${") || hexEscapeRe.MatchString(pw) {
		return DBTarget{}, false
	}
	t, err := ParseDBTarget(scheme + "x:y@" + rest[at+1:])
	if err != nil {
		return DBTarget{}, false
	}
	t.User, t.password = user, secret(pw)
	return t, true
}

// CheckPGDSNFlag refuses a --pg-dsn that carries a password (AC-31, exit 2).
func CheckPGDSNFlag(dsn string) error {
	if len(dsnPasswords(dsn)) > 0 {
		return errors.New(pgPasswordFlagMsg)
	}
	return nil
}

// DBTargetFromFlags builds the Seed-side DBTarget from --pg-dsn and the
// password read from --pg-password-stdin (Inputs.PGPassword, already read
// before Detect, AC-31). ok is false when no --pg-dsn was given (the
// password alone, if any, is then applied by the owner to whatever target it
// resolves). An error means an invalid flag value (exit 2).
func DBTargetFromFlags(in Inputs) (t DBTarget, ok bool, err error) {
	if in.PGDSN == "" {
		return DBTarget{}, false, nil
	}
	if err := CheckPGDSNFlag(in.PGDSN); err != nil {
		return DBTarget{}, false, fmt.Errorf("--pg-dsn: %w", err)
	}
	t, err = ParseDBTarget(in.PGDSN)
	if err != nil {
		return DBTarget{}, false, fmt.Errorf("--pg-dsn: %w", err)
	}
	if in.PGPassword != "" {
		t = t.WithPassword(in.PGPassword)
	}
	t.Source = SourceFlag
	return t, true, nil
}

// HostIsLocal classifies a DSN host for the AC-19 default: loopback names and
// addresses (and an empty host, a socket) are local, everything else remote.
func HostIsLocal(h string) bool {
	h = strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]"))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
