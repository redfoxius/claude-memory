package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testScriptsDir = "/Users/owner/.claude/hooks/claude-memory"

func testDesired() []HookEntry { return DesiredHooks(testScriptsDir) }

func recordedFor(entries []HookEntry) RecordedHooks {
	r := RecordedHooks{}
	for _, e := range entries {
		r[e.Event] = e.Canonical()
	}
	return r
}

var (
	desiredRecorded = recordedFor(testDesired())
	oldRecorded     = recordedFor(DesiredHooks("/opt/old/hooks/claude-memory"))
)

type settingsCase struct {
	name      string
	in        string // input base name under testdata/settings ("" = missing file)
	recorded  RecordedHooks
	overwrite bool
	state     State // AnalyzeSettings state of the input
	changed   bool  // MergeSettings changed
	// roundTrip: UnmergeSettings(merge output) must give back the input
	// byte-for-byte (AC-54: after-uninstall equals pre-install).
	roundTrip bool
	check     func(t *testing.T, a SettingsAnalysis, sum MergeSummary)
}

var settingsCases = []settingsCase{
	{name: "missing", state: StateAbsent, changed: true,
		check: func(t *testing.T, _ SettingsAnalysis, s MergeSummary) {
			if !s.CreatedHooksKey || s.Added != 2 {
				t.Errorf("summary %+v, want 2 added and CreatedHooksKey", s)
			}
		}},
	{name: "empty-file", in: "empty-file", state: StateAbsent, changed: true},
	{name: "empty-object", in: "empty-object", state: StateAbsent, changed: true, roundTrip: true},
	{name: "no-hooks-key", in: "no-hooks-key", state: StateAbsent, changed: true, roundTrip: true},
	{name: "other-events", in: "other-events", state: StateAbsent, changed: true, roundTrip: true,
		check: func(t *testing.T, _ SettingsAnalysis, s MergeSummary) {
			if s.CreatedHooksKey {
				t.Error("CreatedHooksKey set although hooks existed")
			}
		}},
	{name: "other-hooks-same-event", in: "other-hooks-same-event", state: StateAbsent, changed: true, roundTrip: true,
		check: func(t *testing.T, a SettingsAnalysis, _ MergeSummary) {
			if a.Events[0].Others != 1 {
				t.Errorf("UserPromptSubmit others = %d, want 1", a.Events[0].Others)
			}
		}},
	{name: "ours-identical", in: "ours-identical", state: StateOK},
	{name: "ours-recorded-outdated", in: "ours-recorded-outdated", recorded: oldRecorded, state: StateOutdated, changed: true,
		check: func(t *testing.T, _ SettingsAnalysis, s MergeSummary) {
			if s.Replaced != 2 || s.Added != 0 {
				t.Errorf("summary %+v, want 2 replaced", s)
			}
		}},
	// The same entries without a manifest record are not ours to change.
	{name: "ours-unrecorded-other-path", in: "ours-recorded-outdated", state: StateModified},
	{name: "ours-legacy-home", in: "ours-legacy-home", state: StateModified,
		check: func(t *testing.T, a SettingsAnalysis, s MergeSummary) {
			if !a.Events[0].Ours[0].Legacy || len(s.Drift) != 2 {
				t.Errorf("legacy=%v drift=%v, want legacy and 2 drift lines", a.Events[0].Ours[0].Legacy, s.Drift)
			}
			if !strings.Contains(a.Events[0].Detail, "legacy") {
				t.Errorf("detail %q does not name the legacy form", a.Events[0].Detail)
			}
		}},
	{name: "ours-legacy-home-overwrite", in: "ours-legacy-home", overwrite: true, state: StateModified, changed: true},
	// A user-raised timeout is kept under --yes (no overwrite), even though
	// the manifest recorded the entry.
	{name: "ours-user-timeout", in: "ours-user-timeout", recorded: desiredRecorded, state: StateModified,
		check: func(t *testing.T, _ SettingsAnalysis, s MergeSummary) {
			if len(s.Drift) != 1 || !strings.HasPrefix(s.Drift[0], EventUserPromptSubmit+": ") {
				t.Errorf("drift %v, want one UserPromptSubmit line", s.Drift)
			}
		}},
	{name: "ours-user-timeout-overwrite", in: "ours-user-timeout", recorded: desiredRecorded, overwrite: true,
		state: StateModified, changed: true,
		check: func(t *testing.T, _ SettingsAnalysis, s MergeSummary) {
			if s.Replaced != 1 {
				t.Errorf("summary %+v, want 1 replaced", s)
			}
		}},
	{name: "ours-duplicated", in: "ours-duplicated", state: StateModified,
		check: func(t *testing.T, a SettingsAnalysis, _ MergeSummary) {
			if len(a.Events[0].Ours) != 2 || !strings.Contains(a.Events[0].Detail, "duplicates") {
				t.Errorf("UserPromptSubmit %+v, want 2 duplicates", a.Events[0])
			}
		}},
	{name: "ours-duplicated-overwrite", in: "ours-duplicated", overwrite: true, state: StateModified, changed: true,
		check: func(t *testing.T, _ SettingsAnalysis, s MergeSummary) {
			if s.Deduped != 1 || s.Replaced != 0 {
				t.Errorf("summary %+v, want 1 deduped, 0 replaced (the desired one is kept)", s)
			}
		}},
	{name: "ours-under-matcher", in: "ours-under-matcher", recorded: oldRecorded, state: StateOutdated, changed: true,
		check: func(t *testing.T, a SettingsAnalysis, s MergeSummary) {
			if !a.Events[0].Ours[0].GroupKeys || s.Replaced != 1 || s.Added != 1 {
				t.Errorf("analysis %+v summary %+v", a.Events[0], s)
			}
		}},
	{name: "unknown-top-level-keys-order", in: "unknown-top-level-keys-order", state: StateAbsent, changed: true, roundTrip: true},
	{name: "tab-indented", in: "tab-indented", state: StateAbsent, changed: true, roundTrip: true},
	{name: "tab-indented-noop", in: "tab-indented-noop", state: StateOK},
	{name: "four-space", in: "four-space", state: StateAbsent, changed: true, roundTrip: true},
	{name: "unicode-escapes-unchanged-noop", in: "unicode-escapes-unchanged-noop", state: StateOK},
	{name: "unicode-escapes-merge", in: "unicode-escapes-merge", state: StateAbsent, changed: true, roundTrip: true},
	{name: "big-number-unchanged", in: "big-number-unchanged", state: StateAbsent, changed: true, roundTrip: true},
	{name: "crlf", in: "crlf", state: StateAbsent, changed: true, roundTrip: true},
	{name: "no-trailing-newline", in: "no-trailing-newline", state: StateAbsent, changed: true},
}

func TestSettingsGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range settingsCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var in []byte
			if tc.in != "" {
				in = readTestdata(t, filepath.Join("testdata", "settings", tc.in+".in.json"))
				if in == nil {
					in = []byte{}
				}
			}
			desired := testDesired()

			a, err := AnalyzeSettings(in, desired, tc.recorded)
			if err != nil {
				t.Fatalf("AnalyzeSettings: %v", err)
			}
			if a.State != tc.state {
				t.Errorf("state = %s (%s), want %s", a.State, a.Detail, tc.state)
			}

			out, sum, changed, err := MergeSettings(in, desired, tc.recorded, tc.overwrite)
			if err != nil {
				t.Fatalf("MergeSettings: %v", err)
			}
			if changed != tc.changed {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
			if !changed && !bytes.Equal(out, in) {
				t.Errorf("no-op merge altered the bytes:\n%s", out)
			}
			if !json.Valid(out) {
				t.Fatalf("merge output is not valid JSON:\n%s", out)
			}
			if len(bytes.TrimSpace(in)) > 0 {
				assertSameOutsideHooks(t, in, out)
			}
			if tc.name == "crlf" {
				assertAllCRLF(t, "merge", out)
			}
			checkGolden(t, filepath.Join("testdata", "settings", tc.name+".merge.golden.json"), out)
			if tc.check != nil {
				tc.check(t, a, sum)
			}

			// Idempotence (AC-64): a second merge changes nothing.
			out2, _, changed2, err := MergeSettings(out, desired, tc.recorded, tc.overwrite)
			if err != nil || changed2 || !bytes.Equal(out2, out) {
				t.Errorf("second merge: changed=%v err=%v", changed2, err)
			}
			a2, err := AnalyzeSettings(out, desired, tc.recorded)
			if err != nil {
				t.Fatal(err)
			}
			if wantAfter := afterMergeState(sum); a2.State != wantAfter {
				t.Errorf("state after merge = %s (%s), want %s", a2.State, a2.Detail, wantAfter)
			}

			// Uninstall: remove what install recorded writing.
			un, _, _, err := UnmergeSettings(out, desiredRecorded, sum.CreatedHooksKey)
			if err != nil {
				t.Fatalf("UnmergeSettings: %v", err)
			}
			if !json.Valid(un) {
				t.Fatalf("unmerge output is not valid JSON:\n%s", un)
			}
			if tc.name == "crlf" {
				assertAllCRLF(t, "unmerge", un)
			}
			checkGolden(t, filepath.Join("testdata", "settings", tc.name+".unmerge.golden.json"), un)
			if tc.roundTrip && !bytes.Equal(un, in) {
				t.Errorf("unmerge(merge(in)) != in:\n--- got ---\n%s\n--- in ---\n%s", un, in)
			}
			un2, _, changedUn2, err := UnmergeSettings(un, desiredRecorded, sum.CreatedHooksKey)
			if err != nil || changedUn2 || !bytes.Equal(un2, un) {
				t.Errorf("second unmerge: changed=%v err=%v", changedUn2, err)
			}
		})
	}
}

// assertAllCRLF fails when b holds a bare LF: a CRLF file must stay CRLF
// (AC-37).
func assertAllCRLF(t *testing.T, what string, b []byte) {
	t.Helper()
	if bytes.Count(b, []byte("\n")) != bytes.Count(b, []byte("\r\n")) {
		t.Errorf("%s output of a CRLF file has a bare LF:\n%q", what, b)
	}
}

func afterMergeState(s MergeSummary) State {
	if len(s.Drift) > 0 {
		return StateModified
	}
	return StateOK
}

// assertSameOutsideHooks checks that every top-level key other than
// "hooks" is semantically unchanged, in the same order.
func assertSameOutsideHooks(t *testing.T, in, out []byte) {
	t.Helper()
	decode := func(b []byte) (map[string]any, []string) {
		d1, err := parseJSONDoc(b)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, k := range d1.root.kids {
			if k.key != "hooks" {
				keys = append(keys, k.key)
			}
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		delete(m, "hooks")
		return m, keys
	}
	mi, ki := decode(in)
	mo, ko := decode(out)
	if !reflect.DeepEqual(mi, mo) || !reflect.DeepEqual(ki, ko) {
		t.Errorf("content outside hooks changed:\nin  %v %v\nout %v %v", ki, mi, ko, mo)
	}
}

func TestSettingsRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, reason string
		line         int
	}{
		{"refuse-comments", "invalid character '/'", 2},
		{"refuse-trailing-comma", "invalid character '}'", 5},
		{"refuse-hooks-is-array", `"hooks" is an array, not an object`, 2},
		{"refuse-truncated", "truncated", 4},
		{"refuse-duplicate-key", `duplicate key "Stop"`, 4},
		{"refuse-event-not-array", `"hooks.UserPromptSubmit" is an object, not an array`, 3},
		{"refuse-top-level-array", "top-level value is an array", 1},
		{"refuse-trailing-data", "unexpected data after the top-level value", 2},
		{"refuse-group-not-object", `"hooks.SessionEnd[0]" is a string, not an object`, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := readTestdata(t, filepath.Join("testdata", "settings", tc.name+".in.json"))
			for _, run := range []func() error{
				func() error { _, err := AnalyzeSettings(in, testDesired(), nil); return err },
				func() error { _, _, _, err := MergeSettings(in, testDesired(), nil, true); return err },
				func() error { _, _, _, err := UnmergeSettings(in, desiredRecorded, true); return err },
			} {
				err := run()
				var r *SettingsRefusal
				if !errors.As(err, &r) {
					t.Fatalf("err = %v, want *SettingsRefusal", err)
				}
				if !strings.Contains(r.Reason, tc.reason) || r.Line != tc.line {
					t.Errorf("refusal %q at line %d, want %q at line %d", r.Reason, r.Line, tc.reason, tc.line)
				}
				if !strings.Contains(err.Error(), "refusing to edit settings.json: ") ||
					!strings.HasSuffix(err.Error(), "fix it or merge integration/settings.snippet.json by hand") {
					t.Errorf("message %q does not follow AC-38", err.Error())
				}
			}
		})
	}
}

func TestIsOurHookCommand(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"/Users/o/.claude/hooks/claude-memory/user-prompt-submit.sh":   true,
		"$HOME/.claude/hooks/claude-memory/session-end.sh":             true,
		`"$HOME/.claude/hooks/claude-memory/session-end.sh"`:           true,
		"  ~/.claude/hooks/claude-memory/user-prompt-submit.sh  ":      true,
		"/usr/local/bin/claude-memory hook":                            true,
		"claude-memory extract --session x":                            true,
		"/usr/local/bin/prompt-logger":                                 false,
		"/Users/o/.claude/hooks/other/user-prompt-submit.sh":           false,
		"/Users/o/.claude/hooks/claude-memory/user-prompt-submit.sh.x": false,
		"claude-memory-hooked":                                         false,
		"echo 'not claude-memory hook'":                                false,
		"cd /x && /opt/bin/claude-memory hook":                         true,
	}
	for cmd, want := range cases {
		if got := IsOurHookCommand(cmd); got != want {
			t.Errorf("IsOurHookCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
	if got := ExpandHome("$HOME/.claude/hooks/claude-memory/session-end.sh", "/Users/o"); got != "/Users/o/.claude/hooks/claude-memory/session-end.sh" {
		t.Errorf("ExpandHome = %q", got)
	}
	if got := ExpandHome("/abs/x.sh", "/Users/o"); got != "/abs/x.sh" {
		t.Errorf("ExpandHome(abs) = %q", got)
	}
	if !IsLegacyHookCommand("${HOME}/x") || IsLegacyHookCommand("/abs/x") {
		t.Error("IsLegacyHookCommand")
	}
}

func TestHookEntryCanonical(t *testing.T) {
	t.Parallel()
	h := HookEntry{Event: EventSessionEnd, Command: "/a/claude-memory/session-end.sh", Timeout: 5}
	if got, want := h.Canonical(), `{"command":"/a/claude-memory/session-end.sh","timeout":5,"type":"command"}`; got != want {
		t.Errorf("Canonical = %s, want %s", got, want)
	}
}

// ---- file I/O (AC-38 symlinks, AC-39 backup + hash re-check) ------------------

var testNow = time.Date(2026, 10, 1, 10, 15, 0, 0, time.UTC)

func writeTestFile(t *testing.T, p string, b []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func TestWriteSettingsFileBackupAndMode(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))
	orig := readTestdata(t, "testdata/settings/no-hooks-key.in.json")
	writeTestFile(t, p.SettingsJSON(), orig, 0o600)

	f, err := ReadSettingsFile(fsys, p.Home, p.SettingsJSON())
	if err != nil {
		t.Fatal(err)
	}
	out, _, changed, err := MergeSettings(f.Content, DesiredHooks(p.HookScriptsDir()), nil, false)
	if err != nil || !changed {
		t.Fatalf("merge: changed=%v err=%v", changed, err)
	}
	backup, err := WriteSettingsFile(fsys, NewFakeClock(testNow), f, out)
	if err != nil {
		t.Fatal(err)
	}
	if want := p.SettingsJSON() + ".bak.claude-memory.20261001T101500Z"; backup != want {
		t.Errorf("backup = %s, want %s", backup, want)
	}
	for path, want := range map[string][]byte{backup: orig, p.SettingsJSON(): out} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s content mismatch (err %v)", path, err)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600 (kept)", path, info.Mode().Perm())
		}
	}
	// A no-op run after the write: Merge reports unchanged, nothing written.
	f2, err := ReadSettingsFile(fsys, p.Home, p.SettingsJSON())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, changed, _ := MergeSettings(f2.Content, DesiredHooks(p.HookScriptsDir()), nil, false); changed {
		t.Error("second merge reports a change")
	}
}

func TestWriteSettingsFileNewFile(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))
	f, err := ReadSettingsFile(fsys, p.Home, p.SettingsJSON())
	if err != nil || f.Exists {
		t.Fatalf("read missing: %+v, %v", f, err)
	}
	out, _, _, err := MergeSettings(nil, DesiredHooks(p.HookScriptsDir()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := WriteSettingsFile(fsys, NewFakeClock(testNow), f, out)
	if err != nil || backup != "" {
		t.Fatalf("write: backup=%q err=%v", backup, err)
	}
	if info, err := os.Stat(p.SettingsJSON()); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("new settings.json: %v, %v", info, err)
	}
}

// AC-39: the file changed between Detect (read) and the write.
func TestWriteSettingsFileConcurrentEdit(t *testing.T) {
	t.Parallel()
	t.Run("before backup", func(t *testing.T) {
		t.Parallel()
		p := testPaths(t)
		fsys := NewFakeFS(t, filepath.Dir(p.Home))
		writeTestFile(t, p.SettingsJSON(), []byte("{}\n"), 0o644)
		f, err := ReadSettingsFile(fsys, p.Home, p.SettingsJSON())
		if err != nil {
			t.Fatal(err)
		}
		concurrent := []byte(`{"model": "sonnet"}` + "\n")
		writeTestFile(t, p.SettingsJSON(), concurrent, 0o644) // Claude Code writes meanwhile

		_, err = WriteSettingsFile(fsys, NewFakeClock(testNow), f, []byte(`{"hooks": {}}`+"\n"))
		if !errors.Is(err, ErrSettingsChanged) || !strings.Contains(err.Error(), "changed during install, re-run") {
			t.Fatalf("err = %v, want ErrSettingsChanged", err)
		}
		if got, _ := os.ReadFile(p.SettingsJSON()); !bytes.Equal(got, concurrent) {
			t.Errorf("file overwritten: %s", got)
		}
		if len(fsys.Writes()) != 0 {
			t.Errorf("writes = %v, want none", fsys.Writes())
		}
	})
	t.Run("between backup and write", func(t *testing.T) {
		t.Parallel()
		p := testPaths(t)
		fsys := NewFakeFS(t, filepath.Dir(p.Home))
		writeTestFile(t, p.SettingsJSON(), []byte("{}\n"), 0o644)
		f, err := ReadSettingsFile(fsys, p.Home, p.SettingsJSON())
		if err != nil {
			t.Fatal(err)
		}
		concurrent := []byte(`{"model": "sonnet"}` + "\n")
		fsys.Fail = func(op FSOp, path string) error {
			if op == OpRename && strings.Contains(path, SettingsBackupSuffix) {
				writeTestFile(t, p.SettingsJSON(), concurrent, 0o644)
			}
			return nil
		}
		_, err = WriteSettingsFile(fsys, NewFakeClock(testNow), f, []byte(`{"hooks": {}}`+"\n"))
		if !errors.Is(err, ErrSettingsChanged) {
			t.Fatalf("err = %v, want ErrSettingsChanged", err)
		}
		if got, _ := os.ReadFile(p.SettingsJSON()); !bytes.Equal(got, concurrent) {
			t.Errorf("file overwritten: %s", got)
		}
	})
	t.Run("created meanwhile", func(t *testing.T) {
		t.Parallel()
		p := testPaths(t)
		fsys := NewFakeFS(t, filepath.Dir(p.Home))
		f, err := ReadSettingsFile(fsys, p.Home, p.SettingsJSON())
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, p.SettingsJSON(), []byte("{}\n"), 0o644)
		if _, err := WriteSettingsFile(fsys, NewFakeClock(testNow), f, []byte("{}\n")); !errors.Is(err, ErrSettingsChanged) {
			t.Fatalf("err = %v, want ErrSettingsChanged", err)
		}
	})
}

func TestWriteSettingsFileReadOnlyFS(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))
	fsys.ReadOnly = true
	writeTestFile(t, p.SettingsJSON(), []byte("{}\n"), 0o644)
	f, err := ReadSettingsFile(fsys, p.Home, p.SettingsJSON())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSettingsFile(fsys, NewFakeClock(testNow), f, []byte(`{"a":1}`)); !errors.Is(err, ErrDryRun) {
		t.Errorf("err = %v, want ErrDryRun", err)
	}
	if got, _ := os.ReadFile(p.SettingsJSON()); string(got) != "{}\n" {
		t.Errorf("file changed: %s", got)
	}
}

func TestReadSettingsFileSymlinks(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	root := filepath.Dir(p.Home)
	fsys := NewFakeFS(t, root)

	// Inside Home (a dotfiles checkout): followed.
	target := filepath.Join(p.Home, "dotfiles", "claude-settings.json")
	writeTestFile(t, target, []byte("{}\n"), 0o644)
	if err := os.MkdirAll(p.ClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.SettingsJSON()); err != nil {
		t.Fatal(err)
	}
	f, err := ReadSettingsFile(fsys, p.Home, p.SettingsJSON())
	if err != nil {
		t.Fatal(err)
	}
	if f.Target != target || !f.Exists {
		t.Errorf("target = %q exists=%v, want %q", f.Target, f.Exists, target)
	}
	out, _, _, _ := MergeSettings(f.Content, testDesired(), nil, false)
	backup, err := WriteSettingsFile(fsys, NewFakeClock(testNow), f, out)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(backup) != filepath.Dir(target) {
		t.Errorf("backup %s not next to the target", backup)
	}
	if li, _ := os.Lstat(p.SettingsJSON()); li.Mode()&fs.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a file")
	}

	// Outside Home: refused.
	outside := filepath.Join(root, "elsewhere", "settings.json")
	writeTestFile(t, outside, []byte("{}\n"), 0o644)
	other := filepath.Join(p.ClaudeDir, "settings.local.json")
	if err := os.Symlink(outside, other); err != nil {
		t.Fatal(err)
	}
	_, err = ReadSettingsFile(fsys, p.Home, other)
	var r *SettingsRefusal
	if !errors.As(err, &r) || !strings.Contains(r.Reason, "outside") {
		t.Errorf("err = %v, want an outside-Home refusal", err)
	}

	// Read-only callers (doctor) follow it and get the flag.
	ff, err := ReadSettingsFileFollow(fsys, p.Home, other)
	if err != nil || !ff.OutsideHome || ff.Target != outside || !ff.Exists {
		t.Errorf("follow: %+v, %v", ff, err)
	}
}

// AC-70: other settings files are read-only inputs for duplicate warnings.
func TestScanOtherSettings(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))
	ours := readTestdata(t, "testdata/settings/ours-identical.in.json")
	writeTestFile(t, p.SettingsJSON(), ours, 0o644) // the user file itself is not "other"
	writeTestFile(t, filepath.Join(p.ClaudeDir, "settings.local.json"),
		readTestdata(t, "testdata/settings/ours-legacy-home.in.json"), 0o644)
	writeTestFile(t, filepath.Join(p.Cwd, ".claude", "settings.json"),
		readTestdata(t, "testdata/settings/other-hooks-same-event.in.json"), 0o644)
	writeTestFile(t, filepath.Join(p.Cwd, ".claude", "settings.local.json"), []byte("{ // nope\n}"), 0o644)

	r := ScanOtherSettings(fsys, p)
	if len(r.Duplicates) != 2 {
		t.Fatalf("duplicates = %+v, want the 2 entries of settings.local.json", r.Duplicates)
	}
	for _, d := range r.Duplicates {
		if d.Path != filepath.Join(p.ClaudeDir, "settings.local.json") {
			t.Errorf("duplicate in %s", d.Path)
		}
	}
	bad := filepath.Join(p.Cwd, ".claude", "settings.local.json")
	if msg, ok := r.Unreadable[bad]; !ok || !strings.Contains(msg, bad) {
		t.Errorf("unreadable = %v, want %s reported", r.Unreadable, bad)
	}
	if len(fsys.Writes()) != 0 {
		t.Errorf("writes = %v", fsys.Writes())
	}

	// Cwd == Home with the default ClaudeDir: <Cwd>/.claude/settings.json is
	// the user file, not a second one.
	p.Cwd = p.Home
	for _, f := range OtherSettingsFiles(p) {
		if f == p.SettingsJSON() {
			t.Errorf("OtherSettingsFiles includes the user settings file")
		}
	}
}
