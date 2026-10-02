package setup

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestReadPortsFieldTypesPinned pins the narrowing of Design 20: the port
// fields of ReadPorts are exactly the read interfaces, so widening one (to
// FS, DBProber, ...) fails here and the compiler then enforces the rest.
func TestReadPortsFieldTypesPinned(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeOf(ReadPorts{})
	want := map[string]reflect.Type{
		"FS":     reflect.TypeOf((*ReadFS)(nil)).Elem(),
		"DB":     reflect.TypeOf((*DBProbe)(nil)).Elem(),
		"Ollama": reflect.TypeOf((*OllamaProbe)(nil)).Elem(),
		"Jobs":   reflect.TypeOf((*JobDetector)(nil)).Elem(),
		"Runner": reflect.TypeOf((*Runner)(nil)).Elem(),
		"Clock":  reflect.TypeOf((*Clock)(nil)).Elem(),
	}
	for name, typ := range want {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Errorf("ReadPorts.%s missing", name)
			continue
		}
		if f.Type != typ {
			t.Errorf("ReadPorts.%s has type %v, want %v", name, f.Type, typ)
		}
	}
	// No field may smuggle in a write-capable port.
	forbidden := []reflect.Type{
		reflect.TypeOf((*FS)(nil)).Elem(),
		reflect.TypeOf((*DBProber)(nil)).Elem(),
		reflect.TypeOf((*OllamaProber)(nil)).Elem(),
		reflect.TypeOf((*JobManager)(nil)).Elem(),
		reflect.TypeOf((*ClaudeCLI)(nil)).Elem(),
		reflect.TypeOf(ProgressSink(nil)),
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		for _, bad := range forbidden {
			if f.Type == bad {
				t.Errorf("ReadPorts.%s has write-capable type %v", f.Name, f.Type)
			}
		}
	}
	// The read interfaces themselves expose no write method.
	for _, typ := range []reflect.Type{want["FS"], want["DB"], want["Ollama"], want["Jobs"]} {
		for _, bad := range []string{"WriteFileAtomic", "MkdirAll", "Remove", "Chmod", "Lock", "Migrate", "Pull", "Install"} {
			if _, ok := typ.MethodByName(bad); ok {
				t.Errorf("%v has write method %s", typ, bad)
			}
		}
	}
}

// TestWritePortsExtendReadPorts: the writable ports embed the read view and
// carry the write-capable counterparts.
func TestWritePortsExtendReadPorts(t *testing.T) {
	t.Parallel()
	wt := reflect.TypeOf(WritePorts{})
	for name, typ := range map[string]reflect.Type{
		"FS":        reflect.TypeOf((*FS)(nil)).Elem(),
		"DB":        reflect.TypeOf((*DBProber)(nil)).Elem(),
		"Ollama":    reflect.TypeOf((*OllamaProber)(nil)).Elem(),
		"Jobs":      reflect.TypeOf((*JobManager)(nil)).Elem(),
		"ClaudeCLI": reflect.TypeOf((*ClaudeCLI)(nil)).Elem(),
		"Progress":  reflect.TypeOf(ProgressSink(nil)),
	} {
		f, ok := wt.FieldByName(name)
		if !ok || f.Type != typ {
			t.Errorf("WritePorts.%s = %v (found %v), want %v", name, f.Type, ok, typ)
		}
	}
	if f, ok := wt.FieldByName("ReadPorts"); !ok || !f.Anonymous {
		t.Error("WritePorts must embed ReadPorts")
	}
}

// A ReadFS-only view must be accepted by every narrowed slice-1 helper (it
// compiles only if the helper parameters are ReadFS), and FakeFS in write
// mode must still satisfy the full FS.
type readOnlyView struct{ ReadFS }

var (
	_ FS     = (*FakeFS)(nil)
	_ ReadFS = readOnlyView{}
)

func TestNarrowedHelpersAcceptReadFS(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	ffs := NewFakeFS(t, filepath.Dir(p.Home))
	var rfs ReadFS = readOnlyView{ffs}

	if _, err := LoadManifest(rfs, p); err != nil {
		t.Errorf("LoadManifest: %v", err)
	}
	if _, _, err := InGitRepo(rfs, p.Cwd, filepath.Dir(p.Home)); err != nil {
		t.Errorf("InGitRepo: %v", err)
	}
	_, _ = ReadMCPRegistration(rfs, p)
	_, _ = ReadSettingsFile(rfs, p.Home, p.SettingsJSON())
	_, _ = ReadSettingsFileFollow(rfs, p.Home, p.SettingsJSON())
	_ = ScanOtherSettings(rfs, p)
	_ = LaunchdJobs{FS: rfs}
	if n := len(ffs.Writes()); n != 0 {
		t.Errorf("read helpers wrote %d times", n)
	}
}

func TestFakeFSWriteModeLock(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	f := NewFakeFS(t, root)
	lock := root + "/install.lock"
	unlock, err := f.Lock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Lock(lock); err == nil {
		t.Error("second Lock succeeded")
	}
	_ = unlock()
	if _, err := f.Lock(lock); err != nil {
		t.Errorf("Lock after unlock: %v", err)
	}
}

func TestFieldSetSourceClear(t *testing.T) {
	t.Parallel()
	var f Field[string]
	if f.IsSet() || f.Get() != "" || f.Source() != "" {
		t.Errorf("zero field not unset: %v", f)
	}
	f.Set("x", SourceDefault)
	if !f.IsSet() || f.Get() != "x" || f.Source() != SourceDefault {
		t.Errorf("after Set: %v", f)
	}
	f.Clear()
	if f.IsSet() {
		t.Error("Clear left the field set")
	}
	st := NewRunState(Inputs{})
	if st.Applied == nil || st.DB.IsSet() || st.Topology.IsSet() {
		t.Error("NewRunState not zeroed")
	}
}
