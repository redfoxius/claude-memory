package main

import (
	"testing"

	"github.com/redfoxius/claude-memory/internal/extraction"
)

// With the guard set, hook and extract must return before touching cfg (nil
// here: any DB/Ollama construction or config read would panic).
func TestSubprocessGuardMakesHookAndExtractNoOps(t *testing.T) {
	t.Setenv(extraction.SubprocessEnv, "1")
	if err := cmdHook(nil); err != nil {
		t.Errorf("cmdHook: %v", err)
	}
	if err := cmdExtract(nil); err != nil {
		t.Errorf("cmdExtract: %v", err)
	}
}

func TestSubprocessGuardUnsetByDefault(t *testing.T) {
	t.Setenv(extraction.SubprocessEnv, "")
	if subprocessGuard() {
		t.Error("guard must be off when the variable is empty")
	}
}
