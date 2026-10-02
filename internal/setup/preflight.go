package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
)

// PreflightResult is what engine phase 0 found (Design 15.0). It fills only
// Inputs-adjacent state: Prior and the two preflight flags. It sets no
// step-owned field (v0.4, H1).
type PreflightResult struct {
	Prior            Prior
	Downgrade        bool
	ConfigDirChanged bool
	// Notes are the preflight warnings (corrupt manifest, changed Claude
	// config dir, downgrade); the renderer prints them with the Seed notes
	// before the status table.
	Notes []Note
	// Unlock releases the install lock; nil when none was taken (dry-run).
	Unlock func() error
}

// ErrLocked wraps a failure to take the install lock (AC-9): exit 2.
var ErrLocked = errors.New("install lock not acquired")

// Preflight is engine phase 0: take the lock (writable mode only, Design
// 21), load the manifest, back a corrupt one up (writable) or only report
// it (dry-run), read the env file, and detect a changed Claude config dir
// and a downgrade. version is the running binary's version.
func Preflight(wp WritePorts, in Inputs, version string, writable bool) (PreflightResult, error) {
	var res PreflightResult
	p := wp.Paths
	if writable {
		unlock, err := wp.FS.Lock(p.Lock())
		if err != nil {
			return res, fmt.Errorf("%w: %v", ErrLocked, err)
		}
		res.Unlock = unlock
	}
	fail := func(err error) (PreflightResult, error) {
		if res.Unlock != nil {
			_ = res.Unlock()
			res.Unlock = nil
		}
		return res, err
	}

	load, err := LoadManifest(wp.ReadPorts.FS, p)
	if err != nil {
		return fail(fmt.Errorf("read manifest: %w", err))
	}
	res.Prior.Manifest = load.Manifest
	if load.Corrupt {
		if writable {
			dst, berr := BackupCorruptManifest(wp.FS, wp.Clock, p)
			if berr != nil {
				return fail(fmt.Errorf("back up corrupt manifest: %w", berr))
			}
			res.Notes = append(res.Notes, Note{NoteWarn, fmt.Sprintf(
				"%s is unusable (%v); treated as absent, backed up as %s", p.Manifest(), load.Err, dst)})
		} else {
			res.Notes = append(res.Notes, Note{NoteWarn, fmt.Sprintf(
				"%s is unusable (%v); treated as absent (dry-run: not backed up)", p.Manifest(), load.Err)})
		}
	}

	b, err := wp.ReadPorts.FS.ReadFile(p.EnvFile())
	switch {
	case err == nil:
		res.Prior.EnvDoc = b
	case errors.Is(err, fs.ErrNotExist):
	default:
		return fail(fmt.Errorf("read env file: %w", err))
	}

	if m := res.Prior.Manifest; m != nil {
		if m.ClaudeConfigDir != "" && m.ClaudeConfigDir != p.ClaudeDir {
			res.ConfigDirChanged = true
			res.Notes = append(res.Notes, Note{NoteWarn, fmt.Sprintf(
				"the Claude config dir changed since the last install (%s, now %s): Claude Code files recorded there are not touched",
				m.ClaudeConfigDir, p.ClaudeDir)})
		}
		if isDowngrade(m.BinaryVersion, version) {
			res.Downgrade = true
			res.Notes = append(res.Notes, Note{NoteWarn, fmt.Sprintf(
				"this binary (%s) is older than the one that installed (%s): outdated artifacts default to keep",
				version, m.BinaryVersion)})
		}
	}
	return res, nil
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-(\d+)-g[0-9a-f]+)?(?:-dirty)?$`)

// parseVersion parses vX.Y.Z[-n-gSHA][-dirty] into {X, Y, Z, n}.
func parseVersion(v string) ([4]int, bool) {
	m := semverRe.FindStringSubmatch(v)
	if m == nil {
		return [4]int{}, false
	}
	var out [4]int
	for i := 0; i < 4; i++ {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [4]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// isDowngrade reports whether the recorded version is newer than the
// running one. "dev" or anything unparseable never counts (spec §8).
func isDowngrade(recorded, running string) bool {
	r, ok1 := parseVersion(recorded)
	c, ok2 := parseVersion(running)
	if !ok1 || !ok2 {
		return false
	}
	for i := range r {
		if r[i] != c[i] {
			return r[i] > c[i]
		}
	}
	return false
}
