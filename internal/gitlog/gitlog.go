// Package gitlog is the git adapter behind memory.CodeHistory. It shells out
// to the git binary (no shell) with a minimal environment and a strict
// per-call context, and answers three questions about a checkout: where it
// is and what HEAD is, whether some files differ between a recorded commit
// and HEAD, and whether files have uncommitted changes.
package gitlog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redfoxius/claude-memory/internal/memory"
)

// waitDelay bounds how long a killed git may hold the caller on pipe close.
const waitDelay = 10 * time.Millisecond

// maxCount caps the commit count printed for a stale record ("100+").
const maxCount = 100

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// ErrUnchecked means a verdict could not be established (unknown sha, git
// failure, timeout). Callers treat the record as unchecked, never as stale.
var ErrUnchecked = errors.New("gitlog: unchecked")

// Exec runs git. The zero value uses "git" from PATH.
type Exec struct {
	// Git is the git binary; empty means "git".
	Git string
}

var _ memory.CodeHistory = Exec{}

// childEnv is the whitelisted environment for git children: no inherited
// GIT_* variable (GIT_DIR and friends would redirect git to another repo).
func childEnv() []string {
	env := []string{"GIT_LITERAL_PATHSPECS=1", "GIT_OPTIONAL_LOCKS=0"}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "HOME" || k == "LANG" || k == "XDG_CONFIG_HOME" || strings.HasPrefix(k, "LC_") {
			env = append(env, kv)
		}
	}
	return env
}

// command builds the git invocation. Exposed to the package tests so they
// can assert argv, env and WaitDelay.
func (e Exec) command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	bin := e.Git
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = childEnv()
	cmd.WaitDelay = waitDelay
	return cmd
}

// run executes git and returns stdout and the exit code (-1 when git could
// not be started or the context ended). A non-zero exit is not an error here;
// callers interpret it.
func (e Exec) run(ctx context.Context, dir string, args ...string) (string, int, error) {
	cmd := e.command(ctx, dir, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0, nil
	}
	if ctx.Err() != nil {
		return out.String(), -1, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode(), nil
	}
	return "", -1, err
}

// Resolve implements memory.CodeHistory with one `git rev-parse
// --show-toplevel HEAD`. On an unborn HEAD git prints the top level and
// exits 128; that still yields the checkout with an empty head.
func (e Exec) Resolve(ctx context.Context, cwd string) (memory.Checkout, string, bool, error) {
	if cwd == "" {
		return memory.Checkout{}, "", false, nil
	}
	out, code, err := e.run(ctx, cwd, "rev-parse", "--show-toplevel", "HEAD")
	if err != nil {
		return memory.Checkout{}, "", false, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	top := strings.TrimSpace(lines[0])
	if top == "" || !filepath.IsAbs(top) {
		return memory.Checkout{}, "", false, nil // not a checkout
	}
	co := memory.Checkout{Dir: top, Repo: filepath.Base(top)}
	head := ""
	if code == 0 && len(lines) > 1 && shaRe.MatchString(strings.TrimSpace(lines[1])) {
		head = strings.TrimSpace(lines[1])
	}
	return co, head, true, nil
}

// Head implements memory.CodeHistory.
func (e Exec) Head(ctx context.Context, dir string) (string, error) {
	out, code, err := e.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", nil // unborn
	}
	return strings.TrimSpace(out), nil
}

// Changed implements memory.CodeHistory: a tree comparison (`git diff
// --quiet sha head -- files`), which is right for squash/rebase/rewritten
// shas; the commit count is only fetched for a changed verdict.
func (e Exec) Changed(ctx context.Context, dir, head, sha string, files []string) (bool, int, error) {
	if !shaRe.MatchString(sha) || !shaRe.MatchString(head) || len(files) == 0 {
		return false, 0, ErrUnchecked
	}
	_, code, err := e.run(ctx, dir, append([]string{"diff", "--quiet", sha, head, "--"}, files...)...)
	if err != nil {
		return false, 0, err
	}
	switch code {
	case 0:
		return false, 0, nil
	case 1:
	default:
		return false, 0, fmt.Errorf("%w: git diff exited %d", ErrUnchecked, code)
	}

	out, code, err := e.run(ctx, dir, append([]string{"rev-list", "--count", "--max-count=" + strconv.Itoa(maxCount), sha + ".." + head, "--"}, files...)...)
	if err != nil || code != 0 {
		return true, 0, nil // verdict stands; count unknown
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return true, n, nil
}

// Dirty implements memory.CodeHistory: true when any listed file is
// modified, staged or untracked.
func (e Exec) Dirty(ctx context.Context, dir string, files []string) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}
	out, code, err := e.run(ctx, dir, append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, files...)...)
	if err != nil {
		return false, err
	}
	if code != 0 {
		return false, fmt.Errorf("%w: git status exited %d", ErrUnchecked, code)
	}
	return strings.TrimSpace(out) != "", nil
}
