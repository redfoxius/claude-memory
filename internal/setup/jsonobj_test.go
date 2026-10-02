package setup

import (
	"errors"
	"strings"
	"testing"
)

func TestDetectIndent(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"{}":                       "  ",
		"{\"a\":1}\n":              "  ",
		"{\n\t\"a\": 1\n}":         "\t",
		"{\n    \"a\": 1\n}":       "    ",
		"{\n  \"a\": 1\n}":         "  ",
		"{\r\n   \"a\": 1\r\n}":    "   ",
		"\n\n{\n\n\t\t\"a\": 1\n}": "\t",
	}
	for in, want := range cases {
		if got := detectIndent([]byte(in)); got != want {
			t.Errorf("detectIndent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseJSONErrorsCarryPosition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, reason string
		line, col  int
	}{
		{"{\n  \"a\": {\"b\": 1, \"b\": 2}\n}", `duplicate key "b"`, 2, 17},
		{"{\n  \"a\": 1,\n}", "invalid character '}'", 3, 1},
		{"{\"a\": [1, 2", "truncated", 1, 12},
		{"{} []", "unexpected data", 1, 4},
		{"{\"a\": 01}", "invalid character '1'", 1, 8},
	}
	for _, tc := range cases {
		_, err := parseJSON([]byte(tc.in))
		var je *jsonError
		if !errors.As(err, &je) {
			t.Errorf("%q: err = %v, want *jsonError", tc.in, err)
			continue
		}
		if !strings.Contains(je.Reason, tc.reason) || je.Line != tc.line || je.Col != tc.col {
			t.Errorf("%q: %q at %d:%d, want %q at %d:%d", tc.in, je.Reason, je.Line, je.Col, tc.reason, tc.line, tc.col)
		}
	}
}

func TestParseJSONKeepsRawBytes(t *testing.T) {
	t.Parallel()
	src := "{ \"n\" : 12345678901234567890 , \"s\":\"caf\\u00e9 \\/\", \"a\" :[ 1.0 ,true,null ] }"
	d, err := parseJSONDoc([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"n": "12345678901234567890", "s": "\"caf\\u00e9 \\/\"", "a": "[ 1.0 ,true,null ]"}
	for k, w := range want {
		if got := string(d.root.member(k).val.raw); got != w {
			t.Errorf("%s raw = %q, want %q", k, got, w)
		}
	}
	if s, _ := d.root.member("s").val.stringValue(); s != "café /" {
		t.Errorf("decoded string = %q", s)
	}
	if d.changed() || string(d.bytes()) != src {
		t.Error("an untouched document must render as its source")
	}
}

func TestJSONEditSplicesAndAppends(t *testing.T) {
	t.Parallel()
	// Odd but valid spacing outside the edited value survives a replacement.
	src := "{ \"keep\" :  [1,2] ,\n  \"edit\":   {\"x\": 1}  }\n"
	d, err := parseJSONDoc([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	n, err := newJSONNode(map[string]int{"y": 2})
	if err != nil {
		t.Fatal(err)
	}
	d.root.member("edit").val = n
	want := "{ \"keep\" :  [1,2] ,\n  \"edit\":   {\n    \"y\": 2\n  }  }\n"
	if got := string(d.bytes()); got != want {
		t.Errorf("replace:\n%q\nwant\n%q", got, want)
	}

	// Appending to a multi-line array keeps the existing elements' bytes.
	src2 := "[\n  {\"a\":1},\n  2\n]"
	d2, err := parseJSONDoc([]byte(src2))
	if err != nil {
		t.Fatal(err)
	}
	three, _ := newJSONNode(3)
	d2.root.appendKid("", three)
	if got, want := string(d2.bytes()), "[\n  {\"a\":1},\n  2,\n  3\n]\n"; got != want {
		t.Errorf("append:\n%q\nwant\n%q", got, want)
	}

	// Removal re-encodes the container with the detected unit.
	d3, _ := parseJSONDoc([]byte("{\n\t\"a\": 1,\n\t\"b\": [ 1, 2 ]\n}\n"))
	d3.root.removeMember("a")
	if got, want := string(d3.bytes()), "{\n\t\"b\": [ 1, 2 ]\n}\n"; got != want {
		t.Errorf("remove:\n%q\nwant\n%q", got, want)
	}
}

func TestCanonicalJSON(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`{"b": 1, "a": "x"}`:                           `{"a":"x","b":1}`,
		`{"timeout": 5.0e0}`:                           `{"timeout":5}`,
		`{"t": 5.00}`:                                  `{"t":5}`,
		`{"t": 1.5}`:                                   `{"t":1.5}`,
		`{"t": 12345678901234567890}`:                  `{"t":12345678901234567890}`,
		`{"t": 1e400}`:                                 `{"t":1e400}`,
		`{"c": "\/a\u002fb \u003c&>"}`:                 `{"c":"/a/b <&>"}`,
		`[ {"z":null}, true ]`:                         `[{"z":null},true]`,
		`{"type":"command","command":"x","timeout":5}`: `{"command":"x","timeout":5,"type":"command"}`,
	}
	for in, want := range cases {
		got, err := canonicalJSON([]byte(in))
		if err != nil || got != want {
			t.Errorf("canonicalJSON(%s) = %s, %v; want %s", in, got, err, want)
		}
	}
}
