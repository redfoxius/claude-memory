package setup

import (
	"context"
	"strings"
	"testing"
)

// Tests for WI-S2-4a: the topology step.

// evidenceDB is a DBProbe whose only working method is LocalServerEvidence.
type evidenceDB struct {
	ok   bool
	why  string
	call int
}

func (d *evidenceDB) Probe(context.Context, string) (DBStatus, error) {
	panic("the topology step never probes the database")
}

func (d *evidenceDB) LocalServerEvidence(context.Context) (bool, string) {
	d.call++
	return d.ok, d.why
}

func topoPorts(h *s23, db DBProbe) ReadPorts {
	rp := h.rp()
	rp.DB = db
	return rp
}

func topoState(in Inputs, man string, envFile string) *RunState {
	st := NewRunState(in)
	if man != "" {
		st.Prior.Manifest = &Manifest{Topology: man}
	}
	if envFile != "" {
		st.Prior.EnvDoc = []byte(envFile)
	}
	return st
}

func TestTopologySeed(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	s := TopologyStep{}
	ctx := context.Background()

	// Errors: exit-2 flag values.
	for in, want := range map[string]string{
		"docker-local":  "deferred",
		"docker-remote": "deferred",
		"nope":          "local or remote",
	} {
		_, err := s.Seed(ctx, h.rp(), NewRunState(Inputs{Topology: in}))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("--topology %s: %v", in, err)
		}
	}
	_, err := s.Seed(ctx, h.rp(), NewRunState(Inputs{PGDSN: "postgresql://u:" + sentinelPassword + "@h/d"}))
	if err == nil || !strings.Contains(err.Error(), "--pg-password-stdin") || strings.Contains(err.Error(), "S3ntinel") {
		t.Errorf("--pg-dsn with a password: %v", err)
	}

	// Sources: manifest wins over a differing flag (the flag is applied in
	// Configure, after the warning); a flag alone is set; nothing is unset.
	st := topoState(Inputs{Topology: "remote"}, "local", "")
	if _, err := s.Seed(ctx, h.rp(), st); err != nil || st.Topology.Get() != TopologyLocal || st.Topology.Source() != SourceManifest {
		t.Errorf("manifest: %v %v", st.Topology, err)
	}
	st = topoState(Inputs{Topology: "remote"}, "", "")
	if _, err := s.Seed(ctx, h.rp(), st); err != nil || st.Topology.Get() != TopologyRemote || st.Topology.Source() != SourceFlag {
		t.Errorf("flag: %v %v", st.Topology, err)
	}
	st = topoState(Inputs{}, "", "")
	if _, err := s.Seed(ctx, h.rp(), st); err != nil || st.Topology.IsSet() {
		t.Errorf("unset: %v %v", st.Topology, err)
	}
}

func TestTopologyDefaultTable(t *testing.T) { // AC-19 detection table
	t.Parallel()
	h := newS23(t)
	ctx := context.Background()
	cases := []struct {
		name    string
		env     Env
		envFile string
		db      *evidenceDB
		want    Topology
	}{
		{"env file loopback DSN", nil, "MEMORY_PG_DSN=postgresql://u:p@127.0.0.1:5432/d\n", &evidenceDB{}, TopologyLocal},
		{"env file localhost DSN", nil, "MEMORY_PG_DSN=postgresql://u:p@localhost/d\n", &evidenceDB{}, TopologyLocal},
		{"env file remote DSN beats local server", nil, "MEMORY_PG_DSN=postgresql://u:p@db.tail.ts.net:5432/d\n", &evidenceDB{ok: true, why: "x"}, TopologyRemote},
		{"Env DSN remote", Env{"MEMORY_PG_DSN": "postgresql://u:p@10.0.0.9/d"}, "", &evidenceDB{ok: true}, TopologyRemote},
		{"Env DSN loopback", Env{"MEMORY_PG_DSN": "postgresql://u:p@[::1]/d"}, "", &evidenceDB{}, TopologyLocal},
		{"env file wins over Env", Env{"MEMORY_PG_DSN": "postgresql://u:p@remote.example/d"}, "MEMORY_PG_DSN=postgresql://u:p@localhost/d\n", &evidenceDB{}, TopologyLocal},
		{"unencoded password still classified", nil, "MEMORY_PG_DSN=postgresql://u:ab/cd@remote.example:5432/d\n", &evidenceDB{ok: true}, TopologyRemote},
		{"unusable DSN falls through to evidence", nil, "MEMORY_PG_DSN=$(pass x)\n", &evidenceDB{ok: true, why: "TCP 127.0.0.1:5432 and a socket"}, TopologyLocal},
		{"no DSN, local server evidence", nil, "", &evidenceDB{ok: true, why: "TCP 127.0.0.1:5432 and a socket"}, TopologyLocal},
		{"no DSN, forwarded port only (no evidence)", nil, "", &evidenceDB{ok: false}, TopologyRemote},
	}
	for _, c := range cases {
		h.env = c.env
		st := topoState(Inputs{}, "", c.envFile)
		got, why := TopologyDefault(ctx, topoPorts(h, c.db), st)
		if got != c.want || why == "" {
			t.Errorf("%s: %s (%s), want %s", c.name, got, why, c.want)
		}
	}
	// No DB port at all (nil) is "no evidence".
	if got, _ := TopologyDefault(ctx, h.rp(), topoState(Inputs{}, "", "")); got != TopologyRemote {
		t.Errorf("nil DB: %s", got)
	}
}

func TestTopologyDetect(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	s := TopologyStep{}
	rp := topoPorts(h, &evidenceDB{ok: true, why: "TCP 127.0.0.1:5432 and a socket"})
	ctx := context.Background()

	d := s.Detect(ctx, rp, topoState(Inputs{}, "", ""))
	if d.State != StateAbsent || !strings.Contains(d.Detail, "suggested: local") || len(d.Artifacts) != 1 || d.Artifacts[0].ID != "topology/topology" {
		t.Errorf("unset: %+v", d)
	}
	st := topoState(Inputs{}, "remote", "")
	st.Topology.Set(TopologyRemote, SourceManifest)
	if d := s.Detect(ctx, rp, st); d.State != StateOK || d.Detail != "remote" {
		t.Errorf("recorded: %+v", d)
	}
	st = topoState(Inputs{Topology: "remote"}, "local", "")
	st.Topology.Set(TopologyLocal, SourceManifest)
	if d := s.Detect(ctx, rp, st); d.State != StateOutdated || !strings.Contains(d.Detail, "replaces local") {
		t.Errorf("flag differs: %+v", d)
	}
	// Configure applied the flag: the post-Apply re-Detect is ok although the
	// manifest on disk is not written yet.
	st = topoState(Inputs{Topology: "remote"}, "local", "")
	st.Topology.Set(TopologyRemote, SourceFlag)
	if d := s.Detect(ctx, rp, st); d.State != StateOK {
		t.Errorf("flag applied: %+v", d)
	}
	// Set from the flag with no manifest yet: ok (the engine records it).
	st = topoState(Inputs{Topology: "local"}, "", "")
	st.Topology.Set(TopologyLocal, SourceFlag)
	if d := s.Detect(ctx, rp, st); d.State != StateOK {
		t.Errorf("flag, no manifest: %+v", d)
	}
	if !s.MissingInput(NewRunState(Inputs{})) || s.MissingInput(st) {
		t.Error("MissingInput")
	}
}

func TestTopologyConfigure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := TopologyStep{}
	local := &evidenceDB{ok: true, why: "TCP 127.0.0.1:5432 and a socket"}

	t.Run("yes takes the evidence default", func(t *testing.T) {
		h := newS23(t)
		st := topoState(Inputs{Yes: true}, "", "")
		if err := s.Configure(ctx, topoPorts(h, local), NewFakePrompter(t, false), st); err != nil {
			t.Fatal(err)
		}
		if st.Topology.Get() != TopologyLocal || st.Topology.Source() != SourceDefault {
			t.Errorf("%v", st.Topology)
		}
	})
	t.Run("yes without evidence is remote", func(t *testing.T) {
		h := newS23(t)
		st := topoState(Inputs{Yes: true}, "", "")
		if err := s.Configure(ctx, topoPorts(h, &evidenceDB{}), NewFakePrompter(t, false), st); err != nil || st.Topology.Get() != TopologyRemote {
			t.Errorf("%v %v", st.Topology, err)
		}
	})
	t.Run("yes keeps a recorded topology", func(t *testing.T) {
		h := newS23(t)
		st := topoState(Inputs{Yes: true}, "remote", "")
		st.Topology.Set(TopologyRemote, SourceManifest)
		if err := s.Configure(ctx, topoPorts(h, local), NewFakePrompter(t, false), st); err != nil || st.Topology.Get() != TopologyRemote || st.Topology.Source() != SourceManifest {
			t.Errorf("%v %v", st.Topology, err)
		}
	})
	t.Run("yes flag change applies without a prompt", func(t *testing.T) {
		h := newS23(t)
		st := topoState(Inputs{Yes: true, Topology: "remote"}, "local", "")
		st.Topology.Set(TopologyLocal, SourceManifest)
		if err := s.Configure(ctx, topoPorts(h, local), NewFakePrompter(t, false), st); err != nil {
			t.Fatal(err)
		}
		if st.Topology.Get() != TopologyRemote || st.Topology.Source() != SourceFlag {
			t.Errorf("%v", st.Topology)
		}
	})
	t.Run("interactive first run asks with the suggestion", func(t *testing.T) {
		h := newS23(t)
		st := topoState(Inputs{}, "", "")
		ui := NewFakePrompter(t, true).ExpectSelect("suggested: local", 1)
		if err := s.Configure(ctx, topoPorts(h, local), ui, st); err != nil {
			t.Fatal(err)
		}
		if st.Topology.Get() != TopologyRemote || st.Topology.Source() != SourcePrompt {
			t.Errorf("%v", st.Topology)
		}
	})
	t.Run("interactive re-run without --reconfigure asks nothing", func(t *testing.T) {
		h := newS23(t)
		st := topoState(Inputs{}, "local", "")
		st.Topology.Set(TopologyLocal, SourceManifest)
		if err := s.Configure(ctx, topoPorts(h, local), NewFakePrompter(t, true), st); err != nil || st.Topology.Get() != TopologyLocal {
			t.Errorf("%v %v", st.Topology, err)
		}
	})
	t.Run("reconfigure warns before the new value is set", func(t *testing.T) { // AC-26
		h := newS23(t)
		st := topoState(Inputs{Reconfigure: true}, "local", "")
		st.Topology.Set(TopologyLocal, SourceManifest)
		ui := NewFakePrompter(t, true).ExpectSelect("Where does Postgres run", 1).ExpectConfirm("records already stored stay in the old database", true)
		if err := s.Configure(ctx, topoPorts(h, local), ui, st); err != nil {
			t.Fatal(err)
		}
		calls := ui.Calls()
		if len(calls) != 2 || !strings.Contains(calls[1], "pg_dump") || !strings.Contains(calls[1], "from local to remote") {
			t.Errorf("transcript %q", calls)
		}
		if st.Topology.Get() != TopologyRemote || st.Topology.Source() != SourcePrompt {
			t.Errorf("%v", st.Topology)
		}
	})
	t.Run("a declined warning keeps the old topology", func(t *testing.T) {
		h := newS23(t)
		st := topoState(Inputs{Reconfigure: true}, "local", "")
		st.Topology.Set(TopologyLocal, SourceManifest)
		ui := NewFakePrompter(t, true).ExpectSelect("Where", 1).ExpectConfirm("stay in the old database", false)
		if err := s.Configure(ctx, topoPorts(h, local), ui, st); err != nil || st.Topology.Get() != TopologyLocal {
			t.Errorf("%v %v", st.Topology, err)
		}
	})
	t.Run("interactive flag change still warns", func(t *testing.T) {
		h := newS23(t)
		st := topoState(Inputs{Topology: "remote"}, "local", "")
		st.Topology.Set(TopologyLocal, SourceManifest)
		ui := NewFakePrompter(t, true).ExpectConfirm("stay in the old database", true)
		if err := s.Configure(ctx, topoPorts(h, local), ui, st); err != nil || st.Topology.Get() != TopologyRemote || st.Topology.Source() != SourceFlag {
			t.Errorf("%v %v", st.Topology, err)
		}
	})
	t.Run("prompt errors propagate", func(t *testing.T) {
		h := newS23(t)
		st := topoState(Inputs{}, "", "")
		ui := NewFakePrompter(t, true).ExpectSelectErr("Where", ErrTooManyAttempts)
		if err := s.Configure(ctx, topoPorts(h, local), ui, st); err != ErrTooManyAttempts || st.Topology.IsSet() {
			t.Errorf("%v %v", st.Topology, err)
		}
	})
}

func TestTopologyPlanWarnsOnChange(t *testing.T) { // AC-26 under --yes
	t.Parallel()
	h := newS23(t)
	s := TopologyStep{}
	st := topoState(Inputs{}, "local", "")
	st.Topology.Set(TopologyRemote, SourceFlag)
	p, err := s.Plan(context.Background(), h.rp(), st, Choices{"topology/topology": ChoiceApply})
	if err != nil || len(p.Actions) != 1 || len(p.Notes) != 1 || p.Notes[0].Level != NoteWarn || !strings.Contains(p.Notes[0].Text, "pg_dump") {
		t.Fatalf("%+v %v", p, err)
	}
	// No change, or not chosen: no warning, no action.
	st.Topology.Set(TopologyLocal, SourceManifest)
	if p, _ := s.Plan(context.Background(), h.rp(), st, Choices{"topology/topology": ChoiceApply}); len(p.Notes) != 0 {
		t.Errorf("unchanged: %+v", p)
	}
	if p, _ := s.Plan(context.Background(), h.rp(), st, Choices{"topology/topology": ChoiceKeep}); len(p.Actions) != 0 {
		t.Errorf("kept: %+v", p)
	}
}

func TestParseTopologyFlag(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]Topology{"": "", "local": TopologyLocal, "remote": TopologyRemote} {
		if got, err := ParseTopologyFlag(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"docker-local", "docker-remote", "A", "Local", "x"} {
		if _, err := ParseTopologyFlag(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// Engine run: a fresh --yes install records the topology in the manifest and
// the second run is a no-op (AC-19, AC-50); --topology docker-* exits 2.
func TestTopologyThroughTheEngine(t *testing.T) {
	t.Parallel()
	run := func(h *eh, in Inputs, rp func(*ReadPorts)) RunResult {
		p := ReadPorts{FS: h.fs, Runner: NewFakeRunner(h.t), Clock: h.clk, Paths: h.p, DB: &evidenceDB{ok: true, why: "TCP 127.0.0.1:5432 and a socket"}}
		if rp != nil {
			rp(&p)
		}
		e := &Engine{Steps: []Step{TopologyStep{}}, Read: p, Write: WritePorts{ReadPorts: p, FS: h.fs}, UI: h.ui, Reporter: h.rep, Version: "v1.0.0"}
		return e.Run(context.Background(), in)
	}
	h := newEH(t, false)
	if r := run(h, Inputs{Yes: true}, nil); r.ExitCode != ExitOK {
		t.Fatalf("run 1: %+v", r)
	}
	if m := h.manifest(); m.Topology != "local" {
		t.Fatalf("manifest topology %q", m.Topology)
	}
	before := len(h.nonLockWrites())
	if r := run(h, Inputs{Yes: true}, nil); r.ExitCode != ExitOK || len(h.nonLockWrites()) != before {
		t.Fatalf("run 2 not a no-op: %+v, writes %v", r, h.nonLockWrites())
	}
	// A flag change under --yes is applied and carries the AC-26 warning.
	r := run(h, Inputs{Yes: true, Topology: "remote"}, nil)
	if r.ExitCode != ExitOK || h.manifest().Topology != "remote" {
		t.Fatalf("run 3: %+v, manifest %q", r, h.manifest().Topology)
	}
	var warned bool
	last := h.rep.Plans[len(h.rep.Plans)-1]
	for _, sp := range last.Steps {
		for _, n := range sp.Plan.Notes {
			warned = warned || strings.Contains(n.Text, "stay in the old database")
		}
	}
	if !warned {
		t.Errorf("run 3 did not show the AC-26 warning: %+v", last)
	}
	h2 := newEH(t, false)
	if r := run(h2, Inputs{Yes: true, Topology: "docker-local"}, nil); r.ExitCode != ExitUsage || !strings.Contains(r.Err.Error(), "deferred") {
		t.Fatalf("docker: %+v", r)
	}
	if len(h2.nonLockWrites()) != 0 {
		t.Errorf("writes %v", h2.nonLockWrites())
	}
}
