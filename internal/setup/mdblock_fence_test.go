package setup

import (
	"errors"
	"testing"
)

// CommonMark fences: same character, closing length >= opening, no info string
// on a closing fence; markers in a fence left open at EOF are refused.
func TestFindMDBlockFences(t *testing.T) {
	t.Parallel()
	const b, e = MDBeginMarker, MDEndMarker
	cases := []struct {
		name    string
		in      string
		found   bool
		wantErr bool
	}{
		{"long fence holds a shorter fence line", "````\n```\n" + b + "\n" + e + "\n```\n````\n", false, false},
		{"tilde fence is not closed by backticks", "~~~\n```\n" + b + "\n" + e + "\n~~~\n", false, false},
		{"closing fence with an info string does not close", "```\n```x\n" + b + "\n" + e + "\n```\n", false, false},
		{"block after a properly closed fence", "```\ncode\n```\n" + b + "\nx\n" + e + "\n", true, false},
		{"markers in a fence never closed are refused", "```\n" + b + "\nx\n" + e + "\n", false, true},
		{"unclosed fence without markers is not a refusal", "```\ncode\n", false, false},
	}
	for _, tc := range cases {
		blk, err := FindMDBlock([]byte(tc.in))
		if tc.wantErr {
			if !errors.Is(err, ErrMDMarkers) {
				t.Errorf("%s: err %v", tc.name, err)
			}
			if _, _, uerr := UpsertMDBlock([]byte(tc.in), "s\n"); !errors.Is(uerr, ErrMDMarkers) {
				t.Errorf("%s: upsert must refuse, got %v", tc.name, uerr)
			}
			continue
		}
		if err != nil || blk.Found != tc.found {
			t.Errorf("%s: found=%v err=%v", tc.name, blk.Found, err)
		}
	}
}
