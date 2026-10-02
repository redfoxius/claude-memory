package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// JobsStepID is the id of the jobs step (AC-7: after claude-md, before the
// final doctor).
const JobsStepID = "jobs"

func jobArtifactID(name string) string { return JobsStepID + "/" + name }

// JobsStep installs the two scheduled jobs on macOS (AC-43, AC-44): `cleanup`
// (daily 07:15) and `ingest-pr` (daily 07:00), as launchd plists rendered from
// the embedded template. One artifact per job, "jobs/<name>". The jobs run the
// installed binary directly (it loads the env file itself) with an explicit
// PATH holding the directories of claude, git and az.
//
// ingest-pr is installed only when RunState.PRRepos is set (--pr-repos, the
// env file or the shell); the value reaches the env file through the envfile
// step only (Design 16), this step never writes it.
//
// States follow LaunchdJobs.Inspect: ok (equal to the rendering and loaded),
// outdated (recorded and unedited but different, the legacy run-with-env.sh
// wrapper, not loaded: replaced and reloaded without a Confirm), modified (an
// edited or hand-written different plist: kept unless the overwrite is
// confirmed, then backed up beside the file).
//
// Only launchd exists in this build: --no-jobs, --jobs-backend none and any
// platform without launchd skip the step with printed instructions. There is
// no cron fallback.
type JobsStep struct {
	// Version is the running binary's version, recorded on its artifacts.
	Version string
}

var (
	_ Step    = JobsStep{}
	_ Seeder  = JobsStep{}
	_ Adopter = JobsStep{}
)

// ID implements Step.
func (JobsStep) ID() string { return JobsStepID }

// Title implements Step.
func (JobsStep) Title() string { return "Scheduled jobs" }

// Requires implements Step.
func (JobsStep) Requires() []string { return nil }

// ErrJobChanged is returned by Apply when a plist differs from what Plan saw.
var ErrJobChanged = errors.New("job plist changed during install, re-run")

// ValidatePRRepos checks a --pr-repos value: comma-separated absolute paths.
func ValidatePRRepos(v string) error {
	n := 0
	for _, r := range strings.Split(v, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		n++
		if !filepath.IsAbs(r) || strings.ContainsAny(r, "\r\n") {
			return errors.New("want comma-separated absolute repository paths")
		}
	}
	if n == 0 {
		return errors.New("no repository path given")
	}
	return nil
}

// Seed sets PRRepos (flag, env file, then Env; unset when none has a value, so
// the env file stays untouched and ingest-pr is not installed) and JobPATH
// (the tool directories found now).
func (JobsStep) Seed(_ context.Context, rc ReadPorts, st *RunState) ([]Note, error) {
	if v := st.Inputs.PRRepos; v != "" {
		if err := ValidatePRRepos(v); err != nil {
			return nil, fmt.Errorf("--pr-repos: %w", err)
		}
	}
	seed := SeedEnvValue(st, rc.Env, EnvKeyPRRepos, st.Inputs.PRRepos, nil)
	var notes []Note
	if seed.Found && !seed.Unparseable && strings.TrimSpace(seed.Value) != "" {
		st.PRRepos.Set(seed.Value, seed.Source)
		notes = seed.Notes
	}
	st.JobPATH.Set(ComputeJobPATH(rc.Runner), SourceDefault)
	return notes, nil
}

const jobsManualHint = "schedule `claude-memory cleanup` (daily 07:15) and `claude-memory ingest-pr` (daily 07:00) yourself; see integration/INSTALL.md step 8"

// jobsUnavailable returns why the jobs cannot be installed here, "" when the
// backend is launchd.
func jobsUnavailable(in Inputs, p PlatformInfo) string {
	switch {
	case in.JobsBackend == JobsNone:
		return "--jobs-backend none: " + jobsManualHint
	case p.OS != OSDarwin || p.JobsBackend != JobsLaunchd:
		return fmt.Sprintf("this build installs jobs with launchd on macOS only (platform %s/%s, jobs backend %s): %s",
			p.OS, p.Arch, orDash(p.JobsBackend), jobsManualHint)
	}
	return ""
}

// jobSpecs are the jobs this run manages.
func jobSpecs(rc ReadPorts, st *RunState) []JobSpec {
	all := DefaultJobSpecs(rc.Paths, binTarget(rc, st), st.JobPATH.Get()) // Seed always sets JobPATH
	if st.PRRepos.IsSet() && strings.TrimSpace(st.PRRepos.Get()) != "" {
		return all
	}
	return slices.DeleteFunc(all, func(j JobSpec) bool { return j.Name == JobIngestPR })
}

type jobView struct {
	spec     JobSpec
	id       string
	path     string
	rendered []byte
	existing []byte
	exists   bool
	state    State
	detail   string
}

func analyzeJobs(ctx context.Context, rc ReadPorts, st *RunState) ([]jobView, error) {
	if rc.Jobs == nil {
		return nil, errors.New("no job adapter is configured")
	}
	recorded := recordedJobHashes(st.Prior.Manifest)
	var out []jobView
	for _, j := range jobSpecs(rc, st) {
		files, err := rc.Jobs.Render(j)
		if err != nil {
			return nil, err
		}
		v := jobView{spec: j, id: jobArtifactID(j.Name)}
		for p, b := range files {
			v.path, v.rendered = p, b
		}
		if b, err := rc.FS.ReadFile(v.path); err == nil {
			v.exists, v.existing = true, b
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", v.path, err)
		}
		if v.state, v.detail, err = rc.Jobs.Detect(ctx, j, recorded); err != nil {
			return nil, fmt.Errorf("job %s: %w", j.Name, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// Detect implements Step.
func (JobsStep) Detect(ctx context.Context, rc ReadPorts, st *RunState) Detection {
	if st.Inputs.NoJobs {
		return Detection{State: StateOK, Detail: "disabled by --no-jobs", SkipReason: "--no-jobs"}
	}
	if why := jobsUnavailable(st.Inputs, rc.Platform); why != "" {
		return Detection{State: StateOK, Detail: "skipped: " + why, SkipReason: why,
			Notes: []Note{{NoteInfo, "jobs not installed: " + why}}}
	}
	views, err := analyzeJobs(ctx, rc, st)
	if err != nil {
		return Detection{State: StateBlocked, Detail: err.Error()}
	}
	var arts []ArtifactState
	var states []State
	var pending []string
	var names []string
	for _, v := range views {
		arts = append(arts, ArtifactState{ID: v.id, State: v.state, Detail: v.detail})
		states = append(states, v.state)
		names = append(names, v.spec.Name)
		if v.state != StateOK {
			pending = append(pending, v.spec.Name+": "+v.detail)
		}
	}
	d := Detection{State: worstState(states...), Artifacts: arts}
	if len(pending) > 0 {
		d.Detail = strings.Join(pending, "; ")
	} else {
		d.Detail = strings.Join(names, ", ") + " installed and loaded"
	}
	if len(views) == 2 {
		d.Notes = append(d.Notes, Note{NoteInfo, "ingest-pr supports Azure DevOps repositories only"})
	} else {
		d.Notes = append(d.Notes, Note{NoteInfo, "ingest-pr is not installed: pass --pr-repos or set MEMORY_PR_INGEST_REPOS to enable it"})
		if b, err := rc.FS.ReadFile(filepath.Join(rc.Paths.LaunchAgentsDir, LaunchdLabelPrefix+JobIngestPR+".plist")); err == nil &&
			bytes.Contains(b, []byte(legacyWrapperSuffix)) {
			d.Notes = append(d.Notes, Note{NoteWarn, "legacy ingest-pr plist left untouched; pass --pr-repos to manage it"})
		}
	}
	return d
}

func jobToken(v jobView) string {
	if !v.exists {
		return v.id + "=absent"
	}
	return v.id + "=" + sha256Hex(v.existing)
}

// Plan implements Step.
func (JobsStep) Plan(ctx context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	views, err := analyzeJobs(ctx, rc, st)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	var tokens []string
	for _, v := range views {
		if ch[v.id] != ChoiceApply || v.state == StateOK {
			continue
		}
		tokens = append(tokens, jobToken(v))
		desc := "create"
		switch v.state {
		case StateOutdated:
			desc = "replace (" + v.detail + ")"
		case StateModified:
			desc = "replace your modified plist (a timestamped backup is kept beside it)"
		}
		p.Actions = append(p.Actions,
			Action{Artifact: v.id, Verb: "write", Path: v.path, Desc: desc},
			Action{Artifact: v.id, Verb: "load", Path: v.path, Desc: fmt.Sprintf("launchctl bootout, then bootstrap gui/%d/%s", rc.Paths.UID, v.spec.Label)})
		old := v.path
		if !v.exists {
			old = ""
		}
		p.Diffs = append(p.Diffs, Diff{Artifact: v.id, Path: v.path, Unified: UnifiedDiff(old, v.path, v.existing, v.rendered)})
	}
	p.Token = strings.Join(tokens, ";")
	return p, nil
}

func (s JobsStep) artifact(v jobView) Artifact {
	return Artifact{Step: JobsStepID, Kind: KindLaunchd, Path: v.path, Identity: v.spec.Label,
		SHA256: sha256Hex(v.rendered), Version: s.Version}
}

// Apply implements Step: it re-reads every chosen plist, aborts when one is
// not what Plan saw (Token), backs up a modified one and installs (write,
// bootout, bootstrap) each, recording the rendered hash.
func (s JobsStep) Apply(ctx context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	if len(p.Actions) == 0 {
		return res, nil
	}
	if wc.Jobs == nil {
		return res, errors.New("no job manager is configured")
	}
	chosen := map[string]bool{}
	for _, a := range p.Actions {
		chosen[a.Artifact] = true
	}
	views, err := analyzeJobs(ctx, wc.ReadPorts, st)
	if err != nil {
		return res, err
	}
	tokens := strings.Split(p.Token, ";")
	for _, v := range views {
		if chosen[v.id] && v.state != StateOK && !slices.Contains(tokens, jobToken(v)) {
			return res, fmt.Errorf("%s: %w", v.path, ErrJobChanged)
		}
	}
	recorded := recordedJobHashes(st.Prior.Manifest)
	for _, v := range views {
		if !chosen[v.id] {
			continue
		}
		if v.state != StateOK {
			// Anything this step did not write itself (unrecorded, legacy,
			// edited) is backed up first, also when it is only outdated:
			// a hand-set schedule must not be lost silently.
			if v.exists && recorded[v.path] != sha256Hex(v.existing) && !bytes.Equal(v.existing, v.rendered) {
				bak := UniqueBackupPath(wc.FS, SettingsBackupPath(v.path, wc.Clock))
				if err := wc.FS.WriteFileAtomic(bak, v.existing, BackupFileMode); err != nil {
					return res, fmt.Errorf("back up %s: %w", v.path, err)
				}
				res.Notes = append(res.Notes, Note{NoteInfo, "backup of your existing " + v.spec.Name + " plist: " + bak})
			}
			if err := wc.Jobs.Install(ctx, v.spec); err != nil {
				return res, fmt.Errorf("install job %s: %w", v.spec.Name, err)
			}
			if v.exists {
				res.Diffs = append(res.Diffs, Diff{Artifact: v.id, Path: v.path, Unified: UnifiedDiff(v.path, v.path, v.existing, v.rendered)})
			}
		}
		res.Artifacts = append(res.Artifacts, s.artifact(v))
	}
	return res, nil
}

// Adopt implements Adopter: a plist equal to the rendering and loaded (a hand
// install that matches, AC-51) is recorded.
func (s JobsStep) Adopt(ctx context.Context, rc ReadPorts, st *RunState) []Artifact {
	if st.Inputs.NoJobs || jobsUnavailable(st.Inputs, rc.Platform) != "" {
		return nil
	}
	views, err := analyzeJobs(ctx, rc, st)
	if err != nil {
		return nil
	}
	var out []Artifact
	for _, v := range views {
		if v.state == StateOK {
			out = append(out, s.artifact(v))
		}
	}
	return out
}
