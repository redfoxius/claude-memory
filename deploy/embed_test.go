package deploy

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReferencedAssetsAreEmbedded asserts every asset path the code
// references exists in FS, including the dotfile that a directory pattern
// would skip (AC-34).
func TestReferencedAssetsAreEmbedded(t *testing.T) {
	for _, p := range []string{InitDBAppRole, InitDBAppRolePSQL, EnvExample} {
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

// TestWrapperRunsSharedPSQL guards the single SQL source: the compose stack's
// 01-app-role.sh must be a thin wrapper that runs app-role.psql, not a second
// copy of the statements (plan Design 5).
func TestWrapperRunsSharedPSQL(t *testing.T) {
	b, err := fs.ReadFile(FS, InitDBAppRole)
	if err != nil {
		t.Fatal(err)
	}
	sh := string(b)
	if !strings.Contains(sh, "app-role.psql") {
		t.Errorf("%s does not reference app-role.psql", InitDBAppRole)
	}
	for _, v := range []string{"app_user=", "app_db=", "app_pw="} {
		if !strings.Contains(sh, v) {
			t.Errorf("%s does not pass -v %s", InitDBAppRole, v)
		}
	}
	for _, stmt := range []string{"CREATE ROLE", "CREATE DATABASE", "CREATE EXTENSION"} {
		if strings.Contains(sh, stmt) {
			t.Errorf("%s still holds %q: the statements live in app-role.psql only", InitDBAppRole, stmt)
		}
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
