package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionFormatter(t *testing.T) {
	t.Parallel()
	bi := func(mainVersion string, settings ...string) *debug.BuildInfo {
		b := &debug.BuildInfo{Main: debug.Module{Path: "claude-memory", Version: mainVersion}}
		for i := 0; i+1 < len(settings); i += 2 {
			b.Settings = append(b.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
		}
		return b
	}
	cases := []struct {
		name  string
		ld    string
		bi    *debug.BuildInfo
		want  string
		short string
	}{
		{"nothing known", "", nil, "claude-memory dev", "dev"},
		{"devel module, no vcs", "", bi("(devel)"), "claude-memory dev", "dev"},
		{"ldflags only", "v0.3.0-4-gabc1234-dirty", nil, "claude-memory v0.3.0-4-gabc1234-dirty", "v0.3.0-4-gabc1234-dirty"},
		{"ldflags wins over module version", "v1.0.0", bi("v0.9.0", "vcs.revision", "0123456789abcdef0123", "vcs.modified", "false"),
			"claude-memory v1.0.0 (revision 0123456789ab)", "v1.0.0"},
		{"build info revision, dirty", "", bi("(devel)", "vcs.revision", "0123456789abcdef0123", "vcs.modified", "true"),
			"claude-memory dev (revision 0123456789ab, modified)", "dev"},
		{"module version from go install", "", bi("v0.2.1"), "claude-memory v0.2.1", "v0.2.1"},
		{"short revision kept whole", "", bi("", "vcs.revision", "abc123"), "claude-memory dev (revision abc123)", "dev"},
	}
	for _, tc := range cases {
		v := resolveVersion(tc.ld, tc.bi)
		if got := v.String(); got != tc.want {
			t.Errorf("%s: String() = %q, want %q", tc.name, got, tc.want)
		}
		if got := v.Short(); got != tc.short {
			t.Errorf("%s: Short() = %q, want %q", tc.name, got, tc.short)
		}
	}
}
