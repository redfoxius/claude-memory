package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
)

// Artifact ids of the binary step.
const (
	BinaryFileArtifact = "binary/file"
	BinaryDirArtifact  = "binary/dir"
)

// quarantineAttr is the macOS attribute Gatekeeper puts on downloads.
const quarantineAttr = "com.apple.quarantine"

// BinaryStep installs the running executable (Paths.Self) to the sticky
// binary path (AC-35): an atomic copy at mode 0755 through the FS port.
// It owns RunState.BinPath, set in Seed via ResolveBinPath.
type BinaryStep struct {
	// Version is the running binary's version, recorded on its artifacts.
	Version string
}

var (
	_ Step   = BinaryStep{}
	_ Seeder = BinaryStep{}
)

// ID implements Step.
func (BinaryStep) ID() string { return BinaryStepName }

// Title implements Step.
func (BinaryStep) Title() string { return "Binary" }

// Requires implements Step.
func (BinaryStep) Requires() []string { return nil }

// recordedBinaryPath is the path the manifest recorded for the binary step
// ("" when none), the same artifact ResolveBinPath reads.
func recordedBinaryPath(m *Manifest) string {
	for _, a := range m.Find(KindFile) {
		if a.Step == BinaryStepName && a.Path != "" && filepath.IsAbs(a.Path) {
			return a.Path
		}
	}
	return ""
}

// ephemeralDir returns the EphemeralDirs entry self lives under, or "".
// Each entry is also tried with its symlinks resolved (macOS /var is
// /private/var), because Paths.Self is resolved.
func ephemeralDir(rc ReadPorts, self string) string {
	for _, d := range rc.Paths.EphemeralDirs {
		if pathUnder(self, d) {
			return d
		}
		if r, err := rc.FS.EvalSymlinks(d); err == nil && pathUnder(self, r) {
			return d
		}
	}
	return ""
}

// Seed sets BinPath (flag > manifest > default), refuses a temporary
// `go run` binary (AC-35, exit 2) and notes a recorded binary left behind by
// a new explicit --bin-dir. A run that skips the step is not refused.
func (BinaryStep) Seed(_ context.Context, rc ReadPorts, st *RunState) ([]Note, error) {
	explicit := st.Inputs.BinDirExplicit
	m := st.Prior.Manifest
	rec := recordedBinaryPath(m)
	path := ResolveBinPath(rc.Paths, m, explicit)
	switch {
	case explicit:
		st.BinPath.Set(path, SourceFlag)
	case rec != "":
		st.BinPath.Set(path, SourceManifest)
	default:
		st.BinPath.Set(path, SourceDefault)
	}

	if !slices.Contains(st.Inputs.Skip, BinaryStepName) {
		if d := ephemeralDir(rc, rc.Paths.Self); rc.Paths.Self != "" && d != "" {
			return nil, fmt.Errorf("refusing to install %s: it is under the temporary directory %s (a `go run` binary); build to a stable path: `make install`", rc.Paths.Self, d)
		}
	}
	var notes []Note
	if explicit && rec != "" && rec != path {
		notes = append(notes, Note{NoteInfo, fmt.Sprintf("the previously installed binary %s is left in place", rec)})
	}
	return notes, nil
}

func binTarget(rc ReadPorts, st *RunState) string {
	if st.BinPath.IsSet() {
		return st.BinPath.Get()
	}
	return ResolveBinPath(rc.Paths, st.Prior.Manifest, st.Inputs.BinDirExplicit)
}

// onPath reports whether dir is one of the entries of the PATH value.
func onPath(pathVar, dir string) bool {
	for _, e := range filepath.SplitList(pathVar) {
		if e != "" && filepath.Clean(e) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

// Detect implements Step.
func (BinaryStep) Detect(ctx context.Context, rc ReadPorts, st *RunState) Detection {
	self := rc.Paths.Self
	if self == "" {
		return Detection{State: StateBlocked, Detail: "cannot determine the running executable"}
	}
	target := binTarget(rc, st)
	dir := filepath.Dir(target)

	d := Detection{}
	var arts []ArtifactState
	// The directory artifact is always listed so the engine's post-Apply
	// re-Detect finds it ok; install records it only when Apply created it.
	dirState := ArtifactState{ID: BinaryDirArtifact, State: StateOK, Detail: dir + " exists"}
	if _, derr := rc.FS.Stat(dir); errors.Is(derr, fs.ErrNotExist) {
		dirState = ArtifactState{ID: BinaryDirArtifact, State: StateAbsent, Detail: "create " + dir}
	}
	info, err := rc.FS.Stat(target)
	switch {
	case err == nil && info.IsDir():
		return Detection{State: StateBlocked, Detail: target + " is a directory"}
	case err == nil:
		d.State, d.Detail = binaryState(rc, self, target, info)
		arts = append(arts, ArtifactState{ID: BinaryFileArtifact, State: d.State, Detail: d.Detail}, dirState)
	case errors.Is(err, fs.ErrNotExist):
		d.State, d.Detail = StateAbsent, fmt.Sprintf("install %s to %s", self, target)
		arts = append(arts, ArtifactState{ID: BinaryFileArtifact, State: StateAbsent, Detail: d.Detail}, dirState)
	default:
		return Detection{State: StateBlocked, Detail: fmt.Sprintf("cannot inspect %s: %v", target, err)}
	}
	d.Artifacts = arts

	if !onPath(rc.Env.Get("PATH"), dir) {
		d.Notes = append(d.Notes, Note{NoteWarn, fmt.Sprintf("%s is not on PATH: add `export PATH=\"%s:$PATH\"` to your shell profile", dir, dir)})
	}
	if rc.Platform.OS == OSDarwin && info != nil {
		// Read-only probe: exit 0 means the attribute is present.
		res, rerr := rc.Runner.Run(ctx, Cmd{Argv: []string{"xattr", "-p", quarantineAttr, target}})
		if rerr == nil && res.ExitCode == 0 {
			d.Notes = append(d.Notes, Note{NoteWarn, fmt.Sprintf("%s carries %s: run `xattr -d %s %s`", target, quarantineAttr, quarantineAttr, target)})
		}
	}
	return d
}

// binaryState compares the installed file with the running executable: the
// same file, or identical bytes and an executable mode, is ok; anything else
// is outdated (nobody hand-edits a binary, so there is no modified state and
// --yes upgrades it).
func binaryState(rc ReadPorts, self, target string, info fs.FileInfo) (State, string) {
	a, aerr := rc.FS.EvalSymlinks(self)
	b, berr := rc.FS.EvalSymlinks(target)
	if aerr == nil && berr == nil && a == b {
		return StateOK, target + " is the running binary"
	}
	sb, err := rc.FS.ReadFile(self)
	if err != nil {
		return StateOutdated, fmt.Sprintf("cannot read the running binary: %v", err)
	}
	tb, err := rc.FS.ReadFile(target)
	if err != nil || !bytes.Equal(sb, tb) {
		return StateOutdated, target + " differs from the running binary"
	}
	if info.Mode().Perm()&0o111 == 0 {
		return StateOutdated, target + " is not executable"
	}
	return StateOK, target + " is up to date"
}

// Plan implements Step.
func (BinaryStep) Plan(_ context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	target := binTarget(rc, st)
	var p Plan
	if ch[BinaryDirArtifact] == ChoiceApply {
		p.Actions = append(p.Actions, Action{Artifact: BinaryDirArtifact, Verb: "write", Path: filepath.Dir(target), Desc: "create directory"})
	}
	if ch[BinaryFileArtifact] == ChoiceApply {
		p.Actions = append(p.Actions, Action{Artifact: BinaryFileArtifact, Verb: "write", Path: target,
			Desc: fmt.Sprintf("copy %s (mode 0755)", rc.Paths.Self)})
	}
	return p, nil
}

// Apply implements Step: the directory first, then the atomic copy.
func (b BinaryStep) Apply(_ context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	target := binTarget(wc.ReadPorts, st)
	dir := filepath.Dir(target)
	for _, a := range p.Actions {
		switch a.Artifact {
		case BinaryDirArtifact:
			if err := wc.FS.MkdirAll(dir, 0o755); err != nil {
				return res, fmt.Errorf("create %s: %w", dir, err)
			}
			res.Artifacts = append(res.Artifacts, Artifact{Step: BinaryStepName, Kind: KindDir, Path: dir, Version: b.Version})
		case BinaryFileArtifact:
			content, err := wc.FS.ReadFile(wc.Paths.Self)
			if err != nil {
				return res, fmt.Errorf("read the running binary: %w", err)
			}
			if err := wc.FS.WriteFileAtomic(target, content, 0o755); err != nil {
				return res, fmt.Errorf("install %s: %w", target, err)
			}
			res.Artifacts = append(res.Artifacts, Artifact{Step: BinaryStepName, Kind: KindFile, Path: target,
				SHA256: sha256Hex(content), Version: b.Version})
		}
	}
	return res, nil
}
