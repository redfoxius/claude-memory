package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
)

// jsonobj is the order-preserving JSON codec behind the settings.json merge
// (spec AC-37 "Codec", plan Design 2).
//
// The input is walked with json.Decoder (UseNumber) and InputOffset is
// recorded around every value, so every node keeps the exact bytes it was
// parsed from. Writing re-emits every untouched subtree byte-for-byte:
//
//   - a container nothing inside changed is copied verbatim;
//   - a container whose own child list is unchanged but whose descendant
//     changed (or whose child value was replaced) is spliced: the original
//     bytes between its children (whitespace, commas, the key text) are
//     copied and only the changed child is re-emitted;
//   - a container that only gained children at its end keeps all of its
//     original bytes and gets the new children appended before its closer;
//   - any other changed container (children removed) is re-encoded with the
//     file's indentation unit, its untouched children still copied verbatim.
//
// Key order, unknown keys, number spellings and string escapes therefore
// survive. A duplicate key at any level is a parse error: last-wins
// re-encoding would silently drop one of them.

type jsonKind uint8

const (
	jsonNull jsonKind = iota
	jsonBool
	jsonNumber
	jsonString
	jsonObject
	jsonArray
)

func (k jsonKind) String() string {
	switch k {
	case jsonNull:
		return "null"
	case jsonBool:
		return "a boolean"
	case jsonNumber:
		return "a number"
	case jsonString:
		return "a string"
	case jsonObject:
		return "an object"
	default:
		return "an array"
	}
}

// jsonNode is one JSON value.
type jsonNode struct {
	kind jsonKind
	// raw is the exact bytes of the value: a slice of the document source
	// for parsed nodes, compact JSON for new ones.
	raw []byte
	// src is the whole document source and [start,end) the value's span in
	// it (parsed nodes only).
	src        []byte
	start, end int
	// isNew marks a node built by the editor (never parsed from src): it is
	// always pretty-printed.
	isNew bool
	// changed marks a parsed container whose child list was edited (a child
	// appended or removed). Replacing a child's value does not set it.
	changed bool
	kids    []*jsonKid // object members or array elements, in order
	// origKids is the number of children the container was parsed with.
	origKids int
}

// jsonKid is one object member (key set) or array element (key empty).
type jsonKid struct {
	key    string // decoded key
	keyRaw []byte // exact key bytes including quotes ("" for array elements)
	val    *jsonNode
	// orig marks a child parsed from src; valStart/valEnd is the span of its
	// value slot there, kept even when val is replaced by a new node.
	orig             bool
	valStart, valEnd int
}

// jsonError is a parse or shape error at a position of the input.
type jsonError struct {
	Reason    string
	Offset    int
	Line, Col int // 1-based
}

func (e *jsonError) Error() string { return fmt.Sprintf("%s at line %d:%d", e.Reason, e.Line, e.Col) }

func newJSONError(src []byte, off int, format string, args ...any) *jsonError {
	if off < 0 {
		off = 0
	}
	if off > len(src) {
		off = len(src)
	}
	line, col := lineCol(src, off)
	return &jsonError{Reason: fmt.Sprintf(format, args...), Offset: off, Line: line, Col: col}
}

// lineCol converts a byte offset into a 1-based line and column.
func lineCol(src []byte, off int) (int, int) {
	before := src[:off]
	line := bytes.Count(before, []byte("\n")) + 1
	col := off - bytes.LastIndexByte(before, '\n')
	return line, col
}

// jsonDoc is a parsed document plus its formatting.
type jsonDoc struct {
	src  []byte
	root *jsonNode
	unit string // indentation unit: "\t" or N spaces
	// blank marks an input with no value at all (empty or whitespace only);
	// root is then a new empty object.
	blank bool
}

// parseJSONDoc parses src strictly: one JSON value, no comments, no
// trailing commas, no trailing data, no duplicate keys. An empty or
// whitespace-only input yields an empty object (blank).
func parseJSONDoc(src []byte) (*jsonDoc, error) {
	d := &jsonDoc{src: src, unit: detectIndent(src)}
	if len(bytes.TrimSpace(src)) == 0 {
		d.blank = true
		d.root = &jsonNode{kind: jsonObject, raw: []byte("{}"), isNew: true}
		return d, nil
	}
	root, err := parseJSON(src)
	if err != nil {
		return nil, err
	}
	d.root = root
	return d, nil
}

// parseJSON parses one strict JSON value from src into a node tree.
func parseJSON(src []byte) (*jsonNode, error) {
	// Pass 1: syntax, with an offset for the error position.
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	var discard any
	if err := dec.Decode(&discard); err != nil {
		var se *json.SyntaxError
		switch {
		case errors.As(err, &se):
			return nil, newJSONError(src, int(se.Offset)-1, "invalid JSON (%s)", se.Error())
		case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
			return nil, newJSONError(src, len(src), "invalid JSON (unexpected end of input: truncated?)")
		default:
			return nil, newJSONError(src, int(dec.InputOffset()), "invalid JSON (%v)", err)
		}
	}
	if rest := skipSpace(src, int(dec.InputOffset())); rest < len(src) {
		return nil, newJSONError(src, rest, "invalid JSON (unexpected data after the top-level value)")
	}

	// Pass 2: the token walk, recording spans. The input is known valid.
	p := &jsonParser{src: src, dec: json.NewDecoder(bytes.NewReader(src))}
	p.dec.UseNumber()
	return p.value()
}

type jsonParser struct {
	src []byte
	dec *json.Decoder
}

// next returns the offset of the next token: the decoder offset with
// whitespace and the ',' / ':' separators skipped.
func (p *jsonParser) next() int {
	i := int(p.dec.InputOffset())
	for i < len(p.src) {
		switch p.src[i] {
		case ' ', '\t', '\n', '\r', ',', ':':
			i++
		default:
			return i
		}
	}
	return i
}

func (p *jsonParser) value() (*jsonNode, error) {
	start := p.next()
	tok, err := p.dec.Token()
	if err != nil {
		return nil, newJSONError(p.src, start, "invalid JSON (%v)", err)
	}
	n := &jsonNode{src: p.src, start: start}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n.kind = jsonObject
			seen := map[string]bool{}
			for p.dec.More() {
				keyStart := p.next()
				ktok, err := p.dec.Token()
				if err != nil {
					return nil, newJSONError(p.src, keyStart, "invalid JSON (%v)", err)
				}
				key, ok := ktok.(string)
				if !ok {
					return nil, newJSONError(p.src, keyStart, "invalid JSON (object key is not a string)")
				}
				keyEnd := int(p.dec.InputOffset())
				if seen[key] {
					return nil, newJSONError(p.src, keyStart, "duplicate key %q", key)
				}
				seen[key] = true
				child, err := p.value()
				if err != nil {
					return nil, err
				}
				n.kids = append(n.kids, &jsonKid{key: key, keyRaw: p.src[keyStart:keyEnd], val: child,
					orig: true, valStart: child.start, valEnd: child.end})
			}
		case '[':
			n.kind = jsonArray
			for p.dec.More() {
				child, err := p.value()
				if err != nil {
					return nil, err
				}
				n.kids = append(n.kids, &jsonKid{val: child, orig: true, valStart: child.start, valEnd: child.end})
			}
		default:
			return nil, newJSONError(p.src, start, "invalid JSON (unexpected %q)", t)
		}
		closeAt := p.next()
		if _, err := p.dec.Token(); err != nil {
			return nil, newJSONError(p.src, closeAt, "invalid JSON (%v)", err)
		}
		n.origKids = len(n.kids)
	case string:
		n.kind = jsonString
	case json.Number:
		n.kind = jsonNumber
	case bool:
		n.kind = jsonBool
	case nil:
		n.kind = jsonNull
	default:
		return nil, newJSONError(p.src, start, "invalid JSON (unexpected token %v)", tok)
	}
	n.end = int(p.dec.InputOffset())
	n.raw = p.src[n.start:n.end]
	return n, nil
}

func skipSpace(src []byte, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
		i++
	}
	return i
}

// detectIndent returns the indentation unit of src: the leading whitespace
// of its first indented line (a tab, or N spaces); two spaces when no line
// is indented.
func detectIndent(src []byte) string {
	for _, line := range bytes.Split(src, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		switch line[0] {
		case '\t':
			return "\t"
		case ' ':
			n := 0
			for n < len(line) && line[n] == ' ' {
				n++
			}
			return strings.Repeat(" ", n)
		}
	}
	return "  "
}

// ---- reading -----------------------------------------------------------------

// member returns the member key of an object node, or nil.
func (n *jsonNode) member(key string) *jsonKid {
	if n == nil || n.kind != jsonObject {
		return nil
	}
	for _, k := range n.kids {
		if k.key == key {
			return k
		}
	}
	return nil
}

// stringValue decodes a string node.
func (n *jsonNode) stringValue() (string, bool) {
	if n == nil || n.kind != jsonString {
		return "", false
	}
	var s string
	if err := json.Unmarshal(n.raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// pos is the offset to report for n (its start, or 0 for new nodes).
func (n *jsonNode) pos() int {
	if n == nil || n.isNew {
		return 0
	}
	return n.start
}

// canonicalJSON returns the canonical form of a JSON value: object keys
// sorted, no insignificant whitespace, strings unescaped where JSON allows,
// integral numbers in plain decimal (exactly: 5.0e0 → 5, big integers keep
// every digit), other numbers as spelled, no HTML escaping. Two values are
// treated as semantically equal iff their canonical forms are.
func canonicalJSON(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	return marshalNoEscape(normalizeNumbers(v))
}

func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		s := string(t)
		if i := strings.IndexAny(s, "eE"); i >= 0 {
			// Bound the exponent: big.Rat would materialize 10^exp.
			if exp, err := strconv.Atoi(strings.TrimPrefix(s[i+1:], "+")); err != nil || exp > 64 || exp < -64 {
				return t
			}
		}
		var r big.Rat
		if _, ok := r.SetString(s); ok && r.IsInt() {
			return json.Number(r.Num().String())
		}
		return t
	case map[string]any:
		for k, x := range t {
			t[k] = normalizeNumbers(x)
		}
	case []any:
		for i, x := range t {
			t[i] = normalizeNumbers(x)
		}
	}
	return v
}

func marshalNoEscape(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// ---- editing -----------------------------------------------------------------

// newJSONNode builds a new node from a Go value (struct field order is kept,
// so use structs where key order matters). New nodes are pretty-printed.
func newJSONNode(v any) (*jsonNode, error) {
	s, err := marshalNoEscape(v)
	if err != nil {
		return nil, err
	}
	n, err := parseJSON([]byte(s))
	if err != nil {
		return nil, err
	}
	markNew(n)
	return n, nil
}

func markNew(n *jsonNode) {
	n.isNew = true
	n.src = nil
	for _, k := range n.kids {
		k.orig = false
		markNew(k.val)
	}
}

func newEmptyObject() *jsonNode { return &jsonNode{kind: jsonObject, raw: []byte("{}"), isNew: true} }
func newEmptyArray() *jsonNode  { return &jsonNode{kind: jsonArray, raw: []byte("[]"), isNew: true} }

// appendKid appends a member (key != "" for objects) or element.
func (n *jsonNode) appendKid(key string, val *jsonNode) {
	k := &jsonKid{key: key, val: val}
	if n.kind == jsonObject {
		kr, _ := marshalNoEscape(key)
		k.keyRaw = []byte(kr)
	}
	n.kids = append(n.kids, k)
	if !n.isNew {
		n.changed = true
	}
}

// removeKid removes the i-th child.
func (n *jsonNode) removeKid(i int) {
	n.kids = append(n.kids[:i:i], n.kids[i+1:]...)
	if !n.isNew {
		n.changed = true
	}
}

// removeMember removes the member key, if present.
func (n *jsonNode) removeMember(key string) bool {
	for i, k := range n.kids {
		if k.key == key {
			n.removeKid(i)
			return true
		}
	}
	return false
}

// dirty reports whether n or anything inside it was edited.
func (n *jsonNode) dirty() bool {
	if n.isNew || n.changed {
		return true
	}
	for _, k := range n.kids {
		if k.val.dirty() {
			return true
		}
	}
	return false
}

// appendOnly reports whether a changed container only gained children at
// its end and spans several lines, so its original bytes can be kept.
func (n *jsonNode) appendOnly() bool {
	if n.isNew || n.origKids == 0 || len(n.kids) <= n.origKids || !bytes.ContainsRune(n.raw, '\n') {
		return false
	}
	for i, k := range n.kids {
		if (i < n.origKids) != k.orig {
			return false
		}
	}
	return true
}

// ---- writing -----------------------------------------------------------------

// changed reports whether the document differs from its source. A blank
// document changes only when something was added to its (implied) object.
func (d *jsonDoc) changed() bool {
	if d.blank {
		return len(d.root.kids) > 0
	}
	return d.root.dirty()
}

// bytes renders the document. An unchanged document is returned as its
// exact source. A changed one keeps everything around the root value and
// ends with a newline.
func (d *jsonDoc) bytes() []byte {
	if !d.changed() {
		return d.src
	}
	e := &jsonEmitter{unit: d.unit}
	if d.blank {
		e.emit(d.root, 0)
		e.buf.WriteByte('\n')
		return e.buf.Bytes()
	}
	e.buf.Write(d.src[:d.root.start])
	e.emit(d.root, 0)
	e.buf.Write(d.src[d.root.end:])
	out := e.buf.Bytes()
	if !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	return out
}

type jsonEmitter struct {
	buf  bytes.Buffer
	unit string
}

func (e *jsonEmitter) indent(depth int) {
	e.buf.WriteByte('\n')
	for range depth {
		e.buf.WriteString(e.unit)
	}
}

func (e *jsonEmitter) emit(n *jsonNode, depth int) {
	switch {
	case n.kind != jsonObject && n.kind != jsonArray:
		e.buf.Write(n.raw)
	case n.isNew:
		e.pretty(n, depth)
	case !n.dirty():
		e.buf.Write(n.raw)
	case !n.changed:
		pos := e.splice(n, depth, len(n.kids))
		e.buf.Write(n.src[pos:n.end])
	case n.appendOnly():
		pos := e.splice(n, depth, n.origKids)
		for _, k := range n.kids[n.origKids:] {
			e.buf.WriteByte(',')
			e.indent(depth + 1)
			e.kid(n, k, depth)
		}
		e.buf.Write(n.src[pos:n.end])
	default:
		e.pretty(n, depth)
	}
}

// splice copies n's source up to the end of its count-th child, re-emitting
// each child value, and returns the source offset reached.
func (e *jsonEmitter) splice(n *jsonNode, depth, count int) int {
	pos := n.start
	for _, k := range n.kids[:count] {
		e.buf.Write(n.src[pos:k.valStart])
		e.emit(k.val, depth+1)
		pos = k.valEnd
	}
	return pos
}

func (e *jsonEmitter) kid(parent *jsonNode, k *jsonKid, depth int) {
	if parent.kind == jsonObject {
		e.buf.Write(k.keyRaw)
		e.buf.WriteString(": ")
	}
	e.emit(k.val, depth+1)
}

func (e *jsonEmitter) pretty(n *jsonNode, depth int) {
	open, closer := byte('{'), byte('}')
	if n.kind == jsonArray {
		open, closer = '[', ']'
	}
	e.buf.WriteByte(open)
	if len(n.kids) == 0 {
		e.buf.WriteByte(closer)
		return
	}
	for i, k := range n.kids {
		if i > 0 {
			e.buf.WriteByte(',')
		}
		e.indent(depth + 1)
		e.kid(n, k, depth)
	}
	e.indent(depth)
	e.buf.WriteByte(closer)
}
