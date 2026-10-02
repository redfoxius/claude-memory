package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// testSection stands in for integration/claude-md-section.md, so the
// goldens do not churn when the real wording changes.
const testSection = "## Shared semantic memory (`claude-memory`)\n\nUse memory_search before answering.\n"

const oldSection = "## Shared semantic memory (`claude-memory`)\n\nOld wording.\n"

func TestMDBlockGolden(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		missing  bool   // the file does not exist
		recorded string // manifest hash
		state    State
		changed  bool
		// roundTrip: RemoveMDBlock(Upsert(in)) == in.
		roundTrip bool
	}{
		{name: "missing", missing: true, state: StateAbsent, changed: true},
		{name: "insert-at-end", state: StateAbsent, changed: true, roundTrip: true},
		{name: "insert-no-trailing-newline", state: StateAbsent, changed: true},
		{name: "refresh", recorded: MDSectionHash(oldSection), state: StateOutdated, changed: true},
		{name: "edited", state: StateModified, changed: true},
		{name: "idempotent", state: StateOK},
		{name: "crlf", state: StateAbsent, changed: true, roundTrip: true},
		{name: "inline-marker", state: StateAbsent, changed: true, roundTrip: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var in []byte
			if !tc.missing {
				in = readTestdata(t, filepath.Join("testdata", "mdblock", tc.name+".in.md"))
			}
			st, detail, err := MDBlockState(in, !tc.missing, testSection, tc.recorded)
			if err != nil || st != tc.state {
				t.Errorf("state = %s (%s), %v; want %s", st, detail, err, tc.state)
			}
			out, changed, err := UpsertMDBlock(in, testSection)
			if err != nil {
				t.Fatal(err)
			}
			if changed != tc.changed {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
			if !changed && !bytes.Equal(out, in) {
				t.Error("no-op upsert altered the bytes")
			}
			checkGolden(t, filepath.Join("testdata", "mdblock", tc.name+".upsert.golden.md"), out)

			// Idempotent: the second upsert is a no-op and the state is ok.
			out2, changed2, err := UpsertMDBlock(out, testSection)
			if err != nil || changed2 || !bytes.Equal(out2, out) {
				t.Errorf("second upsert: changed=%v err=%v", changed2, err)
			}
			if st, _, _ := MDBlockState(out, true, testSection, MDSectionHash(testSection)); st != StateOK {
				t.Errorf("state after upsert = %s", st)
			}
			// Text outside the markers is unchanged.
			blk, err := FindMDBlock(out)
			if err != nil || !blk.Found {
				t.Fatalf("block not found after upsert: %v", err)
			}
			if tc.name == "refresh" || tc.name == "edited" {
				if !bytes.Equal(out[:blk.Start], in[:blk.Start]) || !bytes.HasSuffix(in, out[blk.End:]) {
					t.Error("text outside the markers changed")
				}
			}

			rm, changedRm, err := RemoveMDBlock(out)
			if err != nil || !changedRm {
				t.Fatalf("remove: changed=%v err=%v", changedRm, err)
			}
			checkGolden(t, filepath.Join("testdata", "mdblock", tc.name+".remove.golden.md"), rm)
			if tc.roundTrip && !bytes.Equal(rm, in) {
				t.Errorf("remove(upsert(in)) != in:\n%q\n%q", rm, in)
			}
			if _, again, _ := RemoveMDBlock(rm); again {
				t.Error("second remove reports a change")
			}
		})
	}
}

func TestMDBlockRefusals(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"unbalanced", "duplicated", "out-of-order"} {
		in := readTestdata(t, filepath.Join("testdata", "mdblock", name+".in.md"))
		if _, _, err := MDBlockState(in, true, testSection, ""); !errors.Is(err, ErrMDMarkers) {
			t.Errorf("%s: state err = %v, want ErrMDMarkers", name, err)
		}
		if _, _, err := UpsertMDBlock(in, testSection); !errors.Is(err, ErrMDMarkers) {
			t.Errorf("%s: upsert err = %v, want ErrMDMarkers", name, err)
		}
		if _, _, err := RemoveMDBlock(in); !errors.Is(err, ErrMDMarkers) {
			t.Errorf("%s: remove err = %v, want ErrMDMarkers", name, err)
		}
	}

	in := readTestdata(t, filepath.Join("testdata", "mdblock", "hand-pasted.in.md"))
	st, detail, err := MDBlockState(in, true, testSection, "")
	if err != nil || st != StateModified {
		t.Errorf("hand-pasted state = %s (%s), %v; want modified", st, detail, err)
	}
	if _, _, err := UpsertMDBlock(in, testSection); !errors.Is(err, ErrMDHandPasted) {
		t.Errorf("hand-pasted upsert err = %v, want ErrMDHandPasted", err)
	}
	if blk, _ := FindMDBlock(in); !blk.HandPasted || blk.HandPastedLine != 3 {
		t.Errorf("FindMDBlock = %+v, want hand-pasted at line 3", blk)
	}
}

// AC-42: a file inside a git repository is detected by walking up for .git
// (a directory, or a file in worktrees and submodules), via the FS port.
func TestInGitRepo(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	fsys := NewFakeFS(t, root)
	mk := func(p string, file bool) {
		t.Helper()
		if file {
			writeTestFile(t, p, []byte("gitdir: /elsewhere/.git/worktrees/x\n"), 0o644)
			return
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(root, "clone", ".git"), false)
	mk(filepath.Join(root, "clone", "sub", "deeper"), false)
	mk(filepath.Join(root, "worktree", ".git"), true)
	mk(filepath.Join(root, "plain", "dir"), false)

	cases := []struct {
		path string
		want bool
		repo string
	}{
		{filepath.Join(root, "clone", "CLAUDE.md"), true, filepath.Join(root, "clone")},
		{filepath.Join(root, "clone", "sub", "deeper", "CLAUDE.md"), true, filepath.Join(root, "clone")},
		{filepath.Join(root, "worktree", "CLAUDE.md"), true, filepath.Join(root, "worktree")},
		{filepath.Join(root, "plain", "dir", "CLAUDE.md"), false, ""},
		{filepath.Join(root, "missing", "dir", "CLAUDE.md"), false, ""}, // need not exist
	}
	for _, tc := range cases {
		got, repo, err := InGitRepo(fsys, tc.path, root)
		if err != nil || got != tc.want || repo != tc.repo {
			t.Errorf("InGitRepo(%s) = %v, %q, %v; want %v, %q", tc.path, got, repo, err, tc.want, tc.repo)
		}
	}
	if len(fsys.Writes()) != 0 {
		t.Errorf("writes = %v", fsys.Writes())
	}
}
