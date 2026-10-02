package setup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// OllamaStepID is the id of the ollama step (AC-7: after migrate).
const OllamaStepID = "ollama"

// OllamaModelArtifact is the step's one artifact id.
const OllamaModelArtifact = "ollama/model"

// OllamaStep owns RunState.Ollama (Design 16, 22): URL, model and the
// optional embed token cap. Seed order: flag, env file, Env, else the AC-32
// defaults with Source=default, so Detect can probe on a fresh --yes run and
// envfile writes both keys. MEMORY_EMBED_MAX_TOKENS is owned here but only
// ever comes from the env file or Env: it has no default and is never
// prompted. Nothing is recorded in the manifest (a pulled model is not ours
// to remove).
type OllamaStep struct {
	// Redactor, when set, masks secrets in error text from the prober.
	Redactor *Redactor
}

var (
	_ Step            = OllamaStep{}
	_ Seeder          = OllamaStep{}
	_ Configurer      = OllamaStep{}
	_ MissingInputter = OllamaStep{}
)

// ID implements Step.
func (OllamaStep) ID() string { return OllamaStepID }

// Title implements Step.
func (OllamaStep) Title() string { return "Ollama" }

// Requires implements Step.
func (OllamaStep) Requires() []string { return nil }

func (o OllamaStep) redact(s string) string {
	if o.Redactor == nil {
		return s
	}
	return o.Redactor.Redact(s)
}

// ValidateOllamaURL accepts an http(s) URL with a host and no credentials (a
// password in the URL would reach Details, notes and the env file's diff).
func ValidateOllamaURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || raw == "":
		return errors.New("Ollama URL: want http://host:port")
	case u.Scheme != "http" && u.Scheme != "https":
		return errors.New("Ollama URL: want an http:// or https:// URL")
	case u.Hostname() == "":
		return errors.New("Ollama URL: no host")
	case u.User != nil:
		return errors.New("Ollama URL: credentials in the URL are not supported")
	}
	return nil
}

func sameURL(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// Seed resolves URL, model and the token cap. An invalid --ollama-url is an
// error (exit 2); an invalid env-file/Env URL is dropped with a note and the
// step then needs input.
func (OllamaStep) Seed(_ context.Context, rc ReadPorts, st *RunState) ([]Note, error) {
	in := st.Inputs
	if in.OllamaURL != "" {
		if err := ValidateOllamaURL(in.OllamaURL); err != nil {
			return nil, fmt.Errorf("--ollama-url: %w", err)
		}
	}
	var notes []Note
	var tgt OllamaTarget
	srcs := []Source{}

	u := SeedEnvValue(st, rc.Env, EnvKeyOllamaURL, in.OllamaURL, sameURL)
	notes = append(notes, u.Notes...)
	switch {
	case !u.Found:
		tgt.URL = DefaultOllamaURL
		srcs = append(srcs, SourceDefault)
	case ValidateOllamaURL(u.Value) != nil:
		notes = append(notes, Note{NoteWarn, EnvKeyOllamaURL + " is not a usable http(s) URL; install needs the Ollama URL (pass --ollama-url)"})
		return notes, nil // unset: Configure asks, --yes fails with the flag name
	default:
		tgt.URL = u.Value
		srcs = append(srcs, u.Source)
	}

	m := SeedEnvValue(st, rc.Env, EnvKeyModel, in.OllamaModel, nil)
	notes = append(notes, m.Notes...)
	if m.Found {
		tgt.Model = m.Value
		srcs = append(srcs, m.Source)
	} else {
		tgt.Model = DefaultOllamaModel
		srcs = append(srcs, SourceDefault)
	}

	if mt := SeedEnvValue(st, rc.Env, EnvKeyMaxTokens, "", nil); mt.Found {
		tgt.EmbedMaxTokens = mt.Value
		notes = append(notes, mt.Notes...)
	}
	st.Ollama.Set(tgt, strongestSource(srcs))
	return notes, nil
}

// strongestSource is the most deliberate of the sources: flag, env file, Env,
// then default.
func strongestSource(srcs []Source) Source {
	for _, want := range []Source{SourceFlag, SourceEnvFile, SourceEnv} {
		for _, s := range srcs {
			if s == want {
				return want
			}
		}
	}
	return SourceDefault
}

// MissingInput implements MissingInputter.
func (OllamaStep) MissingInput(st *RunState) bool { return !st.Ollama.IsSet() }

// remoteWarning is the AC-33 text for a non-loopback Ollama URL.
func remoteWarning(u string) string {
	return fmt.Sprintf("Ollama at %s is not on this machine: prompts are sent over the network for embedding. "+
		"The prompt hook gives up after MEMORY_HOOK_TIMEOUT (800 ms by default), and a slow remote Ollama - "+
		"e.g. on CPU, where DEPLOY.md measured 0.23 s for 15 tokens and 2.3 s for 150 - makes the hook time out "+
		"and inject nothing at all; memory_search via MCP still works. Doctor's ollama.embed warning (> 500 ms) is the watchdog.", u)
}

// ollamaRemedy is what to do about an Ollama that does not answer (printed,
// never run).
func ollamaRemedy(rc ReadPorts, u string) string {
	if isLoopbackURL(u) {
		return Hint(rc.Platform, CompOllama) + "; start it (macOS: `brew services start ollama`; Linux: `systemctl start ollama` or `ollama serve`), or pass --ollama-url"
	}
	return "check the Ollama URL and that the remote server is reachable from here"
}

// Detect implements Step: version, tags, then one embed that must return 1024
// dimensions (the schema is VECTOR(1024)).
func (o OllamaStep) Detect(ctx context.Context, rc ReadPorts, st *RunState) Detection {
	if !st.Ollama.IsSet() {
		return Detection{State: StateAbsent, Detail: "no Ollama URL chosen yet",
			Artifacts: []ArtifactState{{ID: OllamaModelArtifact, State: StateAbsent, Detail: "no Ollama URL chosen yet"}}}
	}
	t := st.Ollama.Get()
	var notes []Note
	if !isLoopbackURL(t.URL) {
		notes = append(notes, Note{NoteWarn, remoteWarning(t.URL)})
	}
	blocked := func(detail, remedy string) Detection {
		return Detection{State: StateBlocked, Detail: detail, Remedy: remedy, Notes: notes}
	}
	if rc.Ollama == nil {
		return blocked("no Ollama prober is configured", "")
	}
	if _, err := rc.Ollama.Version(ctx, t.URL); err != nil {
		return blocked("no Ollama at "+t.URL+": "+o.redact(err.Error()), ollamaRemedy(rc, t.URL))
	}
	has, err := rc.Ollama.HasModel(ctx, t.URL, t.Model)
	if err != nil {
		return blocked("cannot list the models at "+t.URL+": "+o.redact(err.Error()), "check the Ollama server logs")
	}
	art := func(s State, detail string) Detection {
		return Detection{State: s, Detail: detail, Notes: notes, Artifacts: []ArtifactState{{ID: OllamaModelArtifact, State: s, Detail: detail}}}
	}
	if !has {
		if t.SkipPull {
			notes = append(notes, Note{NoteWarn, fmt.Sprintf("model %s is not pulled (you declined): embeddings fail until `ollama pull %s` runs on %s", t.Model, t.Model, t.URL)})
			return art(StateOK, "model "+t.Model+" not pulled (declined)")
		}
		return art(StateAbsent, "model "+t.Model+" is not pulled at "+t.URL)
	}
	dims, lat, err := rc.Ollama.EmbedDims(ctx, t.URL, t.Model)
	switch {
	case err != nil:
		return blocked("embedding with "+t.Model+" failed: "+o.redact(err.Error()), "check the Ollama server logs; `ollama run "+t.Model+"` reports load errors")
	case dims != SchemaEmbeddingDims:
		return blocked(fmt.Sprintf("model %s returns %d dims, schema needs %d", t.Model, dims, SchemaEmbeddingDims),
			"use a 1024-dimension model (bge-m3): pass --ollama-model")
	}
	return art(StateOK, fmt.Sprintf("%s at %s, %d dims in %s", t.Model, t.URL, dims, roundDur(lat)))
}

// Plan implements Step.
func (OllamaStep) Plan(_ context.Context, _ ReadPorts, st *RunState, ch Choices) (Plan, error) {
	var p Plan
	if ch[OllamaModelArtifact] != ChoiceApply || !st.Ollama.IsSet() {
		return p, nil
	}
	t := st.Ollama.Get()
	p.Actions = append(p.Actions, Action{Artifact: OllamaModelArtifact, Verb: "pull", Path: t.URL,
		Desc: "pull model " + t.Model + " (POST /api/pull, then verify 1024 dimensions)"})
	if !isLoopbackURL(t.URL) {
		p.Notes = append(p.Notes, Note{NoteWarn, remoteWarning(t.URL)})
	}
	return p, nil
}

// Apply implements Step: pull with live progress to WritePorts.Progress, then
// the 1024-dim verify.
func (o OllamaStep) Apply(ctx context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	if len(p.Actions) == 0 {
		return res, nil
	}
	if !st.Ollama.IsSet() {
		return res, errors.New("no Ollama URL chosen")
	}
	if wc.Ollama == nil {
		return res, errors.New("no Ollama prober is configured")
	}
	t := st.Ollama.Get()
	label := "pull " + t.Model
	progress := func(done, total int64) {
		if wc.Progress != nil {
			wc.Progress(OllamaStepID, label, done, total)
		}
	}
	if err := wc.Ollama.Pull(ctx, t.URL, t.Model, progress); err != nil {
		return res, fmt.Errorf("pull %s: %s", t.Model, o.redact(err.Error()))
	}
	dims, _, err := wc.Ollama.EmbedDims(ctx, t.URL, t.Model)
	switch {
	case err != nil:
		return res, fmt.Errorf("embedding with %s failed: %s", t.Model, o.redact(err.Error()))
	case dims != SchemaEmbeddingDims:
		return res, fmt.Errorf("model %s returns %d dims, schema needs %d", t.Model, dims, SchemaEmbeddingDims)
	}
	return res, nil
}

// Configure resolves the URL and asks the questions (AC-32, AC-33): under
// --reconfigure the URL and model (re-asked on a declined remote warning),
// the remote-URL confirmation, and the pull y/N. --yes asks nothing: the
// remote warning is printed through Detect and Plan notes and the URL
// accepted (AC-33).
func (o OllamaStep) Configure(ctx context.Context, rc ReadPorts, ui Prompter, st *RunState) error {
	if !ui.Interactive() {
		if !st.Ollama.IsSet() {
			return errors.New("no usable Ollama URL: pass --ollama-url")
		}
		return nil
	}
	cur, set := st.Ollama.Get(), st.Ollama.IsSet()
	t := cur
	changed := false
	if !set || st.Inputs.Reconfigure {
		def := firstNonEmpty(cur.URL, DefaultOllamaURL)
		for attempt := 0; ; attempt++ {
			if attempt >= MaxPromptAttempts {
				return ErrTooManyAttempts
			}
			u, err := ui.Text("Ollama URL", def, ValidateOllamaURL)
			if err != nil {
				return err
			}
			if !isLoopbackURL(u) {
				ok, err := ui.Confirm(remoteWarning(u)+" Use it anyway?", false)
				if err != nil {
					return err
				}
				if !ok {
					def = DefaultOllamaURL
					continue
				}
			}
			t.URL = u
			break
		}
		m, err := ui.Text("Embedding model (must return 1024 dimensions)", firstNonEmpty(cur.Model, DefaultOllamaModel), nonBlank("model"))
		if err != nil {
			return err
		}
		t.Model = m
		changed = true
	} else if !isLoopbackURL(t.URL) {
		ok, err := ui.Confirm(remoteWarning(t.URL)+" Use it anyway?", false)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("remote Ollama %s was not confirmed: pass --ollama-url for a local one", t.URL)
		}
	}
	t.SkipPull = false
	if rc.Ollama != nil {
		if _, verr := rc.Ollama.Version(ctx, t.URL); verr == nil {
			if has, herr := rc.Ollama.HasModel(ctx, t.URL, t.Model); herr == nil && !has {
				pull, err := ui.Confirm(fmt.Sprintf("Model %s is not on %s. Pull it now (it can be over a gigabyte)?", t.Model, t.URL), true)
				if err != nil {
					return err
				}
				t.SkipPull = !pull
			}
		}
	}
	if changed || t != cur {
		src := st.Ollama.Source()
		if changed {
			src = SourcePrompt
		}
		st.Ollama.Set(t, src)
	}
	return nil
}
