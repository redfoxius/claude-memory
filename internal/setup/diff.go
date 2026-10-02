package setup

import (
	"fmt"
	"strings"
)

// Minimal unified diff (WI-S2-1b, AC-13): LCS over lines, 3 lines of
// context, no dependency. Env file, settings, skills, CLAUDE.md and job
// units all render their planned changes through it.

const diffContext = 3

// diffMaxCells bounds the LCS table; beyond it the changed middle part is
// shown as a whole replace instead of a minimal diff (install files are
// small, so this is a safety valve, not a normal path).
const diffMaxCells = 4_000_000

type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
}

// UnifiedDiff returns the unified diff turning old into new, or "" when they
// are equal. oldName/newName fill the "---"/"+++" header; an empty side
// (nil/empty bytes) is shown as /dev/null when the matching name is "".
func UnifiedDiff(oldName, newName string, oldB, newB []byte) string {
	if string(oldB) == string(newB) {
		return ""
	}
	a := splitDiffLines(string(oldB))
	b := splitDiffLines(string(newB))
	ops := diffOps(a, b)

	var sb strings.Builder
	if oldName == "" {
		oldName = "/dev/null"
	}
	if newName == "" {
		newName = "/dev/null"
	}
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", oldName, newName)
	writeHunks(&sb, ops)
	return sb.String()
}

// noEOLMark tags a last line that had no trailing newline, so that "x" and
// "x\n" at end of file compare as different lines.
const noEOLMark = "\x00noeol"

// splitDiffLines splits s into lines without their "\n"; "" has no lines.
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	noEOL := !strings.HasSuffix(s, "\n")
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if noEOL {
		lines[len(lines)-1] += noEOLMark
	}
	return lines
}

func diffOps(a, b []string) []diffOp {
	// Common prefix and suffix are trimmed first; they are context.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	am, bm := a[pre:len(a)-suf], b[pre:len(b)-suf]

	var mid []diffOp
	if len(am)*len(bm) > diffMaxCells {
		for _, l := range am {
			mid = append(mid, diffOp{kind: '-', text: l})
		}
		for _, l := range bm {
			mid = append(mid, diffOp{kind: '+', text: l})
		}
	} else {
		mid = lcsOps(am, bm)
	}

	ops := make([]diffOp, 0, len(a)+len(b))
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{kind: ' ', text: l})
	}
	ops = append(ops, mid...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{kind: ' ', text: l})
	}
	return ops
}

// lcsOps is the classic O(n*m) LCS table over the changed middle part.
func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		ops := make([]diffOp, 0, n+m)
		for _, l := range a {
			ops = append(ops, diffOp{kind: '-', text: l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{kind: '+', text: l})
		}
		return ops
	}
	t := make([][]int32, n+1)
	for i := range t {
		t[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				t[i][j] = t[i+1][j+1] + 1
			case t[i+1][j] >= t[i][j+1]:
				t[i][j] = t[i+1][j]
			default:
				t[i][j] = t[i][j+1]
			}
		}
	}
	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{kind: ' ', text: a[i]})
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			ops = append(ops, diffOp{kind: '-', text: a[i]})
			i++
		default:
			ops = append(ops, diffOp{kind: '+', text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: '-', text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: '+', text: b[j]})
	}
	return ops
}

func writeHunks(sb *strings.Builder, ops []diffOp) {
	// Find change indexes, then group them into hunks with diffContext
	// lines around; two changes closer than 2*context share a hunk.
	var changes []int
	for i, o := range ops {
		if o.kind != ' ' {
			changes = append(changes, i)
		}
	}
	oldLine, newLine := make([]int, len(ops)+1), make([]int, len(ops)+1)
	for i, o := range ops { // 1-based line numbers before op i
		oldLine[i+1], newLine[i+1] = oldLine[i], newLine[i]
		if o.kind != '+' {
			oldLine[i+1]++
		}
		if o.kind != '-' {
			newLine[i+1]++
		}
	}
	for c := 0; c < len(changes); {
		start := max(changes[c]-diffContext, 0)
		end := changes[c]
		for c < len(changes) && changes[c] <= end+2*diffContext+1 {
			end = changes[c]
			c++
		}
		end = min(end+diffContext, len(ops)-1)

		oldStart, newStart := oldLine[start]+1, newLine[start]+1
		oldCount := oldLine[end+1] - oldLine[start]
		newCount := newLine[end+1] - newLine[start]
		if oldCount == 0 {
			oldStart--
		}
		if newCount == 0 {
			newStart--
		}
		fmt.Fprintf(sb, "@@ -%s +%s @@\n", hunkRange(oldStart, oldCount), hunkRange(newStart, newCount))
		for _, o := range ops[start : end+1] {
			text, noEOL := strings.CutSuffix(o.text, noEOLMark)
			sb.WriteByte(o.kind)
			sb.WriteString(text)
			sb.WriteByte('\n')
			if noEOL {
				sb.WriteString("\\ No newline at end of file\n")
			}
		}
	}
}

func hunkRange(start, count int) string {
	if count == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}
