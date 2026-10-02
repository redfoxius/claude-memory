package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"regexp"
	"strings"

	"claude-memory/deploy"
	"claude-memory/internal/config"
)

// DatabaseStepID is the id of the database step (AC-7: after envfile).
const DatabaseStepID = "database"

// Artifact ids of the database step (Design 18): the database itself, and
// the transient bootstrap-file artifact that deletes a leftover bootstrap.sql
// (v0.5, N1). bootstrap-file is never recorded in the manifest.
const (
	DatabaseArtifact      = "database/database"
	BootstrapFileArtifact = "database/bootstrap-file"
)

// DatabaseStep owns RunState.DB (Design 16, 22): the Seed from --pg-dsn, the
// env file and Env, the AC-20 prompts with the auth re-ask, the AC-21
// existing/create choice, and bootstrap.sql. It never runs psql or sudo: on
// the create path it writes bootstrap.sql (0600 in <StateDir>), returns an
// Await carrying the command for the user to run, and deletes the file once
// the database is ok (through the bootstrap-file artifact).
//
// Secrets: the password lives in DBTarget (unexported), the env file and
// bootstrap.sql only. It is never put in a Note, Detail, error, Action or
// Diff; a bootstrap.sql write has no Diff.
type DatabaseStep struct {
	// Version is the running binary's version, recorded on its artifacts.
	Version string
	// Rand is the entropy source of generated passwords (AC-22); nil means
	// crypto/rand. A port so tests are deterministic.
	Rand io.Reader
	// Redactor learns every password this step sees or generates (AC-30). The
	// composition root passes the run's Redactor; without one the step fails
	// closed and withholds error text instead of printing it unredacted.
	Redactor *Redactor
	// AppRolePSQL is the body of bootstrap.sql; nil means the embedded
	// deploy/initdb/app-role.psql.
	AppRolePSQL []byte
}

var (
	_ Step            = DatabaseStep{}
	_ Seeder          = DatabaseStep{}
	_ Configurer      = DatabaseStep{}
	_ MissingInputter = DatabaseStep{}
)

// ID implements Step.
func (DatabaseStep) ID() string { return DatabaseStepID }

// Title implements Step.
func (DatabaseStep) Title() string { return "Database" }

// Requires implements Step: the env file holding the password is written
// first (AC-7, AC-21).
func (DatabaseStep) Requires() []string { return []string{EnvFileStepID} }

func (s DatabaseStep) register(pw string) {
	if s.Redactor != nil && pw != "" {
		s.Redactor.Register(pw)
	}
}

func (s DatabaseStep) redact(v string) string {
	return redactOrWithhold(s.Redactor, v)
}

func (s DatabaseStep) body() ([]byte, error) {
	if s.AppRolePSQL != nil {
		return s.AppRolePSQL, nil
	}
	b, err := fs.ReadFile(deploy.FS, deploy.InitDBAppRolePSQL)
	if err != nil {
		return nil, fmt.Errorf("embedded %s: %w", deploy.InitDBAppRolePSQL, err)
	}
	return b, nil
}

// sameDSN compares two DSN strings by connection (host, port, database,
// user, password, effective sslmode); unparseable values fall back to string
// equality.
func sameDSN(a, b string) bool {
	ta, _, oka := parseEnvDSN(a, false)
	tb, _, okb := parseEnvDSN(b, false)
	if oka && okb {
		return ta.SameConnection(tb)
	}
	return a == b
}

// Seed sets DB from --pg-dsn (+ the stdin password), else the env file's DSN,
// else Env's MEMORY_PG_DSN (Design 16, N10). An invalid flag is an error (exit
// 2); an invalid env-file/Env value leaves DB unset with a note (Design 24),
// and the step then needs input.
func (s DatabaseStep) Seed(_ context.Context, rc ReadPorts, st *RunState) ([]Note, error) {
	in := st.Inputs
	t, ok, err := DBTargetFromFlags(in)
	if err != nil {
		return nil, err
	}
	if ok {
		t.Mode = DBModeExisting
		s.register(string(t.password))
		st.DB.Set(t, SourceFlag)
		return nil, nil
	}
	seed := SeedEnvValue(st, rc.Env, EnvKeyDSN, "", sameDSN)
	if !seed.Found {
		return seed.Notes, nil
	}
	// An unparseable line (AC-27) is recovered (AC-28), not skipped, and the
	// shell's value never stands in for it.
	t, _, ok = parseEnvDSN(seed.Value, seed.Unparseable)
	if !ok {
		where := "the env file"
		if seed.Source == SourceEnv {
			where = "your shell"
		}
		return append(seed.Notes, Note{NoteWarn, fmt.Sprintf(
			"%s in %s is not a usable postgresql:// URL; install needs the database settings (pass --pg-dsn, or answer the prompts)", EnvKeyDSN, where)}), nil
	}
	if in.PGPassword != "" {
		t = t.WithPassword(in.PGPassword)
	}
	t.Mode, t.Source = DBModeExisting, seed.Source
	s.register(string(t.password))
	st.DB.Set(t, seed.Source)
	return seed.Notes, nil
}

// MissingInput implements MissingInputter.
func (DatabaseStep) MissingInput(st *RunState) bool { return !st.DB.IsSet() }

// ---- assessment ------------------------------------------------------------

type dbKind int

const (
	dbOK            dbKind = iota
	dbNoInput              // no DB set yet
	dbNeedBootstrap        // the role/database/vector must be created by bootstrap.sql
	dbPending              // bootstrap.sql exists and the database is not ready yet
	dbFail                 // cannot connect and bootstrap.sql cannot fix it
	dbUnsafe               // bootstrap.sql would be needed but its values cannot be rendered safely
	dbBlocked              // cannot assess
)

// dbAssessment is what Detect, Plan and Apply all derive from one probe, so
// they cannot disagree (like the env file's analysis).
type dbAssessment struct {
	kind   dbKind
	detail string // never holds a secret
	remedy string
	class  DBErrorClass // set for dbFail
	file   bool         // bootstrap.sql exists
}

func hostPort(t DBTarget) string {
	return net.JoinHostPort(t.Host, firstNonEmpty(t.Port, DefaultPGPort))
}

// classMessage is the AC-20 classifier text for a failed connection. cause is
// already redacted.
func classMessage(rc ReadPorts, class DBErrorClass, t DBTarget, cause string) string {
	switch class {
	case DBErrAuth:
		return fmt.Sprintf("authentication failed for role %s at %s (Postgres shows this also when the role does not exist)", t.User, hostPort(t))
	case DBErrNoDB:
		return fmt.Sprintf("database %s does not exist at %s", t.Name, hostPort(t))
	case DBErrHBA:
		return fmt.Sprintf("the server at %s rejected this client (pg_hba.conf has no matching entry; see DEPLOY.md)", hostPort(t))
	case DBErrUnreachable:
		m := fmt.Sprintf("cannot reach %s (timeout or connection refused)", hostPort(t))
		if HostIsLocal(t.Host) {
			return m + "; is Postgres running here? " + Hint(rc.Platform, CompPostgres)
		}
		return m + "; check `tailscale status`, the server firewall and DEPLOY.md"
	case DBErrDSN:
		return "the database settings do not parse as a connection string"
	}
	if cause != "" {
		return "cannot connect to " + hostPort(t) + ": " + cause
	}
	return "cannot connect to " + hostPort(t)
}

const manualBootstrapRemedy = "run deploy/initdb/app-role.psql yourself as a Postgres superuser with " +
	"`psql -v app_user=… -v app_db=… -v app_pw=… -f app-role.psql`, then re-check"

func (s DatabaseStep) assess(ctx context.Context, rc ReadPorts, st *RunState) dbAssessment {
	var a dbAssessment
	path := rc.Paths.Bootstrap()
	if _, err := rc.FS.Stat(path); err == nil {
		a.file = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		a.kind, a.detail = dbBlocked, fmt.Sprintf("cannot inspect %s: %v", path, err)
		return a
	}
	if !st.DB.IsSet() {
		a.kind, a.detail = dbNoInput, "no database chosen yet"
		return a
	}
	t := st.DB.Get()
	dsn := t.DSN()
	switch {
	case dsn == "":
		a.kind, a.detail = dbBlocked, "the database settings are invalid (host, port, database name or sslmode)"
		return a
	case rc.DB == nil:
		a.kind, a.detail = dbBlocked, "no database prober is configured"
		return a
	}
	status, err := rc.DB.Probe(ctx, dsn)
	cause := ""
	if err != nil {
		cause = s.redact(err.Error())
	}
	who := t.String()
	switch {
	case status.Connected && status.VectorVersion != "":
		a.kind, a.detail = dbOK, fmt.Sprintf("connected to %s (pgvector %s)", who, status.VectorVersion)
		return a
	case status.Connected && status.Migrations == nil && err != nil:
		a.kind, a.detail = dbBlocked, "connected to "+who+", but the catalog could not be read: "+cause
		return a
	case status.Connected:
		return s.needBootstrap(rc, st, a, "the vector extension is not installed in database "+t.Name)
	case status.ErrorClass == DBErrNoDB:
		return s.needBootstrap(rc, st, a, "database "+t.Name+" does not exist")
	case status.ErrorClass == DBErrAuth && (a.file || t.Mode == DBModeCreate):
		return s.needBootstrap(rc, st, a, "role "+t.User+" does not exist yet, or has another password")
	}
	a.kind, a.class, a.detail = dbFail, status.ErrorClass, classMessage(rc, status.ErrorClass, t, cause)
	a.remedy = failRemedy(status.ErrorClass)
	return a
}

// failRemedy is the classifier's remedy for a failed connection that
// bootstrap.sql cannot fix (LOW-7: reported as blocked, not as a default
// apply that Plan would refuse).
func failRemedy(class DBErrorClass) string {
	return "fix the cause above, then re-run install" + failHint(class) + " (install --reconfigure asks for the database settings again)"
}

// bootstrapPasswordRe reads the password line of a rendered bootstrap.sql.
var bootstrapPasswordRe = regexp.MustCompile(`(?m)^\\set app_pw '([^']*)'\r?$`)

// bootstrapMatches reports whether the bootstrap.sql on disk was rendered for
// t (same role, database and password). A file for other values (a
// --reconfigure on the create path chose a new password) would create the
// role with the old one, so the step must re-render it, not wait for it.
func bootstrapMatches(rc ReadPorts, t DBTarget) bool {
	b, err := rc.FS.ReadFile(rc.Paths.Bootstrap())
	if err != nil {
		return false
	}
	text := string(b)
	get := func(name string) (string, bool) {
		m := regexp.MustCompile(`(?m)^\\set ` + name + ` '([^']*)'\r?$`).FindStringSubmatch(text)
		if m == nil {
			return "", false
		}
		return m[1], true
	}
	user, ok1 := get("app_user")
	db, ok2 := get("app_db")
	pw := bootstrapPasswordRe.FindStringSubmatch(text)
	return ok1 && ok2 && pw != nil && user == t.User && db == t.Name && pw[1] == string(t.password)
}

// needBootstrap classifies a database that bootstrap.sql would have to
// create: pending when the file is already there, impossible for a remote
// server, refused when its values cannot be rendered safely, else needed.
func (s DatabaseStep) needBootstrap(rc ReadPorts, st *RunState, a dbAssessment, reason string) dbAssessment {
	t := st.DB.Get()
	switch {
	case a.file && bootstrapMatches(rc, t):
		a.kind = dbPending
		a.detail = "awaiting user action: " + reason + "; run the command install printed, then re-check " +
			"(if it ran but this still fails, the role already existed with another password: enter that password " +
			"with install --reconfigure, or change it yourself with ALTER ROLE)"
		a.remedy = strings.Join(bootstrapInstructions(rc.Platform.OS, rc.Paths.Bootstrap()), "\n")
		return a
	case (st.Topology.IsSet() && st.Topology.Get() == TopologyRemote) || !HostIsLocal(t.Host):
		a.kind, a.class = dbFail, DBErrOther
		a.detail = reason + "; install can create it only on this machine, so create it on the server with " + manualBootstrapRemedy
		return a
	}
	for _, err := range []error{
		ValidateBootstrapName("role name", t.User),
		ValidateBootstrapName("database name", t.Name),
		ValidateBootstrapPassword(string(t.password)),
	} {
		if err != nil {
			a.kind, a.detail, a.remedy = dbUnsafe, err.Error(), manualBootstrapRemedy
			return a
		}
	}
	a.kind = dbNeedBootstrap
	a.detail = reason + "; install writes bootstrap.sql for you to run"
	return a
}

func bootstrapInstructions(goos, path string) []string {
	return []string{
		"install wrote " + path + " (mode 0600; it holds the database password). Run this command yourself; install never runs sudo or psql:",
		"  " + BootstrapCommand(goos, path),
		"It revokes the PUBLIC privileges on the database and on its public schema (CREATE), so only the app role has access.",
		"Then re-check. If the database still reports an authentication failure, the role already existed with another password: " +
			"enter that password (install --reconfigure), or change it yourself with ALTER ROLE.",
	}
}

// ---- Detect / Plan / Apply -------------------------------------------------

// priorEnvDB is the database the env file currently points at, for the AC-26
// warning ("the old database").
func priorEnvDB(st *RunState) (DBTarget, bool) {
	if st.Prior.EnvDoc == nil {
		return DBTarget{}, false
	}
	v, ok := config.ParseEnvDoc(st.Prior.EnvDoc).Get(EnvKeyDSN)
	if !ok || v == "" {
		return DBTarget{}, false
	}
	t, _, ok := parseEnvDSN(v, false)
	return t, ok
}

func sameDatabase(a, b DBTarget) bool {
	return strings.EqualFold(a.Host, b.Host) && a.Port == b.Port && a.Name == b.Name
}

// dbChangeNote is the AC-26 warning when the chosen database is not the one
// the env file points at: records stay in the old database.
func dbChangeNote(st *RunState) (Note, bool) {
	if !st.DB.IsSet() {
		return Note{}, false
	}
	prior, ok := priorEnvDB(st)
	if !ok || sameDatabase(prior, st.DB.Get()) {
		return Note{}, false
	}
	return warnDataStays("database", prior.String(), st.DB.Get().String()), true
}

// Detect implements Step.
func (s DatabaseStep) Detect(ctx context.Context, rc ReadPorts, st *RunState) Detection {
	a := s.assess(ctx, rc, st)
	db := ArtifactState{ID: DatabaseArtifact, Detail: a.detail}
	file := ArtifactState{ID: BootstrapFileArtifact, State: StateOK, Detail: "no " + BootstrapFileName}
	detail := a.detail
	switch a.kind {
	case dbOK:
		db.State = StateOK
		if a.file {
			file.State, file.Detail = StateOutdated, "remove "+rc.Paths.Bootstrap()+" (it holds a password)"
			detail += "; " + BootstrapFileName + " can be removed"
		}
	case dbNoInput, dbNeedBootstrap:
		db.State = StateAbsent
	default: // pending, fail, unsafe, blocked
		db.State = StateBlocked
	}
	if a.file && file.State == StateOK {
		file.Detail = BootstrapFileName + " is still needed"
	}
	d := Detection{State: worstState(db.State, file.State), Detail: detail, Remedy: a.remedy, Artifacts: []ArtifactState{db, file}}
	if n, ok := dbChangeNote(st); ok {
		d.Notes = append(d.Notes, n)
	}
	return d
}

// Plan implements Step.
func (s DatabaseStep) Plan(ctx context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	var p Plan
	if ch[DatabaseArtifact] == ChoiceApply {
		a := s.assess(ctx, rc, st)
		if a.kind != dbNeedBootstrap {
			return p, errors.New("nothing install can create: " + a.detail)
		}
		p.Actions = append(p.Actions, Action{Artifact: DatabaseArtifact, Verb: "write", Path: rc.Paths.Bootstrap(),
			Desc: "write " + BootstrapFileName + " (mode 0600, holds the database password) and print the command that runs it; install never runs psql or sudo"})
	}
	if ch[BootstrapFileArtifact] == ChoiceApply {
		p.Actions = append(p.Actions, Action{Artifact: BootstrapFileArtifact, Verb: "remove", Path: rc.Paths.Bootstrap(),
			Desc: "remove " + BootstrapFileName + " (the database works)"})
	}
	return p, nil
}

// Apply implements Step. Create path: write bootstrap.sql and ask the user to
// run it (Await); once the database is ok the engine applies bootstrap-file,
// which removes the file.
func (s DatabaseStep) Apply(ctx context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	path := wc.Paths.Bootstrap()
	for _, act := range p.Actions {
		switch act.Artifact {
		case DatabaseArtifact:
			if a := s.assess(ctx, wc.ReadPorts, st); a.kind != dbNeedBootstrap {
				return res, errors.New("nothing install can create: " + a.detail)
			}
			body, err := s.body()
			if err != nil {
				return res, err
			}
			t := st.DB.Get()
			out, err := RenderBootstrap(body, t.User, t.Name, string(t.password))
			if err != nil {
				return res, err
			}
			dir := wc.Paths.StateDir
			_, serr := wc.FS.Stat(dir)
			created := errors.Is(serr, fs.ErrNotExist)
			if err := wc.FS.MkdirAll(dir, 0o700); err != nil {
				return res, fmt.Errorf("create %s: %w", dir, err)
			}
			if err := wc.FS.WriteFileAtomic(path, out, 0o600); err != nil {
				return res, fmt.Errorf("write %s: %w", path, err)
			}
			if created {
				res.Artifacts = append(res.Artifacts, Artifact{Step: DatabaseStepID, Kind: KindDir, Path: dir, Version: s.Version})
			} else if n, changed, err := tightenDir(wc.FS, dir); err != nil {
				return res, err
			} else if changed {
				res.Notes = append(res.Notes, n)
			}
			res.Await = &Await{Instructions: bootstrapInstructions(wc.Platform.OS, path), Artifacts: []string{DatabaseArtifact}}
		case BootstrapFileArtifact:
			if err := wc.FS.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return res, fmt.Errorf("remove %s: %w", path, err)
			}
			res.Notes = append(res.Notes, Note{NoteInfo, "removed " + path})
		}
	}
	return res, nil
}

// ---- Configure -------------------------------------------------------------

// Configure asks for (or, under --yes, derives) the database settings
// (AC-20, AC-21, AC-26).
func (s DatabaseStep) Configure(ctx context.Context, rc ReadPorts, ui Prompter, st *RunState) error {
	if !ui.Interactive() {
		return s.configureAuto(ctx, rc, st)
	}
	return s.configureAsk(ctx, rc, ui, st)
}

// failHint is what the user can do about a failed connection under --yes.
func failHint(class DBErrorClass) string {
	switch class {
	case DBErrAuth:
		return "; pass the right password with --pg-password-stdin, or fix " + EnvKeyDSN
	case DBErrDSN:
		return "; fix --pg-dsn or " + EnvKeyDSN
	}
	return ""
}

// configureAuto is the --yes path: take the flag/env/env-file target (Seed
// did), or for topology local with no DSN the AC-21 create defaults with a
// generated password; a connection failure fails the step (no re-ask).
func (s DatabaseStep) configureAuto(ctx context.Context, rc ReadPorts, st *RunState) error {
	if !st.DB.IsSet() {
		if !st.Topology.IsSet() || st.Topology.Get() != TopologyLocal {
			return errors.New("no database is configured: pass --pg-dsn (and --pg-password-stdin) or set " + EnvKeyDSN +
				"; with --topology local and no DSN, install creates the database for you")
		}
		t, src, err := s.createTarget(DefaultPGDB, DefaultPGUser, st.Inputs.PGPassword)
		if err != nil {
			return err
		}
		t.Source = src
		st.DB.Set(t, src)
		return nil
	}
	switch a := s.assess(ctx, rc, st); a.kind {
	case dbFail:
		return errors.New(a.detail + failHint(a.class))
	case dbBlocked:
		return errors.New(a.detail)
	}
	return nil
}

// createTarget is the create-path target: localhost:5432, sslmode disable on
// loopback, the given role and database, and the typed password if one was
// passed on stdin, else a generated one (AC-21, AC-22).
func (s DatabaseStep) createTarget(name, user, given string) (DBTarget, Source, error) {
	pw, src := given, SourceFlag
	if pw == "" {
		var err error
		if pw, err = GeneratePassword(s.Rand); err != nil {
			return DBTarget{}, "", err
		}
		src = SourceGenerated
	}
	s.register(pw)
	t := DBTarget{Host: "localhost", Port: DefaultPGPort, Name: name, User: user, SSLMode: "disable", Mode: DBModeCreate, Source: src}
	return t.WithPassword(pw), src, nil
}

var (
	dbModeOptions = []string{
		"existing - use a database and role that already exist",
		"create   - create the role and database for me (install writes bootstrap.sql; you run it)",
	}
	sslOptions = []string{
		"prefer  - encrypt when the server supports it",
		"require - always encrypt (Tailscale already encrypts the link)",
		"disable - no TLS (Tailscale already encrypts the link)",
	}
	sslValues = []string{"prefer", "require", "disable"}
)

func nonBlank(what string) func(string) error {
	return func(v string) error {
		if strings.TrimSpace(v) == "" {
			return errors.New(what + " is empty")
		}
		return nil
	}
}

func (s DatabaseStep) configureAsk(ctx context.Context, rc ReadPorts, ui Prompter, st *RunState) error {
	local := st.Topology.IsSet() && st.Topology.Get() == TopologyLocal
	cur, set := st.DB.Get(), st.DB.IsSet()

	if set && !st.Inputs.Reconfigure {
		a := s.assess(ctx, rc, st)
		switch {
		case a.kind == dbFail && a.class == DBErrAuth && rc.DB != nil:
			t, err := s.connectLoop(ctx, rc, ui, cur, local, true)
			if err != nil {
				return err
			}
			return s.finish(ui, st, t)
		case a.kind == dbFail:
			ok, err := ui.Confirm(a.detail+". Enter the database settings again?", true)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New(a.detail)
			}
		case a.kind == dbBlocked:
			return errors.New(a.detail)
		default:
			return nil // ok, pending, or something bootstrap.sql will create: nothing to ask
		}
	}

	create := false
	if local {
		def := 0
		if !set {
			def = 1
		}
		i, err := ui.Select("Postgres runs on this machine. Use an existing database, or create the role and database?", dbModeOptions, def)
		if err != nil {
			return err
		}
		create = i == 1
	}
	if create {
		return s.askCreate(ui, st)
	}
	t, err := s.askExisting(ui, st, cur, set, local)
	if err != nil {
		return err
	}
	if rc.DB != nil {
		if t, err = s.connectLoop(ctx, rc, ui, t, local, false); err != nil {
			return err
		}
	}
	return s.finish(ui, st, t)
}

// finish shows the AC-26 warning before the new target reaches RunState and
// sets it unless the user declines.
func (s DatabaseStep) finish(ui Prompter, st *RunState, t DBTarget) error {
	if prior, ok := priorEnvDB(st); ok && !sameDatabase(prior, t) {
		w := warnDataStays("database", prior.String(), t.String())
		ok, err := ui.Confirm(w.Text+" Continue?", true)
		if err != nil {
			return err
		}
		if !ok {
			return nil // keep what is set
		}
	}
	t.Source = SourcePrompt
	s.register(string(t.password))
	st.DB.Set(t, SourcePrompt)
	return nil
}

// askCreate is the AC-21 create path: names (strict rule), loopback defaults
// and a password (stdin if given, else generated).
func (s DatabaseStep) askCreate(ui Prompter, st *RunState) error {
	name, err := ui.Text("Database name to create", DefaultPGDB, func(v string) error { return ValidateBootstrapName("database name", v) })
	if err != nil {
		return err
	}
	user, err := ui.Text("Role name to create", DefaultPGUser, func(v string) error { return ValidateBootstrapName("role name", v) })
	if err != nil {
		return err
	}
	t, _, err := s.createTarget(name, user, st.Inputs.PGPassword)
	if err != nil {
		return err
	}
	return s.finish(ui, st, t)
}

// askExisting asks for the AC-20 fields; the password comes from the stdin
// flag when given, else a no-echo prompt.
func (s DatabaseStep) askExisting(ui Prompter, st *RunState, cur DBTarget, set, local bool) (DBTarget, error) {
	defHost, defPort, defName, defUser, defSSL := "", DefaultPGPort, DefaultPGDB, DefaultPGUser, 0
	if local {
		defHost = "localhost"
	}
	if set {
		defHost, defPort, defName, defUser = cur.Host, cur.Port, cur.Name, cur.User
		for i, v := range sslValues {
			if v == cur.SSLMode {
				defSSL = i
			}
		}
	}
	var t DBTarget
	var err error
	if t.Host, err = ui.Text("Database host", defHost, func(v string) error { _, e := NormalizeHost(v); return e }); err != nil {
		return t, err
	}
	t.Host, _ = NormalizeHost(t.Host)
	if t.Port, err = ui.Text("Database port", defPort, ValidatePort); err != nil {
		return t, err
	}
	if t.Name, err = ui.Text("Database name", defName, ValidateDBName); err != nil {
		return t, err
	}
	if t.User, err = ui.Text("Database user", defUser, nonBlank("user")); err != nil {
		return t, err
	}
	i, err := ui.Select("TLS mode", sslOptions, defSSL)
	if err != nil {
		return t, err
	}
	t.SSLMode = sslValues[i]
	pw := st.Inputs.PGPassword
	if pw == "" {
		if pw, err = ui.Secret("Database password"); err != nil {
			return t, err
		}
	}
	s.register(pw)
	t = t.WithPassword(pw)
	t.Mode, t.Source = DBModeExisting, SourcePrompt
	return t, nil
}

// connectLoop probes t (read-only, the adapter's 5 s timeout) and classifies a
// failure (AC-20). An auth failure asks for the password again: the user gets
// three typed attempts (a seeded password is probed first and is not one),
// then ErrTooManyAttempts (the engine skips the step, AC-10).
// A missing database or vector extension on this machine is fine: Detect then
// takes the bootstrap.sql path with this password.
func (s DatabaseStep) connectLoop(ctx context.Context, rc ReadPorts, ui Prompter, t DBTarget, local, seeded bool) (DBTarget, error) {
	// A seeded password was not typed at this question, so it does not use up
	// one of the user's attempts: they get MaxPromptAttempts typed ones.
	limit := MaxPromptAttempts
	if seeded {
		limit++
	}
	for attempt := 1; ; attempt++ {
		status, err := rc.DB.Probe(ctx, t.DSN())
		cause := ""
		if err != nil {
			cause = s.redact(err.Error())
		}
		msg := classMessage(rc, status.ErrorClass, t, cause)
		switch {
		case status.Connected:
			return t, nil
		case status.ErrorClass == DBErrNoDB && local && HostIsLocal(t.Host):
			return t, nil
		case status.ErrorClass == DBErrAuth:
			if attempt >= limit {
				return t, fmt.Errorf("%w: %s (if the role does not exist yet, re-run and choose create)", ErrTooManyAttempts, msg)
			}
			pw, err := ui.Secret(msg + ". Password for role " + t.User)
			if err != nil {
				return t, err
			}
			s.register(pw)
			t = t.WithPassword(pw)
		default:
			return t, errors.New(msg)
		}
	}
}
