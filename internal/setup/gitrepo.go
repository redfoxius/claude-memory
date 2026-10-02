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
func InGitRepo(fsys ReadFS, path, ceiling string) (bool, string, error) {
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

// InGitRepoResolved is InGitRepo for a path that may reach a repository
// through a symlinked directory: it checks the path as given and, when its
// directory resolves elsewhere, the resolved directory too (AC-42). A walk
// error is returned, so a caller that must not write into a repository can
// fail closed.
func InGitRepoResolved(fsys ReadFS, path, ceiling string) (bool, string, error) {
	if in, root, err := InGitRepo(fsys, path, ceiling); err != nil || in {
		return in, root, err
	}
	dir := filepath.Dir(filepath.Clean(path))
	resolved, err := fsys.EvalSymlinks(dir)
	if err != nil || resolved == dir {
		return false, "", nil
	}
	return InGitRepo(fsys, filepath.Join(resolved, filepath.Base(path)), ceiling)
}
