package importer

import (
	"bytes"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxFileBytes bounds a file the importer will read.
const maxFileBytes = 1 << 20

// maxItemFiles bounds the files of one INSIGHTS item.
const maxItemFiles = 20

// FS is the read-only filesystem port. Symlinks are never followed by the
// walks, so Lstat and ReadDir entry types decide what is read.
type FS interface {
	ReadDir(p string) ([]fs.DirEntry, error)
	ReadFile(p string) ([]byte, error)
	Lstat(p string) (fs.FileInfo, error)
	EvalSymlinks(p string) (string, error)
}

// Env holds the ports discovery needs; cmd wires the adapters.
type Env struct {
	FS FS
	// NamespaceOf is the namespace of a directory from namespaces.yaml only.
	NamespaceOf func(dir string) string
	// Toplevel is the git toplevel containing dir.
	Toplevel func(dir string) (string, bool)
	// Decode turns an encoded project dir name into the existing directory.
	Decode func(name string) (string, bool)
}

// repoOf is the basename of the toplevel of dir, or "*" outside a checkout.
func (e Env) repoOf(dir string) string {
	if top, ok := e.Toplevel(dir); ok {
		return filepath.Base(top)
	}
	return "*"
}

// DiscoverAutoMem reads <projectsDir>/*/memory/*.md (regular files only,
// non-recursive) and returns the items belonging to namespace target.
func DiscoverAutoMem(env Env, projectsDir, target string) ([]Item, []Skip, error) {
	projects, err := env.FS.ReadDir(projectsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("read projects dir: %w", err)
	}
	var items []Item
	var skips []Skip
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		memDir := filepath.Join(projectsDir, p.Name(), "memory")
		if fi, err := env.FS.Lstat(memDir); err != nil || !fi.IsDir() {
			continue // no memory dir, or a symlink to one: never followed
		}
		files, err := env.FS.ReadDir(memDir)
		if err != nil {
			continue
		}
		var names []string
		for _, f := range files {
			if !f.IsDir() && strings.EqualFold(filepath.Ext(f.Name()), ".md") {
				names = append(names, f.Name())
			}
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		home, ok := env.Decode(p.Name())
		if !ok {
			skips = append(skips, Skip{Origin: p.Name(), Reason: "undecodable project dir"})
			continue
		}
		ns := env.NamespaceOf(home)
		repo := env.repoOf(home)
		for _, name := range names {
			origin := p.Name() + "/" + name
			if IsIndexFile(name) {
				skips = append(skips, Skip{Origin: origin, Reason: "index file"})
				continue
			}
			if ns != target {
				skips = append(skips, Skip{Origin: origin, Reason: "other namespace (" + ns + ")"})
				continue
			}
			full := filepath.Join(memDir, name)
			data, reason := readRegular(env.FS, full)
			if reason != "" {
				skips = append(skips, Skip{Origin: origin, Reason: reason})
				continue
			}
			item, skip := ParseAutoMemory(name, data)
			if skip != nil {
				skips = append(skips, Skip{Origin: origin, Reason: skip.Reason})
				continue
			}
			item.Origin, item.Home, item.Repo = origin, home, repo
			item.Key = ImportKey("automem", origin, string(data))
			items = append(items, item)
		}
	}
	return items, skips, nil
}

// readRegular reads a regular, non-oversized file. reason is a fixed string
// when the file is not read.
func readRegular(fsys FS, path string) (data []byte, reason string) {
	fi, err := fsys.Lstat(path)
	if err != nil {
		return nil, "unreadable"
	}
	if !fi.Mode().IsRegular() {
		return nil, "not a regular file"
	}
	if fi.Size() > maxFileBytes {
		return nil, "file too large"
	}
	data, err = fsys.ReadFile(path)
	if err != nil {
		return nil, "unreadable"
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, "contains NUL"
	}
	if !utf8.Valid(data) {
		return nil, "not UTF-8"
	}
	return data, ""
}

// DiscoverInsights finds INSIGHTS.md files under paths (a path may be a file
// or a directory, walked without following symlinks) and returns the entries
// belonging to namespace target.
func DiscoverInsights(env Env, paths []string, target string) ([]Item, []Skip, error) {
	var files []string
	var skips []Skip
	for _, p := range paths {
		// Resolve symlinks first so the locator, key and provenance are
		// toplevel-relative whatever spelling the path came in.
		if real, err := env.FS.EvalSymlinks(p); err == nil {
			p = real
		}
		fi, err := env.FS.Lstat(p)
		if err != nil {
			return nil, nil, fmt.Errorf("stat %s: %w", p, err)
		}
		switch {
		case fi.Mode().IsRegular():
			files = append(files, p)
		case fi.IsDir():
			files = append(files, walkInsights(env.FS, p)...)
		default:
			skips = append(skips, Skip{Origin: p, Reason: "not a file or directory"})
		}
	}
	var items []Item
	for _, file := range files {
		its, sk := insightsFile(env, file, target)
		items = append(items, its...)
		skips = append(skips, sk...)
	}
	return items, skips, nil
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// walkInsights returns the INSIGHTS.md files (any case) below dir.
func walkInsights(fsys FS, dir string) []string {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		switch {
		case e.IsDir():
			if !skipDirs[e.Name()] {
				out = append(out, walkInsights(fsys, full)...)
			}
		case e.Type().IsRegular() && strings.EqualFold(e.Name(), "INSIGHTS.md"):
			out = append(out, full)
		}
	}
	return out
}

func insightsFile(env Env, file, target string) ([]Item, []Skip) {
	data, reason := readRegular(env.FS, file)
	if reason != "" {
		return nil, []Skip{{Origin: file, Reason: reason}}
	}
	dir := filepath.Dir(file)
	top, inGit := env.Toplevel(dir)
	home := dir
	if inGit {
		home = top
	}
	if ns := env.NamespaceOf(home); ns != target {
		return nil, []Skip{{Origin: file, Reason: "other namespace (" + ns + ")"}}
	}

	repo, rel := "*", file
	if inGit {
		repo = filepath.Base(top)
		if r, err := filepath.Rel(top, file); err == nil && !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r)
		}
	}
	locator := rel
	if inGit {
		locator = repo + "/" + rel
	}

	entries, parseSkips := ParseInsights(data)
	var skips []Skip
	for _, s := range parseSkips {
		skips = append(skips, Skip{Origin: rel + ":" + strings.TrimPrefix(s.Origin, "line "), Reason: s.Reason})
	}
	var items []Item
	for _, en := range entries {
		var files []string
		if inGit {
			files = resolveFiles(env.FS, top, dir, en.FileRefs)
		}
		items = append(items, Item{
			Kind:    en.Kind,
			Title:   en.Title,
			Content: fmt.Sprintf("%s\n(imported from %s, %s)", en.Text, rel, en.Date),
			Repo:    repo,
			Home:    home,
			Files:   files,
			Tags:    []string{"imported", "insights"},
			Key:     ImportKey("insights", locator, en.Date+" "+en.Text),
			Origin:  fmt.Sprintf("%s:%d", rel, en.Line),
		})
	}
	return items, skips
}

// resolveFiles maps raw `path[:N[-M]]` tokens to toplevel-relative paths of
// existing regular files that stay inside the toplevel after symlink
// resolution. The line suffix is kept; anything else is dropped (AC-35).
func resolveFiles(fsys FS, top, fileDir string, refs []string) []string {
	realTop, err := fsys.EvalSymlinks(top)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, ref := range refs {
		path, suffix := ref, ""
		if i := strings.LastIndex(ref, ":"); i >= 0 {
			path, suffix = ref[:i], ref[i:]
		}
		if path == "" || filepath.IsAbs(path) {
			continue
		}
		for _, base := range []string{fileDir, top} {
			real, err := fsys.EvalSymlinks(filepath.Join(base, path))
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(realTop, real)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if fi, err := fsys.Lstat(real); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			stored := filepath.ToSlash(rel) + suffix
			if !seen[stored] {
				seen[stored] = true
				out = append(out, stored)
			}
			break
		}
		if len(out) >= maxItemFiles {
			return out[:maxItemFiles]
		}
	}
	return out
}
