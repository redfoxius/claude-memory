package main

import (
	"errors"
	"reflect"
	"testing"

	"claude-memory/internal/setup"
)

func TestBuildPaths(t *testing.T) {
	t.Parallel()
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	base := setup.Paths{
		Home:            "/home/u",
		ClaudeDir:       "/home/u/.claude",
		ClaudeJSON:      "/home/u/.claude.json",
		ConfigDir:       "/home/u/.config/claude-memory",
		StateDir:        "/home/u/.local/state/claude-memory",
		ShareDir:        "/home/u/.local/share/claude-memory",
		BinDir:          "/home/u/.local/bin",
		LaunchAgentsDir: "/home/u/Library/LaunchAgents",
		SystemdUserDir:  "/home/u/.config/systemd/user",
		Cwd:             "/work/repo",
		Self:            "/home/u/.local/bin/claude-memory",
		UID:             501,
	}
	with := func(f func(*setup.Paths)) setup.Paths { p := base; f(&p); return p }

	cases := []struct {
		name    string
		getenv  func(string) string
		binDir  string
		want    setup.Paths
		wantErr error // errBadHome, or nil; other errors checked by wantAnyErr
		anyErr  bool
	}{
		{name: "defaults", getenv: env("HOME", "/home/u"), want: base},
		{name: "trailing slash on HOME", getenv: env("HOME", "/home/u/"), want: base},
		{name: "CLAUDE_CONFIG_DIR moves settings and .claude.json",
			getenv: env("HOME", "/home/u", "CLAUDE_CONFIG_DIR", "/cfg/claude"),
			want: with(func(p *setup.Paths) {
				p.ClaudeDir = "/cfg/claude"
				p.ClaudeJSON = "/cfg/claude/.claude.json"
			})},
		{name: "--bin-dir", getenv: env("HOME", "/home/u"), binDir: "/opt/bin/",
			want: with(func(p *setup.Paths) { p.BinDir = "/opt/bin" })},
		{name: "HOME unset", getenv: env(), wantErr: errBadHome},
		{name: "HOME relative", getenv: env("HOME", "home/u"), wantErr: errBadHome},
		{name: "CLAUDE_CONFIG_DIR relative", getenv: env("HOME", "/home/u", "CLAUDE_CONFIG_DIR", ".claude"), anyErr: true},
		{name: "--bin-dir relative", getenv: env("HOME", "/home/u"), binDir: "bin", anyErr: true},
	}
	for _, tc := range cases {
		got, err := buildPaths(tc.getenv, tc.binDir, 501, "/home/u/.local/bin/claude-memory", "/work/repo")
		switch {
		case tc.wantErr != nil:
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
			}
		case tc.anyErr:
			if err == nil || errors.Is(err, errBadHome) {
				t.Errorf("%s: err = %v, want a non-HOME error", tc.name, err)
			}
		case err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case !reflect.DeepEqual(got, tc.want):
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestBuildEnvAllowList(t *testing.T) {
	t.Parallel()
	got := buildEnv([]string{
		"MEMORY_PG_DSN=postgresql://u:p@h/db",
		"MEMORY_OLLAMA_URL=http://x=y", // value with '='
		"NO_COLOR=1", "CI=true", "PATH=/usr/bin:/bin", "XDG_RUNTIME_DIR=/run/user/501",
		"HOME=/home/u", "AWS_SECRET_ACCESS_KEY=nope", "CLAUDE_CONFIG_DIR=/c", "MEMORYX=no", "malformed",
	})
	want := setup.Env{
		"MEMORY_PG_DSN":     "postgresql://u:p@h/db",
		"MEMORY_OLLAMA_URL": "http://x=y",
		"NO_COLOR":          "1",
		"CI":                "true",
		"PATH":              "/usr/bin:/bin",
		"XDG_RUNTIME_DIR":   "/run/user/501",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildEnv = %#v, want %#v", got, want)
	}
}
