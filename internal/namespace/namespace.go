// Package namespace resolves which memory namespace a project directory
// belongs to, from ~/.config/claude-memory/namespaces.yaml and an explicit
// MEMORY_NAMESPACE override.
package namespace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Fallback is the namespace used when neither the environment, a rule, nor
// the file's `default:` yields one: the shared "global" namespace. No
// project (company or otherwise) is special-cased in code.
const Fallback = "global"

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidName reports whether s is a legal namespace name.
func ValidName(s string) bool { return validName.MatchString(s) }

// Rule maps project path globs to a namespace.
type Rule struct {
	Namespace string    `yaml:"namespace"`
	Paths     []string  `yaml:"paths"`
	PRIngest  *PRIngest `yaml:"pr_ingest,omitempty"`
}

// Config is the parsed namespaces.yaml.
//
//	default: global
//	namespaces:
//	  - namespace: acme
//	    paths: ["~/work/acme/**"]
//	  - namespace: pet-game
//	    paths: ["~/src/pet-game", "~/src/pet-game/**"]
type Config struct {
	Default    string `yaml:"default"`
	Namespaces []Rule `yaml:"namespaces"`

	// PRIngestProblems lists namespaces whose pr_ingest is unusable (set by
	// Parse). They never make Parse fail: ingest-pr skips those namespaces
	// and doctor reports them.
	PRIngestProblems []PRIngestProblem `yaml:"-"`

	home string
}

// Load reads and validates a namespaces.yaml. A missing file is not an
// error: it yields an empty Config (everything resolves to Fallback).
func Load(path string) (*Config, error) {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{home: home}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(data, path, home)
}

// Parse parses and validates namespaces.yaml content read by the caller.
// path is used in error messages only; home expands "~" in path globs.
func Parse(data []byte, path, home string) (*Config, error) {
	c := &Config{home: home}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Default != "" && !ValidName(c.Default) {
		return nil, fmt.Errorf("%s: invalid default namespace %q", path, c.Default)
	}
	for _, r := range c.Namespaces {
		if !ValidName(r.Namespace) {
			return nil, fmt.Errorf("%s: invalid namespace %q", path, r.Namespace)
		}
	}
	c.collectPRIngestProblems()
	return c, nil
}

// Resolve returns the namespace for dir: the rule whose matching path glob
// is most specific (longest literal prefix; an exact path beats "<path>/**")
// wins; otherwise `default:`, otherwise Fallback (global).
func (c *Config) Resolve(dir string) string {
	ns, _ := c.Explain(dir)
	return ns
}

// Why values returned by Explain.
const (
	WhyRule     = "rule"     // a path glob in namespaces.yaml matched
	WhyDefault  = "default"  // no glob matched; the file's `default:`
	WhyFallback = "fallback" // nothing configured; built-in global
)

// Explain is Resolve plus how the namespace was chosen: WhyRule (with the
// matching glob in detail), WhyDefault or WhyFallback.
func (c *Config) Explain(dir string) (ns, why string) {
	dir = filepath.Clean(dir)
	best, bestGlob, bestScore := "", "", -1
	for _, r := range c.Namespaces {
		for _, g := range r.Paths {
			eg := c.expand(g)
			if !match(eg, dir) {
				continue
			}
			if s := specificity(eg); s > bestScore {
				best, bestGlob, bestScore = r.Namespace, g, s
			}
		}
	}
	switch {
	case best != "":
		return best, WhyRule + " " + bestGlob
	case c.Default != "":
		return c.Default, WhyDefault
	}
	return Fallback, WhyFallback
}

func (c *Config) expand(g string) string {
	if g == "~" || strings.HasPrefix(g, "~/") {
		g = filepath.Join(c.home, g[1:])
	}
	return filepath.Clean(g)
}

// specificity ranks a glob: length of its literal prefix before the first
// wildcard, so "/a/b/**" beats "/a/**".
func specificity(g string) int {
	if i := strings.IndexAny(g, "*?["); i >= 0 {
		return i
	}
	return len(g) + 2 // an exact path beats "<path>/**" (whose prefix is len+1)
}

// match reports whether dir matches glob g, where "**" matches any number
// of path segments (including zero) and other segments use filepath.Match.
func match(g, dir string) bool {
	return matchSegs(strings.Split(g, "/"), strings.Split(dir, "/"))
}

func matchSegs(g, d []string) bool {
	for len(g) > 0 {
		if g[0] == "**" {
			if len(g) == 1 {
				return true
			}
			for i := 0; i <= len(d); i++ {
				if matchSegs(g[1:], d[i:]) {
					return true
				}
			}
			return false
		}
		if len(d) == 0 {
			return false
		}
		if ok, err := filepath.Match(g[0], d[0]); err != nil || !ok {
			return false
		}
		g, d = g[1:], d[1:]
	}
	return len(d) == 0
}

// ForDir resolves the namespace for dir given an optional explicit override
// (MEMORY_NAMESPACE). An invalid override is an error.
func (c *Config) ForDir(override, dir string) (string, error) {
	if override != "" {
		if !ValidName(override) {
			return "", fmt.Errorf("invalid namespace %q", override)
		}
		return override, nil
	}
	return c.Resolve(dir), nil
}
