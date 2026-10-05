package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redfoxius/claude-memory/internal/config"
)

// Regression tests for the slice-2 review findings (branch
// fix/install-s2-review-4a7). Each one fails against the code before its fix.

const review4a7DSN = "postgresql://claude_memory:s3cret@db.example:5432/claude_memory?sslmode=prefer"

// HIGH-1: an `export` line must never be converted while the key has a plain
// assignment: the binary reads the plain line, and a converted export line
// above it would win the first-non-empty rule.
func TestEnvFileExportShadowedByPlainIsNeverConverted(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	old := "postgresql://claude_memory:old-pw-1@db.example:5432/claude_memory?sslmode=prefer"
	file := "export MEMORY_PG_DSN=" + old + "\nMEMORY_PG_DSN=" + review4a7DSN + "\n"
	h.write(h.p.EnvFile(), file, 0o600)
	st := dbState(t, "s3cret", SourceEnvFile)

	det := EnvFileStep{}.Detect(context.Background(), h.rp(), st)
	if got := stateOf(det, envFormatArtifact); got != StateOK {
		t.Errorf("format artifact is %s, want ok (nothing may be converted)", got)
	}
	found := false
	for _, n := range det.Notes {
		found = found || strings.Contains(n.Text, "line 1") && strings.Contains(n.Text, "plain assignment")
	}
	if !found {
		t.Errorf("no duplicate note for the shadowed export line: %+v", det.Notes)
	}

	r := h.envRun(st, envFormatArtifact)
	if r.after != file {
		t.Fatalf("file changed:\n%s", r.after)
	}
	if got := config.ParseEnvData([]byte(r.after), 0o600).Values["MEMORY_PG_DSN"]; got != review4a7DSN {
		t.Errorf("the binary would read %q", got)
	}
}

// MED-2: the DSN present only as an `export` line or a quoted value is not
// read as intended by the binary: outdated, not ok.
func TestEnvFileDSNOnlyInExportOrQuotedFormIsOutdated(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]string{
		"export": "export MEMORY_PG_DSN=" + review4a7DSN + "\n",
		"quoted": "MEMORY_PG_DSN=\"" + review4a7DSN + "\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			h := newS23(t)
			h.write(h.p.EnvFile(), line, 0o600)
			det := EnvFileStep{}.Detect(context.Background(), h.rp(), dbState(t, "s3cret", SourceEnvFile))
			if got := stateOf(det, envKeyArtifact(EnvKeyDSN)); got != StateOutdated {
				t.Errorf("DSN artifact %s (%s), want outdated", got, stateDetail(det, envKeyArtifact(EnvKeyDSN)))
			}
			if d := stateDetail(det, envKeyArtifact(EnvKeyDSN)); !strings.Contains(d, "not read") {
				t.Errorf("detail %q", d)
			}
			// The default apply rewrites it to the plain form.
			r := h.envRun(dbState(t, "s3cret", SourceEnvFile))
			if !strings.Contains(r.after, "MEMORY_PG_DSN="+review4a7DSN+"\n") || strings.Contains(r.after, "export") || strings.Contains(r.after, `"`) {
				t.Errorf("after: %q", r.after)
			}
		})
	}
}

// MED-3: an unparseable DSN line is recovered (AC-28), and the shell's
// (stale) value never stands in for it.
func TestDatabaseSeedRecoversUnparseableLineAndIgnoresShell(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.env = Env{"MEMORY_PG_DSN": "postgresql://u:shell-pw-123@shell.example:5432/shelldb"}
	st := NewRunState(Inputs{})
	st.Prior.EnvDoc = []byte("MEMORY_PG_DSN=postgresql://claude_memory:pa$word@file.example:5432/filedb?sslmode=require\n")
	red := NewRedactor()
	notes, err := DatabaseStep{Redactor: red}.Seed(context.Background(), h.rp(), st)
	if err != nil || !st.DB.IsSet() {
		t.Fatalf("%v set=%v notes=%v", err, st.DB.IsSet(), notes)
	}
	d := st.DB.Get()
	if d.Host != "file.example" || d.Name != "filedb" || fmt.Sprint(d.password) == "pa$word" && false || st.DB.Source() != SourceEnvFile {
		t.Errorf("%v %v", d, st.DB)
	}
	if !d.SameConnection(mustTarget(t, "postgresql://claude_memory@file.example:5432/filedb?sslmode=require", "pa$word", "")) {
		t.Errorf("the password was not recovered: %v", d)
	}
	drift := false
	for _, n := range notes {
		drift = drift || strings.Contains(n.Text, "differs from the env file")
	}
	if !drift {
		t.Errorf("no drift note: %+v", notes)
	}
}

// MED-4: --reconfigure on the create path while a bootstrap.sql for another
// password exists: the file must be re-rendered, not waited for.
func TestDatabaseStaleBootstrapIsRerendered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newS23(t)
	writeBootstrapFor(t, h, "First-pw-12345")
	st := dbSt(Inputs{Reconfigure: true})
	st.DB.Set(localTarget("Second-pw-12345", DBModeCreate), SourcePrompt)
	db := fixedDB(authErr, nil)
	step := DatabaseStep{Redactor: NewRedactor()}

	det := step.Detect(ctx, h.dbRP(db), st)
	if got := stateOf(det, DatabaseArtifact); got != StateAbsent {
		t.Fatalf("database artifact %s (%s), want absent (needs bootstrap)", got, det.Detail)
	}
	plan, err := step.Plan(ctx, h.dbRP(db), st, Choices{DatabaseArtifact: ChoiceApply})
	if err != nil || len(plan.Actions) != 1 {
		t.Fatalf("%+v %v", plan, err)
	}
	if res, err := step.Apply(ctx, h.wpDB(db), st, plan); err != nil || res.Await == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if b := readFile(t, h.p.Bootstrap()); !strings.Contains(b, "Second-pw-12345") || strings.Contains(b, "First-pw-12345") {
		t.Errorf("bootstrap.sql was not re-rendered:\n%s", b)
	}
	// With the file now matching, the step waits for the user again.
	if det := step.Detect(ctx, h.dbRP(db), st); stateOf(det, DatabaseArtifact) != StateBlocked {
		t.Errorf("matching file: %s (%s)", stateOf(det, DatabaseArtifact), det.Detail)
	}
}

// MED-5: the env diff never prints an unparseable old DSN line (a keyword
// form carries the password in a shape no Redactor pattern knows).
func TestEnvFileDiffMasksUnparseableOldDSN(t *testing.T) {
	t.Parallel()
	const oldPW = "Kw-old-secret-99"
	h := newS23(t)
	h.write(h.p.EnvFile(), "MEMORY_PG_DSN=host=db.example password="+oldPW+" dbname=claude_memory\nOTHER=1\n", 0o600)
	st := dbState(t, "Kw-new-pw-12345", SourceEnvFile) // differs => modified => confirmed overwrite keeps a backup
	r := h.envRun(st, envKeyArtifact(EnvKeyDSN))
	if len(r.plan.Diffs) != 1 || len(r.res.Diffs) != 1 {
		t.Fatalf("diffs: plan %d, applied %d", len(r.plan.Diffs), len(r.res.Diffs))
	}
	for _, d := range []Diff{r.plan.Diffs[0], r.res.Diffs[0]} {
		if strings.Contains(d.Unified, oldPW) {
			t.Errorf("diff prints the old password:\n%s", d.Unified)
		}
		if !strings.Contains(d.Unified, "OTHER=1") {
			t.Errorf("diff lost its context:\n%s", d.Unified)
		}
	}
	// LOW-11: the backup holds the old password by design (spec AC-30), and
	// nothing else under the config directory may.
	entries, err := os.ReadDir(h.p.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	baks := 0
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(h.p.ConfigDir, e.Name()))
		if !strings.Contains(string(b), oldPW) {
			continue
		}
		if !strings.HasPrefix(e.Name(), "env"+envBackupSuffix) {
			t.Errorf("%s holds the old password", e.Name())
			continue
		}
		baks++
		if fi, _ := os.Stat(filepath.Join(h.p.ConfigDir, e.Name())); fi.Mode().Perm() != 0o600 {
			t.Errorf("backup mode %v", fi.Mode().Perm())
		}
	}
	if baks != 1 {
		t.Errorf("want exactly one backup holding the old password, got %d", baks)
	}
}

// MED-6: a step without a Redactor fails closed: error text from a probe is
// withheld, never printed raw.
func TestStepsWithoutRedactorWithholdErrorText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const leak = "Leaky-pw-123456"
	boom := errors.New("connect failed for " + leak + " at host=h password=" + leak)

	h := newS23(t)
	st := dbSt(Inputs{})
	st.DB.Set(localTarget(leak, DBModeExisting), SourceEnvFile)
	det := (DatabaseStep{}).Detect(ctx, h.dbRP(fixedDB(DBStatus{ErrorClass: DBErrOther}, boom)), st)
	if strings.Contains(det.Detail, leak) || strings.Contains(det.Remedy, leak) {
		t.Errorf("database: %q", det.Detail)
	}
	det = (MigrateStep{}).Detect(ctx, h.dbRP(fixedDB(DBStatus{Connected: true}, boom)), st)
	if strings.Contains(det.Detail, leak) {
		t.Errorf("migrate: %q", det.Detail)
	}
	if got := (OllamaStep{}).redact(boom.Error()); strings.Contains(got, leak) {
		t.Errorf("ollama: %q", got)
	}
}

// LOW-7: a connection bootstrap.sql cannot fix is blocked with a remedy, not
// "absent" (whose default apply Plan refuses).
func TestDatabaseDetectFailIsBlockedWithRemedy(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	st := dbSt(Inputs{})
	st.DB.Set(localTarget("Some-pw-123456", DBModeExisting), SourceEnvFile)
	det := (DatabaseStep{Redactor: NewRedactor()}).Detect(context.Background(), h.dbRP(fixedDB(DBStatus{ErrorClass: DBErrUnreachable}, nil)), st)
	if stateOf(det, DatabaseArtifact) != StateBlocked || det.Remedy == "" {
		t.Errorf("%s remedy %q", stateOf(det, DatabaseArtifact), det.Remedy)
	}
}

// LOW-10: no fmt verb prints a DBTarget password, however the target is
// reached (RunState, its Field, the target, the password itself).
func TestRunStateNeverPrintsThePassword(t *testing.T) {
	t.Parallel()
	const pw = "Kw-print-secret-77"
	st := NewRunState(Inputs{PGPassword: pw})
	st.DB.Set(localTarget(pw, DBModeCreate), SourceGenerated)
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		for name, v := range map[string]any{"state": st, "field": st.DB, "fieldptr": &st.DB, "target": st.DB.Get(), "password": st.DB.Get().password} {
			if out := fmt.Sprintf(verb, v); strings.Contains(out, pw) {
				t.Errorf("%s of %s prints the password: %s", verb, name, out)
			}
		}
	}
}

// LOW-12: an existing directory with loose permissions that holds secrets is
// tightened to 0700, with a note.
func TestExistingSecretDirsAreTightened(t *testing.T) {
	t.Parallel()
	t.Run("config dir", func(t *testing.T) {
		h := newS23(t)
		if err := os.MkdirAll(h.p.ConfigDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(h.p.ConfigDir, 0o755); err != nil {
			t.Fatal(err)
		}
		r := h.envRun(dbState(t, "s3cret", SourceFlag))
		if fi, err := os.Stat(h.p.ConfigDir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("config dir mode %v %v", fi.Mode().Perm(), err)
		}
		if len(r.res.Notes) == 0 || !strings.Contains(r.res.Notes[0].Text, "tightened") {
			t.Errorf("notes %+v", r.res.Notes)
		}
	})
	t.Run("state dir", func(t *testing.T) {
		ctx := context.Background()
		h := newS23(t)
		if err := os.MkdirAll(h.p.StateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(h.p.StateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		st := dbSt(Inputs{})
		st.DB.Set(localTarget("Some-pw-123456", DBModeCreate), SourceGenerated)
		db := fixedDB(authErr, nil)
		step := DatabaseStep{Redactor: NewRedactor(), AppRolePSQL: []byte("-- b\n")}
		plan, err := step.Plan(ctx, h.dbRP(db), st, Choices{DatabaseArtifact: ChoiceApply})
		if err != nil {
			t.Fatal(err)
		}
		res, err := step.Apply(ctx, h.wpDB(db), st, plan)
		if err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(h.p.StateDir); fi.Mode().Perm() != 0o700 {
			t.Errorf("state dir mode %v", fi.Mode().Perm())
		}
		if len(res.Notes) == 0 || !strings.Contains(res.Notes[0].Text, "tightened") {
			t.Errorf("notes %+v", res.Notes)
		}
	})
}

// LOW-15: a seeded (not typed) password does not use up one of the three
// attempts the user gets.
func TestDatabaseSeededPasswordLeavesThreeTypedAttempts(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	st := dbSt(Inputs{})
	st.Topology.Set(TopologyRemote, SourceFlag)
	d := localTarget("Seeded-pw-1234", DBModeExisting)
	d.Host = "db.example"
	st.DB.Set(d, SourceEnvFile)
	ui := newPW(t, true).
		secret("authentication failed", "bad-pw-0001").secret("authentication failed", "bad-pw-0002").secret("authentication failed", "bad-pw-0003")
	err := (DatabaseStep{Redactor: NewRedactor()}).Configure(context.Background(), h.dbRP(fixedDB(authErr, nil)), ui, st)
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("%v", err)
	}
}

// LOW-8: a reserved database name is refused (install and the shared psql
// file), and the printed instructions say PUBLIC privileges are revoked.
func TestBootstrapRefusesReservedDatabaseNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"postgres", "template0", "template1"} {
		if err := ValidateBootstrapName("database name", name); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := RenderBootstrap([]byte("-- b\n"), "claude_memory", name, "Some-pw-123456"); err == nil {
			t.Errorf("%s rendered", name)
		}
	}
	// A role may still be called postgres-like names only by the usual rule.
	if err := ValidateBootstrapName("role name", "postgres"); err != nil {
		t.Errorf("role name rule changed: %v", err)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "deploy", "initdb", "app-role.psql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'postgres', 'template0', 'template1'", `\if :app_db_reserved`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("app-role.psql lacks %q", want)
		}
	}
	if joined := strings.Join(bootstrapInstructions(OSLinux, "/x/bootstrap.sql"), "\n"); !strings.Contains(joined, "PUBLIC") {
		t.Errorf("instructions do not mention the PUBLIC revoke:\n%s", joined)
	}
}
