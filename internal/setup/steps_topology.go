package setup

import (
	"context"
	"fmt"
	"strings"

	"github.com/redfoxius/claude-memory/internal/config"
)

// TopologyStepID is the id of the topology step (AC-7).
const TopologyStepID = "topology"

// topologyArtifact is the step's one artifact id.
const topologyArtifact = "topology/topology"

// ParseTopologyFlag validates --topology. "" means "not given". The docker
// topologies are slice 3 and refused with the word "deferred" (exit 2).
func ParseTopologyFlag(v string) (Topology, error) {
	switch v {
	case "":
		return "", nil
	case string(TopologyLocal):
		return TopologyLocal, nil
	case string(TopologyRemote):
		return TopologyRemote, nil
	}
	if strings.HasPrefix(v, "docker-") {
		return "", fmt.Errorf("--topology %s is deferred (slice 3); use local or remote", v)
	}
	return "", fmt.Errorf("--topology %q: want local or remote", v)
}

// TopologyStep owns RunState.Topology (AC-19, Design 16, 22). The choice is
// stored in the manifest header by the engine (written whenever something is
// applied), so the step has no artifact of its own to write: Apply is a
// no-op that marks the field recorded, and Detect is ok once the topology is
// the recorded one.
type TopologyStep struct{}

var (
	_ Step            = TopologyStep{}
	_ Seeder          = TopologyStep{}
	_ Configurer      = TopologyStep{}
	_ MissingInputter = TopologyStep{}
)

// ID implements Step.
func (TopologyStep) ID() string { return TopologyStepID }

// Title implements Step.
func (TopologyStep) Title() string { return "Topology" }

// Requires implements Step.
func (TopologyStep) Requires() []string { return nil }

func recordedTopology(st *RunState) Topology {
	if st.Prior.Manifest == nil {
		return ""
	}
	return Topology(st.Prior.Manifest.Topology)
}

// Seed sets Topology from the manifest, else from --topology; a flag that
// differs from the manifest is not applied here but in Configure, after the
// AC-26 warning (Design 15, 22). It rejects an invalid or deferred
// --topology and a --pg-dsn that carries a password (AC-31).
func (TopologyStep) Seed(_ context.Context, _ ReadPorts, st *RunState) ([]Note, error) {
	flag, err := ParseTopologyFlag(st.Inputs.Topology)
	if err != nil {
		return nil, err
	}
	if st.Inputs.PGDSN != "" {
		if err := CheckPGDSNFlag(st.Inputs.PGDSN); err != nil {
			return nil, fmt.Errorf("--pg-dsn: %w", err)
		}
	}
	switch rec := recordedTopology(st); {
	case rec == TopologyLocal || rec == TopologyRemote:
		st.Topology.Set(rec, SourceManifest)
	case flag != "":
		st.Topology.Set(flag, SourceFlag)
	}
	return nil, nil
}

// existingDSNHost returns the host of the DSN the install would adopt (env
// file, then Env: Design 16 order), for the AC-19 default.
func existingDSNHost(rc ReadPorts, st *RunState) (string, bool) {
	var raw string
	if st.Prior.EnvDoc != nil {
		if v, ok := config.ParseEnvDoc(st.Prior.EnvDoc).Get("MEMORY_PG_DSN"); ok {
			raw = v
		}
	}
	if raw == "" {
		raw = rc.Env.Get("MEMORY_PG_DSN")
	}
	if raw == "" {
		return "", false
	}
	if t, err := ParseDBTarget(raw); err == nil {
		return t.Host, true
	}
	if t, ok := RecoverDSN(raw); ok {
		return t.Host, true
	}
	return "", false
}

// TopologyDefault is the AC-19 default and why: an existing DSN decides by
// its host's class (loopback or socket: local, else remote); otherwise local
// only when 127.0.0.1:5432 accepts TCP and a local socket or postgres
// process exists (a forwarded port alone is not evidence); otherwise remote.
func TopologyDefault(ctx context.Context, rc ReadPorts, st *RunState) (Topology, string) {
	if h, ok := existingDSNHost(rc, st); ok {
		if HostIsLocal(h) {
			return TopologyLocal, "the existing DSN points at this machine"
		}
		return TopologyRemote, "the existing DSN points at " + h
	}
	if rc.DB != nil {
		if ok, why := rc.DB.LocalServerEvidence(ctx); ok {
			return TopologyLocal, "local Postgres found: " + why
		}
	}
	return TopologyRemote, "no local Postgres server found"
}

// Detect implements Step. A topology decided in this run (Source not
// manifest: a flag, the prompt or the AC-19 default) that the manifest does
// not hold yet is outdated, so the step is applied and the engine records the
// header; Apply then marks the field recorded (Source=manifest), which is why
// the post-Apply re-Detect, taken before the manifest is written, is ok.
func (TopologyStep) Detect(ctx context.Context, rc ReadPorts, st *RunState) Detection {
	flag, _ := ParseTopologyFlag(st.Inputs.Topology) // Seed already refused a bad value
	a := func(state State, detail string) Detection {
		return Detection{State: state, Detail: detail, Artifacts: []ArtifactState{{ID: topologyArtifact, State: state, Detail: detail}}}
	}
	if !st.Topology.IsSet() {
		def, why := TopologyDefault(ctx, rc, st)
		return a(StateAbsent, fmt.Sprintf("not chosen yet (suggested: %s, %s)", def, why))
	}
	cur := st.Topology.Get()
	// A change requested by --topology is applied by Configure; once it has
	// set the value, flag == cur.
	if flag != "" && flag != cur {
		return a(StateOutdated, fmt.Sprintf("--topology %s replaces %s", flag, cur))
	}
	if rec := recordedTopology(st); st.Topology.Source() != SourceManifest && rec != cur {
		if rec == "" {
			return a(StateOutdated, "record topology "+string(cur))
		}
		return a(StateOutdated, fmt.Sprintf("topology changes from %s to %s", rec, cur))
	}
	return a(StateOK, string(cur))
}

// MissingInput implements MissingInputter.
func (TopologyStep) MissingInput(st *RunState) bool { return !st.Topology.IsSet() }

// topologyOptions are the Select answers, in the order of topologyChoices.
var (
	topologyOptions = []string{
		"local  - Postgres and pgvector on this machine",
		"remote - Postgres elsewhere (for example over Tailscale)",
	}
	topologyChoices = []Topology{TopologyLocal, TopologyRemote}
)

// Configure asks for the topology (AC-19), or takes --topology / the AC-19
// default without a prompt. On a change from the recorded topology it shows
// the AC-26 warning and asks before the new value reaches RunState; under
// --yes the same warning is part of the plan (Plan).
func (TopologyStep) Configure(ctx context.Context, rc ReadPorts, ui Prompter, st *RunState) error {
	flag, _ := ParseTopologyFlag(st.Inputs.Topology)
	cur := st.Topology.Get()
	set := st.Topology.IsSet()
	def, why := TopologyDefault(ctx, rc, st)

	target, src := cur, st.Topology.Source()
	switch {
	case flag != "":
		target, src = flag, SourceFlag
	case !ui.Interactive():
		if set {
			return nil
		}
		target, src = def, SourceDefault
	case set && !st.Inputs.Reconfigure:
		return nil
	default:
		suggest := def
		if set {
			suggest = cur
		}
		idx := 0
		for i, c := range topologyChoices {
			if c == suggest {
				idx = i
			}
		}
		q := "Where does Postgres run?"
		if !set {
			q += " (suggested: " + string(def) + ", " + why + ")"
		}
		i, err := ui.Select(q, topologyOptions, idx)
		if err != nil {
			return err
		}
		target, src = topologyChoices[i], SourcePrompt
	}

	if rec := recordedTopology(st); rec != "" && target != rec && ui.Interactive() {
		w := warnDataStays("topology", string(rec), string(target))
		ok, err := ui.Confirm(w.Text+" Continue?", true)
		if err != nil {
			return err
		}
		if !ok {
			return nil // keep what is set
		}
	}
	if !set || target != cur {
		st.Topology.Set(target, src)
	}
	return nil
}

// Plan implements Step: the choice is recorded in the manifest header by the
// engine; a change from the recorded topology carries the AC-26 warning.
func (TopologyStep) Plan(_ context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	var p Plan
	if ch[topologyArtifact] != ChoiceApply || !st.Topology.IsSet() {
		return p, nil
	}
	cur := st.Topology.Get()
	p.Actions = append(p.Actions, Action{Artifact: topologyArtifact, Verb: "write", Path: rc.Paths.Manifest(),
		Desc: "record topology " + string(cur)})
	if rec := recordedTopology(st); rec != "" && rec != cur {
		p.Notes = append(p.Notes, warnDataStays("topology", string(rec), string(cur)))
	}
	return p, nil
}

// Apply implements Step: nothing to write; the engine records the topology in
// the manifest header after this step succeeds. The step marks its own field
// recorded (Source=manifest) so the success rule's re-Detect, which runs
// before that write, finds it ok.
//
// The flip is deliberate and has one consequence: after Apply the field's
// Source is "manifest" even though this run decided the value (flag, prompt
// or default). Nothing may use Source to ask "was this chosen in this run?"
// after the Apply phase; Detect and Plan of this step only compare against
// the manifest, which by then agrees.
func (TopologyStep) Apply(_ context.Context, _ WritePorts, st *RunState, _ Plan) (StepResult, error) {
	if st.Topology.IsSet() {
		st.Topology.Set(st.Topology.Get(), SourceManifest)
	}
	return StepResult{}, nil
}
