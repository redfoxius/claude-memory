package setup

import (
	"path/filepath"
	"sort"
	"strings"
)

// Existence-checked decoding of the directory names under
// <ClaudeDir>/projects (AC-47). Claude Code names a project directory after
// its absolute path with every '/' replaced by '-', so a literal hyphen in a
// directory name is indistinguishable from a separator: the name
// "-Users-me-work-Block-strike" may be /Users/me/work/Block-strike or
// /Users/me/work/Block/strike. The decoder resolves this by walking the
// filesystem through the read-only port: at each level it tries the longest
// run of segments that names an existing directory first and backtracks when
// the rest does not resolve. Only decodings in which every prefix exists are
// returned, so a wrong guess can only hide a suggestion, never invent one.

// maxDecodeSteps bounds the filesystem probes for one name.
const maxDecodeSteps = 512

// DecodeProjectName returns the existing directory that name encodes. root
// is the directory the encoded path is anchored at: "/" in production; a
// test passes its sandbox root, in which case name must begin with root's own
// encoding and the walk never probes above root. ok is false when the name is
// not an encoded path or no decoding exists.
func DecodeProjectName(fsys ReadFS, root, name string) (string, bool) {
	root = filepath.Clean(root)
	if !strings.HasPrefix(name, "-") || len(name) < 2 {
		return "", false
	}
	rest := name[1:]
	if root != string(filepath.Separator) {
		pre := strings.ReplaceAll(strings.TrimPrefix(root, string(filepath.Separator)), string(filepath.Separator), "-")
		if !strings.HasPrefix(rest, pre+"-") {
			return "", false
		}
		rest = rest[len(pre)+1:]
	}
	segs := strings.Split(rest, "-")
	steps := 0
	var walk func(dir string, i int) (string, bool)
	walk = func(dir string, i int) (string, bool) {
		if i == len(segs) {
			return dir, true
		}
		for j := len(segs); j > i; j-- {
			if steps++; steps > maxDecodeSteps {
				return "", false
			}
			cand := strings.Join(segs[i:j], "-")
			if cand == "" {
				continue
			}
			p := filepath.Join(dir, cand)
			if fi, err := fsys.Stat(p); err != nil || !fi.IsDir() {
				continue
			}
			if got, ok := walk(p, j); ok {
				return got, true
			}
		}
		return "", false
	}
	return walk(root, 0)
}

// ProjectDirs decodes every entry of projectsDir (<ClaudeDir>/projects), anchored at root, and
// returns the distinct existing directories, sorted. A missing or unreadable
// directory yields nil.
func ProjectDirs(fsys ReadFS, root, projectsDir string) []string {
	ents, err := fsys.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if d, ok := DecodeProjectName(fsys, root, e.Name()); ok && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// SuggestParentDirs returns up to max distinct parent directories of dirs,
// most frequent first (ties alphabetical). The home directory and the root
// are never suggested: a mapping over them would swallow every project.
func SuggestParentDirs(dirs []string, home string, max int) []string {
	count := map[string]int{}
	for _, d := range dirs {
		par := filepath.Dir(d)
		if par == d || par == string(filepath.Separator) || par == filepath.Clean(home) {
			continue
		}
		count[par]++
	}
	out := make([]string, 0, len(count))
	for p := range count {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if count[out[i]] != count[out[j]] {
			return count[out[i]] > count[out[j]]
		}
		return out[i] < out[j]
	})
	if len(out) > max {
		out = out[:max]
	}
	return out
}
