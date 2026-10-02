package config

import (
	"bytes"
	"testing"
)

func TestEnvDocRoundTripIsByteIdentical(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]string{
		"empty":     "",
		"plain":     "A=1\nB=2\n",
		"comments":  "# top\n\nA=1 # not a comment\n  # indented\nB = 2\n",
		"crlf":      "A=1\r\nB=2\r\n",
		"mixed eol": "A=1\r\nB=2\nC=3\r\n",
		"unknown":   "no equals here\n1BAD=x\nexport A=1\nA='q'\n",
		"dups":      "A=1\nA=2\n",
	} {
		if got := ParseEnvDoc([]byte(in)).Marshal(); !bytes.Equal(got, []byte(in)) {
			t.Errorf("%s: Marshal = %q, want %q", name, got, in)
		}
	}
	// A missing final newline is added; a BOM is dropped.
	if got := string(ParseEnvDoc([]byte("A=1")).Marshal()); got != "A=1\n" {
		t.Errorf("no final newline: %q", got)
	}
	if got := string(ParseEnvDoc([]byte("\ufeffA=1\n")).Marshal()); got != "A=1\n" {
		t.Errorf("BOM: %q", got)
	}
	if v, ok := ParseEnvDoc([]byte("\ufeffA=1\n")).Get("A"); !ok || v != "1" {
		t.Errorf("BOM first key: %q %v", v, ok)
	}
}

func TestEnvDocSetInPlaceAndAppend(t *testing.T) {
	t.Parallel()
	d := ParseEnvDoc([]byte("# c\nA=1\nB=2\nA=3\n"))
	if ch, err := d.Set("A", "9"); err != nil || !ch {
		t.Fatalf("Set: %v %v", ch, err)
	}
	if ch, _ := d.Set("A", "9"); ch {
		t.Error("second Set must be a no-op")
	}
	if ch, _ := d.Set("C", "x y"); !ch {
		t.Error("append must change")
	}
	if got, want := string(d.Marshal()), "# c\nA=9\nB=2\nA=3\nC=x y\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if _, err := d.Set("bad key", "x"); err == nil {
		t.Error("invalid key accepted")
	}
	if _, err := d.Set("K", "a\nb"); err == nil {
		t.Error("newline accepted")
	}
}

func TestEnvDocAppendMatchesCRLFAndMissingNewline(t *testing.T) {
	t.Parallel()
	d := ParseEnvDoc([]byte("A=1\r\nB=2"))
	_, _ = d.Set("C", "3")
	if got, want := string(d.Marshal()), "A=1\r\nB=2\r\nC=3\r\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestEnvDocEntryForms(t *testing.T) {
	t.Parallel()
	d := ParseEnvDoc([]byte("export A=1\nB=\"two\"\nC='pa$word'\nD=$(x)\nE=plain\n"))
	cases := []struct {
		key                                       string
		value                                     string
		export, quoted, unparseable, plain, convt bool
	}{
		{"A", "1", true, false, false, true, true},
		{"B", "two", false, true, false, true, true},
		{"C", "pa$word", false, true, false, false, false},
		{"D", "$(x)", false, false, true, false, false},
		{"E", "plain", false, false, false, true, false},
	}
	for _, c := range cases {
		e, ok := d.Entry(c.key)
		if !ok || e.Value != c.value || e.Export != c.export || e.Quoted != c.quoted ||
			e.Unparseable != c.unparseable || e.Plain != c.plain || e.Convertible() != c.convt {
			t.Errorf("%s: %+v ok=%v", c.key, e, ok)
		}
	}
	if _, ok := d.Get("D"); ok {
		t.Error("Get of an unparseable line must report false")
	}
	if _, ok := d.Get("MISSING"); ok {
		t.Error("Get of a missing key")
	}
}

func TestEnvDocPlainLineBeatsExportLine(t *testing.T) {
	t.Parallel()
	d := ParseEnvDoc([]byte("export A=1\nA=2\n"))
	if v, _ := d.Get("A"); v != "2" {
		t.Errorf("Get = %q, the loader uses the plain line", v)
	}
}

func TestEnvDocNormalize(t *testing.T) {
	t.Parallel()
	d := ParseEnvDoc([]byte("export A=1\nB=\"two\"\nC='pa$word'\nD=$(x)\n# keep\n"))
	got := d.NormalizeAll()
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("NormalizeAll = %v", got)
	}
	if want := "A=1\nB=two\nC='pa$word'\nD=$(x)\n# keep\n"; string(d.Marshal()) != want {
		t.Errorf("got %q want %q", d.Marshal(), want)
	}
	if d.Normalize("A") {
		t.Error("Normalize of a plain line must be a no-op")
	}
	// What is left: C (quoted, but not convertible losslessly) and D.
	kinds := map[FindingKind]int{}
	for _, f := range d.Findings() {
		kinds[f.Kind]++
	}
	if kinds[FindingUnparseableValue] != 1 || kinds[FindingQuotedWholeValue] != 1 || len(kinds) != 2 {
		t.Errorf("findings %v", kinds)
	}
}

func TestEnvDocRemove(t *testing.T) {
	t.Parallel()
	d := ParseEnvDoc([]byte("A=1\n# A=2\nB=2\nexport A=3\n"))
	if !d.Remove("A") || d.Remove("A") {
		t.Fatal("Remove result")
	}
	if want := "# A=2\nB=2\n"; string(d.Marshal()) != want {
		t.Errorf("got %q want %q", d.Marshal(), want)
	}
}

func TestEnvDocFindingsMatchParseEnvData(t *testing.T) {
	t.Parallel()
	in := "export A=1\nB=\"x\"\nC=$(y)\nD\nB=2\n"
	got := ParseEnvDoc([]byte(in)).Findings()
	want := ParseEnvData([]byte(in), 0o600).Findings
	if len(got) != len(want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("finding %d: %+v vs %+v", i, got[i], want[i])
		}
	}
}
