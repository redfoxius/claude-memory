package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/redfoxius/claude-memory/internal/config"
)

// Doctor: the check registry and its parallel runner (spec AC-57..AC-60,
// AC-67; plan WI-S1-11, Design 13). Doctor is read-only by construction: it
// runs with a read-only FS and Runner, uses only the read halves of the
// probers (DBProber.Probe opens without migrating), never executes the
// registered MCP command or `claude mcp get|list`, and never prints — the
// renderers in report.go print through the Redactor.

// Default doctor bounds (AC-4, AC-59).
const (
	DefaultCheckTimeout   = 3 * time.Second
	DefaultDoctorDeadline = 10 * time.Second
)

// Defaults of the settings doctor reads (they mirror internal/config.Load).
const (
	DefaultOllamaURL   = "http://127.0.0.1:11434"
	DefaultOllamaModel = "bge-m3"
	// SchemaEmbeddingDims is the records.embedding dimension (VECTOR(1024)).
	SchemaEmbeddingDims = 1024
)

// Doctor thresholds (AC-58).
const (
	PGLatencyWarn    = 100 * time.Millisecond
	EmbedLatencyWarn = 500 * time.Millisecond
)

// DoctorDeps is everything doctor reads: the values built in main.go and the
// read-only adapters (AC-57, AC-68).
type DoctorDeps struct {
	Paths    Paths
	Env      Env
	Platform PlatformInfo
	FS       ReadFS // read-only view: doctor cannot write by type
	Runner   Runner // read-only adapter: Mutating commands return ErrReadOnly
	Clock    Clock
	DB       DBProbe // Probe only: doctor never migrates
	Ollama   OllamaProbe
	// Assets is the embedded integration tree (package integration's FS):
	// hook scripts, skills and the CLAUDE.md section to compare against.
	Assets fs.FS
	// Version describes the running binary (e.g. "v0.3.0 (revision abc123)").
	Version string
	// Redactor, when set, receives every secret doctor reads (the DSN
	// password) before any check runs, so the output sink masks it (AC-30).
	Redactor *Redactor
}

// DoctorOptions bound the run (AC-59).
type DoctorOptions struct {
	Timeout  time.Duration // per check; 0 = DefaultCheckTimeout
	Deadline time.Duration // whole run; 0 = DefaultDoctorDeadline
}

// CheckResult is the outcome of one check.
type CheckResult struct {
	ID, Title string
	Status    Status
	Detail    string
	Remedy    string
	Duration  time.Duration
	// NotInstalled is set by install's final doctor on a fail whose owning
	// step the user skipped (or this build does not install): "not installed:
	// <step> skipped". The fail is printed but not counted (AC-62).
	NotInstalled string
}

// DoctorSummary counts results by status.
type DoctorSummary struct {
	Pass, Fail, Warn, Info, Skip int
}

// Total is the number of checks.
func (s DoctorSummary) Total() int { return s.Pass + s.Fail + s.Warn + s.Info + s.Skip }

// DoctorReport is the result of a doctor run, checks in table order.
type DoctorReport struct {
	Checks  []CheckResult
	Summary DoctorSummary
}

// OK reports whether the run passes: no fail, and with strict no warn
// either (AC-59: exit 0 when OK, else 1).
func (r DoctorReport) OK(strict bool) bool {
	return r.HardFails() == 0 && (!strict || r.Summary.Warn == 0)
}

// NotInstalledFails counts fails that install annotated as "not installed:
// <step> skipped" (AC-62); they are printed but do not fail the run. A
// standalone doctor never sets CheckResult.NotInstalled.
func (r DoctorReport) NotInstalledFails() int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == StatusFail && c.NotInstalled != "" {
			n++
		}
	}
	return n
}

// HardFails is the number of fails that count: all of them except those
// annotated as not installed.
func (r DoctorReport) HardFails() int { return r.Summary.Fail - r.NotInstalledFails() }

// Result returns the result of the check with id.
func (r DoctorReport) Result(id string) (CheckResult, bool) {
	for _, c := range r.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return CheckResult{}, false
}

// RunDoctor runs the 23 doctor checks (AC-58) and returns the report.
func RunDoctor(ctx context.Context, deps DoctorDeps, opts DoctorOptions) DoctorReport {
	d := newDoctor(deps)
	return RunChecks(ctx, d.checks(), deps.Clock, opts)
}

// RunChecks runs checks concurrently (AC-59): each starts as soon as its
// prerequisites (Check.Requires, which must name earlier checks) have
// finished, and is skipped ("because <id> failed") when one of them failed
// or was skipped. Each Run gets a context bounded by opts.Timeout and the
// whole run is bounded by opts.Deadline; a check that has not finished by
// then reports fail "timed out". The report lists the checks in input order.
func RunChecks(ctx context.Context, checks []Check, clk Clock, opts DoctorOptions) DoctorReport {
	timeout, deadline := opts.Timeout, opts.Deadline
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	if deadline <= 0 {
		deadline = DefaultDoctorDeadline
	}
	if clk == nil {
		clk = realClock{}
	}
	runCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	type slot struct {
		done chan struct{}
		res  CheckResult
	}
	slots := make([]*slot, len(checks))
	index := map[string]int{}
	for i, c := range checks {
		slots[i] = &slot{done: make(chan struct{}), res: CheckResult{ID: c.ID, Title: c.Title}}
		index[c.ID] = i
	}

	timedOut := func(s *slot, what string) {
		s.res.Status = StatusFail
		s.res.Detail = "timed out " + what
		s.res.Remedy = "re-run with a larger --timeout/--deadline; a hanging network endpoint is the usual cause"
	}

	for i, c := range checks {
		go func() {
			s := slots[i]
			defer close(s.done)
			for _, req := range c.Requires {
				j, ok := index[req]
				if !ok || j >= i {
					s.res.Status = StatusFail
					s.res.Detail = fmt.Sprintf("internal error: prerequisite %q is not an earlier check", req)
					return
				}
				p := slots[j]
				select {
				case <-p.done:
				case <-runCtx.Done():
					timedOut(s, fmt.Sprintf("waiting for %s (--deadline %s reached)", req, deadline))
					return
				}
				if p.res.Status == StatusFail || p.res.Status == StatusSkip {
					verb := "failed"
					if p.res.Status == StatusSkip {
						verb = "was skipped"
					}
					s.res.Status = StatusSkip
					s.res.Detail = "because " + req + " " + verb
					return
				}
			}

			start := clk.Now()
			cctx, ccancel := context.WithTimeout(runCtx, timeout)
			defer ccancel()
			type outcome struct {
				st             Status
				detail, remedy string
			}
			ch := make(chan outcome, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						ch <- outcome{StatusFail, fmt.Sprintf("internal error: %v", r), ""}
					}
				}()
				st, detail, remedy := c.Run(cctx)
				ch <- outcome{st, detail, remedy}
			}()
			select {
			case o := <-ch:
				s.res.Status, s.res.Detail, s.res.Remedy = o.st, o.detail, o.remedy
				if s.res.Status == "" {
					s.res.Status = StatusFail
				}
			case <-cctx.Done():
				if runCtx.Err() != nil {
					timedOut(s, fmt.Sprintf("(--deadline %s reached)", deadline))
				} else {
					timedOut(s, fmt.Sprintf("after %s", timeout))
				}
			}
			s.res.Duration = clk.Now().Sub(start)
		}()
	}

	var rep DoctorReport
	for _, s := range slots {
		<-s.done // every waiter selects on runCtx, so this ends by the deadline
		rep.Checks = append(rep.Checks, s.res)
		switch s.res.Status {
		case StatusPass:
			rep.Summary.Pass++
		case StatusFail:
			rep.Summary.Fail++
		case StatusWarn:
			rep.Summary.Warn++
		case StatusInfo:
			rep.Summary.Info++
		case StatusSkip:
			rep.Summary.Skip++
		}
	}
	return rep
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// ---- shared state ----------------------------------------------------------

// doctor holds the inputs every check shares. prepare reads the env file and
// the manifest once, before the parallel run; probe results that dependents
// need (the DB status) are written by their check before it finishes and read
// only by checks that Require it, so the runner's done channel orders them.
type doctor struct {
	DoctorDeps

	envPath    string
	envExists  bool
	envErr     error // read error other than not-exist
	envFile    *config.EnvFile
	manifest   ManifestLoad
	manifestEr error
	// binPath is the binary path every check compares against: the manifest-
	// recorded one, else Paths.InstalledBinary() (ResolveBinPath, AC-35).
	binPath string

	dbStatus DBStatus // written by pg.connect
	dbErr    error
}

func newDoctor(deps DoctorDeps) *doctor {
	if deps.Clock == nil {
		deps.Clock = realClock{}
	}
	if deps.Env == nil {
		deps.Env = Env{}
	}
	d := &doctor{DoctorDeps: deps, envPath: deps.Paths.EnvFile()}
	d.prepare()
	return d
}

func (d *doctor) prepare() {
	if info, err := d.FS.Stat(d.envPath); err == nil {
		if info.IsDir() {
			d.envErr = fmt.Errorf("%s is a directory", d.envPath)
		} else if b, err := d.FS.ReadFile(d.envPath); err == nil {
			d.envExists = true
			d.envFile = config.ParseEnvData(b, info.Mode())
		} else {
			d.envErr = err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		d.envErr = err
	}
	if dsn, _ := d.setting("MEMORY_PG_DSN", ""); dsn != "" && d.Redactor != nil {
		for _, pw := range dsnPasswords(dsn) {
			d.Redactor.Register(pw)
		}
	}
	d.manifest, d.manifestEr = LoadManifest(d.FS, d.Paths)
	d.binPath = ResolveBinPath(d.Paths, d.manifest.Manifest, false)
}

// setting returns the effective value of key as the binary would see it:
// the process environment wins over the env file (config.LoadFromFile sets
// only unset keys), else def. source says where it came from.
func (d *doctor) setting(key, def string) (val, source string) {
	if v := d.Env.Get(key); v != "" {
		return v, "environment"
	}
	if d.envFile != nil {
		if v := d.envFile.Values[key]; v != "" {
			return v, "env file"
		}
	}
	return def, "default"
}

func (d *doctor) ollamaURL() string {
	u, _ := d.setting("MEMORY_OLLAMA_URL", DefaultOllamaURL)
	return u
}

func (d *doctor) ollamaModel() string {
	m, _ := d.setting("MEMORY_OLLAMA_MODEL", DefaultOllamaModel)
	return m
}

// prRepos reports whether PR ingest is configured (MEMORY_PR_INGEST_REPOS).
func (d *doctor) prRepos() bool {
	v, _ := d.setting("MEMORY_PR_INGEST_REPOS", "")
	return strings.Trim(v, " ,") != ""
}

// dsnPasswords returns every form of the password of a DSN that must be
// masked: the percent-decoded password and the password as written (they
// differ when the DSN encodes it). URL DSNs go through url.Parse; only when
// that fails (an unencoded '@' or '/' in the password) does the manual split
// on the last '@' apply. Key/value DSNs are scanned quote-aware (libpq: a
// value may be 'single quoted' with \' and \\ escapes).
func dsnPasswords(dsn string) []string {
	var out []string
	add := func(pw string) {
		if pw != "" && !slices.Contains(out, pw) {
			out = append(out, pw)
		}
	}
	if _, rest, ok := strings.Cut(dsn, "://"); ok {
		if u, err := url.Parse(dsn); err == nil {
			if pw, set := u.User.Password(); set {
				add(pw)
			}
			// As written, when the DSN encodes it (the Redactor also learns
			// the pctEncode form of the decoded one).
			auth := rest
			if i := strings.IndexAny(rest, "/?#"); i >= 0 {
				auth = rest[:i]
			}
			if at := strings.LastIndex(auth, "@"); at >= 0 {
				if _, raw, ok := strings.Cut(auth[:at], ":"); ok {
					add(raw)
				}
			}
			return out
		}
		// Unparseable (an unencoded '@' or '/' in the password): split on the
		// last '@' before the first '/', else on the last '@'.
		end := len(rest)
		if i := strings.Index(rest, "/"); i >= 0 && strings.Contains(rest[:i], "@") {
			end = i
		}
		if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
			if _, pw, ok := strings.Cut(rest[:at], ":"); ok {
				add(pw)
				if dec, err := url.PathUnescape(pw); err == nil {
					add(dec)
				}
			}
		}
		return out
	}
	for _, kv := range scanKeyValueDSN(dsn) {
		if kv.key == "password" {
			add(kv.val)
			add(kv.raw)
		}
	}
	return out
}

type dsnPair struct{ key, val, raw string }

// scanKeyValueDSN splits a libpq key/value connection string. val is the
// unquoted, unescaped value; raw is the value as written (inside the quotes
// for a quoted one). A malformed tail ends the scan.
func scanKeyValueDSN(s string) []dsnPair {
	var out []dsnPair
	i := 0
	skipSpace := func() {
		for i < len(s) && strings.ContainsRune(" \t\r\n", rune(s[i])) {
			i++
		}
	}
	for {
		skipSpace()
		start := i
		for i < len(s) && s[i] != '=' && !strings.ContainsRune(" \t\r\n", rune(s[i])) {
			i++
		}
		key := s[start:i]
		skipSpace()
		if key == "" || i >= len(s) || s[i] != '=' {
			return out
		}
		i++
		skipSpace()
		var val, raw strings.Builder
		if i < len(s) && s[i] == '\'' {
			i++
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '\\' && i+1 < len(s) {
					raw.WriteByte(c)
					raw.WriteByte(s[i+1])
					val.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == '\'' {
					i++
					closed = true
					break
				}
				raw.WriteByte(c)
				val.WriteByte(c)
				i++
			}
			if !closed {
				// Unterminated quote: still report what was read, so the
				// password is masked.
				out = append(out, dsnPair{key, val.String(), raw.String()})
				return out
			}
		} else {
			for i < len(s) && !strings.ContainsRune(" \t\r\n", rune(s[i])) {
				c := s[i]
				if c == '\\' && i+1 < len(s) {
					raw.WriteByte(c)
					raw.WriteByte(s[i+1])
					val.WriteByte(s[i+1])
					i += 2
					continue
				}
				raw.WriteByte(c)
				val.WriteByte(c)
				i++
			}
		}
		out = append(out, dsnPair{key, val.String(), raw.String()})
	}
}

// checks is the AC-58 table, in report order.
func (d *doctor) checks() []Check {
	return []Check{
		{ID: "binary.version", Title: "claude-memory binary", Run: d.checkBinary},
		{ID: "env.file", Title: "env file exists", Run: d.checkEnvFile},
		{ID: "env.perms", Title: "env file permissions", Requires: []string{"env.file"}, Run: d.checkEnvPerms},
		{ID: "env.format", Title: "env file format and DSN", Requires: []string{"env.file"}, Run: d.checkEnvFormat},
		{ID: "pg.connect", Title: "Postgres connection", Requires: []string{"env.format"}, Run: d.checkPGConnect},
		{ID: "pg.latency", Title: "Postgres round trip", Requires: []string{"pg.connect"}, Run: d.checkPGLatency},
		{ID: "pg.vector", Title: "pgvector extension", Requires: []string{"pg.connect"}, Run: d.checkPGVector},
		{ID: "pg.schema", Title: "schema migrations", Requires: []string{"pg.connect"}, Run: d.checkPGSchema},
		{ID: "ollama.reachable", Title: "Ollama reachable", Run: d.checkOllamaReachable},
		{ID: "ollama.model", Title: "embedding model pulled", Requires: []string{"ollama.reachable"}, Run: d.checkOllamaModel},
		{ID: "ollama.embed", Title: "embedding round trip", Requires: []string{"ollama.model"}, Run: d.checkOllamaEmbed},
		{ID: "tools.git", Title: "git on PATH", Run: d.checkToolGit},
		{ID: "tools.claude", Title: "claude CLI on PATH", Run: d.checkToolClaude},
		{ID: "tools.az", Title: "az CLI on PATH", Run: d.checkToolAz},
		{ID: "tools.gh", Title: "gh CLI on PATH and logged in", Run: d.checkToolGh},
		{ID: "tools.glab", Title: "glab CLI on PATH and logged in", Run: d.checkToolGlab},
		{ID: "mcp.registered", Title: "MCP server registered", Run: d.checkMCP},
		{ID: "hooks.scripts", Title: "hook scripts", Run: d.checkHookScripts},
		{ID: "hooks.settings", Title: "hooks in settings.json", Run: d.checkHookSettings},
		{ID: "skills", Title: "skills", Run: d.checkSkills},
		{ID: "claude-md", Title: "CLAUDE.md memory block", Run: d.checkClaudeMD},
		{ID: "namespaces", Title: "namespaces.yaml", Run: d.checkNamespaces},
		{ID: "jobs", Title: "scheduled jobs", Run: d.checkJobs},
		{ID: "dirs.state", Title: "state directory", Run: d.checkStateDir},
		{ID: "manifest", Title: "install manifest", Run: d.checkManifest},
	}
}

// CheckIDs lists the doctor check ids in report order.
func CheckIDs() []string {
	d := &doctor{}
	var ids []string
	for _, c := range d.checks() {
		ids = append(ids, c.ID)
	}
	return ids
}

// redact masks secrets in a message a check builds from an error (belt and
// braces: the renderer redacts everything again).
func (d *doctor) redact(s string) string {
	if d.Redactor == nil {
		return NewRedactor().Redact(s)
	}
	return d.Redactor.Redact(s)
}

func pass(detail string) (Status, string, string) { return StatusPass, detail, "" }
