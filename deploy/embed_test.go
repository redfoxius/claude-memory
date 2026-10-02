package deploy

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestReferencedAssetsAreEmbedded asserts every asset path the code
// references exists in FS, including the dotfile that a directory pattern
// would skip (AC-34).
func TestReferencedAssetsAreEmbedded(t *testing.T) {
	for _, p := range []string{InitDBAppRole, EnvExample} {
		want, err := os.ReadFile(filepath.FromSlash(p))
		if err != nil {
			t.Fatalf("read %s from the tree: %v", p, err)
		}
		got, err := fs.ReadFile(FS, p)
		if err != nil {
			t.Errorf("asset %q is not embedded: %v", p, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("asset %q differs from the tree", p)
		}
	}
	if info, err := fs.Stat(FS, InitDBDir); err != nil || !info.IsDir() {
		t.Errorf("asset dir %q: %v", InitDBDir, err)
	}
}

// TestInitDBFullyEmbedded guards against a new initdb file the pattern
// misses (e.g. a dotfile): every file in deploy/initdb is in FS.
func TestInitDBFullyEmbedded(t *testing.T) {
	entries, err := os.ReadDir(InitDBDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, err := fs.Stat(FS, InitDBDir+"/"+e.Name()); err != nil {
			t.Errorf("%s/%s is not embedded: %v", InitDBDir, e.Name(), err)
		}
	}
}
