package integration

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReferencedAssetsAreEmbedded asserts every asset path the code
// references exists in FS (AC-34, embed half).
func TestReferencedAssetsAreEmbedded(t *testing.T) {
	paths := append([]string{
		HookUserPromptSubmit, HookSessionEnd, ClaudeMDSection, SettingsSnippet,
		LaunchdCleanup, LaunchdIngestPR,
	}, HookScripts...)
	for _, s := range Skills {
		paths = append(paths, SkillFile(s))
	}
	for _, p := range paths {
		info, err := fs.Stat(FS, p)
		if err != nil {
			t.Errorf("asset %q is not embedded: %v", p, err)
			continue
		}
		if info.IsDir() || info.Size() == 0 {
			t.Errorf("asset %q is a directory or empty", p)
		}
	}
	for _, d := range []string{SkillsDir, LaunchdDir} {
		if info, err := fs.Stat(FS, d); err != nil || !info.IsDir() {
			t.Errorf("asset dir %q: %v", d, err)
		}
	}
}

// TestEmbeddedMatchesWorkingTree guards against an asset directory that the
// embed pattern silently misses (e.g. a new file in skills/): every file
// under the embedded directories in the repo is in FS, byte-identical.
func TestEmbeddedMatchesWorkingTree(t *testing.T) {
	for _, root := range []string{"hooks", "skills", "launchd"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			want, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			got, err := fs.ReadFile(FS, filepath.ToSlash(p))
			if err != nil {
				t.Errorf("%s is in the tree but not embedded: %v", p, err)
				return nil
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from its embedded copy", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestAssetsAreLocationNeutral pins AC-66's wording rule: neither skill nor
// the CLAUDE.md section names acme/ (the section goes to the user-level
// CLAUDE.md by default, spec §13 #1).
func TestAssetsAreLocationNeutral(t *testing.T) {
	paths := []string{ClaudeMDSection}
	for _, s := range Skills {
		paths = append(paths, SkillFile(s))
	}
	for _, p := range paths {
		b, err := fs.ReadFile(FS, p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "acme") {
			t.Errorf("%s mentions acme; the embedded assets must be location-neutral", p)
		}
	}
	b, _ := fs.ReadFile(FS, ClaudeMDSection)
	// The heading doctor/install use to detect a hand-pasted section (AC-42).
	if !strings.HasPrefix(string(b), "## Shared semantic memory (`claude-memory`)\n") {
		t.Errorf("%s must start with the section heading", ClaudeMDSection)
	}
	if strings.Contains(string(b), "<!-- BEGIN claude-memory") {
		t.Errorf("%s must not contain the managed-block markers", ClaudeMDSection)
	}
}
