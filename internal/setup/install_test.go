package setup

import (
	"slices"
	"testing"
)

// TestInstallStepsRegistry is the registry in AC-7 order, ending in the final
// doctor: the exact order and every Requires pointing at an earlier step.
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
	want := []string{"platform", "binary", "prereqs", "topology", "envfile", "database", "migrate", "ollama", "namespaces",
		"hooks.scripts", "hooks.settings", "mcp", "skills", "claude-md", "jobs", "doctor"}
	if !slices.Equal(ids, want) {
		t.Errorf("registry = %v, want %v", ids, want)
	}
	hs := steps[slices.IndexFunc(steps, func(s Step) bool { return s.ID() == "hooks.settings" })].Requires()
	if !slices.Contains(hs, "migrate") || !slices.Contains(hs, "hooks.scripts") {
		t.Errorf("hooks.settings must require migrate and hooks.scripts (AC-7), has %v", hs)
	}
}

// TestSkipAcceptsUnregisteredStepIDs (C4): --skip works for the ids this build does not register (skills, jobs); a truly
// unknown id is still a usage error.
func TestSkipAcceptsUnregisteredStepIDs(t *testing.T) {
	t.Parallel()
	for _, id := range AllStepIDs {
		e := &Engine{Steps: []Step{PlatformStep{}}, KnownIDs: AllStepIDs}
		s := &session{e: e, idx: map[string]*stepRun{}, in: Inputs{Skip: []string{id}}}
		if err := s.setup(); err != nil {
			t.Errorf("--skip %s: %v", id, err)
		}
	}
	e := &Engine{Steps: []Step{PlatformStep{}}, KnownIDs: AllStepIDs}
	s := &session{e: e, idx: map[string]*stepRun{}, in: Inputs{Skip: []string{"nonsense"}}}
	if err := s.setup(); err == nil {
		t.Error("--skip nonsense must fail")
	}
}
