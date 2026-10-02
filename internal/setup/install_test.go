package setup

import (
	"slices"
	"testing"
)

// TestInstallStepsRegistry is the 2a registry (AC-7): the exact order, every
// Requires pointing at an earlier step, and no mcp or jobs step. 2a leaves
// WritePorts.ClaudeCLI and WritePorts.Jobs (and ReadPorts.Jobs) nil, so a
// registered step that used them would dereference nil.
func TestInstallStepsRegistry(t *testing.T) {
	t.Parallel()
	steps := InstallSteps("v1.0.0", NewRedactor())
	var ids []string
	seen := map[string]bool{}
	for _, s := range steps {
		if seen[s.ID()] {
			t.Errorf("duplicate step %q", s.ID())
		}
		for _, req := range s.Requires() {
			if !seen[req] {
				t.Errorf("step %q requires %q, which is not registered before it", s.ID(), req)
			}
		}
		seen[s.ID()] = true
		ids = append(ids, s.ID())
	}
	want := []string{"platform", "binary", "prereqs", "topology", "envfile", "database", "migrate", "ollama", "namespaces"}
	if !slices.Equal(ids, want) {
		t.Errorf("registry = %v, want %v", ids, want)
	}
	for _, banned := range []string{"mcp", "jobs", "hooks.scripts", "hooks.settings", "skills", "claude-md", "doctor"} {
		if seen[banned] {
			t.Errorf("slice 2a must not register %q", banned)
		}
	}
}
