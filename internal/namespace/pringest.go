package namespace

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// KnownKeys are the keys namespaces.yaml understands at each level. The
// installer's re-render loss check reads them from here so a new key cannot
// be added to the schema and forgotten there.
var KnownKeys = struct{ Top, Rule, PRIngest []string }{
	Top:      []string{"default", "namespaces"},
	Rule:     []string{"namespace", "paths", "pr_ingest"},
	PRIngest: []string{"enabled", "provider"},
}

// PR-ingest provider names a rule may set (kept as plain strings so this
// package does not import internal/prsource).
var prProviders = []string{"azuredevops", "github", "gitlab"}

// PRIngest is the optional per-namespace `pr_ingest` section: Enabled false
// opts the namespace's repos out of ingest-pr, Provider replaces the
// provider detected from the origin remote (the only way to reach a
// self-hosted GitLab).
type PRIngest struct {
	Enabled  *bool  `yaml:"enabled,omitempty"`
	Provider string `yaml:"provider,omitempty"`

	problem string // set by UnmarshalYAML for a wrongly typed value
}

// IsEnabled reports whether ingest-pr may ingest the namespace (default true).
func (p PRIngest) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

// UnmarshalYAML reads the known keys and never returns an error: a wrongly
// typed value is recorded in problem and surfaced through
// Config.PRIngestProblems, because a Parse error would make every subcommand
// fall back to the global namespace. Unknown keys are ignored.
func (p *PRIngest) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		p.problem = "pr_ingest must be a mapping with enabled and/or provider"
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		switch k {
		case "enabled":
			var b bool
			if v.Kind != yaml.ScalarNode || v.Decode(&b) != nil {
				p.problem = "pr_ingest.enabled must be true or false"
				continue
			}
			p.Enabled = &b
		case "provider":
			var s string
			if v.Kind != yaml.ScalarNode || v.Decode(&s) != nil {
				p.problem = "pr_ingest.provider must be a string"
				continue
			}
			p.Provider = s
		}
	}
	return nil
}

// PRIngestProblem is one reason a namespace's pr_ingest cannot be used.
type PRIngestProblem struct {
	Namespace string
	Reason    string
}

// PRIngestFor returns the pr_ingest settings of namespace ns, merged over
// its rules. problem is non-empty (and the settings zero) when a value is
// wrongly typed, the provider is unknown, or two rules disagree; callers
// must then skip the namespace's repos.
func (c *Config) PRIngestFor(ns string) (p PRIngest, problem string) {
	var set bool
	for _, r := range c.Namespaces {
		if r.Namespace != ns || r.PRIngest == nil {
			continue
		}
		q := *r.PRIngest
		if q.problem != "" {
			return PRIngest{}, q.problem
		}
		if q.Provider != "" && !slices.Contains(prProviders, q.Provider) {
			return PRIngest{}, fmt.Sprintf("pr_ingest.provider %q is unknown (use azuredevops, github or gitlab)", q.Provider)
		}
		if !set {
			p, set = q, true
			continue
		}
		if q.Provider != p.Provider || (q.Enabled == nil) != (p.Enabled == nil) ||
			(q.Enabled != nil && *q.Enabled != *p.Enabled) {
			return PRIngest{}, "pr_ingest differs between rules of this namespace"
		}
	}
	return p, ""
}

// PRIngestError is returned by Marshal (and so Save, Add, Init) when a
// pr_ingest section is unusable; the file must be fixed by hand first.
type PRIngestError struct{ Problems []PRIngestProblem }

func (e *PRIngestError) Error() string {
	var parts []string
	for _, p := range e.Problems {
		parts = append(parts, p.Namespace+": "+p.Reason)
	}
	return strings.Join(parts, "; ")
}

func (c *Config) pringestError() error {
	c.collectPRIngestProblems()
	if len(c.PRIngestProblems) == 0 {
		return nil
	}
	return &PRIngestError{Problems: c.PRIngestProblems}
}

// collectPRIngestProblems fills PRIngestProblems, one entry per affected
// namespace in file order.
func (c *Config) collectPRIngestProblems() {
	c.PRIngestProblems = nil
	seen := map[string]bool{}
	for _, r := range c.Namespaces {
		if seen[r.Namespace] {
			continue
		}
		seen[r.Namespace] = true
		if _, problem := c.PRIngestFor(r.Namespace); problem != "" {
			c.PRIngestProblems = append(c.PRIngestProblems, PRIngestProblem{r.Namespace, problem})
		}
	}
}
