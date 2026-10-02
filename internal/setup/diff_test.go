package setup

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func TestUnifiedDiffEqual(t *testing.T) {
	if got := UnifiedDiff("a", "b", []byte("x\ny\n"), []byte("x\ny\n")); got != "" {
		t.Errorf("equal inputs: %q", got)
	}
	if got := UnifiedDiff("a", "b", nil, nil); got != "" {
		t.Errorf("both empty: %q", got)
	}
}

func TestUnifiedDiffCases(t *testing.T) {
	lines := func(from, to int) string {
		var b strings.Builder
		for i := from; i <= to; i++ {
			fmt.Fprintf(&b, "l%d\n", i)
		}
		return b.String()
	}
	tests := []struct {
		name, old, new, want string
	}{
		{"new file", "", "a\nb\n", "--- /dev/null\n+++ f\n@@ -0,0 +1,2 @@\n+a\n+b\n"},
		{"delete file", "a\nb\n", "", "--- f\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b\n"},
		{"single change", "a\nb\nc\n", "a\nB\nc\n", "--- f\n+++ f\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"},
		{"three context lines", lines(1, 20), strings.Replace(lines(1, 20), "l10\n", "X\n", 1),
			"--- f\n+++ f\n@@ -7,7 +7,7 @@\n l7\n l8\n l9\n-l10\n+X\n l11\n l12\n l13\n"},
		{"two hunks", lines(1, 30), strings.Replace(strings.Replace(lines(1, 30), "l3\n", "A\n", 1), "l28\n", "B\n", 1),
			"--- f\n+++ f\n@@ -1,6 +1,6 @@\n l1\n l2\n-l3\n+A\n l4\n l5\n l6\n@@ -25,6 +25,6 @@\n l25\n l26\n l27\n-l28\n+B\n l29\n l30\n"},
		{"merged hunks", lines(1, 12), strings.Replace(strings.Replace(lines(1, 12), "l2\n", "A\n", 1), "l9\n", "B\n", 1),
			"--- f\n+++ f\n@@ -1,12 +1,12 @@\n l1\n-l2\n+A\n l3\n l4\n l5\n l6\n l7\n l8\n-l9\n+B\n l10\n l11\n l12\n"},
		{"no newline at end", "a\nb", "a\nb\n", "--- f\n+++ f\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+b\n"},
		{"append", "a\n", "a\nb\n", "--- f\n+++ f\n@@ -1 +1,2 @@\n a\n+b\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := "f"
			oldName, newName := name, name
			if tc.old == "" {
				oldName = ""
			}
			if tc.new == "" {
				newName = ""
			}
			got := UnifiedDiff(oldName, newName, []byte(tc.old), []byte(tc.new))
			if got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

// applyUnified applies a diff produced by UnifiedDiff to old.
func applyUnified(t *testing.T, old, diff string) string {
	t.Helper()
	src := splitDiffLines(old)
	var out []string
	pos := 0 // index into src
	dl := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	for i := 2; i < len(dl); { // skip ---/+++
		h := dl[i]
		if !strings.HasPrefix(h, "@@ ") {
			t.Fatalf("bad hunk header %q", h)
		}
		f := strings.Fields(h)
		start := strings.SplitN(strings.TrimPrefix(f[1], "-"), ",", 2)
		n, _ := strconv.Atoi(start[0])
		cnt := 1
		if len(start) == 2 {
			cnt, _ = strconv.Atoi(start[1])
		}
		if cnt == 0 {
			n++
		}
		for pos < n-1 {
			out = append(out, src[pos])
			pos++
		}
		i++
		for i < len(dl) && !strings.HasPrefix(dl[i], "@@ ") {
			l := dl[i]
			i++
			if strings.HasPrefix(l, "\\") {
				// the marker is read via noEOL on the previous line
				continue
			}
			text := l[1:]
			noEOL := i < len(dl) && strings.HasPrefix(dl[i], "\\")
			switch l[0] {
			case ' ':
				if src[pos] != text && src[pos] != text+noEOLMark {
					t.Fatalf("context mismatch %q vs %q", src[pos], text)
				}
				out = append(out, src[pos])
				pos++
			case '-':
				if src[pos] != text && src[pos] != text+noEOLMark {
					t.Fatalf("delete mismatch %q vs %q", src[pos], text)
				}
				pos++
			case '+':
				if noEOL {
					text += noEOLMark
				}
				out = append(out, text)
			}
		}
	}
	out = append(out, src[pos:]...)
	if len(out) == 0 {
		return ""
	}
	last := out[len(out)-1]
	if strings.HasSuffix(last, noEOLMark) {
		out[len(out)-1] = strings.TrimSuffix(last, noEOLMark)
		return strings.Join(out, "\n")
	}
	return strings.Join(out, "\n") + "\n"
}

func TestUnifiedDiffRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	gen := func() string {
		n := rng.Intn(25)
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "line%d\n", rng.Intn(6))
		}
		s := b.String()
		if s != "" && rng.Intn(4) == 0 {
			s = strings.TrimSuffix(s, "\n")
		}
		return s
	}
	for i := 0; i < 500; i++ {
		a, b := gen(), gen()
		d := UnifiedDiff("a", "b", []byte(a), []byte(b))
		if a == b {
			if d != "" {
				t.Fatalf("equal but diff %q", d)
			}
			continue
		}
		if got := applyUnified(t, a, d); got != b {
			t.Fatalf("round trip %d failed\nold=%q\nnew=%q\ndiff=%q\ngot=%q", i, a, b, d, got)
		}
	}
}

func TestUnifiedDiffLargeFallback(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 2500; i++ {
		fmt.Fprintf(&a, "a%d\n", i)
		fmt.Fprintf(&b, "b%d\n", i)
	}
	d := UnifiedDiff("a", "b", []byte(a.String()), []byte(b.String()))
	if !strings.Contains(d, "-a0\n") || !strings.Contains(d, "+b2499\n") {
		t.Errorf("fallback diff missing lines")
	}
	if got := applyUnified(t, a.String(), d); got != b.String() {
		t.Error("fallback round trip failed")
	}
}
