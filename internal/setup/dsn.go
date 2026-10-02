package setup

import "strings"

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
