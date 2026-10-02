package setup

import "path/filepath"

// ResolveBinPath returns the path of the installed claude-memory binary
// (plan Design 16, spec AC-35). The binary path is sticky: an explicit
// --bin-dir wins (p.BinDir then holds it), else the path the manifest
// recorded for the `binary` step, else p.InstalledBinary(). Doctor has no
// --bin-dir and calls it with explicit=false and the manifest it loaded
// (nil when there is none), so install and doctor agree after
// `install --bin-dir X`. Install uses filepath.Dir of the result for the
// not-on-PATH note.
func ResolveBinPath(p Paths, m *Manifest, explicit bool) string {
	if explicit {
		return p.InstalledBinary()
	}
	for _, a := range m.Find(KindFile) {
		if a.Step == BinaryStepName && a.Path != "" && filepath.IsAbs(a.Path) {
			return a.Path
		}
	}
	return p.InstalledBinary()
}

// BinaryStepName is the install step that writes the binary, as recorded in
// Artifact.Step.
const BinaryStepName = "binary"
