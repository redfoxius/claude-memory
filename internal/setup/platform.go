package setup

// Operating systems the feature supports (spec §4); anything else is
// reported as-is in PlatformInfo.OS and treated as unsupported.
const (
	OSDarwin = "darwin"
	OSLinux  = "linux"
)

// Jobs backends (AC-16). There is no cron backend (deferred, spec §12.1).
const (
	JobsLaunchd = "launchd"
	JobsSystemd = "systemd"
	JobsNone    = "none"
)

// PlatformInfo is the detected platform: plain data, not a port (spec §10,
// D5). It is produced once by detectPlatform in cmd/claude-memory and handed
// to the engine and doctor.
type PlatformInfo struct {
	OS, Arch, OSVersion string
	WSL                 bool
	JobsBackend         string   // launchd | systemd | none
	JobsBackendReason   string   // why the backend was chosen, or why it is none
	SystemdEnv          []string // XDG_RUNTIME_DIR / DBUS_SESSION_BUS_ADDRESS when needed (AC-16) [S2]
	PackageManagers     []string // brew, apt-get, dnf, pacman found on PATH [S2]
}

// Supported reports whether install can run on this OS (darwin or linux).
func (p PlatformInfo) Supported() bool { return p.OS == OSDarwin || p.OS == OSLinux }
