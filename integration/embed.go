// Package integration embeds the Claude Code integration assets (hook
// scripts, skills, launchd templates, the CLAUDE.md section and the
// settings.json snippet) so a copied or released binary can install and
// check them without a repo checkout (spec AC-34). install and doctor read
// assets only from FS, never from the working tree.
package integration

import "embed"

// FS holds the embedded assets, at their paths relative to this directory.
// Slice 2 adds the systemd templates.
//
//go:embed hooks skills launchd claude-md-section.md settings.snippet.json
var FS embed.FS

// Asset paths inside FS. Code that references an asset uses these names;
// embed_test.go asserts every one of them exists.
const (
	HookUserPromptSubmit = "hooks/user-prompt-submit.sh"
	HookSessionEnd       = "hooks/session-end.sh"
	ClaudeMDSection      = "claude-md-section.md"
	SettingsSnippet      = "settings.snippet.json"
	SkillsDir            = "skills"
	LaunchdDir           = "launchd"
	LaunchdCleanup       = "launchd/io.github.claude-memory.cleanup.plist"
	LaunchdIngestPR      = "launchd/io.github.claude-memory.ingest-pr.plist"
)

// HookScripts lists the hook wrapper scripts, in install order.
var HookScripts = []string{HookUserPromptSubmit, HookSessionEnd}

// Skills lists the skill directory names under SkillsDir.
var Skills = []string{"remember", "memory-digest"}

// SkillFile is the path of a skill's SKILL.md inside FS.
func SkillFile(name string) string { return SkillsDir + "/" + name + "/SKILL.md" }
