package namespace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const fileHeader = `# claude-memory namespaces. Maps project directories to memory namespaces.
# Resolution: MEMORY_NAMESPACE env > most specific matching path below >
# default > "global". Paths are globs ("**" = any depth, "~" = home).
# Manage with: claude-memory namespaces add|which|init
`

// Marshal renders c exactly as Save writes it: the explanatory header
// followed by the YAML body. It is pure (no I/O), so a caller that owns its
// own filesystem port (the installer) can write the bytes itself.
//
// It refuses (see PRIngestError) while any pr_ingest section is unusable:
// re-rendering would drop a malformed value and so turn an opt-out into
// "enabled".
func Marshal(c *Config) ([]byte, error) {
	if err := c.pringestError(); err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal namespaces: %w", err)
	}
	return append([]byte(fileHeader), body...), nil
}

// Save writes c to path (mode 0600, parent dir created), prefixed with a
// short explanatory header. The write is atomic (temp file + rename).
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	out, err := Marshal(c)
	if err != nil {
		return fmt.Errorf("fix pr_ingest in %s first: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".namespaces-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write namespaces: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Init creates a new namespaces file with the given default and initial
// rules. It refuses to overwrite an existing file unless force is set.
func Init(path, def string, rules []Rule, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite, or `namespaces add`)", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if def == "" {
		def = Fallback
	}
	if !ValidName(def) {
		return fmt.Errorf("invalid default namespace %q", def)
	}
	c := &Config{Default: def, Namespaces: []Rule{}}
	for _, r := range rules {
		if err := c.Add(r.Namespace, r.Paths...); err != nil {
			return err
		}
	}
	return Save(path, c)
}

// Add appends path globs to namespace name in the file at path (creating
// the file and the namespace if needed).
func Add(path, name string, globs ...string) error {
	c, err := Load(path)
	if err != nil {
		return err
	}
	if err := c.Add(name, globs...); err != nil {
		return err
	}
	return Save(path, c)
}

// Add appends path globs to namespace name in c (creating the namespace if
// needed), skipping globs it already holds.
func (c *Config) Add(name string, globs ...string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid namespace name %q (lowercase letters, digits, '-' and '_')", name)
	}
	if len(globs) == 0 {
		return fmt.Errorf("namespace %q needs at least one path", name)
	}
	for _, g := range globs {
		if err := validateGlob(g); err != nil {
			return err
		}
	}
	for i := range c.Namespaces {
		if c.Namespaces[i].Namespace == name {
			for _, g := range globs {
				if !contains(c.Namespaces[i].Paths, g) {
					c.Namespaces[i].Paths = append(c.Namespaces[i].Paths, g)
				}
			}
			return nil
		}
	}
	c.Namespaces = append(c.Namespaces, Rule{Namespace: name, Paths: append([]string(nil), globs...)})
	return nil
}

// validateGlob rejects globs that could never match: empty ones and ones
// with a malformed segment pattern (e.g. an unclosed "["), which match()
// would otherwise treat as "no match" silently.
func validateGlob(g string) error {
	if strings.TrimSpace(g) == "" {
		return fmt.Errorf("empty path glob")
	}
	for _, seg := range strings.Split(g, "/") {
		if seg == "**" {
			continue
		}
		if _, err := filepath.Match(seg, ""); err != nil {
			return fmt.Errorf("invalid path glob %q: %w", g, err)
		}
	}
	return nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
