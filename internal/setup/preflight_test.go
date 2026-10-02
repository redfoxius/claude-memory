package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightCorruptManifestBackedUpOnceWritable(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	if err := os.MkdirAll(h.p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.p.Manifest(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	wp := h.engine().Write
	res, err := Preflight(wp, Inputs{}, "v1.0.0", true)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Unlock()
	if res.Prior.Manifest != nil || !hasNote(res.Notes, "backed up as") {
		t.Fatalf("%+v", res)
	}
	matches, _ := filepath.Glob(h.p.Manifest() + ".corrupt.*")
	if len(matches) != 1 || !strings.HasSuffix(matches[0], ".corrupt.20261002T120000Z") {
		t.Fatalf("backups = %v", matches)
	}
	if b, _ := os.ReadFile(matches[0]); string(b) != "{not json" {
		t.Fatalf("backup content %q", b)
	}
}

func TestPreflightCorruptManifestOnlyReportedInDryRun(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	_ = os.MkdirAll(h.p.ConfigDir, 0o700)
	_ = os.WriteFile(h.p.Manifest(), []byte(`{"schema":99}`), 0o600)
	res, err := Preflight(h.engine().Write, Inputs{DryRun: true}, "v1.0.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unlock != nil || !hasNote(res.Notes, "not backed up") {
		t.Fatalf("%+v", res)
	}
	if w := h.fs.Writes(); len(w) != 0 {
		t.Fatalf("dry-run preflight wrote: %v (the lock included)", w)
	}
}

func TestPreflightLockOnlyWhenWritableAndHeldFails(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	wp := h.engine().Write
	res, err := Preflight(wp, Inputs{}, "v1", true)
	if err != nil || res.Unlock == nil {
		t.Fatalf("%v %+v", err, res)
	}
	if _, err := Preflight(wp, Inputs{}, "v1", true); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("second preflight err = %v", err)
	}
	_ = res.Unlock()
}

func TestPreflightConfigDirChangedAndEnvDoc(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	if err := SaveManifest(h.fs, h.p, &Manifest{Schema: 1, BinaryVersion: "dev", ClaudeConfigDir: "/other/.claude"}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(h.p.EnvFile(), []byte("A=1\n"), 0o600)
	res, err := Preflight(h.engine().Write, Inputs{DryRun: true}, "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ConfigDirChanged || !hasNote(res.Notes, "/other/.claude") || res.Downgrade {
		t.Fatalf("%+v", res)
	}
	if string(res.Prior.EnvDoc) != "A=1\n" || res.Prior.Manifest == nil {
		t.Fatalf("prior %+v", res.Prior)
	}
}

func TestIsDowngrade(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		recorded, running string
		want              bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.2.0", "v1.2.0", false},
		{"v1.2.0", "v1.3.0", false},
		{"v0.20.2-5-gabc1234", "v0.20.2-3-gdef5678", true},
		{"v0.20.2-5-gabc1234-dirty", "v0.20.2", true},
		{"v0.20.2", "v0.20.2-1-gabc1234", false},
		{"dev", "v1.0.0", false},
		{"v1.2.0", "dev", false},
		{"v1.2.0", "", false},
		{"garbage", "v1.0.0", false},
		{"2.0.0", "v1.9.9", true},
	} {
		if got := isDowngrade(c.recorded, c.running); got != c.want {
			t.Errorf("isDowngrade(%q, %q) = %v, want %v", c.recorded, c.running, got, c.want)
		}
	}
}
