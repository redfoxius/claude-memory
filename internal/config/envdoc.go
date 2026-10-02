package config

import (
	"errors"
	"fmt"
	"strings"
)

// EnvDoc is a line-level model of an env file (plan WI-S2-5, B-4): it keeps
// every line - comments, blanks, unknown keys, duplicates and lines it cannot
// parse - in order, with its own line terminator, so an edit touches only the
// lines it names and everything else round-trips byte for byte. It is pure:
// no I/O and no process environment. internal/setup's envfile step is the
// only writer of the installed env file; this type is the editor it uses.
//
// The line grammar is the one ParseEnvData reads (spec AC-27): a line is
// blank, a `#` comment, `[export ]KEY=VALUE`, or something else (no '=', or
// an invalid key), which is kept untouched.
type EnvDoc struct {
	lines []envLine
	eol   string // newline style for appended lines
}

type envLineKind int

const (
	envOther envLineKind = iota // no '=', invalid key, blank or comment: kept verbatim
	envAssign
)

type envLine struct {
	raw    string // the line without its terminator
	eol    string // "\n", "\r\n", or "" for a last line without a newline
	kind   envLineKind
	export bool
	key    string
	value  string // trimmed, as written (quotes included)
}

// EnvEntry describes the first assignment of a key.
type EnvEntry struct {
	Line int // 1-based line number
	// Value is the value without `export` and without whole-value quotes.
	Value string
	// Raw is the value as written, quotes included.
	Raw string
	// Export is set for an `export KEY=VALUE` line (the binary ignores it).
	Export bool
	// Quoted is set when quotes wrap the whole value.
	Quoted bool
	// Unparseable is set when the line has text the plain format cannot
	// represent safely (AC-27 unparseable-value), e.g. `$(...)` or a
	// backslash. Such a line is never converted.
	Unparseable bool
	// Plain reports that Value, written without quotes, is clean under the
	// AC-27 classifier. A quoted `'pa$word'` is Quoted but not Plain.
	Plain  bool
	Detail string // the classifier's reason when Unparseable or !Plain
}

// Convertible reports whether Normalize can rewrite the line to the plain
// form (it has an `export` prefix or whole-value quotes, and nothing else
// the plain format cannot hold).
func (e EnvEntry) Convertible() bool {
	return (e.Export || e.Quoted) && !e.Unparseable && e.Plain
}

// ParseEnvDoc parses data into an EnvDoc. A leading UTF-8 BOM is dropped.
func ParseEnvDoc(data []byte) *EnvDoc {
	s := strings.TrimPrefix(string(data), "\ufeff")
	d := &EnvDoc{}
	if s == "" {
		d.eol = "\n"
		return d
	}
	pieces := strings.Split(s, "\n")
	for i, p := range pieces {
		last := i == len(pieces)-1
		if last && p == "" {
			break // the text ended with a newline
		}
		eol := "\n"
		if last {
			eol = ""
		}
		if t, ok := strings.CutSuffix(p, "\r"); ok {
			p = t
			eol = "\r\n"
		}
		d.lines = append(d.lines, parseEnvLine(p, eol))
	}
	d.eol = "\n"
	for _, l := range d.lines {
		if l.eol != "" {
			d.eol = l.eol
			break
		}
	}
	return d
}

func parseEnvLine(raw, eol string) envLine {
	l := envLine{raw: raw, eol: eol}
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return l
	}
	if rest, ok := cutExport(line); ok {
		l.export = true
		line = rest
	}
	k, v, ok := strings.Cut(line, "=")
	if !ok {
		return envLine{raw: raw, eol: eol}
	}
	key := strings.TrimSpace(k)
	if !envKeyRe.MatchString(key) {
		return envLine{raw: raw, eol: eol}
	}
	l.kind, l.key, l.value = envAssign, key, strings.TrimSpace(v)
	return l
}

// find returns the index of the line the loader would use for key: the first
// plain assignment with a non-empty value (the loader's first-non-empty
// rule), else the first plain one (an empty value), else the first `export`
// one; -1 when there is none.
func (d *EnvDoc) find(key string) int {
	empty, exp := -1, -1
	for i, l := range d.lines {
		if l.kind != envAssign || l.key != key {
			continue
		}
		switch {
		case l.export:
			if exp < 0 {
				exp = i
			}
		case l.value != "":
			return i
		case empty < 0:
			empty = i
		}
	}
	if empty >= 0 {
		return empty
	}
	return exp
}

// hasPlain reports whether key has a plain (non-export) assignment.
func (d *EnvDoc) hasPlain(key string) bool {
	for _, l := range d.lines {
		if l.kind == envAssign && !l.export && l.key == key {
			return true
		}
	}
	return false
}

// ShadowedExport is an `export KEY=VALUE` line that Normalize leaves alone
// because KEY also has a plain assignment: the binary reads the plain line, and
// converting the export one could change which value wins.
type ShadowedExport struct {
	Line int // 1-based
	Key  string
}

// ShadowedExports lists the export lines whose key also has a plain
// assignment, in file order (reported as duplicates; removing one needs the
// user's consent, so install never does it).
func (d *EnvDoc) ShadowedExports() []ShadowedExport {
	var out []ShadowedExport
	for i, l := range d.lines {
		if l.kind == envAssign && l.export && d.hasPlain(l.key) {
			out = append(out, ShadowedExport{Line: i + 1, Key: l.key})
		}
	}
	return out
}

func entryOf(i int, l envLine) EnvEntry {
	e := EnvEntry{Line: i + 1, Raw: l.value, Value: l.value, Export: l.export, Plain: true}
	kind, detail := classifyValue(l.value)
	switch kind {
	case FindingUnparseableValue:
		e.Unparseable, e.Plain, e.Detail = true, false, detail
	case FindingQuotedWholeValue:
		e.Quoted = true
		e.Value = l.value[1 : len(l.value)-1]
		if k2, d2 := classifyValue(e.Value); k2 != "" {
			e.Plain, e.Detail = false, d2
		}
	}
	return e
}

// Entry returns the first assignment of key (see find) and whether the file
// has one.
func (d *EnvDoc) Entry(key string) (EnvEntry, bool) {
	i := d.find(key)
	if i < 0 {
		return EnvEntry{}, false
	}
	return entryOf(i, d.lines[i]), true
}

// Get returns the value of key without `export` and whole-value quotes. It
// reports false when the key is absent or its line is unparseable.
func (d *EnvDoc) Get(key string) (string, bool) {
	e, ok := d.Entry(key)
	if !ok || e.Unparseable {
		return "", false
	}
	return e.Value, true
}

// Set writes `KEY=value` (plain: no quotes, no export): in place of the first
// assignment of key, or appended. Duplicates are left alone. It reports
// whether the text changed. The caller decides whether an unparseable line
// may be replaced; Set does not check.
func (d *EnvDoc) Set(key, value string) (bool, error) {
	if !envKeyRe.MatchString(key) {
		return false, fmt.Errorf("invalid env key %q", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return false, errors.New("env value contains a newline")
	}
	text := key + "=" + value
	if i := d.find(key); i >= 0 {
		l := d.lines[i]
		if l.raw == text {
			return false, nil
		}
		d.lines[i] = envLine{raw: text, eol: l.eol, kind: envAssign, key: key, value: value}
		return true, nil
	}
	// Appending after a last line without a newline gives that line one.
	if n := len(d.lines); n > 0 && d.lines[n-1].eol == "" {
		d.lines[n-1].eol = d.eol
	}
	d.lines = append(d.lines, envLine{raw: text, eol: d.eol, kind: envAssign, key: key, value: value})
	return true, nil
}

// Remove deletes every assignment of key and reports whether there was one.
func (d *EnvDoc) Remove(key string) bool {
	out := d.lines[:0:0]
	removed := false
	for _, l := range d.lines {
		if l.kind == envAssign && l.key == key {
			removed = true
			continue
		}
		out = append(out, l)
	}
	d.lines = out
	return removed
}

// Normalize rewrites the first assignment of key to the plain form (no
// `export`, no whole-value quotes) when that is lossless (Entry.Convertible).
// It reports whether it changed anything.
func (d *EnvDoc) Normalize(key string) bool {
	i := d.find(key)
	if i < 0 {
		return false
	}
	return d.normalizeLine(i)
}

// NormalizeAll applies Normalize to every convertible line and returns the
// keys it converted, in file order.
func (d *EnvDoc) NormalizeAll() []string {
	var keys []string
	for i := range d.lines {
		if d.lines[i].kind == envAssign && d.normalizeLine(i) {
			keys = append(keys, d.lines[i].key)
		}
	}
	return keys
}

func (d *EnvDoc) normalizeLine(i int) bool {
	l := d.lines[i]
	e := entryOf(i, l)
	if !e.Convertible() {
		return false
	}
	// The binary ignores an export line. Converting one while the key has a
	// plain assignment would make it a candidate for the loader's
	// first-non-empty rule and could change the value the binary reads.
	if l.export && d.hasPlain(l.key) {
		return false
	}
	d.lines[i] = envLine{raw: l.key + "=" + e.Value, eol: l.eol, kind: envAssign, key: l.key, value: e.Value}
	return true
}

// Findings returns the AC-27 format findings of the document as it stands
// (the mode finding is not included: a document has no mode).
func (d *EnvDoc) Findings() []Finding {
	return parseEnv(d.text(false)).Findings
}

// Marshal returns the document text. Untouched lines keep their own line
// terminators, appended ones use the file's style (CRLF when the first
// terminated line is CRLF), and a missing final newline is added. An empty
// document is empty.
func (d *EnvDoc) Marshal() []byte { return []byte(d.text(true)) }

// Bytes is Marshal.
func (d *EnvDoc) Bytes() []byte { return d.Marshal() }

func (d *EnvDoc) text(finalEOL bool) string {
	var b strings.Builder
	for i, l := range d.lines {
		b.WriteString(l.raw)
		eol := l.eol
		if eol == "" && finalEOL && i == len(d.lines)-1 {
			eol = d.eol
		}
		b.WriteString(eol)
	}
	return b.String()
}
