package setup

import (
	"context"
	"fmt"
	"strings"
)

// EnvFileStepID is the id of the envfile step (AC-7: before database).
const EnvFileStepID = "envfile"

// EnvFileStep is the single writer of <ConfigDir>/env (Design 16, AC-28):
// plain KEY=VALUE, no quotes, no export, file 0600 in a 0700 directory. It
// owns every env-key artifact. Its desired keys come from RunState (the DSN
// from DB.DSN(), the Ollama keys, PRRepos): a field that is not set yields no
// artifact and no write, so that line stays byte-identical. Detect and Plan
// both read RunState, so a Configure result reaches the file through rule A.
type EnvFileStep struct {
	// Version is the running binary's version, recorded on its artifacts.
	Version string
}

var _ Step = EnvFileStep{}

// ID implements Step.
func (EnvFileStep) ID() string { return EnvFileStepID }

// Title implements Step.
func (EnvFileStep) Title() string { return "Env file" }

// Requires implements Step.
func (EnvFileStep) Requires() []string { return nil }

// Detect implements Step.
func (EnvFileStep) Detect(_ context.Context, rc ReadPorts, st *RunState) Detection {
	an, err := analyzeEnv(rc.FS, rc.Paths, st)
	if err != nil {
		return Detection{State: StateBlocked, Detail: err.Error()}
	}
	states := make([]State, 0, len(an.arts))
	var pending []string
	for _, a := range an.arts {
		states = append(states, a.State)
		if a.State != StateOK {
			pending = append(pending, strings.TrimPrefix(a.ID, "envfile/"))
		}
	}
	d := Detection{State: worstState(states...), Artifacts: an.arts, Notes: an.notes}
	switch {
	case len(pending) > 0:
		d.Detail = an.path + ": " + strings.Join(pending, ", ")
	case len(an.wants) == 0:
		d.Detail = an.path + ": no key is managed in this run"
	default:
		d.Detail = an.path + " is up to date"
	}
	return d
}

// Plan implements Step.
func (EnvFileStep) Plan(_ context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	an, err := analyzeEnv(rc.FS, rc.Paths, st)
	if err != nil {
		return Plan{}, err
	}
	res, err := an.envApply(ch)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	if res.mkdir {
		p.Actions = append(p.Actions, Action{Artifact: envDirArtifact, Verb: "write", Path: an.envDir(), Desc: "create directory (mode 0700)"})
	}
	if len(res.converted) > 0 {
		p.Actions = append(p.Actions, Action{Artifact: envFormatArtifact, Verb: "write", Path: an.path,
			Desc: "convert to plain KEY=VALUE: " + strings.Join(res.converted, ", ")})
	}
	for _, k := range res.setKeys {
		verb := "set"
		if an.state[envKeyArtifact(k)] == StateAbsent {
			verb = "add"
		}
		p.Actions = append(p.Actions, Action{Artifact: envKeyArtifact(k), Verb: "write", Path: an.path, Desc: verb + " " + k})
	}
	if res.chmod && !res.contentChg {
		p.Actions = append(p.Actions, Action{Artifact: envModeArtifact, Verb: "chmod", Path: an.path, Desc: "mode 0600 (content unchanged)"})
	}
	if res.contentChg {
		old := an.path
		if !an.exists {
			old = ""
		}
		p.Diffs = append(p.Diffs, Diff{Artifact: "envfile", Path: an.path, Unified: UnifiedDiff(old, an.path, maskUnparseableDSN(an.before), maskUnparseableDSN(res.after))})
	}
	return p, nil
}

// Apply implements Step: one atomic write (0600 in a 0700 directory), or a
// plain chmod when only the mode is wrong (AC-29). It re-reads the file, so
// the write is based on what is on disk now. Overwriting a `modified`
// artifact first keeps a 0600 backup beside the file.
func (e EnvFileStep) Apply(_ context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	an, err := analyzeEnv(wc.ReadPorts.FS, wc.Paths, st)
	if err != nil {
		return res, err
	}
	ch := Choices{}
	for _, a := range p.Actions {
		ch[a.Artifact] = ChoiceApply
	}
	r, err := an.envApply(ch)
	if err != nil {
		return res, err
	}
	if r.mkdir || r.contentChg {
		if err := wc.FS.MkdirAll(an.envDir(), 0o700); err != nil {
			return res, fmt.Errorf("create %s: %w", an.envDir(), err)
		}
		if r.mkdir {
			res.Artifacts = append(res.Artifacts, Artifact{Step: EnvFileStepID, Kind: KindDir, Path: an.envDir(), Version: e.Version})
		}
		if !an.dirMissing {
			// An existing directory keeps the mode it has: tighten it.
			n, changed, err := tightenDir(wc.FS, an.envDir())
			if err != nil {
				return res, err
			}
			if changed {
				res.Notes = append(res.Notes, n)
			}
		}
	}
	if r.contentChg {
		if r.modified && an.exists {
			bak := an.path + envBackupSuffix + wc.Clock.Now().UTC().Format("20060102T150405Z")
			if err := wc.FS.WriteFileAtomic(bak, an.before, 0o600); err != nil {
				return res, fmt.Errorf("back up %s: %w", an.path, err)
			}
			res.Notes = append(res.Notes, Note{NoteInfo, "backup of the previous env file: " + bak})
		}
		if err := wc.FS.WriteFileAtomic(an.path, r.after, 0o600); err != nil {
			return res, fmt.Errorf("write %s: %w", an.path, err)
		}
		old := an.path
		if !an.exists {
			old = ""
		}
		res.Diffs = append(res.Diffs, Diff{Artifact: "envfile", Path: an.path, Unified: UnifiedDiff(old, an.path, maskUnparseableDSN(an.before), maskUnparseableDSN(r.after))})
	} else if r.chmod {
		if err := wc.FS.Chmod(an.path, 0o600); err != nil {
			return res, fmt.Errorf("chmod %s: %w", an.path, err)
		}
	}
	for _, k := range r.setKeys {
		res.Artifacts = append(res.Artifacts, Artifact{Step: EnvFileStepID, Kind: KindEnvKey, Path: an.path, Identity: k, Version: e.Version})
	}
	return res, nil
}
