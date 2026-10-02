package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Doctor checks for the Claude Code integration: mcp.registered,
// hooks.scripts, hooks.settings (incl. AC-70), skills, claude-md (AC-40,
// AC-41, AC-58, AC-67, AC-70). Everything here is a file read: the
// registered MCP command, the hook scripts and the claude CLI are never run.

// Embedded asset paths, relative to DoctorDeps.Assets (package integration
// keeps the same names; its embed_test asserts they exist).
const (
	assetHookUserPromptSubmit = "hooks/" + HookScriptUserPromptSubmit
	assetHookSessionEnd       = "hooks/" + HookScriptSessionEnd
	assetSkillsDir            = "skills"
	assetClaudeMDSection      = "claude-md-section.md"
)

// SkillNames are the skills install copies (integration.Skills).
var SkillNames = []string{"remember", "memory-digest"}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (d *doctor) manifestM() *Manifest { return d.manifest.Manifest }

// recordedFileHash returns the sha256 the manifest recorded for a file
// artifact at p ("" when none).
func (d *doctor) recordedFileHash(p string) string {
	for _, a := range d.manifestM().Find(KindFile) {
		if a.Path == p {
			return a.SHA256
		}
	}
	return ""
}

// fileState compares an installed file with the manifest record (when
// present) and the embedded version: ok, outdated (ours, unchanged, older)
// or modified (differs from both).
func fileState(installed []byte, embedded []byte, recorded string) (State, string) {
	h := sha256Hex(installed)
	switch {
	case recorded != "" && h == recorded && embedded != nil && h != sha256Hex(embedded):
		return StateOutdated, "an older version written by install"
	case recorded != "" && h == recorded:
		return StateOK, "as installed"
	case embedded != nil && h == sha256Hex(embedded):
		return StateOK, "matches this version"
	case recorded != "":
		return StateModified, "edited since install"
	default:
		return StateModified, "differs from this version (edited, or a manual install of another version)"
	}
}

// ---- mcp.registered ----------------------------------------------------------

func (d *doctor) checkMCP(context.Context) (Status, string, string) {
	reg, err := ReadMCPRegistration(d.FS, d.Paths)
	bin := d.binPath
	addCmd := "claude mcp add --scope user " + MCPServerName + " -- " + bin + " serve"
	if err != nil {
		return StatusFail, "cannot read " + d.Paths.ClaudeJSON + ": " + d.redact(err.Error()), "check the file's permissions"
	}
	if reg.ParseError != nil {
		return StatusWarn, d.redact(reg.ParseError.Error()) + " (registration unknown)",
			"Claude Code owns this file and rewrites it; restart Claude Code, then re-run doctor"
	}
	st, detail := reg.State(bin)
	var warns []string
	status := StatusPass
	remedy := ""
	switch st {
	case StateAbsent:
		return StatusFail, detail, addCmd
	case StateOutdated, StateModified:
		status = StatusWarn
		warns = append(warns, detail)
		remedy = "claude mcp remove --scope user " + MCPServerName + " && " + addCmd
	default:
		warns = append(warns, detail)
	}
	if !reg.Malformed && reg.Server.Command != "" {
		cmd := ExpandHome(reg.Server.Command, d.Paths.Home)
		if filepath.IsAbs(cmd) {
			info, err := d.FS.Stat(cmd)
			switch {
			case err != nil:
				msg := "the registered command " + cmd + " does not exist: Claude Code cannot start the MCP server"
				if st == StateOutdated {
					msg += "; " + detail
				}
				return StatusFail, msg,
					"install the binary there (make install), or re-register: claude mcp remove --scope user " + MCPServerName + " && " + addCmd
			case info.Mode()&0o111 == 0:
				return StatusFail, "the registered command " + cmd + " is not executable", "chmod 755 " + cmd
			}
		}
	}
	if len(reg.LocalShadows) > 0 {
		status = StatusWarn
		warns = append(warns, "a local-scope "+MCPServerName+" entry overrides the user scope in: "+strings.Join(capList(reg.LocalShadows, 3), ", "))
		if remedy == "" {
			remedy = "in each listed project: claude mcp remove --scope local " + MCPServerName
		}
	}
	return status, strings.Join(warns, "; "), remedy
}

// ---- hooks.scripts -------------------------------------------------------------

func (d *doctor) checkHookScripts(context.Context) (Status, string, string) {
	dir := d.Paths.HookScriptsDir()
	var fails, warns, oks []string
	var failRemedy string
	for _, s := range []struct{ name, asset string }{
		{HookScriptUserPromptSubmit, assetHookUserPromptSubmit},
		{HookScriptSessionEnd, assetHookSessionEnd},
	} {
		p := filepath.Join(dir, s.name)
		info, err := d.FS.Stat(p)
		if err != nil {
			fails = append(fails, s.name+" missing")
			failRemedy = "mkdir -p " + dir + " && cp integration/hooks/*.sh " + dir + "/ && chmod 755 " + dir + "/*.sh (integration/INSTALL.md step 5)"
			continue
		}
		if info.Mode()&0o111 == 0 {
			fails = append(fails, s.name+" is not executable")
			if failRemedy == "" {
				failRemedy = "chmod 755 " + p
			}
			continue
		}
		b, err := d.FS.ReadFile(p)
		if err != nil {
			fails = append(fails, s.name+": "+err.Error())
			continue
		}
		if m := hookBinRe.FindSubmatch(b); m != nil {
			bin := ExpandHome(strings.TrimSpace(string(m[1])), d.Paths.Home)
			if filepath.IsAbs(bin) {
				if bi, err := d.FS.Stat(bin); err != nil || bi.Mode()&0o111 == 0 {
					fails = append(fails, s.name+" runs "+bin+", which does not exist or is not executable (the hook then silently does nothing)")
					if failRemedy == "" {
						failRemedy = "install the binary there (make install), or set CLAUDE_MEMORY_BIN"
					}
					continue
				}
			}
		}
		raw, _ := fs.ReadFile(d.Assets, s.asset)
		embedded, rerr := RenderHookScript(raw, d.binPath)
		if rerr != nil {
			warns = append(warns, s.name+": cannot render this version for "+d.binPath+": "+rerr.Error())
			continue
		}
		st, why := fileState(b, embedded, d.recordedFileHash(p))
		switch {
		case st == StateOK:
			oks = append(oks, s.name)
		case sha256Hex(b) == sha256Hex(raw):
			// A manual install of this version: the raw script, which runs
			// $HOME/.local/bin/claude-memory unless CLAUDE_MEMORY_BIN is set.
			oks = append(oks, s.name+" (manual install)")
		default:
			warns = append(warns, s.name+": "+string(st)+", "+why)
		}
	}
	switch {
	case len(fails) > 0:
		return StatusFail, joinDetail(strings.Join(fails, "; "), warns), failRemedy
	case len(warns) > 0:
		return StatusWarn, strings.Join(warns, "; "),
			"compare with integration/hooks/ (`diff`) and copy this version's scripts over if the change was not deliberate"
	}
	return pass(strings.Join(oks, ", ") + " in " + dir)
}

// ---- hooks.settings --------------------------------------------------------------

func (d *doctor) checkHookSettings(context.Context) (Status, string, string) {
	settingsPath := d.Paths.SettingsJSON()
	snippet := "merge integration/settings.snippet.json into " + settingsPath + " by hand (integration/INSTALL.md step 5)"
	f, err := ReadSettingsFileFollow(d.FS, d.Paths.Home, settingsPath)
	if err != nil {
		return StatusFail, d.redact(err.Error()), "fix the file so it is strict JSON (no comments, no trailing commas), or " + snippet
	}
	if !f.Exists {
		return StatusFail, settingsPath + " does not exist: no hooks are wired", snippet
	}
	recorded := d.manifestM().RecordedHooks(settingsPath)
	a, err := AnalyzeSettings(f.Content, DesiredHooks(d.Paths.HookScriptsDir()), recorded)
	if err != nil {
		var sr *SettingsRefusal
		if errors.As(err, &sr) {
			sr.Path = settingsPath
		}
		return StatusFail, d.redact(err.Error()), "fix " + settingsPath + " (strict JSON, \"hooks\" an object of arrays)"
	}

	var fails, warns []string
	if f.OutsideHome {
		warns = append(warns, settingsPath+" is a symlink to "+f.Target+", outside "+d.Paths.Home+"; `claude-memory install` will refuse to edit it (AC-38)")
	}
	for _, ev := range a.Events {
		switch {
		case ev.State == StateAbsent:
			fails = append(fails, "no "+ev.Event+" entry")
		case len(ev.Ours) > 1:
			warns = append(warns, fmt.Sprintf("%s: %d claude-memory entries (the hook runs %d times)", ev.Event, len(ev.Ours), len(ev.Ours)))
		case ev.State == StateModified && ev.Ours[0].Legacy:
			warns = append(warns, ev.Event+": legacy $HOME/... entry from a manual install")
		case ev.State != StateOK:
			warns = append(warns, ev.Event+": "+string(ev.State)+", "+ev.Detail)
		}
		for _, o := range ev.Ours {
			if p, ok := hookCommandPath(o.Command, d.Paths.Home); ok {
				if _, err := d.FS.Stat(p); err != nil {
					fails = append(fails, ev.Event+" runs "+p+", which does not exist")
				}
			}
		}
	}
	for _, s := range a.Stray {
		warns = append(warns, "claude-memory entry under "+s.Event+" (not an event install manages)")
	}
	other := ScanOtherSettings(d.FS, d.Paths)
	for _, dup := range other.Duplicates {
		warns = append(warns, "duplicate claude-memory hook in "+dup.Path+" ("+dup.Event+"): it would fire twice")
	}
	for _, p := range slices.Sorted(maps.Keys(other.Unreadable)) {
		warns = append(warns, p+" is not readable strict JSON")
	}

	switch {
	case len(fails) > 0:
		return StatusFail, joinDetail(strings.Join(fails, "; "), warns), snippet
	case len(warns) > 0:
		return StatusWarn, strings.Join(warns, "; "),
			"keep exactly one claude-memory entry per event, in " + settingsPath + " only (the legacy $HOME form works; `claude-memory install` will offer to replace it)"
	}
	return pass("one claude-memory entry each for " + EventUserPromptSubmit + " and " + EventSessionEnd)
}

// hookCommandPath returns the script/binary path a hook command runs, with
// $HOME expanded, when its first word is an absolute path.
func hookCommandPath(cmd, home string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return "", false
	}
	p := ExpandHome(fields[0], home)
	return p, filepath.IsAbs(p)
}

// ---- skills ----------------------------------------------------------------------

func (d *doctor) checkSkills(context.Context) (Status, string, string) {
	var warns, oks []string
	for _, name := range SkillNames {
		root := path.Join(assetSkillsDir, name)
		var files []string
		_ = fs.WalkDir(d.Assets, root, func(p string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		if len(files) == 0 {
			warns = append(warns, name+": not embedded in this binary")
			continue
		}
		installedDir := filepath.Join(d.Paths.SkillsDir(), name)
		if _, err := d.FS.Stat(installedDir); err != nil {
			warns = append(warns, name+": not installed")
			continue
		}
		var issues []string
		for _, ap := range files {
			rel := strings.TrimPrefix(ap, root+"/")
			ip := filepath.Join(installedDir, filepath.FromSlash(rel))
			b, err := d.FS.ReadFile(ip)
			if err != nil {
				issues = append(issues, rel+" missing")
				continue
			}
			embedded, _ := fs.ReadFile(d.Assets, ap)
			if st, why := fileState(b, embedded, d.recordedFileHash(ip)); st != StateOK {
				issues = append(issues, rel+" "+string(st)+" ("+why+")")
			}
		}
		if len(issues) > 0 {
			warns = append(warns, name+": "+strings.Join(issues, ", "))
		} else {
			oks = append(oks, name)
		}
	}
	if len(warns) > 0 {
		return StatusWarn, joinDetail(strings.Join(warns, "; "), nil),
			"copy integration/skills/<name> to " + d.Paths.SkillsDir() + "/ (integration/INSTALL.md step 6), unless you edited them on purpose"
	}
	return pass(strings.Join(oks, ", ") + " match this version")
}

// ---- claude-md -------------------------------------------------------------------

func (d *doctor) checkClaudeMD(context.Context) (Status, string, string) {
	target := filepath.Join(d.Paths.ClaudeDir, "CLAUDE.md")
	recordedHash := ""
	if blocks := d.manifestM().Find(KindMDBlock); len(blocks) > 0 {
		target, recordedHash = blocks[0].Path, blocks[0].SHA256
	}
	section, err := fs.ReadFile(d.Assets, assetClaudeMDSection)
	if err != nil {
		return StatusInfo, "the CLAUDE.md section is not embedded in this binary", ""
	}
	b, err := d.FS.ReadFile(target)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return StatusInfo, "cannot read " + target + ": " + err.Error(), ""
	}
	st, why, err := MDBlockState(b, exists, string(section), recordedHash)
	remedy := "add integration/claude-md-section.md to " + target + " between the lines " + MDBeginMarker + " and " + MDEndMarker +
		" (or keep your section in a project CLAUDE.md)"
	switch {
	case err != nil:
		return StatusInfo, target + ": " + err.Error(), "fix the claude-memory markers by hand (one BEGIN line, then one END line)"
	case st == StateOK:
		return pass("memory block up to date in " + target)
	case st == StateModified:
		return StatusInfo, target + ": " + why, "replace the block (or the pasted section) with integration/claude-md-section.md between the markers"
	}
	return StatusInfo, target + ": " + why, remedy
}
