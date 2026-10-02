package setup

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// Tests for WI-S2-6: the migrate step.

func migrations(missing map[string][]string) []MigrationStatus {
	var out []MigrationStatus
	for _, id := range []string{"0001", "0002"} {
		out = append(out, MigrationStatus{ID: id, Missing: missing[id]})
	}
	return out
}

func TestMigrateDetect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vec := func(ms []MigrationStatus, unknown ...string) DBStatus {
		return DBStatus{Connected: true, VectorVersion: "0.8.0", Migrations: ms, Unknown: unknown}
	}
	cases := []struct {
		name      string
		status    DBStatus
		err       error
		noDB      bool
		want      State
		blockedBy string
		detail    string
		note      string
	}{
		{name: "no DB chosen", noDB: true, want: StateBlocked, blockedBy: "database", detail: "no database chosen"},
		{name: "unreachable", status: DBStatus{ErrorClass: DBErrAuth}, want: StateBlocked, blockedBy: "database", detail: "not reachable yet (auth)"},
		{name: "no vector", status: DBStatus{Connected: true, Migrations: migrations(nil)}, want: StateBlocked, blockedBy: "database", detail: "vector extension"},
		{name: "catalog unreadable", status: DBStatus{Connected: true}, err: errors.New("permission denied"), want: StateBlocked, detail: "could not be read"},
		{name: "vector present, catalog unreadable", status: DBStatus{Connected: true, VectorVersion: "0.8.0"}, want: StateBlocked, detail: "could not be read"},
		{name: "all applied", status: vec(migrations(nil)), want: StateOK, detail: "migrations 0001, 0002 applied"},
		{name: "0002 missing", status: vec(migrations(map[string][]string{"0002": {"records.namespace"}})), want: StateOutdated, detail: "schema behind: 0002 (missing records.namespace)"},
		{name: "no schema", status: vec(migrations(map[string][]string{"0001": {"records", "records.embedding"}, "0002": {"records.namespace"}})), want: StateAbsent, detail: "no claude-memory schema yet"},
		{name: "unknown objects are an info note", status: vec(migrations(nil), "records.future_col"), want: StateOK, note: "records.future_col"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newS23(t)
			st := NewRunState(Inputs{})
			if !c.noDB {
				st.DB.Set(localTarget("Mig-pw-12345", DBModeExisting), SourceEnvFile)
			}
			d := (MigrateStep{}).Detect(ctx, h.dbRP(fixedDB(c.status, c.err)), st)
			if d.State != c.want || d.BlockedBy != c.blockedBy || !strings.Contains(d.Detail, c.detail) {
				t.Errorf("%+v, want %s by %q containing %q", d, c.want, c.blockedBy, c.detail)
			}
			if c.note != "" && (len(d.Notes) != 1 || d.Notes[0].Level != NoteInfo || !strings.Contains(d.Notes[0].Text, c.note)) {
				t.Errorf("notes %+v", d.Notes)
			}
			if c.want != StateBlocked && (len(d.Artifacts) != 1 || d.Artifacts[0].ID != "migrate/schema") {
				t.Errorf("artifacts %+v", d.Artifacts)
			}
		})
	}
}

// Apply migrates with RunState's DSN, never the process environment's, and a
// failure is returned without the password.
func TestMigrateApplyUsesStepStateDSN(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newS23(t)
	h.env = Env{"MEMORY_PG_DSN": "postgresql://shell:Shell-pw-1234@shellhost:5432/shelldb"}
	st := NewRunState(Inputs{})
	target := localTarget("Step-pw-12345", DBModeExisting)
	st.DB.Set(target, SourceEnvFile)
	db := fixedDB(healthy, nil)
	step := MigrateStep{}
	plan, err := step.Plan(ctx, h.dbRP(db), st, Choices{MigrateSchemaArtifact: ChoiceApply})
	if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Verb != "migrate" || strings.Contains(plan.Actions[0].Desc+plan.Actions[0].Path, "Step-pw") {
		t.Fatalf("%+v %v", plan, err)
	}
	if p, _ := step.Plan(ctx, h.dbRP(db), st, Choices{MigrateSchemaArtifact: ChoiceKeep}); len(p.Actions) != 0 {
		t.Errorf("kept: %+v", p)
	}
	res, err := step.Apply(ctx, h.wpDB(db), st, plan)
	if err != nil || len(res.Artifacts) != 0 || res.Await != nil {
		t.Fatalf("%+v %v", res, err)
	}
	if len(db.migrated) != 1 || db.migrated[0] != target.DSN() || strings.Contains(db.migrated[0], "shellhost") {
		t.Errorf("Migrate got %q, want the step-state DSN %q", db.migrated, target.DSN())
	}

	red := NewRedactor()
	red.Register("Step-pw-12345")
	db.migrateErr = errors.New("migrate: connect: failed for Step-pw-12345 at postgresql://u:Step-pw-12345@h/d")
	_, err = (MigrateStep{Redactor: red}).Apply(ctx, h.wpDB(db), st, plan)
	if err == nil || strings.Contains(err.Error(), "Step-pw-12345") || !strings.HasPrefix(err.Error(), "migrate: ") || strings.HasPrefix(err.Error(), "migrate: migrate:") {
		t.Errorf("%v", err)
	}
	if _, err := step.Apply(ctx, h.wpDB(db), NewRunState(Inputs{}), plan); err == nil {
		t.Error("Apply without a DB did not fail")
	}
}

// engine: migrate precedes hooks.settings and uses the step-state DSN; a
// failing database blocks migrate and hooks.settings but not skills (AC-7,
// AC-25).
func TestMigrateThroughTheEngine(t *testing.T) {
	t.Parallel()
	mk := func(t *testing.T, dbApplyErr error) (*eh, *scriptDB, *FakeStep, RunResult) {
		h := newEH(t, false)
		var mu sync.Mutex
		migrated := false
		db := &scriptDB{evidence: true, fn: func(DBTarget) (DBStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			if migrated {
				return healthy, nil
			}
			return DBStatus{Connected: true, VectorVersion: "0.8.0", Migrations: migrations(map[string][]string{"0001": {"records"}, "0002": {"records.namespace"}})}, nil
		}}
		world := NewFakeWorld(map[string]State{"database/database": StateAbsent, "envfile": StateOK, "hooks.settings": StateAbsent, "skills": StateAbsent})
		dbStep := &FakeStep{StepID: "database", Log: h.log, DetectF: world.DetectFor("database/database"),
			SeedF: func(st *RunState) ([]Note, error) {
				st.DB.Set(localTarget("Step-pw-12345", DBModeExisting), SourceEnvFile)
				return nil, nil
			},
			ApplyF: func(n int, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
				if dbApplyErr != nil {
					return StepResult{}, dbApplyErr
				}
				world.Set("database/database", StateOK)
				return StepResult{}, nil
			}}
		envStep := &FakeStep{StepID: "envfile", Log: h.log, DetectF: world.DetectFor("envfile")}
		var hooksSawMigrated bool
		hooks := &FakeStep{StepID: "hooks.settings", Req: []string{"migrate"}, Log: h.log, DetectF: world.DetectFor("hooks.settings"),
			ApplyF: func(n int, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
				mu.Lock()
				hooksSawMigrated = migrated
				mu.Unlock()
				world.Set("hooks.settings", StateOK)
				return StepResult{}, nil
			}}
		skills := &FakeStep{StepID: "skills", Log: h.log, DetectF: world.DetectFor("skills"), ApplyF: world.ApplyOK()}
		migDB := &migrateFlagDB{scriptDB: db, onMigrate: func() { mu.Lock(); migrated = true; mu.Unlock() }}
		rp := ReadPorts{FS: h.fs, Runner: NewFakeRunner(t), DB: migDB, Clock: h.clk, Paths: h.p,
			Env: Env{"MEMORY_PG_DSN": "postgresql://shell:Shell-pw-1234@shellhost:5432/shelldb"}}
		e := &Engine{Steps: []Step{dbStep, envStep, MigrateStep{}, hooks, skills}, Read: rp,
			Write: WritePorts{ReadPorts: rp, FS: h.fs, DB: migDB}, UI: h.ui, Reporter: h.rep, Version: "v1.2.0"}
		res := e.Run(context.Background(), Inputs{Yes: true})
		if dbApplyErr == nil && !hooksSawMigrated {
			t.Error("hooks.settings was applied before the schema was migrated")
		}
		return h, db, hooks, res
	}

	t.Run("migrate runs after database, before hooks.settings, with the step-state DSN", func(t *testing.T) {
		h, db, _, res := mk(t, nil)
		wantExit(t, res, ExitOK)
		if len(db.migrated) != 1 || !strings.Contains(db.migrated[0], "Step-pw-12345@localhost") || strings.Contains(db.migrated[0], "shellhost") {
			t.Errorf("migrated with %q", db.migrated)
		}
		if o := outcome(t, res, "migrate"); o.Outcome != OutcomeApplied {
			t.Errorf("%+v", o)
		}
		for _, d := range db.dsns {
			if strings.Contains(d, "shellhost") {
				t.Errorf("probed with the process environment's DSN: %s", d)
			}
		}
		if h.log.Index("Apply database") > h.log.Index("Apply hooks.settings") {
			t.Errorf("order: %v", h.log.Calls())
		}
		if len(h.manifest().Artifacts) != 0 {
			for _, a := range h.manifest().Artifacts {
				if a.Step == "migrate" {
					t.Errorf("migrate recorded %+v", a)
				}
			}
		}
	})
	t.Run("a failing database blocks migrate and hooks.settings, not skills", func(t *testing.T) {
		_, db, _, res := mk(t, errors.New("boom"))
		wantExit(t, res, ExitFailed)
		if len(db.migrated) != 0 {
			t.Errorf("migrated despite a failed database: %v", db.migrated)
		}
		for id, want := range map[string]Outcome{"database": OutcomeFailed, "migrate": OutcomeBlocked, "hooks.settings": OutcomeBlocked, "skills": OutcomeApplied} {
			if o := outcome(t, res, id); o.Outcome != want {
				t.Errorf("%s: %+v, want %s", id, o, want)
			}
		}
	})
}

// migrateFlagDB runs a hook when Migrate is called (the world changes).
type migrateFlagDB struct {
	*scriptDB
	onMigrate func()
}

func (m *migrateFlagDB) Migrate(ctx context.Context, dsn string) error {
	err := m.scriptDB.Migrate(ctx, dsn)
	m.onMigrate()
	return err
}
