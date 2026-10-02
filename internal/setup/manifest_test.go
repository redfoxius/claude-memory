package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testManifest(p Paths) *Manifest {
	settings := p.SettingsJSON()
	return &Manifest{
		Schema:          ManifestSchema,
		BinaryVersion:   "v0.3.0-4-gabc1234",
		Platform:        ManifestPlatform{OS: OSDarwin, Arch: "arm64"},
		Topology:        "remote",
		JobsBackend:     JobsLaunchd,
		ClaudeConfigDir: p.ClaudeDir,
		InstalledAt:     testNow,
		UpdatedAt:       testNow,
		Artifacts: []Artifact{
			{Step: "hooks.scripts", Kind: KindFile, Path: filepath.Join(p.HookScriptsDir(), HookScriptUserPromptSubmit),
				SHA256: strings.Repeat("ab", 32), Version: "v0.3.0-4-gabc1234"},
			{Step: "hooks.settings", Kind: KindSettingsHook, Path: settings, Identity: EventUserPromptSubmit,
				Entry: DesiredHooks(p.HookScriptsDir())[0].Canonical(), Version: "v0.3.0-4-gabc1234", CreatedContainer: true},
			{Step: "hooks.settings", Kind: KindSettingsHook, Path: settings, Identity: EventSessionEnd,
				Entry: DesiredHooks(p.HookScriptsDir())[1].Canonical(), Version: "v0.3.0-4-gabc1234"},
			{Step: "mcp", Kind: KindMCP, Identity: MCPServerName, Version: "v0.3.0-4-gabc1234"},
			{Step: "claude-md", Kind: KindMDBlock, Path: filepath.Join(p.ClaudeDir, "CLAUDE.md"),
				SHA256: MDSectionHash(testSection), Version: "v0.3.0-4-gabc1234"},
			{Step: "envfile", Kind: KindEnvKey, Path: p.EnvFile(), Identity: "MEMORY_PG_DSN", Version: "v0.3.0-4-gabc1234"},
		},
	}
}

func TestManifestRoundTrip(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))

	l, err := LoadManifest(fsys, p)
	if err != nil || l.Present() || l.Corrupt {
		t.Fatalf("missing manifest: %+v, %v", l, err)
	}

	m := testManifest(p)
	if err := SaveManifest(fsys, p, m); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{p.Manifest(): 0o600, p.ConfigDir: 0o700} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v, %v; want mode %v", path, info, err, want)
		}
	}
	// Golden with the temp root replaced, so the schema is pinned (AC-49).
	b, _ := os.ReadFile(p.Manifest())
	checkGolden(t, "testdata/manifest/install.golden.json",
		[]byte(strings.ReplaceAll(string(b), filepath.Dir(p.Home), "/ROOT")))

	l, err = LoadManifest(fsys, p)
	if err != nil || !l.Present() {
		t.Fatalf("load: %+v, %v", l, err)
	}
	got := l.Manifest
	if got.BinaryVersion != m.BinaryVersion || !got.InstalledAt.Equal(testNow) || len(got.Artifacts) != len(m.Artifacts) {
		t.Errorf("round trip lost data: %+v", got)
	}
	rec := got.RecordedHooks(p.SettingsJSON())
	if rec[EventUserPromptSubmit] != DesiredHooks(p.HookScriptsDir())[0].Canonical() || len(rec) != 2 {
		t.Errorf("RecordedHooks = %v", rec)
	}
	if !got.CreatedHooksKey(p.SettingsJSON()) || got.CreatedHooksKey("/other/settings.json") {
		t.Error("CreatedHooksKey")
	}
	if a, ok := got.Lookup(KindMCP, "", MCPServerName); !ok || a.Step != "mcp" {
		t.Errorf("Lookup(mcp) = %+v, %v", a, ok)
	}
	if n := len(got.Find(KindSettingsHook)); n != 2 {
		t.Errorf("Find(settings-hook) = %d", n)
	}
}

func TestManifestCorruptAndUnsupported(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"unparseable": `{"schema": 1, "artifacts": [`,
		"unsupported": `{"schema": 2, "artifacts": []}`,
		"no kind":     `{"schema": 1, "artifacts": [{"step": "x"}]}`,
		"secret":      `{"schema": 1, "artifacts": [{"step": "envfile", "kind": "env-key", "identity": "postgresql://u:S3ntinel-pw@h/db"}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := testPaths(t)
			fsys := NewFakeFS(t, filepath.Dir(p.Home))
			writeTestFile(t, p.Manifest(), []byte(content), 0o600)
			l, err := LoadManifest(fsys, p)
			if err != nil || l.Present() || !l.Corrupt || l.Err == nil {
				t.Fatalf("load = %+v, %v; want corrupt", l, err)
			}
			if strings.Contains(l.Err.Error(), "S3ntinel") {
				t.Errorf("error leaks the secret: %v", l.Err)
			}
			backup, err := BackupCorruptManifest(fsys, NewFakeClock(testNow), p)
			if err != nil || backup != p.Manifest()+".corrupt.20261001T101500Z" {
				t.Fatalf("backup = %q, %v", backup, err)
			}
			if b, _ := os.ReadFile(backup); string(b) != content {
				t.Errorf("backup content %q", b)
			}
		})
	}
}

func TestManifestRefusesSecrets(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))
	m := testManifest(p)
	m.Artifacts[0].Path = "postgresql://claude_memory:S3ntinel-pw@db/claude_memory"
	if err := SaveManifest(fsys, p, m); err == nil {
		t.Fatal("SaveManifest accepted a URL password")
	}
	if len(fsys.Writes()) != 0 {
		t.Errorf("writes = %v", fsys.Writes())
	}
}

func TestManifestUpsertDropTrimAndRetained(t *testing.T) {
	t.Parallel()
	m := &Manifest{}
	a := Artifact{Step: "hooks.scripts", Kind: KindFile, Path: "/h/a.sh", Version: "v1"}
	if !m.Upsert(a) || m.Upsert(a) {
		t.Fatal("Upsert must report a change once")
	}
	a2 := a
	a2.SHA256 = "abc"
	if !m.Upsert(a2) || len(m.Artifacts) != 1 || m.Artifacts[0].SHA256 != "abc" {
		t.Fatalf("hash change not recorded: %+v", m.Artifacts)
	}
	m.Upsert(Artifact{Step: "envfile", Kind: KindEnvKey, Path: "/e", Identity: "K"})
	if !m.Trim([]ArtifactKey{a.Key(), {Kind: KindFile, Path: "/nope"}}) || len(m.Artifacts) != 1 {
		t.Fatalf("Trim: %+v", m.Artifacts)
	}
	p := Paths{ClaudeDir: "/home/u/.claude"}
	for _, c := range []struct {
		a    Artifact
		want bool
	}{
		{Artifact{Kind: KindEnvKey}, true},
		{Artifact{Kind: KindDir, Path: "/home/u/.config/claude-memory"}, true},
		{Artifact{Kind: KindDir, Path: "/home/u/.local/bin"}, true},
		{Artifact{Kind: KindDir, Path: "/home/u/.claude/hooks/claude-memory"}, false},
		{Artifact{Kind: KindDir, Path: "/home/u/.claude/skills/remember"}, false},
		{Artifact{Kind: KindDir, Path: "/home/u/.claudex/skills"}, true},
		{Artifact{Kind: KindFile, Path: "/home/u/.claude/x"}, false},
	} {
		if got := ArtifactRetained(c.a, p); got != c.want {
			t.Errorf("ArtifactRetained(%+v) = %v, want %v", c.a, got, c.want)
		}
	}
}
