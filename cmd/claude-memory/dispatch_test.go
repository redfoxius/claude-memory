package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runChild runs this test binary as claude-memory (TestMain's runMainEnv
// hook) with args and exactly env (nothing inherited but what the caller
// passes), from a temp working directory outside the repo. It returns the
// combined output and the exit code.
func runChild(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append([]string{runMainEnv + "=1", "PATH=" + os.Getenv("PATH")}, env...)
	cmd.Dir = t.TempDir()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run child: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), code
}

const dsnRequired = "MEMORY_PG_DSN is required"

// TestEarlyDispatch is AC-1 (slice 1 half): doctor and version run before the
// config is loaded, so they work with an empty HOME, a 0644 env file and no
// MEMORY_PG_DSN; migrate is dispatched after config load and needs the DSN.
func TestEarlyDispatch(t *testing.T) {
	t.Parallel()
	emptyHome := t.TempDir()

	// A HOME whose env file is group/world-readable: the post-config path
	// refuses it, the early path must not even read it.
	badPermHome := t.TempDir()
	cfgDir := filepath.Join(badPermHome, ".config", "claude-memory")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(cfgDir, "env")
	if err := os.WriteFile(envPath, []byte("MEMORY_PG_DSN=postgresql://u:p@127.0.0.1:1/db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(envPath, 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		home     string
		args     []string
		wantCode int
		want     string // substring of the output
		notWant  []string
	}{
		{"version, empty HOME", emptyHome, []string{"version"}, 0, "claude-memory ", []string{dsnRequired}},
		{"version, 0644 env file", badPermHome, []string{"version"}, 0, "claude-memory ", []string{"mode 0600"}},
		// doctor reports instead of failing to start: exit 1 because checks fail.
		{"doctor --json, empty HOME", emptyHome, []string{"doctor", "--json"}, 1, `"schema": 1`, []string{dsnRequired, "error:"}},
		{"doctor, 0644 env file", badPermHome, []string{"doctor"}, 1, "FAIL  env.perms", []string{dsnRequired, "must have mode 0600"}},
		{"doctor unknown flag", emptyHome, []string{"doctor", "--bogus"}, 2, "flag provided but not defined", nil},
		{"doctor positional arg", emptyHome, []string{"doctor", "extra"}, 2, "no arguments", nil},
		{"doctor --latency deferred", emptyHome, []string{"doctor", "--latency=5"}, 2, "§12.1", nil},
		{"version extra arg", emptyHome, []string{"version", "x"}, 2, "usage", nil},
		{"migrate needs the DSN", emptyHome, []string{"migrate"}, 1, dsnRequired, nil},
		{"migrate refuses a 0644 env file", badPermHome, []string{"migrate"}, 1, "must have mode 0600", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, code := runChild(t, []string{"HOME=" + tc.home}, tc.args...)
			if code != tc.wantCode {
				t.Errorf("exit code %d, want %d; output:\n%s", code, tc.wantCode, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output does not contain %q:\n%s", tc.want, out)
			}
			for _, nw := range tc.notWant {
				if strings.Contains(out, nw) {
					t.Errorf("output contains %q:\n%s", nw, out)
				}
			}
		})
	}
}

// TestDoctorWithoutHomeExits3 is AC-59/AC-68: doctor cannot start without an
// absolute HOME.
func TestDoctorWithoutHomeExits3(t *testing.T) {
	t.Parallel()
	for _, env := range [][]string{nil, {"HOME=relative/home"}} {
		out, code := runChild(t, env, "doctor")
		if code != 3 {
			t.Errorf("env %q: exit code %d, want 3; output:\n%s", env, code, out)
		}
		if !strings.Contains(out, "HOME") {
			t.Errorf("env %q: output does not name HOME:\n%s", env, out)
		}
	}
}

func TestParseDoctorFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args    []string
		want    doctorOptions
		wantErr bool
	}{
		{nil, doctorOptions{Timeout: 3e9, Deadline: 10e9}, false},
		{[]string{"--json", "--strict", "--timeout", "1s", "--deadline=5s"},
			doctorOptions{JSON: true, Strict: true, Timeout: 1e9, Deadline: 5e9}, false},
		{[]string{"-json"}, doctorOptions{JSON: true, Timeout: 3e9, Deadline: 10e9}, false},
		{[]string{"--timeout", "0s"}, doctorOptions{}, true},
		{[]string{"--deadline", "-1s"}, doctorOptions{}, true},
		{[]string{"--timeout", "soon"}, doctorOptions{}, true},
		{[]string{"--latency"}, doctorOptions{}, true},
		{[]string{"--bogus"}, doctorOptions{}, true},
		{[]string{"x"}, doctorOptions{}, true},
	}
	for _, tc := range cases {
		var stderr bytes.Buffer
		got, err := parseDoctorFlags(tc.args, &stderr)
		if tc.wantErr {
			if exitCode(err) != 2 {
				t.Errorf("%q: exit code %d (err %v), want 2", tc.args, exitCode(err), err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()
	if exitCode(nil) != 0 || exitCode(errors.New("x")) != 1 || exitCode(usageError(errors.New("x"))) != 2 {
		t.Error("exitCode mapping wrong")
	}
	wrapped := errors.Join(errors.New("ctx"), &exitError{code: 3, err: errors.New("home")})
	if exitCode(wrapped) != 3 {
		t.Errorf("wrapped exit code = %d, want 3", exitCode(wrapped))
	}
}
