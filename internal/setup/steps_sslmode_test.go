package setup

import (
	"context"
	"strings"
	"testing"
)

// defaultsPrompter answers every Text with its default, records the default
// index of the TLS Select and returns a fixed password.
type defaultsPrompter struct{ tlsDef int }

func (p *defaultsPrompter) Select(q string, _ []string, def int) (int, error) {
	if strings.Contains(q, "TLS") {
		p.tlsDef = def
	}
	return def, nil
}
func (*defaultsPrompter) Confirm(string, bool) (bool, error) { return true, nil }
func (*defaultsPrompter) Text(_, def string, _ func(string) error) (string, error) {
	return def, nil
}
func (*defaultsPrompter) Secret(string) (string, error) { return "Pw-sslmode-1234", nil }
func (*defaultsPrompter) Interactive() bool             { return true }

// TestPGSSLModeSemantics: --pg-sslmode overrides the DSN's sslmode wherever
// the DSN comes from, makes the value flag-sourced (so the env file is
// rewritten, not kept as hand-edited), and defaults the prompt and the local
// create path.
func TestPGSSLModeSemantics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const envDSN = "MEMORY_PG_DSN=postgresql://u:Seed-pw-1234@env.example:5432/db?sslmode=disable\n"

	t.Run("flag DSN", func(t *testing.T) {
		h := newS23(t)
		st := NewRunState(Inputs{PGDSN: "postgresql://u@h.example:5432/db?sslmode=disable", PGSSLMode: "require"})
		if _, err := (DatabaseStep{}).Seed(ctx, h.rp(), st); err != nil {
			t.Fatal(err)
		}
		if st.DB.Get().SSLMode != "require" || st.DB.Source() != SourceFlag {
			t.Errorf("%#v %v", st.DB.Get(), st.DB)
		}
	})
	t.Run("env-file DSN is rewritten, not kept as modified", func(t *testing.T) {
		h := newS23(t)
		st := NewRunState(Inputs{PGSSLMode: "require"})
		st.Prior.EnvDoc = []byte(envDSN)
		h.write(h.p.EnvFile(), envDSN, 0o600)
		if _, err := (DatabaseStep{}).Seed(ctx, h.rp(), st); err != nil {
			t.Fatal(err)
		}
		if st.DB.Get().SSLMode != "require" || st.DB.Source() != SourceFlag {
			t.Fatalf("%#v %v", st.DB.Get(), st.DB)
		}
		var key ArtifactState
		for _, a := range (EnvFileStep{}).Detect(ctx, h.rp(), st).Artifacts {
			if a.ID == "envfile/MEMORY_PG_DSN" {
				key = a
			}
		}
		if key.State != StateOutdated {
			t.Errorf("MEMORY_PG_DSN state = %q (%s), want outdated", key.State, key.Detail)
		}
	})
	t.Run("env-file DSN already at the flag's sslmode keeps its source", func(t *testing.T) {
		h := newS23(t)
		st := NewRunState(Inputs{PGSSLMode: "disable"})
		st.Prior.EnvDoc = []byte(envDSN)
		if _, err := (DatabaseStep{}).Seed(ctx, h.rp(), st); err != nil {
			t.Fatal(err)
		}
		if st.DB.Source() != SourceEnvFile {
			t.Errorf("source = %v, want envfile", st.DB)
		}
	})
	t.Run("shell DSN", func(t *testing.T) {
		h := newS23(t)
		h.env = Env{"MEMORY_PG_DSN": "postgresql://u:Shell-pw-1234@shell.example:5432/db"}
		st := NewRunState(Inputs{PGSSLMode: "require"})
		if _, err := (DatabaseStep{}).Seed(ctx, h.rp(), st); err != nil {
			t.Fatal(err)
		}
		if st.DB.Get().SSLMode != "require" || st.DB.Source() != SourceFlag {
			t.Errorf("%#v %v", st.DB.Get(), st.DB)
		}
	})
	t.Run("create mode", func(t *testing.T) {
		tg, _, err := (DatabaseStep{Redactor: NewRedactor()}).createTarget("db", "u", "", "require")
		if err != nil || tg.SSLMode != "require" {
			t.Errorf("%#v %v", tg, err)
		}
		tg, _, _ = (DatabaseStep{Redactor: NewRedactor()}).createTarget("db", "u", "", "")
		if tg.SSLMode != "disable" {
			t.Errorf("default create sslmode = %q", tg.SSLMode)
		}
	})
	t.Run("prompt default", func(t *testing.T) {
		for flagVal, want := range map[string]int{"": 0, "prefer": 0, "require": 1, "disable": 2} {
			ui := &defaultsPrompter{}
			st := NewRunState(Inputs{PGSSLMode: flagVal})
			if _, err := (DatabaseStep{Redactor: NewRedactor()}).askExisting(ui, st, DBTarget{}, false, false); err != nil {
				t.Fatal(err)
			}
			if ui.tlsDef != want {
				t.Errorf("--pg-sslmode %q: TLS default %d, want %d", flagVal, ui.tlsDef, want)
			}
		}
	})
}
