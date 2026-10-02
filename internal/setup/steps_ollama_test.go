package setup

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for WI-S2-7: the ollama step.

type scriptOllama struct {
	mu         sync.Mutex
	versionErr error
	hasModel   bool
	tagsErr    error
	dims       int
	embedErr   error
	pullErr    error
	ticks      [][2]int64 // progress callbacks Pull makes, in order
	calls      []string
}

func newOllama() *scriptOllama { return &scriptOllama{hasModel: true, dims: 1024} }

func (o *scriptOllama) rec(s string) {
	o.mu.Lock()
	o.calls = append(o.calls, s)
	o.mu.Unlock()
}

func (o *scriptOllama) Version(_ context.Context, u string) (string, error) {
	o.rec("version " + u)
	return "0.12.3", o.versionErr
}

func (o *scriptOllama) HasModel(_ context.Context, u, m string) (bool, error) {
	o.rec("tags " + u + " " + m)
	return o.hasModel, o.tagsErr
}

func (o *scriptOllama) EmbedDims(_ context.Context, u, m string) (int, time.Duration, error) {
	o.rec("embed " + u + " " + m)
	return o.dims, 40 * time.Millisecond, o.embedErr
}

func (o *scriptOllama) Pull(_ context.Context, u, m string, progress func(done, total int64)) error {
	o.rec("pull " + u + " " + m)
	for _, t := range o.ticks {
		progress(t[0], t[1])
	}
	if o.pullErr == nil {
		o.mu.Lock()
		o.hasModel = true
		o.mu.Unlock()
	}
	return o.pullErr
}

func (h *s23) ollamaRP(o OllamaProbe) ReadPorts {
	rp := h.rp()
	rp.Ollama = o
	return rp
}

func (h *s23) ollamaWP(o OllamaProber, sink ProgressSink) WritePorts {
	rp := h.rp()
	rp.Ollama = o
	return WritePorts{ReadPorts: rp, FS: h.fs, Runner: h.runner, Ollama: o, Progress: sink}
}

func TestOllamaSeed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := OllamaStep{}
	t.Run("nothing: the AC-32 defaults with Source=default and no token cap", func(t *testing.T) {
		h := newS23(t)
		st := NewRunState(Inputs{})
		if notes, err := s.Seed(ctx, h.rp(), st); err != nil || len(notes) != 0 {
			t.Fatalf("%v %v", notes, err)
		}
		o := st.Ollama.Get()
		if o.URL != "http://127.0.0.1:11434" || o.Model != "bge-m3" || o.EmbedMaxTokens != "" || st.Ollama.Source() != SourceDefault || !st.Ollama.IsSet() {
			t.Errorf("%+v %v", o, st.Ollama)
		}
	})
	t.Run("order flag > env file > Env > default", func(t *testing.T) {
		h := newS23(t)
		h.env = Env{"MEMORY_OLLAMA_URL": "http://shell:11434", "MEMORY_OLLAMA_MODEL": "shell-model", "MEMORY_EMBED_MAX_TOKENS": "999"}
		st := NewRunState(Inputs{})
		st.Prior.EnvDoc = []byte("MEMORY_OLLAMA_URL=http://file:11434\nMEMORY_EMBED_MAX_TOKENS=4096\n")
		notes, err := s.Seed(ctx, h.rp(), st)
		if err != nil {
			t.Fatal(err)
		}
		o := st.Ollama.Get()
		// URL from the file (Env differs: a drift note), model from Env, tokens from the file.
		if o.URL != "http://file:11434" || o.Model != "shell-model" || o.EmbedMaxTokens != "4096" || st.Ollama.Source() != SourceEnvFile {
			t.Errorf("%+v %v", o, st.Ollama)
		}
		drift := 0
		for _, n := range notes {
			if strings.Contains(n.Text, "differs from the env file") {
				drift++
			}
		}
		if drift < 1 {
			t.Errorf("notes %+v", notes)
		}
		st = NewRunState(Inputs{OllamaURL: "http://flag:11434", OllamaModel: "flag-model"})
		st.Prior.EnvDoc = []byte("MEMORY_OLLAMA_URL=http://file:11434\n")
		if _, err := s.Seed(ctx, h.rp(), st); err != nil {
			t.Fatal(err)
		}
		if o := st.Ollama.Get(); o.URL != "http://flag:11434" || o.Model != "flag-model" || st.Ollama.Source() != SourceFlag {
			t.Errorf("%+v %v", o, st.Ollama)
		}
	})
	t.Run("the token cap comes only from the file or Env", func(t *testing.T) {
		h := newS23(t)
		h.env = Env{"MEMORY_EMBED_MAX_TOKENS": "1500"}
		st := NewRunState(Inputs{})
		if _, err := s.Seed(ctx, h.rp(), st); err != nil || st.Ollama.Get().EmbedMaxTokens != "1500" {
			t.Errorf("%v %+v", err, st.Ollama.Get())
		}
	})
	t.Run("an invalid flag URL is an error; an invalid file URL leaves it unset", func(t *testing.T) {
		h := newS23(t)
		for _, bad := range []string{"ftp://x", "not a url", "http://u:p@host:11434", "http://"} {
			if _, err := s.Seed(ctx, h.rp(), NewRunState(Inputs{OllamaURL: bad})); err == nil || !strings.Contains(err.Error(), "--ollama-url") {
				t.Errorf("%q: %v", bad, err)
			}
		}
		st := NewRunState(Inputs{})
		st.Prior.EnvDoc = []byte("MEMORY_OLLAMA_URL=ftp://nope\n")
		notes, err := s.Seed(ctx, h.rp(), st)
		if err != nil || st.Ollama.IsSet() || len(notes) != 1 || !s.MissingInput(st) {
			t.Errorf("%v %v %v", notes, err, st.Ollama)
		}
	})
}

func ollamaState(url, model string, src Source) *RunState {
	st := NewRunState(Inputs{})
	st.Ollama.Set(OllamaTarget{URL: url, Model: model}, src)
	return st
}

func TestOllamaDetect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := OllamaStep{}
	const local = "http://127.0.0.1:11434"
	t.Run("ok", func(t *testing.T) {
		h := newS23(t)
		d := s.Detect(ctx, h.ollamaRP(newOllama()), ollamaState(local, "bge-m3", SourceDefault))
		if d.State != StateOK || !strings.Contains(d.Detail, "1024 dims") || len(d.Artifacts) != 1 || len(d.Notes) != 0 {
			t.Errorf("%+v", d)
		}
	})
	t.Run("absent Ollama is blocked with the install hint, no BlockedBy", func(t *testing.T) {
		h := newS23(t)
		h.plat.PackageManagers = []string{"brew"}
		o := newOllama()
		o.versionErr = errors.New("connection refused")
		d := s.Detect(ctx, h.ollamaRP(o), ollamaState(local, "bge-m3", SourceDefault))
		if d.State != StateBlocked || d.BlockedBy != "" || !strings.Contains(d.Remedy, "brew install ollama") || !strings.Contains(d.Remedy, "brew services start ollama") {
			t.Errorf("%+v", d)
		}
	})
	t.Run("a missing model is absent", func(t *testing.T) {
		h := newS23(t)
		o := newOllama()
		o.hasModel = false
		d := s.Detect(ctx, h.ollamaRP(o), ollamaState(local, "bge-m3", SourceDefault))
		if d.State != StateAbsent || !strings.Contains(d.Detail, "not pulled") {
			t.Errorf("%+v", d)
		}
		for _, c := range o.calls {
			if strings.HasPrefix(c, "embed") || strings.HasPrefix(c, "pull") {
				t.Errorf("Detect called %s", c)
			}
		}
	})
	t.Run("a declined pull is ok with a warning", func(t *testing.T) {
		h := newS23(t)
		o := newOllama()
		o.hasModel = false
		st := NewRunState(Inputs{})
		st.Ollama.Set(OllamaTarget{URL: local, Model: "bge-m3", SkipPull: true}, SourcePrompt)
		d := s.Detect(ctx, h.ollamaRP(o), st)
		if d.State != StateOK || len(d.Notes) != 1 || !strings.Contains(d.Notes[0].Text, "ollama pull bge-m3") {
			t.Errorf("%+v", d)
		}
	})
	t.Run("wrong dimensions are blocked", func(t *testing.T) {
		h := newS23(t)
		o := newOllama()
		o.dims = 768
		d := s.Detect(ctx, h.ollamaRP(o), ollamaState(local, "nomic", SourceFlag))
		if d.State != StateBlocked || !strings.Contains(d.Detail, "returns 768 dims, schema needs 1024") {
			t.Errorf("%+v", d)
		}
	})
	t.Run("errors from tags and embed are blocked", func(t *testing.T) {
		h := newS23(t)
		o := newOllama()
		o.tagsErr = errors.New("500")
		if d := s.Detect(ctx, h.ollamaRP(o), ollamaState(local, "m", SourceDefault)); d.State != StateBlocked || !strings.Contains(d.Detail, "cannot list the models") {
			t.Errorf("%+v", d)
		}
		o = newOllama()
		o.embedErr = errors.New("model failed to load")
		if d := s.Detect(ctx, h.ollamaRP(o), ollamaState(local, "m", SourceDefault)); d.State != StateBlocked || !strings.Contains(d.Detail, "embedding with m failed") {
			t.Errorf("%+v", d)
		}
	})
	t.Run("a non-loopback URL carries the AC-33 warning", func(t *testing.T) {
		h := newS23(t)
		d := s.Detect(ctx, h.ollamaRP(newOllama()), ollamaState("http://gpu.tail.ts.net:11434", "bge-m3", SourceFlag))
		if d.State != StateOK || len(d.Notes) != 1 || d.Notes[0].Level != NoteWarn ||
			!strings.Contains(d.Notes[0].Text, "800 ms") || !strings.Contains(d.Notes[0].Text, "inject nothing") {
			t.Errorf("%+v", d)
		}
		if d := s.Detect(ctx, h.ollamaRP(newOllama()), NewRunState(Inputs{})); d.State != StateAbsent {
			t.Errorf("unset: %+v", d)
		}
	})
}

func TestOllamaConfigure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := OllamaStep{}
	const local = "http://127.0.0.1:11434"
	t.Run("--yes asks nothing and accepts a remote URL", func(t *testing.T) {
		h := newS23(t)
		st := ollamaState("http://gpu.example:11434", "bge-m3", SourceFlag)
		if err := s.Configure(ctx, h.ollamaRP(newOllama()), autoPrompter{}, st); err != nil || st.Ollama.Get().URL != "http://gpu.example:11434" {
			t.Errorf("%v", err)
		}
	})
	t.Run("--yes with no usable URL names the flag", func(t *testing.T) {
		h := newS23(t)
		if err := s.Configure(ctx, h.ollamaRP(newOllama()), autoPrompter{}, NewRunState(Inputs{})); err == nil || !strings.Contains(err.Error(), "--ollama-url") {
			t.Errorf("%v", err)
		}
	})
	t.Run("a remote URL asks for confirmation with the 800 ms wording (AC-33)", func(t *testing.T) {
		h := newS23(t)
		st := ollamaState("http://gpu.example:11434", "bge-m3", SourceFlag)
		ui := newPW(t, true).conf("800 ms", true)
		if err := s.Configure(ctx, h.ollamaRP(newOllama()), ui, st); err != nil {
			t.Fatal(err)
		}
		if c := ui.calls[0]; !strings.Contains(c, "800 ms") || !strings.Contains(c, "inject nothing") || !strings.Contains(c, "gpu.example") {
			t.Errorf("transcript %q", c)
		}
	})
	t.Run("a declined remote URL fails the step", func(t *testing.T) {
		h := newS23(t)
		st := ollamaState("http://gpu.example:11434", "bge-m3", SourceFlag)
		if err := s.Configure(ctx, h.ollamaRP(newOllama()), newPW(t, true).conf("800 ms", false), st); err == nil || !strings.Contains(err.Error(), "not confirmed") {
			t.Errorf("%v", err)
		}
	})
	t.Run("a loopback model that is missing asks to pull; no sets SkipPull", func(t *testing.T) {
		h := newS23(t)
		o := newOllama()
		o.hasModel = false
		st := ollamaState(local, "bge-m3", SourceDefault)
		if err := s.Configure(ctx, h.ollamaRP(o), newPW(t, true).conf("Pull it now", false), st); err != nil || !st.Ollama.Get().SkipPull {
			t.Errorf("%v %+v", err, st.Ollama.Get())
		}
		st = ollamaState(local, "bge-m3", SourceDefault)
		if err := s.Configure(ctx, h.ollamaRP(o), newPW(t, true).conf("Pull it now", true), st); err != nil || st.Ollama.Get().SkipPull {
			t.Errorf("%v %+v", err, st.Ollama.Get())
		}
	})
	t.Run("a present model asks nothing", func(t *testing.T) {
		h := newS23(t)
		if err := s.Configure(ctx, h.ollamaRP(newOllama()), newPW(t, true), ollamaState(local, "bge-m3", SourceDefault)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("reconfigure asks URL and model; a declined remote URL is re-asked", func(t *testing.T) {
		h := newS23(t)
		st := ollamaState(local, "bge-m3", SourceEnvFile)
		st.Inputs.Reconfigure = true
		ui := newPW(t, true).text("Ollama URL", "http://gpu.example:11434").conf("800 ms", false).
			text("Ollama URL", "http://127.0.0.1:11435").text("Embedding model", "")
		if err := s.Configure(ctx, h.ollamaRP(newOllama()), ui, st); err != nil {
			t.Fatal(err)
		}
		if o := st.Ollama.Get(); o.URL != "http://127.0.0.1:11435" || o.Model != "bge-m3" || st.Ollama.Source() != SourcePrompt {
			t.Errorf("%+v %v", o, st.Ollama)
		}
	})
	t.Run("the token cap is never asked and survives Configure", func(t *testing.T) {
		h := newS23(t)
		st := NewRunState(Inputs{Reconfigure: true})
		st.Ollama.Set(OllamaTarget{URL: local, Model: "bge-m3", EmbedMaxTokens: "4096"}, SourceEnvFile)
		ui := newPW(t, true).text("Ollama URL", "").text("Embedding model", "")
		if err := s.Configure(ctx, h.ollamaRP(newOllama()), ui, st); err != nil || st.Ollama.Get().EmbedMaxTokens != "4096" {
			t.Errorf("%v %+v", err, st.Ollama.Get())
		}
		for _, c := range ui.calls {
			if strings.Contains(strings.ToLower(c), "token") {
				t.Errorf("prompted for the token cap: %s", c)
			}
		}
	})
}

func TestOllamaApplyPullsWithProgressAndVerifiesDims(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := OllamaStep{Redactor: NewRedactor()}
	const local = "http://127.0.0.1:11434"
	type tick struct {
		step, label string
		done, total int64
	}
	run := func(t *testing.T, o *scriptOllama) ([]tick, StepResult, error) {
		h := newS23(t)
		st := ollamaState(local, "bge-m3", SourceDefault)
		var got []tick
		sink := func(step, label string, done, total int64) { got = append(got, tick{step, label, done, total}) }
		plan, err := s.Plan(ctx, h.ollamaRP(o), st, Choices{OllamaModelArtifact: ChoiceApply})
		if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Verb != "pull" {
			t.Fatalf("%+v %v", plan, err)
		}
		res, err := s.Apply(ctx, h.ollamaWP(o, sink), st, plan)
		return got, res, err
	}
	t.Run("progress reaches the sink in order, then the verify", func(t *testing.T) {
		o := newOllama()
		o.hasModel = false
		o.ticks = [][2]int64{{0, 100}, {40, 100}, {100, 100}}
		got, res, err := run(t, o)
		if err != nil || len(res.Artifacts) != 0 {
			t.Fatalf("%+v %v", res, err)
		}
		want := []tick{{"ollama", "pull bge-m3", 0, 100}, {"ollama", "pull bge-m3", 40, 100}, {"ollama", "pull bge-m3", 100, 100}}
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Errorf("progress %+v", got)
		}
		if n := len(o.calls); n != 2 || !strings.HasPrefix(o.calls[0], "pull ") || !strings.HasPrefix(o.calls[1], "embed ") {
			t.Errorf("calls %v", o.calls)
		}
	})
	t.Run("a nil sink is fine", func(t *testing.T) {
		h := newS23(t)
		o := newOllama()
		o.ticks = [][2]int64{{1, 2}}
		st := ollamaState(local, "bge-m3", SourceDefault)
		if _, err := s.Apply(ctx, h.ollamaWP(o, nil), st, Plan{Actions: []Action{{Artifact: OllamaModelArtifact}}}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a pull error is returned", func(t *testing.T) {
		o := newOllama()
		o.pullErr = errors.New("registry unreachable")
		if _, _, err := run(t, o); err == nil || !strings.Contains(err.Error(), "registry unreachable") {
			t.Errorf("%v", err)
		}
	})
	t.Run("wrong dimensions fail with the AC-32 text", func(t *testing.T) {
		o := newOllama()
		o.dims = 768
		if _, _, err := run(t, o); err == nil || !strings.Contains(err.Error(), "returns 768 dims, schema needs 1024") {
			t.Errorf("%v", err)
		}
	})
}

// A fresh --yes run with no Ollama input probes the default URL for bge-m3 and
// the env file gains both keys; the second run is a no-op (AC-32, N3).
func TestOllamaFreshYesRunThroughTheEngine(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	o := newOllama()
	rp := ReadPorts{FS: h.fs, Runner: NewFakeRunner(t), Ollama: o, Clock: h.clk, Paths: h.p}
	var ticks [][2]int64
	wp := WritePorts{ReadPorts: rp, FS: h.fs, Ollama: o, Progress: func(_, _ string, d, tot int64) { ticks = append(ticks, [2]int64{d, tot}) }}
	run := func() RunResult {
		e := &Engine{Steps: []Step{EnvFileStep{Version: "v1"}, OllamaStep{}}, Read: rp, Write: wp, UI: h.ui, Reporter: h.rep, Version: "v1.2.0"}
		return e.Run(context.Background(), Inputs{Yes: true})
	}
	wantExit(t, run(), ExitOK)
	b, err := os.ReadFile(h.p.EnvFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "MEMORY_OLLAMA_URL=http://127.0.0.1:11434\n") || !strings.Contains(string(b), "MEMORY_OLLAMA_MODEL=bge-m3\n") ||
		strings.Contains(string(b), "MEMORY_EMBED_MAX_TOKENS") {
		t.Errorf("env file:\n%s", b)
	}
	if !slicesContain(o.calls, "tags http://127.0.0.1:11434 bge-m3") {
		t.Errorf("did not probe the default URL for bge-m3: %v", o.calls)
	}
	writes := len(h.nonLockWrites())
	wantExit(t, run(), ExitOK)
	if got := len(h.nonLockWrites()); got != writes {
		t.Errorf("second run wrote: %v", h.nonLockWrites()[writes:])
	}
}

func slicesContain(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// A model that is missing is pulled by the engine under --yes, with the
// callbacks reaching the sink in order; an absent Ollama ends blocked (exit 1).
func TestOllamaPullAndBlockedThroughTheEngine(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	o := newOllama()
	o.hasModel = false
	o.ticks = [][2]int64{{0, 10}, {5, 10}, {10, 10}}
	rp := ReadPorts{FS: h.fs, Runner: NewFakeRunner(t), Ollama: o, Clock: h.clk, Paths: h.p}
	var ticks [][2]int64
	wp := WritePorts{ReadPorts: rp, FS: h.fs, Ollama: o, Progress: func(_, _ string, d, tot int64) { ticks = append(ticks, [2]int64{d, tot}) }}
	e := &Engine{Steps: []Step{OllamaStep{}}, Read: rp, Write: wp, UI: h.ui, Reporter: h.rep, Version: "v1.2.0"}
	wantExit(t, e.Run(context.Background(), Inputs{Yes: true}), ExitOK)
	if len(ticks) != 3 || ticks[0] != [2]int64{0, 10} || ticks[2] != [2]int64{10, 10} {
		t.Errorf("ticks %v", ticks)
	}

	h2 := newEH(t, false)
	o2 := newOllama()
	o2.versionErr = errors.New("connection refused")
	rp2 := ReadPorts{FS: h2.fs, Runner: NewFakeRunner(t), Ollama: o2, Clock: h2.clk, Paths: h2.p, Platform: PlatformInfo{OS: OSLinux}}
	e2 := &Engine{Steps: []Step{OllamaStep{}}, Read: rp2, Write: WritePorts{ReadPorts: rp2, FS: h2.fs, Ollama: o2}, UI: h2.ui, Reporter: h2.rep, Version: "v1.2.0"}
	res := e2.Run(context.Background(), Inputs{Yes: true})
	wantExit(t, res, ExitFailed)
	if oc := outcome(t, res, "ollama"); oc.Outcome != OutcomeBlocked || !strings.Contains(oc.Remedy, "ollama") {
		t.Errorf("%+v", oc)
	}
}
