package setup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/config"
)

// Tests for WI-S2-5: the envfile step, its goldens (AC-64) and the Seed
// helper. Fixtures live in testdata/envfile/<case>.in (the file before) and
// <case>.golden (the file after install).

const envDSNBase = "postgresql://claude_memory:%s@db.example:5432/claude_memory?sslmode=prefer"

func mustTarget(t *testing.T, dsn, pw string, src Source) DBTarget {
	t.Helper()
	tg, err := ParseDBTarget(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if pw != "" {
		tg = tg.WithPassword(pw)
	}
	tg.Source = src
	return tg
}

func dbState(t *testing.T, pw string, src Source) *RunState {
	st := NewRunState(Inputs{})
	st.DB.Set(mustTarget(t, strings.Replace(envDSNBase, "%s", "x", 1), pw, src), src)
	return st
}

// envRun detects, takes the default choices (plus consent for the artifacts
// in consent, which stand for the interactive overwrite confirmation), plans
// and applies, and checks the success rule.
type envRun struct {
	det    Detection
	plan   Plan
	res    StepResult
	before string
	after  string
}

func (h *s23) envRun(st *RunState, consent ...string) envRun {
	h.t.Helper()
	step := EnvFileStep{Version: "v1.0.0"}
	ctx := context.Background()
	var r envRun
	if b, err := os.ReadFile(h.p.EnvFile()); err == nil {
		r.before = string(b)
	}
	r.det = step.Detect(ctx, h.rp(), st)
	if r.det.State == StateBlocked {
		h.t.Fatalf("blocked: %s", r.det.Detail)
	}
	ch := Choices{}
	for _, a := range r.det.Artifacts {
		ch[a.ID] = DefaultChoice(a.State)
		for _, c := range consent {
			if c == a.ID && a.State != StateOK {
				ch[a.ID] = ChoiceApply
			}
		}
	}
	var err error
	if r.plan, err = step.Plan(ctx, h.rp(), st, ch); err != nil {
		h.t.Fatal(err)
	}
	if hasApply(ch) {
		wp := h.wp()
		wp.Clock = NewFakeClock(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
		if r.res, err = step.Apply(ctx, wp, st, r.plan); err != nil {
			h.t.Fatal(err)
		}
		again := step.Detect(ctx, h.rp(), st)
		for id, c := range ch {
			if c != ChoiceApply {
				continue
			}
			for _, a := range again.Artifacts {
				if a.ID == id && a.State != StateOK {
					h.t.Errorf("after apply: %s is %s (%s)", id, a.State, a.Detail)
				}
			}
		}
	}
	if b, err := os.ReadFile(h.p.EnvFile()); err == nil {
		r.after = string(b)
	}
	return r
}

// assertIdempotent: a second run over the result finds everything ok and
// writes nothing.
func (h *s23) assertIdempotent(st *RunState, consent ...string) {
	h.t.Helper()
	before := len(h.fs.Writes())
	r := h.envRun(st, consent...)
	for _, a := range r.det.Artifacts {
		if a.State != StateOK && !(a.State == StateModified && !contains(consent, a.ID)) {
			h.t.Errorf("second run: %s is %s (%s)", a.ID, a.State, a.Detail)
		}
	}
	if got := len(h.fs.Writes()); got != before {
		h.t.Errorf("second run wrote: %v", h.fs.Writes()[before:])
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (h *s23) loadCase(name string, mode fs.FileMode) {
	h.t.Helper()
	in := readTestdata(h.t, filepath.Join("testdata", "envfile", name+".in"))
	if in != nil {
		h.write(h.p.EnvFile(), string(in), mode)
	}
}

func goldenEnv(t *testing.T, name, got string) {
	t.Helper()
	checkGolden(t, filepath.Join("testdata", "envfile", name+".golden"), []byte(got))
}

func fullState(t *testing.T, pw string, src Source) *RunState {
	st := dbState(t, pw, src)
	st.Ollama.Set(OllamaTarget{URL: "http://127.0.0.1:11434", Model: "bge-m3"}, SourceDefault)
	return st
}

func TestEnvFileGoldenFresh(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	st := fullState(t, sentinelPassword, SourceGenerated)
	st.Ollama.Set(OllamaTarget{URL: "http://127.0.0.1:11434", Model: "bge-m3", EmbedMaxTokens: "1024"}, SourceEnv)
	st.PRRepos.Set("acme/api,acme/web", SourceFlag)
	r := h.envRun(st)
	goldenEnv(t, "fresh", r.after)
	// 0600 in a 0700 directory, one env-key artifact per key, the dir recorded.
	info, err := os.Stat(h.p.EnvFile())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", info, err)
	}
	if di, _ := os.Stat(h.p.ConfigDir); di == nil || di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode: %v", di)
	}
	var keys, dirs int
	for _, a := range r.res.Artifacts {
		switch a.Kind {
		case KindEnvKey:
			keys++
			if a.Step != "envfile" || a.Path != h.p.EnvFile() || a.SHA256 != "" || a.Entry != "" {
				t.Errorf("artifact %+v", a)
			}
		case KindDir:
			dirs++
		}
	}
	if keys != 5 || dirs != 1 {
		t.Errorf("artifacts %+v", r.res.Artifacts)
	}
	// The written DSN round-trips and the line has no finding (Design 24).
	ef := config.ParseEnvData([]byte(r.after), 0o600)
	if len(ef.Findings) != 0 {
		t.Errorf("findings %+v", ef.Findings)
	}
	tg, err := ParseDBTarget(ef.Values["MEMORY_PG_DSN"])
	if err != nil || string(tg.password) != sentinelPassword {
		t.Errorf("round trip: %v", err)
	}
	h.assertIdempotent(st)
}

func TestEnvFileGoldenUpdateInPlace(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("update", 0o600)
	st := fullState(t, "n3w-pw", SourceFlag)
	st.Ollama.Set(OllamaTarget{URL: "http://127.0.0.1:11434", Model: "bge-m3"}, SourceEnvFile)
	r := h.envRun(st)
	goldenEnv(t, "update", r.after)
	if !strings.Contains(r.after, "# my own settings\nMEMORY_HOOK_TIMEOUT=3s") {
		t.Error("comments / unknown keys not preserved in place")
	}
	if len(r.plan.Diffs) != 1 || !strings.Contains(r.plan.Diffs[0].Unified, "-MEMORY_PG_DSN=") {
		t.Errorf("plan diff %+v", r.plan.Diffs)
	}
	h.assertIdempotent(st)
}

func TestEnvFileGoldenUnknownKept(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("unknown", 0o600)
	r := h.envRun(fullState(t, "pw-pw-pw-1", SourceFlag))
	goldenEnv(t, "unknown", r.after)
	for _, keep := range []string{"# keep me\nFOO=bar\n\nFOO=dup\nno equals line\nexport EXPORTED_UNKNOWN=1\nQUOTED_UNKNOWN=\"x y\"\nMEMORY_HOOK_TIMEOUT=3s\n"} {
		if !strings.HasPrefix(r.after, keep) {
			t.Errorf("unknown lines changed:\n%s", r.after)
		}
	}
	// The export / quoted unknown lines are a format finding, kept without consent.
	if len(r.res.Artifacts) == 0 || strings.Contains(r.after, "\nEXPORTED_UNKNOWN=") {
		t.Errorf("unexpected conversion without consent")
	}
}

func TestEnvFileGoldenExportConverted(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("export", 0o600)
	st := dbState(t, "s3cret", SourceEnvFile)

	// Without consent (--yes) the unrelated export line is left alone; the
	// managed DSN line, which the binary does not read, is rewritten in place
	// (outdated, not modified: its connection is the chosen one).
	r0 := h.envRun(st)
	if r0.det.State != StateModified || !strings.Contains(r0.after, "\nMEMORY_PG_DSN=") && !strings.HasPrefix(r0.after, "MEMORY_PG_DSN=") ||
		!strings.Contains(r0.after, "export FOO=bar") {
		t.Fatalf("yes: state %s, after %q", r0.det.State, r0.after)
	}
	// With consent every export line becomes plain, in place; a backup is kept.
	r := h.envRun(st, envFormatArtifact)
	goldenEnv(t, "export", r.after)
	if baks, _ := filepath.Glob(h.p.EnvFile() + envBackupSuffix + "*"); len(baks) != 1 {
		t.Errorf("backups %v", baks)
	} else if fi, _ := os.Stat(baks[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v", fi.Mode())
	}
	h.assertIdempotent(st)
}

func TestEnvFileGoldenQuotedUnquoted(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("quoted", 0o600)
	st := NewRunState(Inputs{})
	st.Ollama.Set(OllamaTarget{URL: "http://127.0.0.1:11434", Model: "bge-m3"}, SourceEnvFile)
	r := h.envRun(st, envFormatArtifact)
	goldenEnv(t, "quoted", r.after)
	h.assertIdempotent(st)
}

func TestEnvFileGoldenUnparseableKept(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("unparseable", 0o600)
	st := NewRunState(Inputs{})
	st.Ollama.Set(OllamaTarget{URL: "http://127.0.0.1:11434", Model: "bge-m3"}, SourcePrompt)
	r := h.envRun(st)
	goldenEnv(t, "unparseable", r.after)
	if !strings.Contains(r.after, "MEMORY_OLLAMA_URL=$(cat /run/ollama-url)\n") || !strings.Contains(r.after, "OTHER=a\\b\n") {
		t.Errorf("unparseable lines touched:\n%s", r.after)
	}
	// Reported: the kept managed key and the unknown one, by line number, no value.
	var kept, other bool
	for _, n := range r.det.Notes {
		kept = kept || (strings.Contains(n.Text, "MEMORY_OLLAMA_URL line 1") && strings.Contains(n.Text, "fix it by hand"))
		other = other || (strings.Contains(n.Text, "OTHER") && strings.Contains(n.Text, "line 2"))
		if strings.Contains(n.Text, "ollama-url") {
			t.Errorf("note leaks the value: %q", n.Text)
		}
	}
	if !kept || !other {
		t.Errorf("notes %+v", r.det.Notes)
	}
	// The model line (a plain, differing value chosen by the user) is replaced.
	if !strings.Contains(r.after, "MEMORY_OLLAMA_MODEL=bge-m3\n") {
		t.Errorf("model not updated:\n%s", r.after)
	}
	h.assertIdempotent(st)
}

func TestEnvFileGoldenBase64SlashReencoded(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("base64slash", 0o600)
	// Seed adopted the DSN from the file (recovered): same connection.
	tg, ok := RecoverDSN("postgresql://claude_memory:ab/cd+ef==@db.example:5432/claude_memory")
	if !ok {
		t.Fatal("recover")
	}
	st := NewRunState(Inputs{})
	st.DB.Set(tg, SourceEnvFile)
	r := h.envRun(st)
	goldenEnv(t, "base64slash", r.after)
	ef := config.ParseEnvData([]byte(r.after), 0o600)
	if len(ef.Findings) != 0 {
		t.Errorf("findings %+v", ef.Findings)
	}
	u, err := ParseDBTarget(ef.Values["MEMORY_PG_DSN"])
	if err != nil || string(u.password) != "ab/cd+ef==" {
		t.Errorf("password after re-encode: %v", err)
	}
	if !strings.Contains(r.after, "MEMORY_OLLAMA_URL=http://127.0.0.1:11434\n") {
		t.Error("other lines changed")
	}
	h.assertIdempotent(st)
}

func TestEnvFileGoldenDollarPasswordEncoded(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("dollar", 0o600)
	tg, ok := RecoverDSN("postgresql://claude_memory:pa$word@db.example:5432/claude_memory")
	if !ok {
		t.Fatal("recover")
	}
	st := NewRunState(Inputs{})
	st.DB.Set(tg, SourceEnvFile)
	r := h.envRun(st)
	goldenEnv(t, "dollar", r.after)
	// No unparseable-value on re-parse: the installer's own file is clean.
	if ef := config.ParseEnvData([]byte(r.after), 0o600); len(ef.Findings) != 0 {
		t.Errorf("findings %+v", ef.Findings)
	}
	h.assertIdempotent(st)
}

func TestEnvFileGoldenBOMAndCRLF(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("bomcrlf", 0o600)
	st := dbState(t, "n3w-pw", SourceFlag)
	r := h.envRun(st)
	goldenEnv(t, "bomcrlf", r.after)
	if strings.Contains(r.after, "\ufeff") || strings.Contains(strings.ReplaceAll(r.after, "\r\n", ""), "\n") || strings.Contains(strings.ReplaceAll(r.after, "\r\n", ""), "\r") {
		t.Errorf("BOM or mixed line endings: %q", r.after)
	}
	h.assertIdempotent(st)
}

// BOM alone: the first key must be seen (the loader strips it too).
func TestEnvFileBOMFirstKeySeen(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.write(h.p.EnvFile(), "\ufeffMEMORY_OLLAMA_MODEL=bge-m3\n", 0o600)
	st := NewRunState(Inputs{})
	st.Ollama.Set(OllamaTarget{Model: "bge-m3"}, SourceEnvFile)
	d := EnvFileStep{}.Detect(context.Background(), h.rp(), st)
	if d.State != StateOK {
		t.Errorf("%+v", d)
	}
}

// The hand-install file: DSN without sslmode is the same connection (no
// rewrite), a PRRepos line the run does not manage stays byte-identical, the
// missing Ollama model is added with Source=default, run 2 is a no-op.
func TestEnvFileHandInstallPRReposPreserved(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("handinstall", 0o600)
	tg, err := ParseDBTarget("postgresql://claude_memory:%2Ft0ken@100.64.0.9:5432/claude_memory")
	if err != nil {
		t.Fatal(err)
	}
	st := NewRunState(Inputs{})
	st.DB.Set(tg, SourceEnvFile)
	st.Ollama.Set(OllamaTarget{URL: "http://127.0.0.1:11434", Model: "bge-m3", EmbedMaxTokens: "2048"}, SourceEnvFile)
	// PRRepos unset: not managed in this run.
	r := h.envRun(st)
	goldenEnv(t, "handinstall", r.after)
	for _, a := range r.det.Artifacts {
		if strings.HasSuffix(a.ID, "MEMORY_PR_INGEST_REPOS") {
			t.Errorf("PRRepos has an artifact: %+v", a)
		}
	}
	for _, line := range strings.SplitAfter(r.before, "\n") {
		if line != "" && !strings.Contains(r.after, line) {
			t.Errorf("line changed: %q", line)
		}
	}
	// The default model is written on run 1 ...
	st.Ollama.Set(OllamaTarget{URL: "http://127.0.0.1:11434", Model: "bge-m3", EmbedMaxTokens: "2048"}, SourceDefault)
	h.assertIdempotent(st)
}

func TestEnvFileUnsetFieldsManageNothing(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.write(h.p.EnvFile(), "MEMORY_PG_DSN=postgresql://u:p@h/d\nMEMORY_PR_INGEST_REPOS=a/b\nMEMORY_OLLAMA_URL=http://x\n", 0o600)
	st := NewRunState(Inputs{}) // nothing set
	d := EnvFileStep{}.Detect(context.Background(), h.rp(), st)
	for _, a := range d.Artifacts {
		if strings.Contains(a.ID, "MEMORY_") {
			t.Errorf("artifact for an unset field: %+v", a)
		}
	}
	if d.State != StateOK {
		t.Errorf("state %s", d.State)
	}
	before := len(h.fs.Writes())
	h.envRun(st)
	if len(h.fs.Writes()) != before {
		t.Error("wrote with nothing managed")
	}
}

func TestEnvFileArtifactIDsMatchEngineMap(t *testing.T) {
	t.Parallel()
	st := fullState(t, "pw-pw-pw-1", SourceFlag)
	st.Ollama.Set(OllamaTarget{URL: "u", Model: "m", EmbedMaxTokens: "1"}, SourceFlag)
	st.PRRepos.Set("a/b", SourceFlag)
	wants, err := desiredEnv(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(wants) != len(EnvKeyFields) {
		t.Errorf("%d wants, %d engine keys", len(wants), len(EnvKeyFields))
	}
	for _, w := range wants {
		if _, ok := EnvKeyFields["MEMORY_"+strings.TrimPrefix(w.Key, "MEMORY_")]; !ok {
			t.Errorf("key %s missing from EnvKeyFields", w.Key)
		}
		key, ok := strings.CutPrefix(envKeyArtifact(w.Key), "envfile/")
		if !ok || EnvKeyFields[key] == "" {
			t.Errorf("artifact id %q does not map", envKeyArtifact(w.Key))
		}
	}
}

func TestEnvFileModeRepairIsChmodOnly(t *testing.T) { // AC-29
	t.Parallel()
	h := newS23(t)
	content := "MEMORY_PG_DSN=" + strings.Replace(envDSNBase, "%s", "x", 1) + "\nFOO=bar"
	h.write(h.p.EnvFile(), content, 0o644)
	st := dbState(t, "x", SourceEnvFile)
	d := EnvFileStep{}.Detect(context.Background(), h.rp(), st)
	var mode ArtifactState
	for _, a := range d.Artifacts {
		if a.ID == envModeArtifact {
			mode = a
		}
	}
	if mode.State != StateOutdated || !strings.Contains(mode.Detail, "mode 0644, the binary refuses to load it") {
		t.Fatalf("mode artifact %+v", mode)
	}
	if DefaultChoice(mode.State) != ChoiceApply {
		t.Error("default must be apply")
	}
	before := len(h.fs.Writes())
	r := h.envRun(st)
	got := h.fs.Writes()[before:]
	if len(got) != 1 || !strings.HasPrefix(got[0], "chmod ") {
		t.Errorf("writes %v, want one chmod", got)
	}
	if r.after != content { // byte-identical, even the missing final newline
		t.Errorf("content changed: %q", r.after)
	}
	if fi, _ := os.Stat(h.p.EnvFile()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if len(r.plan.Diffs) != 0 {
		t.Errorf("a chmod has no diff: %+v", r.plan.Diffs)
	}
}

func TestEnvFileHandEditedValueIsKeptUnlessConfirmed(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.write(h.p.EnvFile(), "MEMORY_OLLAMA_MODEL=my-model\n", 0o600)
	st := NewRunState(Inputs{})
	st.Ollama.Set(OllamaTarget{Model: "bge-m3"}, SourceEnv) // a shell value, never a source over the file; here it differs
	r := h.envRun(st)
	if r.det.State != StateModified || r.after != r.before {
		t.Fatalf("yes: %s changed=%v", r.det.State, r.after != r.before)
	}
	key := envKeyArtifact(EnvKeyModel)
	r = h.envRun(st, key)
	if !strings.Contains(r.after, "MEMORY_OLLAMA_MODEL=bge-m3\n") {
		t.Errorf("confirmed overwrite not applied: %q", r.after)
	}
	if baks, _ := filepath.Glob(h.p.EnvFile() + envBackupSuffix + "*"); len(baks) != 1 {
		t.Errorf("backups %v", baks)
	}
}

func TestEnvFileNoSecretInDetectionOrNotes(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.write(h.p.EnvFile(), "MEMORY_PG_DSN=postgresql://claude_memory:$(pass x)@h/d\nOTHER=$(secret-"+sentinelPassword+")\n", 0o644)
	st := dbState(t, sentinelPassword, SourceFlag)
	step := EnvFileStep{}
	d := step.Detect(context.Background(), h.rp(), st)
	var all []string
	all = append(all, d.Detail)
	for _, a := range d.Artifacts {
		all = append(all, a.Detail)
	}
	for _, n := range d.Notes {
		all = append(all, n.Text)
	}
	p, _ := step.Plan(context.Background(), h.rp(), st, Choices{envKeyArtifact(EnvKeyDSN): ChoiceApply})
	for _, a := range p.Actions {
		all = append(all, a.Desc)
	}
	for _, s := range all {
		if strings.Contains(s, "S3ntinel") || strings.Contains(s, pctEncode(sentinelPassword)) || strings.Contains(s, "pass x") {
			t.Errorf("secret in %q", s)
		}
	}
}

func TestEnvFileBlockedCases(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	st := NewRunState(Inputs{})
	st.DB.Set(DBTarget{Host: "h", Port: "0", Name: "d"}, SourceFlag) // invalid
	if d := (EnvFileStep{}).Detect(context.Background(), h.rp(), st); d.State != StateBlocked || strings.Contains(d.Detail, "S3ntinel") {
		t.Errorf("invalid DB: %+v", d)
	}
	h.fs.Fail = func(op FSOp, p string) error {
		if op == OpRead && p == h.p.EnvFile() {
			return errors.New("EIO")
		}
		return nil
	}
	if d := (EnvFileStep{}).Detect(context.Background(), h.rp(), NewRunState(Inputs{})); d.State != StateBlocked {
		t.Errorf("read error: %+v", d)
	}
}

func TestEnvFileDirArtifact(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	st := fullState(t, "pw-pw-pw-1", SourceFlag)
	d := EnvFileStep{}.Detect(context.Background(), h.rp(), st)
	if d.State != StateAbsent || d.Artifacts[0].ID != envDirArtifact || d.Artifacts[0].State != StateAbsent {
		t.Fatalf("%+v", d)
	}
	// An existing directory: no dir artifact action, none recorded.
	h2 := newS23(t)
	if err := os.MkdirAll(h2.p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r := h2.envRun(fullState(t, "pw-pw-pw-1", SourceFlag))
	for _, a := range r.res.Artifacts {
		if a.Kind == KindDir {
			t.Errorf("dir recorded though it existed: %+v", a)
		}
	}
}

func TestEnvFileDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.loadCase("update", 0o600)
	h.fs.ReadOnly = true
	st := dbState(t, "n3w-pw", SourceFlag)
	step := EnvFileStep{}
	d := step.Detect(context.Background(), h.rp(), st)
	ch := Choices{}
	for _, a := range d.Artifacts {
		ch[a.ID] = DefaultChoice(a.State)
	}
	p, err := step.Plan(context.Background(), h.rp(), st, ch)
	if err != nil || len(p.Diffs) != 1 || len(h.fs.Writes()) != 0 {
		t.Fatalf("%+v %v %v", p, err, h.fs.Writes())
	}
	if !strings.Contains(p.Diffs[0].Unified, "--- "+h.p.EnvFile()) {
		t.Errorf("diff header: %s", p.Diffs[0].Unified)
	}
}

// ---- Seed helper ------------------------------------------------------------

func TestSeedEnvValueOrder(t *testing.T) { // Design 16, N10
	t.Parallel()
	file := []byte("MEMORY_PG_DSN=postgresql://u:file@h/d\nMEMORY_OLLAMA_MODEL=file-model\nQ=\"quoted\"\nBAD=$(x)\n")
	st := func(f []byte) *RunState {
		s := NewRunState(Inputs{})
		s.Prior.EnvDoc = f
		return s
	}
	envv := Env{"MEMORY_PG_DSN": "postgresql://u:shell@h/d", "MEMORY_OLLAMA_MODEL": "file-model", "MEMORY_EMBED_MAX_TOKENS": "512"}

	// flag beats everything, and no drift note.
	if s := SeedEnvValue(st(file), envv, "MEMORY_PG_DSN", "postgresql://flag@h/d", nil); !s.Found || s.Source != SourceFlag || len(s.Notes) != 0 {
		t.Errorf("flag: %+v", s)
	}
	// file beats Env; a differing shell value is a drift note, never a source.
	s := SeedEnvValue(st(file), envv, "MEMORY_PG_DSN", "", nil)
	if s.Source != SourceEnvFile || s.Value != "postgresql://u:file@h/d" || len(s.Notes) != 1 || !strings.Contains(s.Notes[0].Text, "differs from the env file") {
		t.Errorf("file: %+v", s)
	}
	if strings.Contains(s.Notes[0].Text, "shell@") || strings.Contains(s.Notes[0].Text, "file@") {
		t.Errorf("drift note leaks a value: %q", s.Notes[0].Text)
	}
	// An equal shell value is no drift (also semantically, via same).
	if s := SeedEnvValue(st(file), envv, "MEMORY_OLLAMA_MODEL", "", nil); len(s.Notes) != 0 || s.Source != SourceEnvFile {
		t.Errorf("equal: %+v", s)
	}
	if s := SeedEnvValue(st(file), envv, "MEMORY_PG_DSN", "", func(a, b string) bool { return true }); len(s.Notes) != 0 {
		t.Errorf("same(): %+v", s)
	}
	// Env only when the file has no usable value.
	if s := SeedEnvValue(st(file), envv, "MEMORY_EMBED_MAX_TOKENS", "", nil); s.Source != SourceEnv || s.Value != "512" || len(s.Notes) != 0 {
		t.Errorf("env: %+v", s)
	}
	if s := SeedEnvValue(st(nil), envv, "MEMORY_PG_DSN", "", nil); s.Source != SourceEnv {
		t.Errorf("no file: %+v", s)
	}
	// An unparseable file line is not usable: falls to Env, else not found.
	// An unparseable file line is returned raw and flagged; the shell's value
	// never stands in for it (MED-3), and the drift is a note.
	if s := SeedEnvValue(st(file), Env{"BAD": "shellvalue"}, "BAD", "", nil); s.Source != SourceEnvFile || !s.Unparseable || s.Value != "$(x)" || len(s.Notes) != 1 {
		t.Errorf("unparseable: %+v", s)
	}
	if s := SeedEnvValue(st(file), nil, "BAD", "", nil); !s.Found || !s.Unparseable || s.Value != "$(x)" {
		t.Errorf("unparseable, no env: %+v", s)
	}
	// Whole-value quotes are stripped; absent everywhere: not found.
	if s := SeedEnvValue(st(file), nil, "Q", "", nil); s.Value != "quoted" {
		t.Errorf("quoted: %+v", s)
	}
	if s := SeedEnvValue(st(file), nil, "NOPE", "", nil); s.Found {
		t.Errorf("absent: %+v", s)
	}
}

// ---- Engine integration -------------------------------------------------------

// dbSeedStep stands in for the database / ollama / jobs owners: it seeds the
// RunState fields the envfile step reads.
type dbSeedStep struct {
	FakeStep
	seed func(*RunState)
}

func (s *dbSeedStep) Seed(_ context.Context, _ ReadPorts, st *RunState) ([]Note, error) {
	s.seed(st)
	return nil, nil
}

func TestEnvFileThroughTheEngine(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	owner := &dbSeedStep{FakeStep: FakeStep{StepID: "database", Log: h.log,
		DetectF: func(int, *RunState) Detection { return Detection{State: StateOK} }}}
	owner.seed = func(st *RunState) {
		tg, _ := ParseDBTarget("postgresql://claude_memory:x@db.example:5432/claude_memory?sslmode=prefer")
		st.DB.Set(tg.WithPassword(sentinelPassword), SourceFlag)
		st.Ollama.Set(OllamaTarget{URL: "http://127.0.0.1:11434", Model: "bge-m3"}, SourceDefault)
	}
	run := func() RunResult {
		rp := ReadPorts{FS: h.fs, Runner: NewFakeRunner(h.t), Clock: h.clk, Paths: h.p}
		e := &Engine{Steps: []Step{owner, EnvFileStep{Version: "v1.0.0"}}, Read: rp, Write: WritePorts{ReadPorts: rp, FS: h.fs}, UI: h.ui, Reporter: h.rep, Version: "v1.0.0"}
		return e.Run(context.Background(), Inputs{Yes: true})
	}
	if r := run(); r.ExitCode != ExitOK {
		t.Fatalf("run 1: %+v", r)
	}
	b, err := os.ReadFile(h.p.EnvFile())
	if err != nil || !strings.Contains(string(b), "MEMORY_OLLAMA_MODEL=bge-m3\n") {
		t.Fatalf("env file: %q %v", b, err)
	}
	var envKeys int
	for _, a := range h.manifest().Artifacts {
		if a.Kind == KindEnvKey {
			envKeys++
		}
	}
	if envKeys != 3 { // DSN, Ollama URL, model
		t.Errorf("manifest env-key artifacts: %d", envKeys)
	}
	if blob, _ := os.ReadFile(h.p.Manifest()); strings.Contains(string(blob), "S3ntinel") {
		t.Error("manifest holds the password")
	}
	before := len(h.nonLockWrites())
	if r := run(); r.ExitCode != ExitOK || len(h.nonLockWrites()) != before {
		t.Fatalf("run 2 is not a no-op: %+v %v", r, h.nonLockWrites()[before:])
	}
	// The password appears in the diff the engine reports only through the
	// renderer, which redacts; the step's own result carries a real diff.
	r1 := h.rep.Plans[0]
	if len(r1.Steps) == 0 || len(r1.Steps[len(r1.Steps)-1].Plan.Diffs) == 0 {
		t.Errorf("no diff in the plan: %+v", r1)
	}
}
