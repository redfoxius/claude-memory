package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/redfoxius/claude-memory/internal/setup"
)

// errRoot is the AC-69 refusal: install and uninstall exit 2 with it when
// run as root without --allow-root. doctor is not affected.
var errRoot = errors.New("refusing to install for root; run as your user (or pass --allow-root)")

// rootGuard refuses to run as the superuser unless allowRoot is set (AC-69).
// euid is injected (the install command passes os.Geteuid()) so the check is
// testable without being root.
func rootGuard(euid int, allowRoot bool) error {
	if euid == 0 && !allowRoot {
		return errRoot
	}
	return nil
}

// installOptions are the parsed `install` flags (AC-4).
type installOptions struct {
	Inputs        setup.Inputs // everything the engine reads
	AllowRoot     bool         // --allow-root (AC-69)
	PasswordStdin bool         // --pg-password-stdin (AC-31)
	Help          bool
}

// deferredInstallFlags exit 2 with a pointer to the spec (AC-4, §12.1).
var deferredInstallFlags = map[string]string{
	"only":      "--only is deferred (spec §12.1, AC-53)",
	"seed-file": "--seed-file is deferred (spec §12.1)",
	"purge":     "--purge-* is deferred (spec §12.1)",
	"latency":   "--latency is a doctor flag and is deferred (spec §12.1, AC-61)",
}

// listFlag is a repeatable string flag.
type listFlag struct{ vals *[]string }

func (l listFlag) String() string { return "" }
func (l listFlag) Set(v string) error {
	*l.vals = append(*l.vals, v)
	return nil
}

// parseInstallFlags parses the install command line. Every error it returns
// is a usage error (exit 2): unknown flags, deferred flags and values, bad
// values, positional arguments.
func parseInstallFlags(args []string, stderr io.Writer) (installOptions, error) {
	for _, a := range args {
		if a == "--" {
			break // everything after is positional, rejected below
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(name, "purge-") {
			name = "purge"
		}
		if msg, ok := deferredInstallFlags[name]; ok {
			return installOptions{}, usageError(errors.New(msg))
		}
	}
	var (
		o      installOptions
		in     = &o.Inputs
		skips  []string
		upgrad bool
	)
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	// The flag package would echo the offending value (and print it a second
	// time through main); install prints its own fixed messages instead.
	fs.SetOutput(io.Discard)
	fs.BoolVar(&in.Yes, "yes", false, "accept the defaults, ask nothing")
	fs.BoolVar(&upgrad, "upgrade", false, "alias of --yes")
	fs.BoolVar(&in.DryRun, "dry-run", false, "show the plan and change nothing")
	fs.BoolVar(&in.Reconfigure, "reconfigure", false, "ask again for values already chosen")
	fs.StringVar(&in.Topology, "topology", "", "database topology: local|remote")
	fs.Var(listFlag{&skips}, "skip", "step ids to skip, comma-separated (repeatable)")
	fs.StringVar(&in.BinDir, "bin-dir", "", "directory to install the binary into (default ~/.local/bin)")
	fs.StringVar(&in.OllamaURL, "ollama-url", "", "Ollama base URL")
	fs.StringVar(&in.PGDSN, "pg-dsn", "", "postgresql:// DSN without a password")
	fs.StringVar(&in.PGSSLMode, "pg-sslmode", "", "prefer|require|disable")
	fs.BoolVar(&o.PasswordStdin, "pg-password-stdin", false, "read the database password from stdin (one line)")
	fs.Var(listFlag{&in.Namespaces}, "namespace", "NAME=GLOB namespace mapping (repeatable)")
	fs.StringVar(&in.ClaudeMD, "claude-md", "", "CLAUDE.md file for the memory section (default ~/.claude/CLAUDE.md)")
	fs.StringVar(&in.JobsBackend, "jobs-backend", "", "launchd|systemd|none")
	fs.StringVar(&in.PRRepos, "pr-repos", "", "comma-separated absolute paths of repositories for ingest-pr")
	fs.BoolVar(&in.NoJobs, "no-jobs", false, "do not install the scheduled jobs")
	fs.BoolVar(&in.NoDoctor, "no-doctor", false, "do not run doctor at the end")
	fs.BoolVar(&o.AllowRoot, "allow-root", false, "allow running as root")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stderr)
			fs.PrintDefaults()
			return installOptions{Help: true}, nil
		}
		return installOptions{}, usageError(sanitizeFlagError(err))
	}
	if fs.NArg() > 0 {
		return installOptions{}, usageError(errors.New("install takes no arguments"))
	}

	// An empty value would silently mean "the default", which is the opposite
	// of what a script passing an unset variable intends.
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if given["claude-md"] && strings.TrimSpace(in.ClaudeMD) == "" {
		return installOptions{}, usageError(errors.New("--claude-md needs a path"))
	}
	if given["pr-repos"] && strings.TrimSpace(in.PRRepos) == "" {
		return installOptions{}, usageError(errors.New("--pr-repos needs at least one repository path"))
	}

	// --upgrade is exactly --yes: one code path (AC-52).
	if upgrad {
		in.Yes, in.Upgrade = true, true
	}
	for _, s := range skips {
		for _, id := range strings.Split(s, ",") {
			if id = strings.TrimSpace(id); id != "" {
				in.Skip = append(in.Skip, id)
			}
		}
	}
	in.BinDirExplicit = in.BinDir != ""

	if err := validateInstallValues(in); err != nil {
		return installOptions{}, usageError(err)
	}
	return o, nil
}

var flagNameRE = regexp.MustCompile(`for (?:flag )?-{1,2}([A-Za-z0-9-]+)`)

// sanitizeFlagError turns a flag-package error into one that never echoes the
// user's input: an unknown flag or a missing argument keeps the flag name
// only; a bad value says which flag, not what was typed.
func sanitizeFlagError(err error) error {
	msg := err.Error()
	if strings.HasPrefix(msg, "flag provided but not defined") || strings.HasPrefix(msg, "flag needs an argument") {
		return err
	}
	if m := flagNameRE.FindStringSubmatch(msg); m != nil {
		return fmt.Errorf("invalid value for flag --%s", m[1])
	}
	return errors.New("invalid flag value")
}

// validateInstallValues rejects bad and deferred flag values early (exit 2);
// the steps' Seed validates again, which is harmless.
func validateInstallValues(in *setup.Inputs) error {
	if _, err := setup.ParseTopologyFlag(in.Topology); err != nil {
		return err
	}
	if in.JobsBackend == "cron" {
		return errors.New("--jobs-backend cron is deferred (spec §12.1): use launchd, systemd or none")
	}
	if !setup.ValidJobsBackend(in.JobsBackend) {
		return fmt.Errorf("--jobs-backend %q: want launchd, systemd or none", in.JobsBackend)
	}
	if m := in.PGSSLMode; m != "" && !slices.Contains([]string{"prefer", "require", "disable"}, m) {
		return fmt.Errorf("--pg-sslmode %q: want prefer, require or disable", m)
	}
	if in.PGDSN != "" {
		if err := setup.CheckPGDSNFlag(in.PGDSN); err != nil {
			return fmt.Errorf("--pg-dsn: %w", err)
		}
	}
	if in.OllamaURL != "" {
		if err := setup.ValidateOllamaURL(in.OllamaURL); err != nil {
			return fmt.Errorf("--ollama-url: %w", err)
		}
	}
	if in.PRRepos != "" {
		if err := setup.ValidatePRRepos(in.PRRepos); err != nil {
			return fmt.Errorf("--pr-repos: %w", err)
		}
	}
	for _, v := range in.Namespaces {
		if _, err := setup.ParseNSFlag(v); err != nil {
			return fmt.Errorf("--namespace %q: %w", v, err)
		}
	}
	return nil
}

// resolveInstallPlatform applies --jobs-backend to the detected platform and
// refuses an unsupported OS (AC-16, AC-17). Both are exit 2, before any prompt
// and before any write.
func resolveInstallPlatform(p setup.PlatformInfo, override string) (setup.PlatformInfo, error) {
	// An unsupported OS is refused first: its message (AC-17) wins over a
	// --jobs-backend complaint.
	if err := setup.UnsupportedError(p); err != nil {
		return p, usageError(err)
	}
	p, err := jobsBackendOverride(p, override)
	if err != nil {
		return p, usageError(err)
	}
	return p, nil
}

// cmdInstall is the install command's composition: flags, root guard, paths,
// the stdin password, platform, adapters, then runInstall. It runs before the
// config is loaded and needs no DSN (AC-1).
func cmdInstall(args []string) error {
	opts, err := parseInstallFlags(args, os.Stderr)
	if err != nil {
		return err
	}
	if opts.Help {
		return quietExit(0)
	}
	if err := rootGuard(os.Geteuid(), opts.AllowRoot); err != nil {
		return usageError(err)
	}
	ctx, stop := interruptContext(context.Background(), os.Stderr)
	defer stop()

	deps, err := buildSetupDeps(ctx, opts.Inputs.BinDir)
	if err != nil {
		// HOME unset or relative, a relative --bin-dir or CLAUDE_CONFIG_DIR.
		return usageError(err)
	}
	if deps.Platform, err = resolveInstallPlatform(deps.Platform, opts.Inputs.JobsBackend); err != nil {
		return err
	}
	if opts.PasswordStdin {
		if err := refuseTerminalStdin(term.IsTerminal(int(os.Stdin.Fd()))); err != nil {
			return usageError(err)
		}
		// Before Detect (AC-31); the secret is registered with the Redactor.
		pw, err := readStdinSecret(ctx, os.Stdin, deps.Redactor, stdinSecretTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return quietExit(setup.ExitInterrupted)
			}
			return usageError(err)
		}
		opts.Inputs.PGPassword = pw
	}
	opts.Inputs.Env = deps.Env

	run := installRun{
		Opts:    opts,
		Deps:    deps,
		UI:      newTTYPrompter(ctx, os.Stdin, os.Stdout, deps.Redactor),
		Read:    readOnlyPorts(deps),
		Write:   writablePorts(deps, opts.Inputs.DryRun),
		Out:     deps.Stdout,
		TTY:     term.IsTerminal(int(os.Stdout.Fd())),
		Version: buildVersion().Short(),
	}
	return runInstall(ctx, run)
}

// installRun is everything runInstall needs, already constructed: tests pass
// fakes, cmdInstall passes the real adapters. Under --dry-run both port sets
// are read-only (Design 20) and Lock is never called.
type installRun struct {
	Opts  installOptions
	Deps  setupDeps
	UI    setup.Prompter
	Read  setup.ReadPorts
	Write setup.WritePorts
	Out   io.Writer
	TTY   bool
	// Version is the running binary's version (injected so tests control it).
	Version string
}

// runInstall builds the engine, runs it and prints the summary. It returns
// nil for exit 0 and an exitError otherwise.
func runInstall(ctx context.Context, r installRun) error {
	red := r.Deps.Redactor
	rend := setup.NewRenderer(r.Out, red, setup.RenderOptions{TTY: r.TTY, Env: r.Deps.Env, DryRun: r.Opts.Inputs.DryRun})
	write := r.Write
	write.Progress = rend.Progress
	ver := r.Version
	eng := &setup.Engine{
		Steps:    setup.InstallSteps(ver, red),
		Read:     r.Read,
		Write:    write,
		UI:       r.UI,
		Reporter: rend,
		Version:  ver,
		KnownIDs: setup.AllStepIDs,
	}
	res := eng.Run(ctx, r.Opts.Inputs)
	rend.Summary(res)

	// The final doctor (AC-62) is the last registered Step (setup.DoctorStep,
	// run by the engine on the read-only ports); its fail already set the
	// engine's exit code, so nothing runs here after the engine.
	return installExit(res, red)
}

// installExit maps an engine result to the process exit: 0 is nil; any other
// code with an error prints that error, redacted (usage errors, exit 2); 130
// and a failure without an error (exit 1, the summary and per-step lines
// already say why) are quiet.
func installExit(res setup.RunResult, red *setup.Redactor) error {
	if res.ExitCode == setup.ExitOK {
		return nil
	}
	if res.Err == nil || res.ExitCode == setup.ExitInterrupted {
		return quietExit(res.ExitCode)
	}
	msg := res.Err.Error()
	if red != nil {
		msg = red.Redact(msg)
	}
	return &exitError{code: res.ExitCode, err: errors.New(msg)}
}

// refuseTerminalStdin refuses --pg-password-stdin when stdin is a terminal
// (AC-31: the flag is for a pipe or file; a typed password would echo).
func refuseTerminalStdin(isTerminal bool) error {
	if isTerminal {
		return errors.New("--pg-password-stdin needs a pipe or file on stdin, not a terminal")
	}
	return nil
}

// interruptContext returns a context cancelled by the first SIGINT/SIGTERM.
// The handler is then removed, so a second Ctrl-C uses the default action and
// kills the process even when a prompt read is blocked.
func interruptContext(parent context.Context, msg io.Writer) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigc:
			signal.Stop(sigc)
			_, _ = io.WriteString(msg, "\ninterrupted: stopping; press Ctrl-C again to quit now\n")
			cancel()
		case <-done:
		}
	}()
	return ctx, func() {
		signal.Stop(sigc)
		close(done)
		cancel()
	}
}
