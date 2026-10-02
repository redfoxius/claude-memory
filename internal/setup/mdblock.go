package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// CLAUDE.md managed-block library (spec AC-42, plan WI-S1-8). The block is
// the text between two marker lines; everything outside them is never
// changed. Markers count only as whole lines (surrounding whitespace
// allowed). A file with unbalanced, duplicated or out-of-order markers is
// refused, never guessed at.

// Managed-block markers (spec §2 "Managed block").
const (
	MDBeginMarker = "<!-- BEGIN claude-memory -->"
	MDEndMarker   = "<!-- END claude-memory -->"
)

// ErrMDMarkers is wrapped by every refusal caused by the markers.
var ErrMDMarkers = errors.New("malformed claude-memory markers")

// ErrMDHandPasted is returned by UpsertMDBlock when the file holds a
// hand-pasted memory section without markers: installing the block would
// duplicate it (AC-42: delete it first).
var ErrMDHandPasted = errors.New("a hand-pasted claude-memory section (no markers) is present; delete it, then install the block")

// MDBlock locates the managed block in a file.
type MDBlock struct {
	Found bool
	// Start is the offset of the begin-marker line, End the offset just
	// past the end-marker line (including its newline, when it has one).
	Start, End int
	// Body is the text between the marker lines.
	Body string
	// HandPasted is set when no markers exist but a heading of the memory
	// section does (a manual paste of claude-md-section.md).
	HandPasted bool
	// HandPastedLine is the 1-based line of that heading.
	HandPastedLine int
}

type mdLine struct {
	start, end int // end includes the newline
	text       string
}

func mdLines(b []byte) []mdLine {
	var out []mdLine
	for i := 0; i < len(b); {
		j := bytes.IndexByte(b[i:], '\n')
		end := len(b)
		if j >= 0 {
			end = i + j + 1
		}
		out = append(out, mdLine{start: i, end: end, text: strings.TrimRight(string(b[i:end]), "\r\n")})
		i = end
	}
	return out
}

// isHandPastedHeading recognizes the heading of claude-md-section.md
// ("## Shared semantic memory (`claude-memory`)") at any level.
func isHandPastedHeading(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "#") && strings.Contains(strings.ToLower(t), "shared semantic memory") && strings.Contains(t, "claude-memory")
}

// mdFenceRe matches a Markdown code-fence line: a run of three or more
// backticks or tildes, optionally followed by an info string.
var mdFenceRe = regexp.MustCompile("^\\s*(`{3,}|~{3,})(.*)$")

// FindMDBlock locates the managed block. It returns an error wrapping
// ErrMDMarkers for unbalanced, duplicated or out-of-order markers, and for
// markers inside a code fence that is still open at the end of the file
// (inserting a second block there would be wrong).
func FindMDBlock(b []byte) (MDBlock, error) {
	var begins, ends []int
	lines := mdLines(b)
	// Fences follow CommonMark: a closing fence uses the same character, is at
	// least as long as the opening one and has no info string.
	var fenceCh byte
	fenceLen, fencedMarkers := 0, 0
	for i, l := range lines {
		if m := mdFenceRe.FindStringSubmatch(l.text); m != nil {
			ch, n := m[1][0], len(m[1])
			switch {
			case fenceLen == 0:
				if ch == '`' && strings.Contains(m[2], "`") {
					break // not a fence: backtick info strings cannot contain backticks
				}
				fenceCh, fenceLen = ch, n
				continue
			case ch == fenceCh && n >= fenceLen && strings.TrimSpace(m[2]) == "":
				fenceLen = 0
				continue
			}
		}
		t := strings.TrimSpace(l.text)
		if fenceLen > 0 {
			// Marker lines inside a fenced code block are documentation, not a block.
			if t == MDBeginMarker || t == MDEndMarker {
				fencedMarkers++
			}
			continue
		}
		switch t {
		case MDBeginMarker:
			begins = append(begins, i)
		case MDEndMarker:
			ends = append(ends, i)
		}
	}
	if fenceLen > 0 && fencedMarkers > 0 {
		return MDBlock{}, fmt.Errorf("%w: claude-memory markers inside a code fence that is never closed", ErrMDMarkers)
	}
	switch {
	case len(begins) == 0 && len(ends) == 0:
		var blk MDBlock
		for i, l := range lines {
			if isHandPastedHeading(l.text) {
				blk.HandPasted, blk.HandPastedLine = true, i+1
				break
			}
		}
		return blk, nil
	case len(begins) > 1 || len(ends) > 1:
		return MDBlock{}, fmt.Errorf("%w: %d begin and %d end markers (duplicated block?)", ErrMDMarkers, len(begins), len(ends))
	case len(begins) != len(ends):
		return MDBlock{}, fmt.Errorf("%w: unbalanced (%d begin, %d end marker)", ErrMDMarkers, len(begins), len(ends))
	case ends[0] < begins[0]:
		return MDBlock{}, fmt.Errorf("%w: end marker (line %d) before begin marker (line %d)", ErrMDMarkers, ends[0]+1, begins[0]+1)
	}
	bl, el := lines[begins[0]], lines[ends[0]]
	return MDBlock{Found: true, Start: bl.start, End: el.end, Body: string(b[bl.end:el.start])}, nil
}

// normalizeSection is the comparison form of a block body: LF line endings
// (a CRLF file holds the same block) and exactly one trailing newline.
func normalizeSection(section string) string {
	return strings.TrimRight(strings.ReplaceAll(section, "\r\n", "\n"), "\n") + "\n"
}

// MDSectionHash is the sha256 (hex) of a block body as UpsertMDBlock writes
// it for section: the form the manifest records for an md-block artifact.
func MDSectionHash(section string) string {
	sum := sha256.Sum256([]byte(normalizeSection(section)))
	return hex.EncodeToString(sum[:])
}

// MDBlockState reports the block's state in a file's content (exists =
// whether the file exists) against the section install would write and the
// body hash the manifest recorded ("" when none):
//
//   - absent: no file, or no block and no hand-pasted section;
//   - ok: the block body is the section;
//   - outdated: the body is what install recorded, and the section changed;
//   - modified: the body differs and is not the recorded one, or a
//     hand-pasted section without markers is present.
//
// Malformed markers return an error wrapping ErrMDMarkers.
func MDBlockState(b []byte, exists bool, section, recordedHash string) (State, string, error) {
	if !exists {
		return StateAbsent, "file does not exist", nil
	}
	blk, err := FindMDBlock(b)
	if err != nil {
		return "", "", err
	}
	switch {
	case blk.HandPasted:
		return StateModified, fmt.Sprintf("hand-pasted memory section without markers at line %d; delete it, then install the block", blk.HandPastedLine), nil
	case !blk.Found:
		return StateAbsent, "no claude-memory block", nil
	}
	body := normalizeSection(blk.Body)
	if body == normalizeSection(section) {
		return StateOK, "block up to date", nil
	}
	sum := sha256.Sum256([]byte(body))
	if recordedHash != "" && hex.EncodeToString(sum[:]) == recordedHash {
		return StateOutdated, "block written by an earlier install differs from this version", nil
	}
	return StateModified, "block differs from this version and from what install recorded (edited?)", nil
}

// newlineOf returns the newline style of b ("\r\n" when it uses CRLF).
func newlineOf(b []byte) string {
	if bytes.Contains(b, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

func withNewline(s, nl string) string {
	if nl == "\n" {
		return s
	}
	return strings.ReplaceAll(s, "\n", nl)
}

// UpsertMDBlock inserts the block at the end of b (after a blank line) or
// refreshes the body between existing markers. changed is false, and out
// is b, when the block already holds section. It refuses malformed
// markers (ErrMDMarkers) and a hand-pasted section (ErrMDHandPasted).
func UpsertMDBlock(b []byte, section string) ([]byte, bool, error) {
	blk, err := FindMDBlock(b)
	if err != nil {
		return nil, false, err
	}
	nl := newlineOf(b)
	body := withNewline(normalizeSection(section), nl)
	if blk.Found {
		if normalizeSection(blk.Body) == normalizeSection(section) {
			return b, false, nil
		}
		lines := mdLines(b[blk.Start:blk.End])
		beginEnd := blk.Start + lines[0].end
		endStart := blk.Start + lines[len(lines)-1].start
		var out bytes.Buffer
		out.Write(b[:beginEnd])
		out.WriteString(body)
		out.Write(b[endStart:])
		return out.Bytes(), true, nil
	}
	if blk.HandPasted {
		return nil, false, ErrMDHandPasted
	}
	var out bytes.Buffer
	out.Write(b)
	if len(b) > 0 {
		if !bytes.HasSuffix(b, []byte("\n")) {
			out.WriteString(nl)
		}
		if !bytes.HasSuffix(out.Bytes(), []byte(nl+nl)) {
			out.WriteString(nl)
		}
	}
	out.WriteString(MDBeginMarker + nl)
	out.WriteString(body)
	out.WriteString(MDEndMarker + nl)
	return out.Bytes(), true, nil
}

// RemoveMDBlock removes the block, markers included. When the block was at
// the end of the file, the blank line UpsertMDBlock put before it is
// removed too, so RemoveMDBlock(UpsertMDBlock(b)) == b for a b that ends
// with a newline. changed is false when there is no block.
func RemoveMDBlock(b []byte) ([]byte, bool, error) {
	blk, err := FindMDBlock(b)
	if err != nil {
		return nil, false, err
	}
	if !blk.Found {
		return b, false, nil
	}
	start := blk.Start
	if blk.End == len(b) {
		before := b[:start]
		nl := newlineOf(b)
		if bytes.HasSuffix(before, []byte(nl+nl)) {
			start -= len(nl)
		} else if bytes.Equal(before, []byte(nl)) {
			start = 0
		}
	}
	out := append(bytes.Clone(b[:start]), b[blk.End:]...)
	return out, true, nil
}
