package setup

import (
	"bytes"
	"strings"
	"testing"

	"github.com/redfoxius/claude-memory/integration"
)

func TestRenderHookScript(t *testing.T) {
	t.Parallel()
	for _, name := range []string{integration.HookUserPromptSubmit, integration.HookSessionEnd} {
		raw := asset(t, name)
		out, err := RenderHookScript(raw, "/opt/x/claude-memory")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Contains(out, []byte(`CLAUDE_MEMORY_BIN="${CLAUDE_MEMORY_BIN:-/opt/x/claude-memory}"`)) {
			t.Errorf("%s: default not replaced:\n%s", name, out)
		}
		if bytes.Contains(out, []byte("$HOME/.local/bin/claude-memory")) {
			t.Errorf("%s: old default still present", name)
		}
		// Only that one line differs.
		want := bytes.Replace(raw, []byte("$HOME/.local/bin/claude-memory"), []byte("/opt/x/claude-memory"), 1)
		if !bytes.Equal(out, want) {
			t.Errorf("%s: rendering changed more than the default", name)
		}
		// Rendering twice with the same path is stable, and re-rendering a
		// rendered script for another path works (the line is still there).
		again, err := RenderHookScript(out, "/opt/y/claude-memory")
		if err != nil || !bytes.Contains(again, []byte(":-/opt/y/claude-memory}")) {
			t.Errorf("%s: re-render: %v", name, err)
		}
	}
}

func TestRenderHookScriptErrors(t *testing.T) {
	t.Parallel()
	raw := asset(t, integration.HookSessionEnd)
	for _, bin := range []string{"", "claude-memory", "rel/bin/claude-memory", `/opt/"x/cm`, "/opt/$HOME/cm", "/opt/`id`/cm", `/opt/a\b/cm`, "/opt/a\nb/cm"} {
		if _, err := RenderHookScript(raw, bin); err == nil {
			t.Errorf("binPath %q: want an error", bin)
		}
	}
	_, err := RenderHookScript([]byte("#!/bin/sh\necho hi\n"), "/opt/x/cm")
	if err == nil || !strings.Contains(err.Error(), "CLAUDE_MEMORY_BIN") {
		t.Errorf("missing line: err = %v", err)
	}
}
