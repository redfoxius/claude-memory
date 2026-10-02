package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installChildEnv is the environment of an install child process: HOME is a
// temp dir, TMPDIR is another (so a binary built under the parent's temp dir
// is not "ephemeral", AC-35), and nothing else is inherited.
func installChildEnv(home, tmp string) []string {
	return []string{"HOME=" + home, "TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH")}
}

// runBinary runs bin with env from cwd and returns the combined output and
// the exit code.
func runBinary(t *testing.T, bin, cwd string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env, cmd.Dir = env, cwd
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run %s: %v", bin, err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

// TestInstallDryRunNoDSN is AC-1 (install half) and AC-34 (S2): the built
// binary, run from a directory that is not a checkout, with HOME an empty
// temp dir and no MEMORY_PG_DSN, dispatches install before the config is
// loaded, prints the status table from the embedded assets, writes nothing
// under HOME and does not fail on the missing DSN.
func TestInstallDryRunNoDSN(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	home, tmp, cwd := t.TempDir(), t.TempDir(), t.TempDir()

	// An unreachable Ollama keeps the run independent of the machine.
	out, code := runBinary(t, bin, cwd, installChildEnv(home, tmp),
		"install", "--dry-run", "--yes", "--topology", "remote", "--ollama-url", "http://127.0.0.1:1")
	if strings.Contains(out, dsnRequired) {
		t.Errorf("install must not require MEMORY_PG_DSN:\n%s", out)
	}
	if code == 2 {
		t.Errorf("exit 2 (usage error); output:\n%s", out)
	}
	for _, want := range []string{"STEP", "Platform", "Binary", "Plan", "Dry run: nothing was changed."} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if got := tree(t, home); len(got) != 0 {
		t.Errorf("--dry-run wrote under HOME: %v", got)
	}
}

// TestInstallRefusesEphemeralBinary is AC-35 through the real command: a
// binary under the child's TMPDIR is refused with exit 2 before any write.
func TestInstallRefusesEphemeralBinary(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	home := t.TempDir()
	tmp := filepath.Dir(bin) // the binary lives under TMPDIR for this child
	out, code := runBinary(t, bin, t.TempDir(), installChildEnv(home, tmp), "install", "--dry-run", "--yes", "--topology", "remote")
	if code != 2 || !strings.Contains(out, "temporary directory") {
		t.Errorf("exit %d, want 2 naming the temporary directory:\n%s", code, out)
	}
}

// TestInstallUsageErrorsExit2 covers the exit-2 paths that need the real
// process: flags, HOME, the non-interactive refusal.
func TestInstallUsageErrorsExit2(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cases := []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{"unknown flag", []string{"HOME=" + home}, []string{"install", "--bogus"}, "flag provided but not defined"},
		{"deferred topology", []string{"HOME=" + home}, []string{"install", "--topology", "docker-local"}, "deferred"},
		{"deferred --only", []string{"HOME=" + home}, []string{"install", "--only", "mcp"}, "§12.1"},
		{"jobs-backend cron", []string{"HOME=" + home}, []string{"install", "--jobs-backend", "cron"}, "§12.1"},
		{"HOME unset", nil, []string{"install", "--dry-run", "--yes"}, "HOME"},
		{"HOME relative", []string{"HOME=rel/home"}, []string{"install", "--dry-run", "--yes"}, "HOME"},
		{"relative --bin-dir", []string{"HOME=" + home}, []string{"install", "--dry-run", "--yes", "--bin-dir", "rel"}, "--bin-dir"},
		{"unknown --skip step (mcp is not in 2a)", []string{"HOME=" + home}, []string{"install", "--dry-run", "--yes", "--skip", "mcp"}, "unknown step"},
		{"non-interactive without --yes", []string{"HOME=" + home}, []string{"install", "--topology", "remote"}, "non-interactive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, code := runChild(t, tc.env, tc.args...)
			if code != 2 {
				t.Errorf("exit %d, want 2; output:\n%s", code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, out)
			}
		})
	}
}
