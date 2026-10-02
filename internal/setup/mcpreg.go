package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sort"
	"strings"
)

// MCP registration reader (spec AC-40, AC-67; plan WI-S1-9, Design 11).
//
// Registration is detected by reading Claude Code's .claude.json
// (Paths.ClaudeJSON: $CLAUDE_CONFIG_DIR/.claude.json, else ~/.claude.json)
// read-only. `claude mcp get|list` are never run: they health-check each
// server by spawning its command, which for us is `claude-memory serve`
// (opens the database and applies migrations). Nothing here takes a Runner,
// and nothing writes .claude.json; slice 2 writes only through
// `claude mcp add|remove --scope user`.
//
// The documented shape (verified with claude 2.1.287, WI-S1-0):
//
//	{"mcpServers": {"claude-memory": {"type": "stdio", "command": "<bin>",
//	  "args": ["serve"], "env": {}}},
//	 "projects": {"<abs path>": {"mcpServers": {...}}}}   // local scope
//
// The reader is tolerant: a missing file or key is absent; an unparseable
// file is absent with ParseError set (doctor warns).

// MCPServerName is the name we register under.
const MCPServerName = "claude-memory"

// MCPServer is one mcpServers entry.
type MCPServer struct {
	Type    string // "stdio" ("" when the key is missing)
	Command string
	Args    []string
	Env     map[string]string
}

// ExpectedMCPServer is the entry `claude mcp add --scope user claude-memory
// -- <bin> serve` creates.
func ExpectedMCPServer(bin string) MCPServer {
	return MCPServer{Type: "stdio", Command: bin, Args: []string{"serve"}, Env: map[string]string{}}
}

// MCPRegistration is what .claude.json says about our server.
type MCPRegistration struct {
	Path       string
	FileExists bool
	// ParseError is set when the file exists but is not valid JSON (or its
	// mcpServers is not an object); the registration then counts as absent.
	ParseError error
	Present    bool // a user-scope mcpServers["claude-memory"] exists
	// Malformed is set when the entry exists but does not have the
	// documented shape (e.g. args is not a string array).
	Malformed bool
	Server    MCPServer
	// LocalShadows lists project paths whose local-scope mcpServers holds
	// a "claude-memory" entry (it overrides the user scope there).
	LocalShadows []string
}

type claudeJSONFile struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	Projects   map[string]json.RawMessage `json:"projects"`
}

type claudeJSONProject struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

type mcpServerJSON struct {
	Type    *string           `json:"type"`
	Command *string           `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// ReadMCPRegistration reads Paths.ClaudeJSON read-only. Only an I/O error
// other than "not found" is returned as an error.
func ReadMCPRegistration(fsys ReadFS, p Paths) (MCPRegistration, error) {
	reg := MCPRegistration{Path: p.ClaudeJSON}
	b, err := fsys.ReadFile(p.ClaudeJSON)
	if errors.Is(err, fs.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return reg, err
	}
	reg.FileExists = true
	parseMCPRegistration(b, &reg)
	return reg, nil
}

func parseMCPRegistration(b []byte, reg *MCPRegistration) {
	if len(bytes.TrimSpace(b)) == 0 {
		return
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		reg.ParseError = fmt.Errorf("parse %s: %w", reg.Path, err)
		return
	}
	var f claudeJSONFile
	if raw, ok := top["mcpServers"]; ok && !isJSONNull(raw) {
		if err := json.Unmarshal(raw, &f.MCPServers); err != nil {
			reg.ParseError = fmt.Errorf("parse %s: mcpServers is not an object", reg.Path)
		}
	}
	if raw, ok := top["projects"]; ok && !isJSONNull(raw) {
		_ = json.Unmarshal(raw, &f.Projects) // a malformed projects map is ignored
	}

	if raw, ok := f.MCPServers[MCPServerName]; ok {
		reg.Present = true
		var s mcpServerJSON
		if err := json.Unmarshal(raw, &s); err != nil || s.Command == nil {
			reg.Malformed = true
		} else {
			reg.Server = MCPServer{Command: *s.Command, Args: s.Args, Env: s.Env}
			if s.Type != nil {
				reg.Server.Type = *s.Type
			}
		}
	}
	for path, raw := range f.Projects {
		var pr claudeJSONProject
		if json.Unmarshal(raw, &pr) != nil {
			continue
		}
		if _, ok := pr.MCPServers[MCPServerName]; ok {
			reg.LocalShadows = append(reg.LocalShadows, path)
		}
	}
	sort.Strings(reg.LocalShadows)
}

func isJSONNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// State compares the user-scope registration with the one install makes
// for bin (AC-40): absent when missing (or the file is unparseable), ok
// when type, command, args and env all match; modified when only the env
// differs (command and args are ours, a user added env); else outdated with
// the differing fields named. Env values are never shown, only key names (an
// env could hold a DSN). Local-scope shadows do not change the state;
// doctor reports them separately.
func (r MCPRegistration) State(bin string) (State, string) {
	switch {
	case r.ParseError != nil:
		return StateAbsent, "unreadable: " + r.ParseError.Error()
	case !r.Present:
		if !r.FileExists {
			return StateAbsent, r.Path + " does not exist"
		}
		return StateAbsent, "no " + MCPServerName + " server in " + r.Path
	case r.Malformed:
		return StateOutdated, "entry does not have the expected shape {type, command, args, env}"
	}
	want := ExpectedMCPServer(bin)
	var diffs []string
	if r.Server.Type != "" && r.Server.Type != want.Type {
		diffs = append(diffs, fmt.Sprintf("type %q, want %q", r.Server.Type, want.Type))
	}
	if r.Server.Command != want.Command {
		diffs = append(diffs, fmt.Sprintf("command %s, want %s", r.Server.Command, want.Command))
	}
	if !slices.Equal(r.Server.Args, want.Args) {
		diffs = append(diffs, fmt.Sprintf("args %q, want %q", r.Server.Args, want.Args))
	}
	if len(diffs) > 0 {
		if len(r.Server.Env) > 0 {
			diffs = append(diffs, "env sets "+strings.Join(slices.Sorted(maps.Keys(r.Server.Env)), ", ")+" (install sets none)")
		}
		return StateOutdated, strings.Join(diffs, "; ")
	}
	if len(r.Server.Env) > 0 {
		// Command and args are ours; the env was added by the user (e.g. a DSN
		// via `claude mcp add -e`). That is hand customization, not an older
		// install: kept unless the user confirms the overwrite.
		return StateModified, "env sets " + strings.Join(slices.Sorted(maps.Keys(r.Server.Env)), ", ") + " (install sets none)"
	}
	return StateOK, "registered: " + want.Command + " serve"
}
