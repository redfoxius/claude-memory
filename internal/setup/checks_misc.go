package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/redfoxius/claude-memory/internal/namespace"
	"github.com/redfoxius/claude-memory/internal/prsource"
)

// Doctor checks binary.version, tools.*, namespaces, jobs, dirs.state,
// manifest (AC-44, AC-49, AC-58).

// ---- binary.version --------------------------------------------------------------

func (d *doctor) checkBinary(context.Context) (Status, string, string) {
	self := d.Paths.Self
	detail := "claude-memory " + orDash(d.Version)
	if self != "" {
		detail += " at " + self
	}
	// Another claude-memory earlier on PATH. Its version is not asked for:
	// running it could be running the registered MCP command (AC-67), so
	// only the paths are compared.
	if found, err := d.Runner.LookPath(BinaryName); err == nil && self != "" {
		resolved := found
		if r, err := d.FS.EvalSymlinks(found); err == nil {
			resolved = r
		}
		if filepath.Clean(resolved) != filepath.Clean(self) {
			return StatusWarn, detail + "; `" + BinaryName + "` on PATH is another file, " + found + " (possibly another version)",
				"remove or update " + found + ", or put " + filepath.Dir(self) + " first on PATH"
		}
	}
	binDir := filepath.Dir(d.binPath)
	if !slices.Contains(filepath.SplitList(d.Env.Get("PATH")), binDir) {
		return StatusInfo, detail + "; " + binDir + " is not on PATH (hooks, MCP and jobs use absolute paths, so only your shell is affected)",
			"add " + binDir + " to PATH in your shell profile"
	}
	return pass(detail)
}

// ---- tools.* ----------------------------------------------------------------------

func (d *doctor) lookTool(name string) (string, bool) {
	p, err := d.Runner.LookPath(name)
	return p, err == nil && p != ""
}

func (d *doctor) checkToolGit(context.Context) (Status, string, string) {
	if p, ok := d.lookTool("git"); ok {
		return pass(p)
	}
	return StatusWarn, "git is not on PATH: staleness checks are off and ingest-pr cannot read repositories",
		"install git (macOS: xcode-select --install; Linux: your package manager)"
}

func (d *doctor) checkToolClaude(context.Context) (Status, string, string) {
	if p, ok := d.lookTool("claude"); ok {
		return pass(p)
	}
	return StatusWarn, "claude is not on PATH: session extraction (SessionEnd hook) and `claude mcp add` do not work",
		"install the Claude Code CLI and put it on PATH"
}

func (d *doctor) checkToolAz(context.Context) (Status, string, string) {
	if !d.prRepos() {
		return StatusInfo, "not needed: MEMORY_PR_INGEST_REPOS is not set (az is needed only for Azure DevOps repos)", ""
	}
	if p, ok := d.lookTool("az"); ok {
		return pass(p)
	}
	return StatusWarn, "az is not on PATH but MEMORY_PR_INGEST_REPOS is set: ingest-pr cannot fetch Azure DevOps PRs (az is needed only for Azure DevOps repos)",
		"install the Azure CLI (az) with the azure-devops extension and run `az login`"
}

// loginCheckTimeout bounds the network `auth status` of tools.gh/tools.glab;
// plus the runner's 1 s WaitDelay it stays below DefaultCheckTimeout so a slow network reads "could not
// check" instead of a framework timeout turning the row into a fail.
const loginCheckTimeout = 1500 * time.Millisecond

func (d *doctor) checkToolGh(ctx context.Context) (Status, string, string) {
	return d.checkLoginTool(ctx, "gh", []string{"gh", "auth", "status", "--hostname", "github.com"}, "gh auth login")
}

func (d *doctor) checkToolGlab(ctx context.Context) (Status, string, string) {
	return d.checkLoginTool(ctx, "glab", []string{"glab", "auth", "status", "--hostname=gitlab.com"}, "glab auth login")
}

// checkLoginTool is info-only: whether a CLI used by ingest-pr for
// GitHub/GitLab repos is on PATH and logged in. It runs the CLI's own
// read-only status command (never --show-token) under its own timeout.
func (d *doctor) checkLoginTool(ctx context.Context, tool string, argv []string, loginCmd string) (Status, string, string) {
	p, ok := d.lookTool(tool)
	if !ok {
		return StatusInfo, "not on PATH (needed only for GitHub/GitLab repos)", ""
	}
	cctx, cancel := context.WithTimeout(ctx, loginCheckTimeout)
	defer cancel()
	res, err := d.Runner.Run(cctx, Cmd{Argv: argv, DropEnv: prsource.ChildEnvDrop})
	if err != nil {
		return StatusInfo, p + ": could not check login (" + d.redact(err.Error()) + ")", ""
	}
	if res.ExitCode == 0 {
		return StatusInfo, p + ": logged in", ""
	}
	out := strings.ToLower(string(res.Stdout) + string(res.Stderr))
	for _, offline := range []string{"error connecting", "dial tcp", "no such host", "timeout", "network is unreachable"} {
		if strings.Contains(out, offline) {
			return StatusInfo, p + ": could not check login (offline?)", ""
		}
	}
	return StatusInfo, p + ": not logged in", "run `" + loginCmd + "` (only needed for GitHub/GitLab repos)"
}

// ---- namespaces -----------------------------------------------------------------

func (d *doctor) checkNamespaces(context.Context) (Status, string, string) {
	p := d.Paths.NamespacesFile()
	b, err := d.FS.ReadFile(p)
	var cfg *namespace.Config
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cfg = &namespace.Config{}
	case err != nil:
		return StatusWarn, "cannot read " + p + ": " + err.Error(), "check the file's permissions"
	default:
		cfg, err = namespace.Parse(b, p, d.Paths.Home)
		if err != nil {
			return StatusWarn, err.Error() + " (every subcommand falls back to the global namespace)",
				"fix " + p + " (format: integration/namespaces.example.yaml), or recreate it with `claude-memory namespaces init`"
		}
	}
	if len(cfg.PRIngestProblems) > 0 {
		var probs []string
		for _, pr := range cfg.PRIngestProblems {
			probs = append(probs, pr.Namespace+": "+pr.Reason)
		}
		return StatusWarn, "pr_ingest unusable, ingest-pr skips these namespaces: " + strings.Join(probs, "; "),
			"fix pr_ingest in " + p + " (enabled: true|false, provider: azuredevops|github|gitlab)"
	}
	resolution := ""
	if d.Paths.Cwd != "" {
		ns, why := cfg.Explain(d.Paths.Cwd)
		resolution = fmt.Sprintf("%s → %s (%s)", d.Paths.Cwd, ns, why)
	}
	if ov := d.Env.Get("MEMORY_NAMESPACE"); ov != "" {
		if !namespace.ValidName(ov) {
			return StatusWarn, fmt.Sprintf("MEMORY_NAMESPACE=%q is not a valid namespace name", ov),
				"use lowercase letters, digits, - and _ (or unset it)"
		}
		resolution = fmt.Sprintf("MEMORY_NAMESPACE=%s overrides the file", ov)
	}
	if errors.Is(err, fs.ErrNotExist) || b == nil {
		return StatusInfo, joinDetail("no "+p+": every directory uses the global namespace", nonEmpty(resolution)),
			"map your projects: claude-memory namespaces init NAME='~/path/**'"
	}
	return pass(joinDetail(fmt.Sprintf("%d mapping(s), default %s", len(cfg.Namespaces), orDash(cfg.Default)), nonEmpty(resolution)))
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// ---- jobs -------------------------------------------------------------------------

const (
	jobsRemedyInstall = "run `claude-memory install` to install the jobs"
	jobsRemedyUpgrade = "run `claude-memory install --upgrade` to re-install the jobs from this version"
	// --yes/--upgrade keep a modified plist (AC-52), so only the interactive
	// overwrite Confirm replaces it.
	jobsRemedyModified = "run `claude-memory install` (interactive) and confirm the overwrite of the modified plist"
)

func (d *doctor) checkJobs(ctx context.Context) (Status, string, string) {
	switch d.Platform.OS {
	case OSDarwin:
	case OSLinux:
		return StatusInfo, "not checked: this build installs scheduled jobs on macOS (launchd) only", ""
	default:
		return StatusInfo, "not checked: unsupported OS " + orDash(d.Platform.OS), ""
	}
	lj := LaunchdJobs{FS: d.FS, Runner: d.Runner, Paths: d.Paths, Assets: d.Assets}
	recorded := recordedJobHashes(d.manifest.Manifest)
	var warns, oks []string
	remedy := ""
	for _, j := range DefaultJobSpecs(d.Paths, d.binPath, ComputeJobPATH(d.Runner)) {
		s, err := lj.Inspect(ctx, j, recorded)
		switch {
		case err != nil:
			warns = append(warns, j.Name+": "+d.redact(err.Error()))
		case s.State == StateAbsent && j.Name == JobIngestPR && !d.prRepos():
			oks = append(oks, j.Name+" not installed (MEMORY_PR_INGEST_REPOS is not set)")
		case s.State == StateAbsent:
			warns = append(warns, j.Name+": not installed ("+s.Detail+")")
			if remedy == "" {
				remedy = jobsRemedyInstall
			}
		case s.State == StateOK:
			oks = append(oks, j.Name+": "+s.Detail)
		default:
			warns = append(warns, j.Name+": "+s.Detail)
			if s.State == StateModified {
				remedy = jobsRemedyModified
			} else if remedy != jobsRemedyModified {
				remedy = jobsRemedyUpgrade
			}
		}
	}
	if len(warns) > 0 {
		return StatusWarn, joinDetail(strings.Join(warns, "; "), oks), remedy
	}
	return pass(strings.Join(oks, "; "))
}

// ---- dirs.state -------------------------------------------------------------------

func (d *doctor) checkStateDir(context.Context) (Status, string, string) {
	dir := d.Paths.StateDir
	bootstrapNote := ""
	if _, err := d.FS.Stat(d.Paths.Bootstrap()); err == nil {
		bootstrapNote = d.Paths.Bootstrap() + " is present (it holds a password; delete it once the database works)"
	}
	info, err := d.FS.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		parent := filepath.Dir(dir)
		for parent != filepath.Dir(parent) {
			if _, err := d.FS.Stat(parent); err == nil {
				break
			}
			parent = filepath.Dir(parent)
		}
		if !d.FS.Writable(parent) {
			return StatusWarn, dir + " does not exist and " + parent + " is not writable: job logs and caches cannot be created",
				"mkdir -p " + dir
		}
		return StatusInfo, dir + " does not exist yet (created on first use)", ""
	case err != nil:
		return StatusWarn, "cannot stat " + dir + ": " + err.Error(), "check the directory's permissions"
	case !info.IsDir():
		return StatusWarn, dir + " is not a directory", "move it away; claude-memory keeps job logs and caches there"
	case !d.FS.Writable(dir):
		return StatusWarn, dir + " is not writable: job logs, PR cursors and the staleness cache cannot be written",
			"chmod u+w " + dir
	case bootstrapNote != "":
		return StatusInfo, dir + " writable; " + bootstrapNote, "rm " + d.Paths.Bootstrap()
	}
	return pass(dir + " writable")
}

// ---- manifest -------------------------------------------------------------------

func (d *doctor) checkManifest(context.Context) (Status, string, string) {
	l := d.manifest
	switch {
	case d.manifestEr != nil:
		return StatusWarn, "cannot read " + l.Path + ": " + d.manifestEr.Error(), "check the file's permissions"
	case l.Corrupt:
		return StatusWarn, "corrupt manifest, treated as absent: " + d.redact(l.Err.Error()),
			"move " + l.Path + " aside; `claude-memory install` (slice 2) rebuilds it"
	case !l.Present():
		return StatusInfo, "no " + l.Path + " (a manual install, or install has not run yet)", ""
	}
	m := l.Manifest
	var warns []string
	if m.ClaudeConfigDir != "" && filepath.Clean(m.ClaudeConfigDir) != filepath.Clean(d.Paths.ClaudeDir) {
		warns = append(warns, "recorded for Claude config dir "+m.ClaudeConfigDir+", but the effective one is "+d.Paths.ClaudeDir+" (CLAUDE_CONFIG_DIR differs?)")
	}
	var missing []string
	for _, a := range m.Artifacts {
		switch a.Kind {
		case KindMCP, KindEnvKey, KindDir:
			continue // not a file of its own (mcp, env-key) or checked elsewhere
		}
		if a.Path == "" {
			continue
		}
		if _, err := d.FS.Lstat(a.Path); err != nil {
			missing = append(missing, a.Path)
		}
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	if len(missing) > 0 {
		warns = append(warns, "recorded artifacts missing: "+strings.Join(capList(missing, 4), ", "))
	}
	if len(warns) > 0 {
		return StatusWarn, strings.Join(warns, "; "), "re-run `claude-memory install` (slice 2), or restore the missing files"
	}
	return pass(fmt.Sprintf("%d artifacts recorded by %s, all present", len(m.Artifacts), orDash(m.BinaryVersion)))
}
