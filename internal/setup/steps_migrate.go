package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MigrateStepID is the id of the migrate step (AC-7: after database; the
// hooks.settings step requires it, so a failed migration never leaves a wired
// hook against an empty schema).
const MigrateStepID = "migrate"

// MigrateSchemaArtifact is the step's one artifact id.
const MigrateSchemaArtifact = "migrate/schema"

// MigrateStep applies the embedded migrations through DBProber.Migrate, the
// same code path as `claude-memory migrate` (AC-25). Detect is the pg.schema
// introspection of DBStatus.Migrations (Design 6): every embedded migration's
// objects present is ok, so a no-op run applies nothing. The DSN is always
// RunState.DB (what topology/database resolved and envfile wrote), never the
// process environment loaded at startup. Nothing is recorded in the manifest:
// uninstall never drops the schema.
type MigrateStep struct {
	// Redactor, when set, masks secrets in a migration error (AC-30); the
	// adapter already sanitizes its errors.
	Redactor *Redactor
}

var _ Step = MigrateStep{}

// ID implements Step.
func (MigrateStep) ID() string { return MigrateStepID }

// Title implements Step.
func (MigrateStep) Title() string { return "Schema migrations" }

// Requires implements Step.
func (MigrateStep) Requires() []string { return []string{DatabaseStepID, EnvFileStepID} }

func (m MigrateStep) redact(s string) string {
	if m.Redactor == nil {
		return s
	}
	return m.Redactor.Redact(s)
}

// pendingMigrations lists the not-fully-applied migrations as "0002 (missing
// records.namespace)" and reports whether the schema is entirely absent (the
// records table is missing).
func pendingMigrations(ms []MigrationStatus) (pending []string, noSchema bool) {
	for _, mg := range ms {
		if mg.Applied() {
			continue
		}
		for _, o := range mg.Missing {
			if o == "records" {
				noSchema = true
			}
		}
		pending = append(pending, mg.ID+" (missing "+strings.Join(capList(mg.Missing, 3), ", ")+")")
	}
	return pending, noSchema
}

// Detect implements Step. Until the database is reachable with the vector
// extension the step is blocked by the database step, which is what turns a
// pending database into "apply (after database)".
func (m MigrateStep) Detect(ctx context.Context, rc ReadPorts, st *RunState) Detection {
	if !st.DB.IsSet() {
		return Detection{State: StateBlocked, BlockedBy: DatabaseStepID, Detail: "no database chosen yet"}
	}
	dsn := st.DB.Get().DSN()
	if dsn == "" {
		return Detection{State: StateBlocked, Detail: "the database settings are invalid (host, port, database name or sslmode)"}
	}
	if rc.DB == nil {
		return Detection{State: StateBlocked, Detail: "no database prober is configured"}
	}
	status, err := rc.DB.Probe(ctx, dsn)
	switch {
	case !status.Connected:
		return Detection{State: StateBlocked, BlockedBy: DatabaseStepID, Detail: "the database is not reachable yet (" + string(orDefaultClass(status.ErrorClass)) + ")"}
	case status.VectorVersion == "" && status.Migrations == nil && err != nil:
		return Detection{State: StateBlocked, Detail: "connected, but the schema could not be read: " + m.redact(err.Error())}
	case status.VectorVersion == "":
		return Detection{State: StateBlocked, BlockedBy: DatabaseStepID, Detail: "the vector extension is not installed in the database yet"}
	case status.Migrations == nil:
		cause := "no catalog data"
		if err != nil {
			cause = m.redact(err.Error())
		}
		return Detection{State: StateBlocked, Detail: "the schema could not be read: " + cause}
	}
	var notes []Note
	if len(status.Unknown) > 0 {
		notes = append(notes, Note{NoteInfo, "objects no migration of this binary creates (a newer binary?): " + strings.Join(capList(status.Unknown, 5), ", ")})
	}
	pending, noSchema := pendingMigrations(status.Migrations)
	state, detail := StateOK, "migrations applied"
	if ids := appliedIDs(status.Migrations); len(ids) > 0 {
		detail = "migrations " + strings.Join(ids, ", ") + " applied"
	}
	switch {
	case noSchema:
		state, detail = StateAbsent, "no claude-memory schema yet: "+strings.Join(pending, "; ")
	case len(pending) > 0:
		state, detail = StateOutdated, "schema behind: "+strings.Join(pending, "; ")
	}
	return Detection{State: state, Detail: detail, Notes: notes,
		Artifacts: []ArtifactState{{ID: MigrateSchemaArtifact, State: state, Detail: detail}}}
}

func orDefaultClass(c DBErrorClass) DBErrorClass {
	if c == "" {
		return DBErrOther
	}
	return c
}

func appliedIDs(ms []MigrationStatus) []string {
	var ids []string
	for _, mg := range ms {
		if mg.Applied() {
			ids = append(ids, mg.ID)
		}
	}
	return ids
}

// Plan implements Step.
func (MigrateStep) Plan(_ context.Context, _ ReadPorts, _ *RunState, ch Choices) (Plan, error) {
	var p Plan
	if ch[MigrateSchemaArtifact] == ChoiceApply {
		p.Actions = append(p.Actions, Action{Artifact: MigrateSchemaArtifact, Verb: "migrate",
			Desc: "apply the embedded schema migrations (the same as `claude-memory migrate`)"})
	}
	return p, nil
}

// Apply implements Step: DBProber.Migrate with the DSN of RunState.DB.
func (m MigrateStep) Apply(ctx context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	if len(p.Actions) == 0 {
		return res, nil
	}
	if !st.DB.IsSet() {
		return res, errors.New("no database chosen")
	}
	dsn := st.DB.Get().DSN()
	if dsn == "" {
		return res, errors.New("the database settings are invalid")
	}
	if wc.DB == nil {
		return res, errors.New("no database prober is configured")
	}
	if err := wc.DB.Migrate(ctx, dsn); err != nil {
		return res, fmt.Errorf("migrate: %s", m.redact(strings.TrimPrefix(err.Error(), "migrate: ")))
	}
	return res, nil
}
