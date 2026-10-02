package setup

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// bootstrap.sql rendering (plan WI-S2-4b, Design 5, AC-21, AC-22). The file
// is three `\set` lines followed by deploy/initdb/app-role.psql verbatim, so
// the installer and the Docker stack share one SQL source. It holds a real
// password and is written 0600; the installer never executes it.
//
// Injection rule: a value is rendered only if every byte is in a small
// alphabet that psql meta-command argument parsing treats literally (no
// quote, backslash, backtick, `$`, space or newline). psql expands
// backslash escapes inside a single-quoted `\set` argument and runs
// backquoted text through the shell, so quoting a hostile value is not an
// option: it is refused instead (owner decision, spec AC-21 v0.3).

// GeneratedPasswordBytes is the entropy of a generated password (AC-22).
const GeneratedPasswordBytes = 32

// BootstrapPasswordRefusal is the create-path message for a password that
// cannot be rendered safely.
const BootstrapPasswordRefusal = "this password cannot be written into bootstrap.sql safely: let the installer generate one, " +
	"or run deploy/initdb/app-role.psql yourself with `psql -v app_pw=…`"

// GeneratePassword returns 32 bytes from r (crypto/rand in production),
// base64url-encoded without padding: 43 characters of [A-Za-z0-9_-], which is
// URL-, shell- and SQL-literal-safe and inside the bootstrap alphabet.
func GeneratePassword(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, GeneratedPasswordBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("generate a password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// reservedDBNames are databases bootstrap.sql must never take over: it
// revokes PUBLIC's privileges on the database it is given, which on one of
// these would break the server's own use of it.
var reservedDBNames = map[string]bool{"postgres": true, "template0": true, "template1": true}

func isNameStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') }
func isNameByte(c byte) bool  { return isNameStart(c) || (c >= '0' && c <= '9') }

// ValidateBootstrapName checks a role or database name against
// ^[a-z_][a-z0-9_]{0,62}$ (checked byte by byte, so no regexp edge such as a
// trailing newline can slip through). what names the value in the error.
func ValidateBootstrapName(what, v string) error {
	ok := len(v) >= 1 && len(v) <= 63 && isNameStart(v[0])
	for i := 1; ok && i < len(v); i++ {
		ok = isNameByte(v[i])
	}
	if ok && what == "database name" && reservedDBNames[v] {
		return fmt.Errorf("%s %q is reserved by Postgres: pick another name", what, v)
	}
	if !ok {
		return fmt.Errorf("%s %q cannot be created by install: use 1-63 characters from a-z, 0-9 and _, not starting with a digit", what, v)
	}
	return nil
}

func isPasswordByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '_' || c == '.' || c == '~' || c == '-'
}

// ValidateBootstrapPassword checks pw against ^[A-Za-z0-9_.~-]{8,256}$. The
// error never contains the password.
func ValidateBootstrapPassword(pw string) error {
	ok := len(pw) >= 8 && len(pw) <= 256
	for i := 0; ok && i < len(pw); i++ {
		ok = isPasswordByte(pw[i])
	}
	if !ok {
		return errors.New(BootstrapPasswordRefusal)
	}
	return nil
}

// RenderBootstrap returns bootstrap.sql: three `\set` lines with the values
// followed by body (the embedded app-role.psql) verbatim. user and db must
// match the name rule and password the password rule, otherwise nothing is
// rendered.
func RenderBootstrap(body []byte, user, db, password string) ([]byte, error) {
	if err := ValidateBootstrapName("role name", user); err != nil {
		return nil, err
	}
	if err := ValidateBootstrapName("database name", db); err != nil {
		return nil, err
	}
	if err := ValidateBootstrapPassword(password); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Grow(len(body) + 128)
	// Every value is in an alphabet that needs no escaping inside '…'.
	fmt.Fprintf(&b, "\\set app_user '%s'\n\\set app_db '%s'\n\\set app_pw '%s'\n", user, db, password)
	b.Write(body)
	if !bytes.HasSuffix(body, []byte("\n")) {
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

// shellWord quotes p for display in a command line the user will paste: as is
// when it holds only safe characters, else single-quoted.
func shellWord(p string) string {
	safe := p != ""
	for i := 0; safe && i < len(p); i++ {
		c := p[i]
		safe = (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("_@%+=:,./-", c) >= 0
	}
	if safe {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// BootstrapCommand is the command the user runs, printed and never executed
// (AC-21). Linux: the redirect is opened by the user's own shell, so the
// postgres OS user needs no read access to the 0600 file or the home
// directory. macOS/Homebrew: the installing user is the superuser.
func BootstrapCommand(os, path string) string {
	if os == OSDarwin {
		return "psql -v ON_ERROR_STOP=1 -d postgres -f " + shellWord(path)
	}
	return "sudo -u postgres psql -v ON_ERROR_STOP=1 -d postgres -f - < " + shellWord(path)
}
