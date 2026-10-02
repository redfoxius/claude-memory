package namespace

import "sort"

// Entry is one row of `namespaces list`.
type Entry struct {
	Namespace string   `json:"namespace"`
	Paths     []string `json:"paths"`
	IsDefault bool     `json:"is_default"`
}

// EffectiveDefault returns the namespace used for unmatched directories:
// the file's `default:`, or Fallback when it is empty.
func (c *Config) EffectiveDefault() string {
	if c.Default != "" {
		return c.Default
	}
	return Fallback
}

// List returns every known namespace: each rule's namespace (globs merged
// across duplicate rules, in file order), the default namespace even when it
// has no rules, and Fallback (global) always. Entries are sorted
// alphabetically with global last. Paths is never nil.
func (c *Config) List() []Entry {
	def := c.EffectiveDefault()
	byName := map[string]*Entry{}
	get := func(name string) *Entry {
		e, ok := byName[name]
		if !ok {
			e = &Entry{Namespace: name, Paths: []string{}, IsDefault: name == def}
			byName[name] = e
		}
		return e
	}
	for _, r := range c.Namespaces {
		e := get(r.Namespace)
		for _, g := range r.Paths {
			if !contains(e.Paths, g) {
				e.Paths = append(e.Paths, g)
			}
		}
	}
	get(def)
	get(Fallback)

	out := make([]Entry, 0, len(byName))
	for _, e := range byName {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		gi, gj := out[i].Namespace == Fallback, out[j].Namespace == Fallback
		if gi != gj {
			return gj
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out
}
