package setup

import (
	"errors"
	"io/fs"
	"path/filepath"
)

// InGitRepo reports whether path lies inside a git working tree, by
// walking up from its directory looking for a ".git" entry, which may be a
// directory (a normal clone) or a file (a worktree or submodule) (AC-42).
// It reads only through the FS port; git is never executed. path need not
// exist. It returns the directory holding ".git".
//
// The walk stops after checking ceiling (inclusive), like git's
// GIT_CEILING_DIRECTORIES; "" walks up to the filesystem root. Production
// passes ""; tests pass their temp root so the walk never leaves it.
func InGitRepo(fsys FS, path, ceiling string) (bool, string, error) {
	dir := filepath.Dir(filepath.Clean(path))
	if ceiling != "" {
		ceiling = filepath.Clean(ceiling)
	}
	for {
		_, err := fsys.Lstat(filepath.Join(dir, ".git"))
		switch {
		case err == nil:
			return true, dir, nil
		case !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission):
			return false, "", err
		}
		parent := filepath.Dir(dir)
		if dir == ceiling || parent == dir {
			return false, "", nil
		}
		dir = parent
	}
}
