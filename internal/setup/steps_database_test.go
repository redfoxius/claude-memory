package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// Tests for WI-S2-4b: the database step. FakeFS / FakeRunner / scripted
// prompter and prober; no psql, sudo, Docker or real HOME.

// ---- fakes -----------------------------------------------------------------

// scriptDB is a DBProber whose Probe answer is a function of the target (so a
// test can make a password right or wrong) and which records what it saw.
type scriptDB struct {
	mu         sync.Mutex
	fn         func(t DBTarget) (DBStatus, error)
	dsns       []string
	migrated   []string
	migrateErr error
	evidence   bool
}

var healthy = DBStatus{Connected: true, VectorVersion: "0.8.0", Migrations: []MigrationStatus{{ID: "0001"}, {ID: "0002"}}}

func fixedDB(st DBStatus, err error) *scriptDB {
	return &scriptDB{fn: func(DBTarget) (DBStatus, error) { return st, err }, evidence: true}
}

func (d *scriptDB) set(st DBStatus, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fn = func(DBTarget) (DBStatus, error) { return st, err }
}

func (d *scriptDB) Probe(_ context.Context, dsn string) (DBStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dsns = append(d.dsns, dsn)
	t, err := ParseDBTarget(dsn)
	if err != nil {
		return DBStatus{ErrorClass: DBErrDSN}, errors.New("bad dsn")
	}
	return d.fn(t)
}

func (d *scriptDB) Migrate(_ context.Context, dsn string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.migrated = append(d.migrated, dsn)
	return d.migrateErr
}

func (d *scriptDB) LocalServerEvidence(context.Context) (bool, string) {
	return d.evidence, "TCP 127.0.0.1:5432 and a socket"
}

var (
	authErr = DBStatus{ErrorClass: DBErrAuth}
	noDBErr = DBStatus{ErrorClass: DBErrNoDB}
	noVec   = DBStatus{Connected: true, Migrations: []MigrationStatus{{ID: "0001", Missing: []string{"records"}}}}
)

// pwPrompter is a strict scripted Prompter with Text and Secret answers (the
// shared FakePrompter never answers those).
type pwPrompter struct {
	t           testing.TB
	interactive bool
	script      []pwStep
	calls       []string
	onSelect    func(q string) // runs before a Select is answered
}

type pwStep struct {
	kind, q string
	idx     int
	yes     bool
	text    string
	err     error
}

func newPW(t testing.TB, interactive bool) *pwPrompter {
	p := &pwPrompter{t: t, interactive: interactive}
	t.Cleanup(func() {
		if len(p.script) > 0 {
			t.Errorf("pwPrompter: %d prompt(s) never asked, first: %s %q", len(p.script), p.script[0].kind, p.script[0].q)
		}
	})
	return p
}

func (p *pwPrompter) sel(q string, idx int) *pwPrompter {
	p.script = append(p.script, pwStep{kind: "select", q: q, idx: idx})
	return p
}
func (p *pwPrompter) conf(q string, yes bool) *pwPrompter {
	p.script = append(p.script, pwStep{kind: "confirm", q: q, yes: yes})
	return p
}
func (p *pwPrompter) text(q, answer string) *pwPrompter {
	p.script = append(p.script, pwStep{kind: "text", q: q, text: answer})
	return p
}
func (p *pwPrompter) secret(q, answer string) *pwPrompter {
	p.script = append(p.script, pwStep{kind: "secret", q: q, text: answer})
	return p
}

func (p *pwPrompter) next(kind, q string) (pwStep, bool) {
	p.calls = append(p.calls, kind+" "+q)
	if !p.interactive {
		p.t.Errorf("pwPrompter: %s %q asked non-interactively", kind, q)
		return pwStep{}, false
	}
	if len(p.script) == 0 {
		p.t.Errorf("pwPrompter: unexpected %s %q", kind, q)
		return pwStep{}, false
	}
	e := p.script[0]
	p.script = p.script[1:]
	if e.kind != kind || !strings.Contains(q, e.q) {
		p.t.Errorf("pwPrompter: got %s %q, want %s containing %q", kind, q, e.kind, e.q)
		return pwStep{}, false
	}
	return e, true
}

func (p *pwPrompter) Select(q string, opts []string, def int) (int, error) {
	if p.onSelect != nil {
		p.onSelect(q)
	}
	e, ok := p.next("select", q)
	if !ok {
		return 0, ErrInterrupted
	}
	if e.idx < 0 {
		return def, nil
	}
	return e.idx, nil
}

func (p *pwPrompter) Confirm(q string, _ bool) (bool, error) {
	e, ok := p.next("confirm", q)
	if !ok {
		return false, ErrInterrupted
	}
	return e.yes, nil
}

func (p *pwPrompter) Text(q, def string, validate func(string) error) (string, error) {
	e, ok := p.next("text", q)
	if !ok {
		return "", ErrInterrupted
	}
	v := e.text
	if v == "" {
		v = def
	}
	if validate != nil {
		if err := validate(v); err != nil {
			p.t.Errorf("pwPrompter: text %q: scripted answer %q rejected: %v", q, v, err)
			return "", ErrInterrupted
		}
	}
	return v, nil
}

func (p *pwPrompter) Secret(q string) (string, error) {
	e, ok := p.next("secret", q)
	if !ok {
		return "", ErrInterrupted
	}
	return e.text, nil
}

func (p *pwPrompter) Interactive() bool { return p.interactive }

var _ Prompter = (*pwPrompter)(nil)

// counterRand is a deterministic entropy source: every call yields different
// bytes (each password differs) without crypto/rand.
type counterRand struct{ n byte }

func (c *counterRand) Read(b []byte) (int, error) {
	c.n++
	for i := range b {
		b[i] = c.n*37 + byte(i)*11
	}
	return len(b), nil
}

// ---- unit harness (s23) ----------------------------------------------------

func (h *s23) dbRP(db DBProbe) ReadPorts {
	rp := h.rp()
	rp.DB = db
	return rp
}

func dbSt(in Inputs) *RunState { return NewRunState(in) }

func localTarget(pw string, mode DBMode) DBTarget {
	t := DBTarget{Host: "localhost", Port: "5432", Name: "claude_memory", User: "claude_memory", SSLMode: "disable", Mode: mode, Source: SourceGenerated}
	return t.WithPassword(pw)
}

func stateOf(d Detection, id string) State {
	for _, a := range d.Artifacts {
		if a.ID == id {
			return a.State
		}
	}
	return ""
}

func writeBootstrap(t *testing.T, h *s23, content string) {
	t.Helper()
	h.write(h.p.Bootstrap(), content, 0o600)
}

// writeBootstrapFor writes the bootstrap.sql install would have rendered for
// the target with this password.
func writeBootstrapFor(t *testing.T, h *s23, pw string) {
	t.Helper()
	tg := localTarget(pw, DBModeCreate)
	out, err := RenderBootstrap([]byte("-- body\n"), tg.User, tg.Name, pw)
	if err != nil {
		t.Fatal(err)
	}
	writeBootstrap(t, h, string(out))
}

// ---- Seed ------------------------------------------------------------------

func TestDatabaseSeed(t *testing.T) {
	t.Parallel()
	const pw = "Seed-pw-1234"
	envDSN := "MEMORY_PG_DSN=postgresql://u:" + pw + "@env.example:5432/dbf?sslmode=require\n"
	ctx := context.Background()
	newStep := func() (DatabaseStep, *Redactor) {
		r := NewRedactor()
		return DatabaseStep{Redactor: r}, r
	}

	t.Run("flag DSN plus the stdin password", func(t *testing.T) {
		h := newS23(t)
		s, red := newStep()
		st := NewRunState(Inputs{PGDSN: "postgresql://u@flag.example:5433/dbx?sslmode=require", PGPassword: pw})
		st.Prior.EnvDoc = []byte(envDSN)
		notes, err := s.Seed(ctx, h.rp(), st)
		if err != nil || len(notes) != 0 {
			t.Fatalf("%v %v", notes, err)
		}
		d := st.DB.Get()
		if d.Host != "flag.example" || d.Port != "5433" || d.Name != "dbx" || !d.SameConnection(d.WithPassword(pw)) || st.DB.Source() != SourceFlag || d.Mode != DBModeExisting {
			t.Errorf("%#v %v", d, st.DB)
		}
		if red.Redact("x "+pw+" y") == "x "+pw+" y" {
			t.Error("the stdin password was not registered with the Redactor")
		}
	})
	t.Run("a flag DSN with a password is refused (AC-31)", func(t *testing.T) {
		h := newS23(t)
		s, _ := newStep()
		_, err := s.Seed(ctx, h.rp(), NewRunState(Inputs{PGDSN: "postgresql://u:secret@h/db"}))
		if err == nil || !strings.Contains(err.Error(), "not argv") || strings.Contains(err.Error(), "secret") {
			t.Errorf("%v", err)
		}
	})
	t.Run("env file", func(t *testing.T) {
		h := newS23(t)
		s, red := newStep()
		st := NewRunState(Inputs{})
		st.Prior.EnvDoc = []byte(envDSN)
		if notes, err := s.Seed(ctx, h.rp(), st); err != nil || len(notes) != 0 {
			t.Fatalf("%v %v", notes, err)
		}
		if d := st.DB.Get(); d.Host != "env.example" || st.DB.Source() != SourceEnvFile || !d.HasPassword() {
			t.Errorf("%#v %v", d, st.DB)
		}
		if red.Redact(pw) == pw {
			t.Error("the env-file password was not registered")
		}
	})
	t.Run("env file wins over Env, with one drift note", func(t *testing.T) { // N10
		h := newS23(t)
		h.env = Env{"MEMORY_PG_DSN": "postgresql://u:Other-pw-999@shell.example:5432/dbs"}
		s, _ := newStep()
		st := NewRunState(Inputs{})
		st.Prior.EnvDoc = []byte(envDSN)
		notes, err := s.Seed(ctx, h.rp(), st)
		if err != nil || len(notes) != 1 || notes[0].Level != NoteWarn || !strings.Contains(notes[0].Text, "differs from the env file") {
			t.Fatalf("%v %v", notes, err)
		}
		if st.DB.Get().Host != "env.example" {
			t.Errorf("adopted the shell value: %#v", st.DB.Get())
		}
		for _, n := range notes {
			if strings.Contains(n.Text, "Other-pw-999") || strings.Contains(n.Text, pw) {
				t.Errorf("note leaks a password: %s", n.Text)
			}
		}
	})
	t.Run("Env only", func(t *testing.T) {
		h := newS23(t)
		h.env = Env{"MEMORY_PG_DSN": "postgresql://u:Other-pw-999@shell.example:5432/dbs"}
		s, _ := newStep()
		st := NewRunState(Inputs{})
		if notes, err := s.Seed(ctx, h.rp(), st); err != nil || len(notes) != 0 {
			t.Fatalf("%v %v", notes, err)
		}
		if st.DB.Get().Host != "shell.example" || st.DB.Source() != SourceEnv {
			t.Errorf("%#v %v", st.DB.Get(), st.DB)
		}
	})
	t.Run("an unusable value leaves DB unset with a note", func(t *testing.T) {
		h := newS23(t)
		s, _ := newStep()
		st := NewRunState(Inputs{})
		st.Prior.EnvDoc = []byte("MEMORY_PG_DSN=$(pass show db)\n")
		// A line the plain format cannot hold and AC-28 cannot recover is not a
		// usable value: the DB stays unset (the step asks), with a note.
		notes, err := s.Seed(ctx, h.rp(), st)
		if err != nil || len(notes) != 1 || st.DB.IsSet() {
			t.Errorf("%v %v set=%v", notes, err, st.DB.IsSet())
		}
		st = NewRunState(Inputs{})
		h.env = Env{"MEMORY_PG_DSN": "postgresql://u:p@h:99999/db"}
		if notes, err := s.Seed(ctx, h.rp(), st); err != nil || len(notes) != 1 || st.DB.IsSet() {
			t.Errorf("%v %v", notes, err)
		}
	})
	t.Run("nothing anywhere", func(t *testing.T) {
		h := newS23(t)
		s, _ := newStep()
		st := NewRunState(Inputs{PGPassword: pw})
		if notes, err := s.Seed(ctx, h.rp(), st); err != nil || len(notes) != 0 || st.DB.IsSet() {
			t.Errorf("%v %v", notes, err)
		}
	})
	t.Run("the stdin password replaces the file's", func(t *testing.T) {
		h := newS23(t)
		s, _ := newStep()
		st := NewRunState(Inputs{PGPassword: "Stdin-pw-5678"})
		st.Prior.EnvDoc = []byte(envDSN)
		if _, err := s.Seed(ctx, h.rp(), st); err != nil {
			t.Fatal(err)
		}
		d := st.DB.Get()
		if !d.SameConnection(d.WithPassword("Stdin-pw-5678")) {
			t.Error("the stdin password was not applied")
		}
	})
}

// ---- Detect ----------------------------------------------------------------

func TestDatabaseDetect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const pw = "Detect-pw-1234"
	cases := []struct {
		name       string
		status     DBStatus
		mode       DBMode
		file       bool
		topology   Topology
		host       string
		pw         string
		wantState  State
		wantDB     State
		wantFile   State
		wantDetail string
		wantRemedy string
	}{
		{name: "ok, no file", status: healthy, wantState: StateOK, wantDB: StateOK, wantFile: StateOK, wantDetail: "connected"},
		{name: "ok with a leftover file", status: healthy, file: true, wantState: StateOutdated, wantDB: StateOK, wantFile: StateOutdated, wantDetail: "can be removed"},
		{name: "auth with the file is pending", status: authErr, file: true, wantState: StateBlocked, wantDB: StateBlocked, wantFile: StateOK,
			wantDetail: "awaiting user action", wantRemedy: "sudo -u postgres psql"},
		{name: "nodb with the file is pending", status: noDBErr, file: true, wantState: StateBlocked, wantDB: StateBlocked, wantFile: StateOK, wantDetail: "awaiting user action"},
		{name: "no vector with the file is pending", status: noVec, file: true, wantState: StateBlocked, wantDB: StateBlocked, wantFile: StateOK, wantDetail: "awaiting user action"},
		{name: "auth, existing mode, no file fails", status: authErr, wantState: StateBlocked, wantDB: StateBlocked, wantFile: StateOK, wantDetail: "authentication failed for role claude_memory"},
		{name: "auth, create mode, no file needs bootstrap", status: authErr, mode: DBModeCreate, wantState: StateAbsent, wantDB: StateAbsent, wantFile: StateOK, wantDetail: "install writes bootstrap.sql"},
		{name: "nodb needs bootstrap", status: noDBErr, wantState: StateAbsent, wantDB: StateAbsent, wantFile: StateOK, wantDetail: "does not exist"},
		{name: "no vector needs bootstrap", status: noVec, wantState: StateAbsent, wantDB: StateAbsent, wantFile: StateOK, wantDetail: "vector extension is not installed"},
		{name: "remote topology cannot be bootstrapped", status: noDBErr, topology: TopologyRemote, wantState: StateBlocked, wantDB: StateBlocked, wantFile: StateOK, wantDetail: "create it on the server"},
		{name: "remote host cannot be bootstrapped", status: noVec, host: "db.example", wantState: StateBlocked, wantDB: StateBlocked, wantFile: StateOK, wantDetail: "create it on the server"},
		{name: "unsafe password is blocked, not rendered", status: noDBErr, pw: "pa$$word word", wantState: StateBlocked, wantDB: StateBlocked, wantFile: StateOK,
			wantDetail: "cannot be written into bootstrap.sql safely", wantRemedy: "app-role.psql"},
		{name: "unreachable fails", status: DBStatus{ErrorClass: DBErrUnreachable}, wantState: StateBlocked, wantDB: StateBlocked, wantFile: StateOK, wantDetail: "cannot reach localhost:5432"},
		{name: "hba fails", status: DBStatus{ErrorClass: DBErrHBA}, wantState: StateBlocked, wantDB: StateBlocked, wantFile: StateOK, wantDetail: "pg_hba.conf"},
		{name: "unreadable catalog is blocked", status: DBStatus{Connected: true}, wantState: StateOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newS23(t)
			st := dbSt(Inputs{})
			d := localTarget(firstNonEmpty(c.pw, pw), c.mode)
			if c.host != "" {
				d.Host = c.host
			}
			st.DB.Set(d, SourceEnvFile)
			if c.topology != "" {
				st.Topology.Set(c.topology, SourceFlag)
			}
			if c.file {
				writeBootstrapFor(t, h, firstNonEmpty(c.pw, pw))
			}
			var perr error
			if c.name == "unreadable catalog is blocked" {
				c.status.VectorVersion, perr = "", errors.New("permission denied")
				c.wantState, c.wantDB, c.wantFile, c.wantDetail = StateBlocked, StateBlocked, StateOK, "catalog could not be read"
			}
			det := (DatabaseStep{}).Detect(ctx, h.dbRP(fixedDB(c.status, perr)), st)
			if det.State != c.wantState || stateOf(det, DatabaseArtifact) != c.wantDB || stateOf(det, BootstrapFileArtifact) != c.wantFile {
				t.Errorf("state %s db %s file %s, want %s %s %s (%s)", det.State, stateOf(det, DatabaseArtifact), stateOf(det, BootstrapFileArtifact), c.wantState, c.wantDB, c.wantFile, det.Detail)
			}
			if !strings.Contains(det.Detail, c.wantDetail) || !strings.Contains(det.Remedy, c.wantRemedy) {
				t.Errorf("detail %q remedy %q, want %q / %q", det.Detail, det.Remedy, c.wantDetail, c.wantRemedy)
			}
			for _, leak := range []string{pw, c.pw} {
				if leak != "" && (strings.Contains(det.Detail, leak) || strings.Contains(det.Remedy, leak)) {
					t.Errorf("Detect leaks the password: %q / %q", det.Detail, det.Remedy)
				}
			}
			if c.file && stateOf(det, BootstrapFileArtifact) == StateOK && !strings.Contains(stateDetail(det, BootstrapFileArtifact), "still needed") {
				t.Errorf("pending file detail: %q", stateDetail(det, BootstrapFileArtifact))
			}
		})
	}

	t.Run("no DB chosen yet", func(t *testing.T) {
		h := newS23(t)
		det := (DatabaseStep{}).Detect(ctx, h.dbRP(fixedDB(healthy, nil)), dbSt(Inputs{}))
		if det.State != StateAbsent || !strings.Contains(det.Detail, "no database chosen") || !(DatabaseStep{}).MissingInput(dbSt(Inputs{})) {
			t.Errorf("%+v", det)
		}
	})
	t.Run("macOS remedy is the plain psql command", func(t *testing.T) {
		h := newS23(t)
		h.plat.OS = OSDarwin
		writeBootstrapFor(t, h, pw)
		st := dbSt(Inputs{})
		st.DB.Set(localTarget(pw, DBModeExisting), SourceEnvFile)
		det := (DatabaseStep{}).Detect(ctx, h.dbRP(fixedDB(authErr, nil)), st)
		if !strings.Contains(det.Remedy, "psql -v ON_ERROR_STOP=1 -d postgres -f ") || strings.Contains(det.Remedy, "sudo -u") {
			t.Errorf("%s", det.Remedy)
		}
	})
	t.Run("a probe error with a DSN in it does not leak", func(t *testing.T) {
		h := newS23(t)
		st := dbSt(Inputs{})
		st.DB.Set(localTarget(pw, DBModeExisting), SourceEnvFile)
		red := NewRedactor()
		red.Register(pw)
		det := DatabaseStep{Redactor: red}.Detect(ctx, h.dbRP(fixedDB(DBStatus{ErrorClass: DBErrOther}, errors.New("boom for "+pw+" at postgresql://u:"+pw+"@h/d"))), st)
		if strings.Contains(det.Detail, pw) || !strings.Contains(det.Detail, "boom") {
			t.Errorf("%q", det.Detail)
		}
	})
	t.Run("the AC-26 warning shows when the DSN moves to another database", func(t *testing.T) {
		h := newS23(t)
		st := dbSt(Inputs{})
		st.Prior.EnvDoc = []byte("MEMORY_PG_DSN=postgresql://u:oldpassword@old.example:5432/old\n")
		d := localTarget(pw, DBModeExisting)
		st.DB.Set(d, SourceFlag)
		det := (DatabaseStep{}).Detect(ctx, h.dbRP(fixedDB(healthy, nil)), st)
		if len(det.Notes) != 1 || !strings.Contains(det.Notes[0].Text, "pg_dump") || !strings.Contains(det.Notes[0].Text, "old.example") || strings.Contains(det.Notes[0].Text, "oldpassword") {
			t.Errorf("%+v", det.Notes)
		}
		// Same database, other credentials: no warning.
		st.Prior.EnvDoc = []byte("MEMORY_PG_DSN=postgresql://other:pw@localhost:5432/claude_memory\n")
		if det := (DatabaseStep{}).Detect(ctx, h.dbRP(fixedDB(healthy, nil)), st); len(det.Notes) != 0 {
			t.Errorf("%+v", det.Notes)
		}
	})
}

func stateDetail(d Detection, id string) string {
	for _, a := range d.Artifacts {
		if a.ID == id {
			return a.Detail
		}
	}
	return ""
}

// ---- Plan / Apply ----------------------------------------------------------

func TestDatabasePlanApplyCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newS23(t)
	h.plat.OS = OSLinux
	step := DatabaseStep{Version: "v1.0.0", AppRolePSQL: []byte("-- body\n")}
	st := dbSt(Inputs{})
	st.DB.Set(localTarget(bootstrapSentinel, DBModeCreate), SourceGenerated)
	db := fixedDB(authErr, nil)

	plan, err := step.Plan(ctx, h.dbRP(db), st, Choices{DatabaseArtifact: ChoiceApply, BootstrapFileArtifact: ChoiceKeep})
	if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Verb != "write" || plan.Actions[0].Path != h.p.Bootstrap() || len(plan.Diffs) != 0 {
		t.Fatalf("%+v %v", plan, err)
	}
	if strings.Contains(plan.Actions[0].Desc, bootstrapSentinel) {
		t.Error("the Action leaks the password")
	}
	if len(h.fs.Writes()) != 0 {
		t.Errorf("Plan wrote: %v", h.fs.Writes())
	}

	res, err := step.Apply(ctx, h.wpDB(db), st, plan)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(h.p.Bootstrap())
	if err != nil {
		t.Fatal(err)
	}
	want := "\\set app_user 'claude_memory'\n\\set app_db 'claude_memory'\n\\set app_pw '" + bootstrapSentinel + "'\n-- body\n"
	if string(got) != want {
		t.Errorf("bootstrap.sql:\n%s\nwant:\n%s", got, want)
	}
	if fi, _ := os.Stat(h.p.Bootstrap()); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(h.p.StateDir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode().Perm())
	}
	if res.Await == nil || len(res.Await.Artifacts) != 1 || res.Await.Artifacts[0] != DatabaseArtifact {
		t.Fatalf("Await %+v", res.Await)
	}
	joined := strings.Join(res.Await.Instructions, "\n")
	if !strings.Contains(joined, "sudo -u postgres psql -v ON_ERROR_STOP=1 -d postgres -f - < "+h.p.Bootstrap()) ||
		!strings.Contains(joined, "never runs sudo or psql") || strings.Contains(joined, bootstrapSentinel) {
		t.Errorf("instructions:\n%s", joined)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Kind != KindDir || res.Artifacts[0].Path != h.p.StateDir {
		t.Errorf("artifacts %+v", res.Artifacts)
	}
	if n := len(h.runner.Calls()); n != 0 {
		t.Errorf("the step ran %d command(s): %v", n, h.runner.Calls())
	}

	// An existing state dir is not recorded again.
	h2 := newS23(t)
	if err := os.MkdirAll(h2.p.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err = step.Apply(ctx, h2.wpDB(db), st, plan)
	if err != nil || len(res.Artifacts) != 0 {
		t.Errorf("%+v %v", res, err)
	}
}

func (h *s23) wpDB(db DBProber) WritePorts {
	rp := h.rp()
	rp.DB = db
	return WritePorts{ReadPorts: rp, FS: h.fs, Runner: h.runner, DB: db}
}

func TestDatabasePlanRefusesWhatBootstrapCannotFix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		status DBStatus
		pw     string
		mode   DBMode
		want   string
	}{
		{"healthy", healthy, bootstrapSentinel, DBModeExisting, "connected"},
		{"auth on an existing role", authErr, bootstrapSentinel, DBModeExisting, "authentication failed"},
		{"unsafe password", noDBErr, "pa ss'word1", DBModeExisting, "cannot be written into bootstrap.sql safely"},
	} {
		h := newS23(t)
		st := dbSt(Inputs{})
		st.DB.Set(localTarget(c.pw, c.mode), SourceEnvFile)
		step := DatabaseStep{AppRolePSQL: []byte("-- b\n")}
		_, err := step.Plan(ctx, h.dbRP(fixedDB(c.status, nil)), st, Choices{DatabaseArtifact: ChoiceApply})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Plan error %v", c.name, err)
		}
		if _, err := step.Apply(ctx, h.wpDB(fixedDB(c.status, nil)), st, Plan{Actions: []Action{{Artifact: DatabaseArtifact}}}); err == nil {
			t.Errorf("%s: Apply wrote bootstrap.sql", c.name)
		}
		if _, serr := os.Stat(h.p.Bootstrap()); serr == nil {
			t.Errorf("%s: bootstrap.sql exists", c.name)
		}
		if strings.Contains(fmt.Sprint(err), c.pw) {
			t.Errorf("%s: error leaks the password", c.name)
		}
	}
}

func TestDatabaseRemoveLeftoverFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newS23(t)
	st := dbSt(Inputs{})
	st.DB.Set(localTarget(bootstrapSentinel, DBModeExisting), SourceEnvFile)
	writeBootstrap(t, h, "x")
	db := fixedDB(healthy, nil)
	step := DatabaseStep{}
	plan, err := step.Plan(ctx, h.dbRP(db), st, Choices{DatabaseArtifact: ChoiceKeep, BootstrapFileArtifact: ChoiceApply})
	if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Verb != "remove" {
		t.Fatalf("%+v %v", plan, err)
	}
	res, err := step.Apply(ctx, h.wpDB(db), st, plan)
	if err != nil || res.Await != nil || len(res.Artifacts) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(h.p.Bootstrap()); err == nil {
		t.Error("bootstrap.sql still there")
	}
	// Removing a file that is already gone is not an error.
	if _, err := step.Apply(ctx, h.wpDB(db), st, plan); err != nil {
		t.Errorf("second remove: %v", err)
	}
	if d := step.Detect(ctx, h.dbRP(db), st); d.State != StateOK {
		t.Errorf("after removal: %+v", d)
	}
}

// ---- Configure -------------------------------------------------------------

func TestDatabaseConfigureYes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	step := func() DatabaseStep { return DatabaseStep{Rand: &counterRand{}, Redactor: NewRedactor()} }

	t.Run("topology local with no DSN takes the create defaults", func(t *testing.T) { // AC-21
		h := newS23(t)
		st := dbSt(Inputs{Yes: true})
		st.Topology.Set(TopologyLocal, SourceFlag)
		s := step()
		if err := s.Configure(ctx, h.dbRP(fixedDB(authErr, nil)), autoPrompter{}, st); err != nil {
			t.Fatal(err)
		}
		d := st.DB.Get()
		if d.Host != "localhost" || d.Port != "5432" || d.Name != "claude_memory" || d.User != "claude_memory" || d.SSLMode != "disable" ||
			d.Mode != DBModeCreate || d.Source != SourceGenerated || st.DB.Source() != SourceGenerated {
			t.Errorf("%#v %v", d, st.DB)
		}
		if err := ValidateBootstrapPassword(string(d.password)); err != nil || len(string(d.password)) != 43 {
			t.Errorf("generated password %q: %v", string(d.password), err)
		}
		if s.Redactor.Redact(string(d.password)) == string(d.password) {
			t.Error("the generated password was not registered with the Redactor")
		}
		if strings.Contains(d.String(), string(d.password)) || strings.Contains(fmt.Sprintf("%v %+v %#v", d, d, d), string(d.password)) {
			t.Error("DBTarget formatting leaks the password")
		}
	})
	t.Run("topology remote with no DSN fails naming the flag", func(t *testing.T) {
		h := newS23(t)
		st := dbSt(Inputs{Yes: true})
		st.Topology.Set(TopologyRemote, SourceFlag)
		err := step().Configure(ctx, h.dbRP(fixedDB(healthy, nil)), autoPrompter{}, st)
		if err == nil || !strings.Contains(err.Error(), "--pg-dsn") || st.DB.IsSet() {
			t.Errorf("%v", err)
		}
	})
	t.Run("the stdin password is used on the create path", func(t *testing.T) {
		h := newS23(t)
		st := dbSt(Inputs{Yes: true, PGPassword: "Given-pw-0001"})
		st.Topology.Set(TopologyLocal, SourceFlag)
		if err := step().Configure(ctx, h.dbRP(fixedDB(authErr, nil)), autoPrompter{}, st); err != nil {
			t.Fatal(err)
		}
		if d := st.DB.Get(); string(d.password) != "Given-pw-0001" || st.DB.Source() != SourceFlag {
			t.Errorf("%v", st.DB)
		}
	})
	t.Run("an auth failure fails, never a re-ask", func(t *testing.T) {
		h := newS23(t)
		st := dbSt(Inputs{Yes: true})
		st.DB.Set(localTarget("Wrong-pw-1234", DBModeExisting), SourceEnvFile)
		err := step().Configure(ctx, h.dbRP(fixedDB(authErr, nil)), autoPrompter{}, st)
		if err == nil || !strings.Contains(err.Error(), "authentication failed") || strings.Contains(err.Error(), "Wrong-pw-1234") {
			t.Errorf("%v", err)
		}
	})
	t.Run("a DB that works needs nothing", func(t *testing.T) {
		h := newS23(t)
		st := dbSt(Inputs{Yes: true})
		st.DB.Set(localTarget("Right-pw-1234", DBModeExisting), SourceEnvFile)
		if err := step().Configure(ctx, h.dbRP(fixedDB(healthy, nil)), autoPrompter{}, st); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("nodb takes the create path with the DSN's password", func(t *testing.T) {
		h := newS23(t)
		st := dbSt(Inputs{Yes: true})
		st.DB.Set(localTarget("Typed-pw-1234", DBModeExisting), SourceEnvFile)
		if err := step().Configure(ctx, h.dbRP(fixedDB(noDBErr, nil)), autoPrompter{}, st); err != nil {
			t.Fatal(err)
		}
		det := (DatabaseStep{}).Detect(ctx, h.dbRP(fixedDB(noDBErr, nil)), st)
		if det.State != StateAbsent || !strings.Contains(det.Detail, "install writes bootstrap.sql") {
			t.Errorf("%+v", det)
		}
	})
}

func TestDatabaseConfigureInteractive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	local := func() *RunState {
		st := dbSt(Inputs{})
		st.Topology.Set(TopologyLocal, SourceFlag)
		return st
	}
	remote := func() *RunState {
		st := dbSt(Inputs{})
		st.Topology.Set(TopologyRemote, SourceFlag)
		return st
	}

	t.Run("remote: the AC-20 prompts, then an auth re-ask that succeeds", func(t *testing.T) {
		h := newS23(t)
		st := remote()
		red := NewRedactor()
		db := &scriptDB{fn: func(d DBTarget) (DBStatus, error) {
			if string(d.password) == "Right-pw-1234" {
				return healthy, nil
			}
			return authErr, errors.New("auth")
		}}
		ui := newPW(t, true).
			text("Database host", "db.tail.ts.net").text("Database port", "").text("Database name", "mem").text("Database user", "memuser").
			sel("TLS mode", 1).secret("Database password", "Wrong-pw-0000").
			secret("authentication failed for role memuser", "Right-pw-1234")
		if err := (DatabaseStep{Redactor: red}).Configure(ctx, h.dbRP(db), ui, st); err != nil {
			t.Fatal(err)
		}
		d := st.DB.Get()
		if d.Host != "db.tail.ts.net" || d.Port != "5432" || d.Name != "mem" || d.User != "memuser" || d.SSLMode != "require" ||
			string(d.password) != "Right-pw-1234" || d.Mode != DBModeExisting || st.DB.Source() != SourcePrompt {
			t.Errorf("%#v %v", d, st.DB)
		}
		if red.Redact("Wrong-pw-0000 Right-pw-1234") != "*** ***" {
			t.Error("typed passwords not registered")
		}
		if len(db.dsns) != 2 {
			t.Errorf("probes: %d", len(db.dsns))
		}
		for _, c := range ui.calls {
			if strings.Contains(c, "Right-pw-1234") || strings.Contains(c, "Wrong-pw-0000") {
				t.Errorf("a question carries a password: %s", c)
			}
		}
	})
	t.Run("three auth failures end in ErrTooManyAttempts (the engine skips the step)", func(t *testing.T) {
		h := newS23(t)
		st := remote()
		ui := newPW(t, true).
			text("Database host", "db.example").text("Database port", "").text("Database name", "").text("Database user", "").
			sel("TLS mode", 0).secret("Database password", "bad-pw-0001").
			secret("authentication failed", "bad-pw-0002").secret("authentication failed", "bad-pw-0003")
		err := (DatabaseStep{}).Configure(ctx, h.dbRP(fixedDB(authErr, nil)), ui, st)
		if !errors.Is(err, ErrTooManyAttempts) || st.DB.IsSet() {
			t.Errorf("%v set=%v", err, st.DB.IsSet())
		}
	})
	t.Run("an unreachable host is reported, not re-asked", func(t *testing.T) {
		h := newS23(t)
		st := remote()
		ui := newPW(t, true).
			text("Database host", "db.example").text("Database port", "").text("Database name", "").text("Database user", "").
			sel("TLS mode", 0).secret("Database password", "pw-123456")
		err := (DatabaseStep{}).Configure(ctx, h.dbRP(fixedDB(DBStatus{ErrorClass: DBErrUnreachable}, nil)), ui, st)
		if err == nil || !strings.Contains(err.Error(), "tailscale status") || errors.Is(err, ErrTooManyAttempts) {
			t.Errorf("%v", err)
		}
	})
	t.Run("local create path", func(t *testing.T) {
		h := newS23(t)
		st := local()
		s := DatabaseStep{Rand: &counterRand{}}
		ui := newPW(t, true).sel("create the role and database", 1).text("Database name to create", "memdb").text("Role name to create", "")
		if err := s.Configure(ctx, h.dbRP(fixedDB(authErr, nil)), ui, st); err != nil {
			t.Fatal(err)
		}
		d := st.DB.Get()
		if d.Name != "memdb" || d.User != "claude_memory" || d.Mode != DBModeCreate || d.SSLMode != "disable" || st.DB.Source() != SourcePrompt || len(string(d.password)) != 43 {
			t.Errorf("%#v %v", d, st.DB)
		}
	})
	t.Run("local existing is probed like remote", func(t *testing.T) {
		h := newS23(t)
		st := local()
		ui := newPW(t, true).sel("create the role and database", 0).
			text("Database host", "").text("Database port", "").text("Database name", "").text("Database user", "").
			sel("TLS mode", 2).secret("Database password", "Local-pw-123")
		if err := (DatabaseStep{}).Configure(ctx, h.dbRP(fixedDB(healthy, nil)), ui, st); err != nil {
			t.Fatal(err)
		}
		if d := st.DB.Get(); d.Host != "localhost" || d.SSLMode != "disable" || d.Mode != DBModeExisting {
			t.Errorf("%#v", d)
		}
	})
	t.Run("a typed name outside the strict rule is rejected by the validator", func(t *testing.T) {
		h := newS23(t)
		st := local()
		var rejected error
		ui := &validatingPrompter{pwPrompter: newPW(t, true).sel("create the role and database", 1), reject: &rejected}
		_ = (DatabaseStep{Rand: &counterRand{}}).Configure(ctx, h.dbRP(fixedDB(authErr, nil)), ui, st)
		if rejected == nil || !strings.Contains(rejected.Error(), "cannot be created by install") {
			t.Errorf("validator accepted a bad name: %v", rejected)
		}
	})
	t.Run("reconfigure to another database warns before the value is set (AC-26)", func(t *testing.T) {
		h := newS23(t)
		st := remote()
		st.Inputs.Reconfigure = true
		st.Prior.EnvDoc = []byte("MEMORY_PG_DSN=postgresql://u:oldpassword@old.example:5432/old\n")
		st.DB.Set(mustTarget(t, "postgresql://u@old.example:5432/old", "oldpassword", SourceEnvFile), SourceEnvFile)
		ui := newPW(t, true).
			text("Database host", "new.example").text("Database port", "").text("Database name", "newdb").text("Database user", "u").
			sel("TLS mode", 0).secret("Database password", "New-pw-12345").
			conf("records already stored stay in the old database", true)
		if err := (DatabaseStep{}).Configure(ctx, h.dbRP(fixedDB(healthy, nil)), ui, st); err != nil {
			t.Fatal(err)
		}
		last := ui.calls[len(ui.calls)-1]
		if !strings.Contains(last, "pg_dump") || !strings.Contains(last, "old.example") || !strings.Contains(last, "new.example") || strings.Contains(last, "oldpassword") || strings.Contains(last, "New-pw-12345") {
			t.Errorf("warning: %s", last)
		}
		if st.DB.Get().Host != "new.example" {
			t.Errorf("not set: %#v", st.DB.Get())
		}
	})
	t.Run("a declined warning keeps the old target", func(t *testing.T) {
		h := newS23(t)
		st := remote()
		st.Inputs.Reconfigure = true
		st.Prior.EnvDoc = []byte("MEMORY_PG_DSN=postgresql://u:oldpassword@old.example:5432/old\n")
		old := mustTarget(t, "postgresql://u@old.example:5432/old", "oldpassword", SourceEnvFile)
		st.DB.Set(old, SourceEnvFile)
		ui := newPW(t, true).
			text("Database host", "new.example").text("Database port", "").text("Database name", "newdb").text("Database user", "u").
			sel("TLS mode", 0).secret("Database password", "New-pw-12345").conf("stay in the old database", false)
		if err := (DatabaseStep{}).Configure(ctx, h.dbRP(fixedDB(healthy, nil)), ui, st); err != nil || st.DB.Get().Host != "old.example" {
			t.Errorf("%v %#v", err, st.DB.Get())
		}
	})
	t.Run("a working DB with no --reconfigure asks nothing", func(t *testing.T) {
		h := newS23(t)
		st := remote()
		st.DB.Set(localTarget("Right-pw-1234", DBModeExisting), SourceEnvFile)
		if err := (DatabaseStep{}).Configure(ctx, h.dbRP(fixedDB(healthy, nil)), newPW(t, true), st); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a seeded DB with the wrong password re-asks only the password", func(t *testing.T) {
		h := newS23(t)
		st := remote()
		st.DB.Set(localTarget("Wrong-pw-1234", DBModeExisting), SourceEnvFile)
		db := &scriptDB{fn: func(d DBTarget) (DBStatus, error) {
			if string(d.password) == "Right-pw-1234" {
				return healthy, nil
			}
			return authErr, nil
		}}
		ui := newPW(t, true).secret("authentication failed", "Right-pw-1234")
		if err := (DatabaseStep{}).Configure(ctx, h.dbRP(db), ui, st); err != nil || string(st.DB.Get().password) != "Right-pw-1234" {
			t.Errorf("%v", err)
		}
	})
}

// validatingPrompter runs the validator the step passes to Text against the
// default and records the first rejection, instead of failing the test.
type validatingPrompter struct {
	*pwPrompter
	reject *error
	n      int
}

func (v *validatingPrompter) Text(q, def string, validate func(string) error) (string, error) {
	v.n++
	bad := "Bad-Name"
	if validate != nil {
		if err := validate(bad); err != nil {
			*v.reject = err
		}
	}
	return "", ErrInterrupted
}

// ---- engine: the plan's end-to-end rows -------------------------------------

type dbRig struct {
	h      *eh
	db     *scriptDB
	runner *FakeRunner
	env    Env
	plat   PlatformInfo
	red    *Redactor
}

func newDBRig(t *testing.T, interactive bool) *dbRig {
	h := newEH(t, interactive)
	return &dbRig{h: h, db: fixedDB(authErr, nil), runner: NewFakeRunner(t), env: Env{}, plat: PlatformInfo{OS: OSLinux, Arch: "amd64", JobsBackend: JobsNone}, red: NewRedactor()}
}

func (r *dbRig) steps() []Step {
	return []Step{TopologyStep{}, EnvFileStep{Version: "v1.2.0"}, DatabaseStep{Version: "v1.2.0", Rand: &counterRand{}, Redactor: r.red}}
}

func (r *dbRig) run(ui Prompter, in Inputs, steps ...Step) RunResult {
	if steps == nil {
		steps = r.steps()
	}
	rp := ReadPorts{FS: r.h.fs, Runner: r.runner, DB: r.db, Clock: r.h.clk, Paths: r.h.p, Env: r.env, Platform: r.plat}
	e := &Engine{Steps: steps, Read: rp, Write: WritePorts{ReadPorts: rp, FS: r.h.fs, Runner: r.runner, DB: r.db},
		UI: ui, Reporter: r.h.rep, Version: "v1.2.0"}
	return e.Run(context.Background(), in)
}

var bootstrapPWRe = regexp.MustCompile(`\\set app_pw '([^']+)'`)

func (r *dbRig) bootstrapPassword() string {
	b, err := os.ReadFile(r.h.p.Bootstrap())
	if err != nil {
		r.h.t.Fatalf("bootstrap.sql: %v", err)
	}
	m := bootstrapPWRe.FindSubmatch(b)
	if m == nil {
		r.h.t.Fatalf("no app_pw in bootstrap.sql:\n%s", b)
	}
	return string(m[1])
}

// assertNoSecretOutput checks everything the engine reported (notes, details,
// actions, remedies, awaits, errors), the manifest and every command argv for
// the password and its percent-encoded form (AC-30). Diffs are excluded: the
// renderer redacts them, and the env-file diff legitimately holds the DSN.
func (r *dbRig) assertNoSecretOutput(res RunResult, pws ...string) {
	r.h.t.Helper()
	var out []string
	for _, n := range r.h.rep.Notes_ {
		out = append(out, n.Text)
	}
	for _, tb := range r.h.rep.Tables {
		for _, row := range tb {
			out = append(out, row.Detail)
			for _, a := range row.Artifacts {
				out = append(out, a.Detail)
			}
		}
	}
	for _, cp := range r.h.rep.Plans {
		for _, n := range cp.Notes {
			out = append(out, n.Text)
		}
		for _, sp := range cp.Steps {
			for _, a := range sp.Plan.Actions {
				out = append(out, a.Desc, a.Path)
			}
			for _, n := range sp.Plan.Notes {
				out = append(out, n.Text)
			}
		}
	}
	for _, ins := range r.h.rep.Awaits {
		out = append(out, ins...)
	}
	for _, o := range res.Outcomes {
		out = append(out, o.Detail, o.Remedy)
		if o.Err != nil {
			out = append(out, o.Err.Error())
		}
		for _, n := range o.Notes {
			out = append(out, n.Text)
		}
	}
	if res.Err != nil {
		out = append(out, res.Err.Error())
	}
	if b, err := os.ReadFile(r.h.p.Manifest()); err == nil {
		out = append(out, string(b))
	}
	for _, c := range r.runner.Calls() {
		out = append(out, strings.Join(c.Argv, " "))
	}
	all := strings.Join(out, "\n")
	for _, pw := range pws {
		for _, form := range []string{pw, pctEncode(pw)} {
			if pw != "" && strings.Contains(all, form) {
				r.h.t.Errorf("output contains the password %q", form)
			}
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// v0.4 row: two consecutive `--yes --topology local` runs with no DSN and a DB
// answering auth both end blocked: awaiting user action (exit 1, same
// password in the env file and bootstrap.sql); once the DB answers ok, the
// third run removes bootstrap.sql and exits 0.
func TestDatabaseYesLocalTwiceBlockedThenDone(t *testing.T) {
	t.Parallel()
	r := newDBRig(t, false)
	in := Inputs{Yes: true, Topology: "local"}

	res1 := r.run(r.h.ui, in)
	wantExit(t, res1, ExitFailed)
	o := outcome(t, res1, "database")
	if o.Outcome != OutcomeBlocked || !strings.Contains(o.Detail, "blocked: awaiting user action") || !o.Hard {
		t.Fatalf("run 1 database outcome: %+v", o)
	}
	if !strings.Contains(o.Remedy, "sudo -u postgres psql -v ON_ERROR_STOP=1 -d postgres -f - < "+r.h.p.Bootstrap()) {
		t.Errorf("remedy:\n%s", o.Remedy)
	}
	pw := r.bootstrapPassword()
	if len(pw) != 43 {
		t.Fatalf("password %q", pw)
	}
	envBytes := readFile(t, r.h.p.EnvFile())
	if !strings.Contains(envBytes, "MEMORY_PG_DSN=postgresql://claude_memory:"+pctEncode(pw)+"@localhost:5432/claude_memory?sslmode=disable") {
		t.Errorf("env file:\n%s", envBytes)
	}
	if fi, _ := os.Stat(r.h.p.Bootstrap()); fi.Mode().Perm() != 0o600 {
		t.Errorf("bootstrap.sql mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(r.h.p.StateDir); fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode %v", fi.Mode().Perm())
	}
	if m := r.h.manifest(); m.Topology != "local" {
		t.Errorf("manifest topology %q", m.Topology)
	}
	bootBytes := readFile(t, r.h.p.Bootstrap())

	// Run 2: still auth, the file is there: pending, nothing regenerated.
	writes := len(r.h.nonLockWrites())
	res2 := r.run(NewFakePrompter(t, false), in)
	wantExit(t, res2, ExitFailed)
	o = outcome(t, res2, "database")
	if o.Outcome != OutcomeBlocked || !strings.Contains(o.Detail, "blocked: awaiting user action") {
		t.Fatalf("run 2 database outcome: %+v", o)
	}
	if got := len(r.h.nonLockWrites()); got != writes {
		t.Errorf("run 2 wrote: %v", r.h.nonLockWrites()[writes:])
	}
	if readFile(t, r.h.p.EnvFile()) != envBytes || readFile(t, r.h.p.Bootstrap()) != bootBytes || r.bootstrapPassword() != pw {
		t.Error("the password or a file changed between the runs")
	}

	// Run 3: the user ran the command.
	r.db.set(healthy, nil)
	res3 := r.run(NewFakePrompter(t, false), in)
	wantExit(t, res3, ExitOK)
	if _, err := os.Stat(r.h.p.Bootstrap()); err == nil {
		t.Error("run 3 did not remove bootstrap.sql")
	}
	if readFile(t, r.h.p.EnvFile()) != envBytes {
		t.Error("run 3 rewrote the env file")
	}
	// A fourth run is a no-op.
	writes = len(r.h.nonLockWrites())
	wantExit(t, r.run(NewFakePrompter(t, false), in), ExitOK)
	if got := len(r.h.nonLockWrites()); got != writes {
		t.Errorf("run 4 wrote: %v", r.h.nonLockWrites()[writes:])
	}
	if n := len(r.runner.Calls()); n != 0 {
		t.Errorf("the install ran commands: %v", r.runner.Calls())
	}
	for _, e := range r.h.manifest().Artifacts {
		if strings.Contains(e.Path, "bootstrap") || e.Identity == "bootstrap-file" {
			t.Errorf("manifest records the bootstrap file: %+v", e)
		}
	}
	r.assertNoSecretOutput(res1, pw)
	r.assertNoSecretOutput(res3, pw)
}

// v0.5 (N1): interactive create path; the DB turns ok before the re-check; the
// re-check passes, bootstrap.sql is deleted in the same run, the step is ok and
// the manifest has no bootstrap-file entry.
func TestDatabaseInteractiveCreateAwaitDeletesFile(t *testing.T) {
	t.Parallel()
	r := newDBRig(t, true)
	ui := newPW(t, true)
	ui.sel("Topology", 0).sel("Env file", 0).sel("Database", 0).
		sel("Where does Postgres run?", 0).
		sel("create the role and database", 1).text("Database name to create", "").text("Role name to create", "").
		conf("Apply this plan?", true).
		sel("waiting for you", 0)
	ui.onSelect = func(q string) {
		if strings.Contains(q, "waiting for you") {
			if _, err := os.Stat(r.h.p.Bootstrap()); err != nil {
				t.Errorf("bootstrap.sql missing when the user is asked to run it: %v", err)
			}
			r.db.set(healthy, nil) // the user ran the command
		}
	}
	res := r.run(ui, Inputs{})
	wantExit(t, res, ExitOK)
	if o := outcome(t, res, "database"); o.Outcome != OutcomeApplied {
		t.Errorf("%+v", o)
	}
	if _, err := os.Stat(r.h.p.Bootstrap()); err == nil {
		t.Error("bootstrap.sql was not deleted in the same run")
	}
	var pwUsed string
	if b := readFile(t, r.h.p.EnvFile()); strings.Contains(b, "MEMORY_PG_DSN=postgresql://claude_memory:") {
		pwUsed = strings.TrimPrefix(strings.SplitN(strings.SplitN(b, "MEMORY_PG_DSN=postgresql://claude_memory:", 2)[1], "@", 2)[0], "")
	}
	if pwUsed == "" {
		t.Errorf("env file has no DSN: %s", readFile(t, r.h.p.EnvFile()))
	}
	for _, e := range r.h.manifest().Artifacts {
		if e.Step == "database" && e.Kind != KindDir {
			t.Errorf("unexpected manifest entry %+v", e)
		}
		if strings.Contains(e.Path, "bootstrap.sql") || e.Identity == "bootstrap-file" {
			t.Errorf("manifest records the bootstrap file: %+v", e)
		}
	}
	if _, ok := r.h.rep.Awaits["database"]; !ok {
		t.Error("the instructions were not reported")
	}
	r.assertNoSecretOutput(res, pwUsed)
	// Next run: no file, DB ok: bootstrap-file is ok, nothing to do.
	writes := len(r.h.nonLockWrites())
	wantExit(t, r.run(NewFakePrompter(t, false), Inputs{Yes: true}), ExitOK)
	if got := len(r.h.nonLockWrites()); got != writes {
		t.Errorf("no-op run wrote: %v", r.h.nonLockWrites()[writes:])
	}
}

// v0.5 (N10): the env file's DSN beats Env's; a no-op --yes run with the DB ok
// writes nothing and notes the drift once. With no env-file DSN, Env's value
// is adopted and written.
func TestDatabaseSeedOrderThroughTheEngine(t *testing.T) {
	t.Parallel()
	const pwX, pwY = "Env-file-pw-1", "Shell-pw-22222"
	t.Run("env file DSN X, Env DSN Y, --yes, DB ok", func(t *testing.T) {
		r := newDBRig(t, false)
		r.db.set(healthy, nil)
		r.env["MEMORY_PG_DSN"] = "postgresql://shell:" + pwY + "@shellhost:5432/shelldb"
		if err := os.MkdirAll(r.h.p.ConfigDir, 0o700); err != nil {
			t.Fatal(err)
		}
		x := "postgresql://claude_memory:" + pwX + "@localhost:5432/claude_memory?sslmode=disable"
		if err := os.WriteFile(r.h.p.EnvFile(), []byte("MEMORY_PG_DSN="+x+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		res := r.run(r.h.ui, Inputs{Yes: true, Topology: "local"}, EnvFileStep{}, DatabaseStep{Redactor: r.red})
		wantExit(t, res, ExitOK)
		for _, w := range r.h.nonLockWrites() {
			if !strings.Contains(w, "install.json") && !strings.Contains(w, "install.lock") {
				t.Errorf("unexpected write %s", w)
			}
		}
		if got := readFile(t, r.h.p.EnvFile()); got != "MEMORY_PG_DSN="+x+"\n" {
			t.Errorf("env file changed:\n%s", got)
		}
		n := 0
		for _, note := range r.h.rep.Notes_ {
			if strings.Contains(note.Text, "differs from the env file") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("drift notes: %d, want 1 (%v)", n, r.h.rep.Notes_)
		}
		r.assertNoSecretOutput(res, pwX, pwY)
	})
	t.Run("no env file DSN, Env DSN Y: the key is written with Y", func(t *testing.T) {
		r := newDBRig(t, false)
		r.db.set(healthy, nil)
		y := "postgresql://shell:" + pwY + "@shellhost.example:5432/shelldb?sslmode=require"
		r.env["MEMORY_PG_DSN"] = y
		res := r.run(r.h.ui, Inputs{Yes: true, Topology: "remote"}, EnvFileStep{}, DatabaseStep{Redactor: r.red})
		wantExit(t, res, ExitOK)
		if got := readFile(t, r.h.p.EnvFile()); !strings.Contains(got, "MEMORY_PG_DSN="+y+"\n") {
			t.Errorf("env file:\n%s", got)
		}
		writes := len(r.h.nonLockWrites())
		wantExit(t, r.run(r.h.ui, Inputs{Yes: true, Topology: "remote"}, EnvFileStep{}, DatabaseStep{}), ExitOK)
		if len(r.h.nonLockWrites()) != writes {
			t.Error("the second run wrote")
		}
		r.assertNoSecretOutput(res, pwY)
	})
}

// A --pg-dsn that points to another database carries the AC-26 warning in the
// combined plan under --yes.
func TestDatabaseDSNChangeWarnsInThePlan(t *testing.T) {
	t.Parallel()
	r := newDBRig(t, false)
	r.db.set(healthy, nil)
	if err := os.MkdirAll(r.h.p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.h.p.EnvFile(), []byte("MEMORY_PG_DSN=postgresql://u:Old-pw-12345@old.example:5432/old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := Inputs{Yes: true, Topology: "remote", PGDSN: "postgresql://u@new.example:5432/newdb", PGPassword: "New-pw-12345"}
	res := r.run(r.h.ui, in, EnvFileStep{}, DatabaseStep{Redactor: r.red})
	wantExit(t, res, ExitOK)
	var warned bool
	for _, cp := range r.h.rep.Plans {
		for _, n := range cp.Notes {
			warned = warned || (strings.Contains(n.Text, "stay in the old database") && strings.Contains(n.Text, "old.example") && strings.Contains(n.Text, "new.example"))
		}
	}
	if !warned {
		t.Errorf("no AC-26 warning in the plan: %+v", r.h.rep.Plans)
	}
	if got := readFile(t, r.h.p.EnvFile()); !strings.Contains(got, "new.example") {
		t.Errorf("env file not updated:\n%s", got)
	}
	r.assertNoSecretOutput(res, "Old-pw-12345", "New-pw-12345")
}

// A failing database blocks its dependents but keeps the independent steps
// running (AC-7); the unsafe-password refusal is a blocked step, exit 1.
func TestDatabaseUnsafePasswordIsBlockedUnderYes(t *testing.T) {
	t.Parallel()
	r := newDBRig(t, false)
	r.db.set(noDBErr, nil)
	if err := os.MkdirAll(r.h.p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const typed = "t'yped pw $1"
	dsn := "postgresql://claude_memory:" + pctEncode(typed) + "@localhost:5432/claude_memory?sslmode=disable"
	if err := os.WriteFile(r.h.p.EnvFile(), []byte("MEMORY_PG_DSN="+dsn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := r.run(r.h.ui, Inputs{Yes: true, Topology: "local"})
	wantExit(t, res, ExitFailed)
	o := outcome(t, res, "database")
	if o.Outcome != OutcomeBlocked || !strings.Contains(o.Detail, "cannot be written into bootstrap.sql safely") {
		t.Errorf("%+v", o)
	}
	if _, err := os.Stat(r.h.p.Bootstrap()); err == nil {
		t.Error("bootstrap.sql was written for an unsafe password")
	}
	r.assertNoSecretOutput(res, typed)
}

func TestDatabaseStepRegistration(t *testing.T) {
	t.Parallel()
	s := DatabaseStep{}
	if s.ID() != "database" || len(s.Requires()) != 1 || s.Requires()[0] != "envfile" {
		t.Errorf("%s %v", s.ID(), s.Requires())
	}
	if !bytes.Contains([]byte(filepath.Base(newS23(t).p.Bootstrap())), []byte("bootstrap.sql")) {
		t.Error("path")
	}
}
