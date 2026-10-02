package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeClaude is a ClaudeCLI that behaves like `claude mcp add|remove --scope
// user`: it records each call and edits the .claude.json at Paths.ClaudeJSON
// directly (Claude Code's own file, not through the FS port), so a re-Detect
// sees the registration. It is the only writer of that file in the tests.
type fakeClaude struct {
	t     testing.TB
	path  string // Paths.ClaudeJSON
	mu    sync.Mutex
	calls []string
	// failAdd / failRemove make the call fail.
	failAdd, failRemove error
	// before, when set, runs at the start of every call (a test hook).
	before func()
}

func newFakeClaude(t testing.TB, p Paths) *fakeClaude { return &fakeClaude{t: t, path: p.ClaudeJSON} }

// Calls returns the recorded calls, e.g. "add claude-memory -- /bin serve".
func (c *fakeClaude) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func (c *fakeClaude) load() map[string]any {
	top := map[string]any{}
	if b, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(b, &top)
	}
	return top
}

func (c *fakeClaude) save(top map[string]any) error {
	b, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o600)
}

func (c *fakeClaude) servers(top map[string]any) map[string]any {
	s, _ := top["mcpServers"].(map[string]any)
	if s == nil {
		s = map[string]any{}
		top["mcpServers"] = s
	}
	return s
}

// MCPAdd implements ClaudeCLI.
func (c *fakeClaude) MCPAdd(_ context.Context, name string, argv []string) error {
	if c.before != nil {
		c.before()
	}
	c.mu.Lock()
	c.calls = append(c.calls, fmt.Sprintf("add %s -- %s", name, strings.Join(argv, " ")))
	c.mu.Unlock()
	if c.failAdd != nil {
		return c.failAdd
	}
	top := c.load()
	srv := c.servers(top)
	if _, dup := srv[name]; dup {
		return errors.New("MCP server " + name + " already exists in user config")
	}
	srv[name] = map[string]any{"type": "stdio", "command": argv[0], "args": argv[1:], "env": map[string]any{}}
	return c.save(top)
}

// MCPRemove implements ClaudeCLI.
func (c *fakeClaude) MCPRemove(_ context.Context, name string) error {
	if c.before != nil {
		c.before()
	}
	c.mu.Lock()
	c.calls = append(c.calls, "remove "+name)
	c.mu.Unlock()
	if c.failRemove != nil {
		return c.failRemove
	}
	top := c.load()
	srv := c.servers(top)
	if _, ok := srv[name]; !ok {
		return errors.New("no MCP server found with name: " + name)
	}
	delete(srv, name)
	return c.save(top)
}
