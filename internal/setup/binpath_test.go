package setup

import (
	"path/filepath"
	"testing"
)

func TestResolveBinPath(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	recorded := &Manifest{Artifacts: []Artifact{
		{Step: "hooks.scripts", Kind: KindFile, Path: "/other/file"},
		{Step: BinaryStepName, Kind: KindFile, Path: "/opt/x/claude-memory"},
	}}
	relative := &Manifest{Artifacts: []Artifact{{Step: BinaryStepName, Kind: KindFile, Path: "rel/claude-memory"}}}
	cases := []struct {
		name     string
		m        *Manifest
		explicit bool
		want     string
	}{
		{"no manifest", nil, false, p.InstalledBinary()},
		{"manifest without binary", &Manifest{}, false, p.InstalledBinary()},
		{"manifest records binary", recorded, false, "/opt/x/claude-memory"},
		{"explicit --bin-dir beats the manifest", recorded, true, p.InstalledBinary()},
		{"relative recorded path ignored", relative, false, p.InstalledBinary()},
	}
	for _, tc := range cases {
		if got := ResolveBinPath(p, tc.m, tc.explicit); got != tc.want {
			t.Errorf("%s: ResolveBinPath = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got, want := filepath.Dir(ResolveBinPath(p, recorded, false)), "/opt/x"; got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}
