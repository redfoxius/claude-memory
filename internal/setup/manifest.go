package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// Install manifest, <ConfigDir>/install.json (spec AC-49, §9; plan
// WI-S1-10 read, WI-S2-1 write). It records every artifact install created
// so a re-run can tell outdated (ours, unchanged) from modified (edited)
// and uninstall can reverse exactly what was done. It never stores secrets.

// ManifestSchema is the schema version this binary reads and writes.
const ManifestSchema = 1

// ManifestMode is the manifest's file mode (its directory is 0700).
const ManifestMode fs.FileMode = 0o600

// ArtifactKind is the kind of one recorded artifact (AC-49).
type ArtifactKind string

// Artifact kinds.
const (
	KindFile         ArtifactKind = "file"          // a file we wrote (hook script, skill file, binary)
	KindDir          ArtifactKind = "dir"           // a directory we created
	KindSettingsHook ArtifactKind = "settings-hook" // one hook entry in settings.json
	KindMCP          ArtifactKind = "mcp"           // the user-scope MCP registration
	KindLaunchd      ArtifactKind = "launchd"       // a launchd job (plist + loaded)
	KindSystemd      ArtifactKind = "systemd"       // a systemd user unit
	KindMDBlock      ArtifactKind = "md-block"      // the CLAUDE.md managed block
	KindEnvKey       ArtifactKind = "env-key"       // a managed key in the env file (never its value)
)

// Artifact is one thing install created or changed.
type Artifact struct {
	Step string       `json:"step"` // the step that wrote it, e.g. "hooks.settings"
	Kind ArtifactKind `json:"kind"`
	// Path is the file the artifact lives in (absolute); Identity names it
	// inside that file or system: the hook event for settings-hook, the
	// launchd label, the MCP server name, the env key.
	Path     string `json:"path,omitempty"`
	Identity string `json:"identity,omitempty"`
	// SHA256 (hex) of the content as written: the file, the md-block body
	// (MDSectionHash), the rendered unit. Empty for kinds without content.
	SHA256  string `json:"sha256,omitempty"`
	Version string `json:"version"` // binary version that wrote it
	// Entry is the canonical JSON of a settings-hook entry (AC-37).
	Entry string `json:"entry,omitempty"`
	// CreatedContainer is set on a settings-hook artifact when install
	// created the "hooks" object, so uninstall may remove it (AC-54).
	CreatedContainer bool `json:"created_container,omitempty"`
	// CreatedFile is set on a settings-hook artifact when install created
	// settings.json itself, so uninstall deletes it only when the unmerge
	// leaves {} (Design 23). CreatedContainer keeps its own meaning.
	CreatedFile bool `json:"created_file,omitempty"`
}

// Key returns the identity of a under which the manifest stores it.
func (a Artifact) Key() ArtifactKey {
	return ArtifactKey{Kind: a.Kind, Path: a.Path, Identity: a.Identity}
}

// ArtifactRetained reports whether uninstall drops a from the manifest
// without reversing it (Design 23): env-key artifacts, and dir artifacts
// outside <ClaudeDir> (<ConfigDir>, <StateDir>, <BinDir>). A dir under
// <ClaudeDir> (hooks/claude-memory, skills/<name>) is owned and reversed.
func ArtifactRetained(a Artifact, p Paths) bool {
	switch a.Kind {
	case KindEnvKey:
		return true
	case KindDir:
		return !pathUnder(a.Path, p.ClaudeDir)
	}
	return false
}

func pathUnder(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Upsert records a (replacing the artifact with the same key) and reports
// whether the artifact set or any recorded field changed.
func (m *Manifest) Upsert(a Artifact) bool {
	for i, have := range m.Artifacts {
		if have.Key() == a.Key() {
			if have == a {
				return false
			}
			m.Artifacts[i] = a
			return true
		}
	}
	m.Artifacts = append(m.Artifacts, a)
	return true
}

// Drop removes the artifact with key k and reports whether it was recorded.
func (m *Manifest) Drop(k ArtifactKey) bool {
	for i, have := range m.Artifacts {
		if have.Key() == k {
			m.Artifacts = append(m.Artifacts[:i:i], m.Artifacts[i+1:]...)
			return true
		}
	}
	return false
}

// Trim drops every key in keys and reports whether anything was dropped.
func (m *Manifest) Trim(keys []ArtifactKey) bool {
	changed := false
	for _, k := range keys {
		if m.Drop(k) {
			changed = true
		}
	}
	return changed
}

// ManifestPlatform is the platform the manifest was written on.
type ManifestPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Manifest is install.json.
type Manifest struct {
	Schema        int              `json:"schema"`
	BinaryVersion string           `json:"binary_version"`
	Platform      ManifestPlatform `json:"platform"`
	Topology      string           `json:"topology"`     // "local" | "remote" | ""
	JobsBackend   string           `json:"jobs_backend"` // launchd | systemd | none | ""
	// ClaudeConfigDir is the effective Paths.ClaudeDir at install time; a
	// re-run that sees another one warns (AC-49).
	ClaudeConfigDir string     `json:"claude_config_dir"`
	InstalledAt     time.Time  `json:"installed_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Artifacts       []Artifact `json:"artifacts"`
}

// Find returns the recorded artifacts of kind (all kinds when kind is "").
func (m *Manifest) Find(kind ArtifactKind) []Artifact {
	if m == nil {
		return nil
	}
	var out []Artifact
	for _, a := range m.Artifacts {
		if kind == "" || a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// Lookup returns the artifact of kind with that path and identity.
func (m *Manifest) Lookup(kind ArtifactKind, path, identity string) (Artifact, bool) {
	for _, a := range m.Find(kind) {
		if a.Path == path && a.Identity == identity {
			return a, true
		}
	}
	return Artifact{}, false
}

// RecordedHooks returns the canonical entries recorded for settingsPath,
// by event, for AnalyzeSettings / MergeSettings / UnmergeSettings.
func (m *Manifest) RecordedHooks(settingsPath string) RecordedHooks {
	out := RecordedHooks{}
	for _, a := range m.Find(KindSettingsHook) {
		if a.Path == settingsPath && a.Identity != "" && a.Entry != "" {
			out[a.Identity] = a.Entry
		}
	}
	return out
}

// CreatedHooksKey reports whether install recorded creating the "hooks"
// object of settingsPath.
func (m *Manifest) CreatedHooksKey(settingsPath string) bool {
	for _, a := range m.Find(KindSettingsHook) {
		if a.Path == settingsPath && a.CreatedContainer {
			return true
		}
	}
	return false
}

// Validate checks the schema and that no recorded string carries a URL
// password (the manifest never stores secrets, AC-49).
func (m *Manifest) Validate() error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("unsupported manifest schema %d (this binary reads %d)", m.Schema, ManifestSchema)
	}
	check := func(field, s string) error {
		if userinfoRe.MatchString(s) {
			return fmt.Errorf("manifest %s holds a URL with a password", field)
		}
		return nil
	}
	for _, f := range [][2]string{{"topology", m.Topology}, {"claude_config_dir", m.ClaudeConfigDir}} {
		if err := check(f[0], f[1]); err != nil {
			return err
		}
	}
	for i, a := range m.Artifacts {
		if a.Kind == "" {
			return fmt.Errorf("manifest artifact %d has no kind", i)
		}
		for _, s := range []string{a.Path, a.Identity, a.Entry} {
			if err := check(fmt.Sprintf("artifact %d", i), s); err != nil {
				return err
			}
		}
	}
	return nil
}

// ManifestLoad is the result of LoadManifest.
type ManifestLoad struct {
	Path     string
	Manifest *Manifest // nil when absent or corrupt
	// Corrupt is set when the file exists but cannot be used (unparseable,
	// unknown schema, invalid); it is then treated as absent and Err says
	// why. Slice 2 backs it up with BackupCorruptManifest.
	Corrupt bool
	Err     error
}

// Present reports whether a usable manifest was loaded.
func (l ManifestLoad) Present() bool { return l.Manifest != nil }

// LoadManifest reads Paths.Manifest() read-only. Only an I/O error other
// than "not found" is returned as an error; a corrupt file is reported in
// the result (AC-49: treated as absent, and reported).
func LoadManifest(fsys ReadFS, p Paths) (ManifestLoad, error) {
	l := ManifestLoad{Path: p.Manifest()}
	b, err := fsys.ReadFile(l.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&m); err != nil {
		l.Corrupt, l.Err = true, fmt.Errorf("parse %s: %w", l.Path, err)
		return l, nil
	}
	if err := m.Validate(); err != nil {
		l.Corrupt, l.Err = true, fmt.Errorf("%s: %w", l.Path, err)
		return l, nil
	}
	l.Manifest = &m
	return l, nil
}

// MarshalManifest renders m as install.json (indented, trailing newline).
func MarshalManifest(m *Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// SaveManifest writes m atomically at 0600 in a 0700 ConfigDir. The engine
// (slice 2) calls it only when the artifact set or a hash changed (AC-9,
// AC-50).
func SaveManifest(fsys FS, p Paths, m *Manifest) error {
	b, err := MarshalManifest(m)
	if err != nil {
		return err
	}
	if err := fsys.MkdirAll(filepath.Dir(p.Manifest()), 0o700); err != nil {
		return err
	}
	return fsys.WriteFileAtomic(p.Manifest(), b, ManifestMode)
}

// BackupCorruptManifest copies a corrupt manifest to
// install.json.corrupt.<UTC ts> (AC-49) and returns that path [S2].
func BackupCorruptManifest(fsys FS, clk Clock, p Paths) (string, error) {
	b, err := fsys.ReadFile(p.Manifest())
	if err != nil {
		return "", err
	}
	dst := p.Manifest() + ".corrupt." + clk.Now().UTC().Format("20060102T150405Z")
	return dst, fsys.WriteFileAtomic(dst, b, ManifestMode)
}
