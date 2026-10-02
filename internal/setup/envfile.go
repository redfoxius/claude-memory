package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"claude-memory/internal/config"
)

// Env-file model for the envfile step (plan WI-S2-5, Design 16): which keys
// install manages, what it wants them to hold, what the file holds now, and
// how a set of per-artifact choices turns the file into its next content.
// The step itself is steps_envfile.go; the line-level editor is
// config.EnvDoc.

// Artifact ids of the envfile step. A managed key is "envfile/<ENVKEY>"
// (kind env-key, retained): the engine's EnvKeyFields map depends on this
// exact format.
const (
	envFormatArtifact = "envfile/format"
	envModeArtifact   = "envfile/mode"
	envDirArtifact    = "envfile/dir"
)

// envBackupSuffix prefixes the timestamp of a backup kept when install
// overwrites a hand-edited env file: env.bak.claude-memory.<UTC ts>, mode 0600.
const envBackupSuffix = ".bak.claude-memory."

func envKeyArtifact(key string) string { return "envfile/" + key }

// Managed env keys, in the order the file gets them.
const (
	EnvKeyDSN       = "MEMORY_PG_DSN"
	EnvKeyOllamaURL = "MEMORY_OLLAMA_URL"
	EnvKeyModel     = "MEMORY_OLLAMA_MODEL"
	EnvKeyMaxTokens = "MEMORY_EMBED_MAX_TOKENS"
	EnvKeyPRRepos   = "MEMORY_PR_INGEST_REPOS"
)

// envWant is one managed key the install wants in the file: its value comes
// from a RunState field that an owner set in this run. A field that is not
// set yields no envWant, so its line, if any, stays byte-identical.
type envWant struct {
	Key, Value string
	Target     *DBTarget // set for MEMORY_PG_DSN: compared by connection, not by spelling
	Src        Source
}

// desiredEnv computes the keys install manages from RunState (Design 16).
// EmbedMaxTokens is owned by the ollama step and only written when it holds
// a value (it is never prompted and has no default).
func desiredEnv(st *RunState) ([]envWant, error) {
	var out []envWant
	if st.DB.IsSet() {
		db := st.DB.Get()
		dsn := db.DSN()
		if dsn == "" {
			return nil, errors.New("the database settings are invalid (host, port, database name or sslmode)")
		}
		out = append(out, envWant{Key: EnvKeyDSN, Value: dsn, Target: &db, Src: st.DB.Source()})
	}
	if st.Ollama.IsSet() {
		o := st.Ollama.Get()
		src := st.Ollama.Source()
		if o.URL != "" {
			out = append(out, envWant{Key: EnvKeyOllamaURL, Value: o.URL, Src: src})
		}
		if o.Model != "" {
			out = append(out, envWant{Key: EnvKeyModel, Value: o.Model, Src: src})
		}
		if o.EmbedMaxTokens != "" {
			out = append(out, envWant{Key: EnvKeyMaxTokens, Value: o.EmbedMaxTokens, Src: src})
		}
	}
	if st.PRRepos.IsSet() {
		out = append(out, envWant{Key: EnvKeyPRRepos, Value: st.PRRepos.Get(), Src: st.PRRepos.Source()})
	}
	return out, nil
}

// parseEnvDSN reads a DSN as it may stand in an env file: a clean URL, or
// (recovered) one whose password was not percent-encoded. recovered is true
// for the second case, which the step re-encodes.
func parseEnvDSN(raw string, forceRecover bool) (t DBTarget, recovered, ok bool) {
	if !forceRecover {
		if t, err := ParseDBTarget(raw); err == nil {
			return t, false, true
		}
	}
	if t, ok := RecoverDSN(raw); ok {
		return t, true, true
	}
	return DBTarget{}, false, false
}

// envAnalysis is what Detect, Plan and Apply all derive from the file on
// disk and RunState, so they cannot disagree.
type envAnalysis struct {
	path       string
	exists     bool
	before     []byte
	mode       fs.FileMode
	wants      []envWant
	arts       []ArtifactState
	state      map[string]State
	notes      []Note
	convert    []string // keys whose lines the format artifact would convert
	dirMissing bool
}

// analyzeEnv reads the env file through fsys and classifies every artifact.
func analyzeEnv(fsys ReadFS, p Paths, st *RunState) (*envAnalysis, error) {
	an := &envAnalysis{path: p.EnvFile(), state: map[string]State{}}
	var err error
	if an.wants, err = desiredEnv(st); err != nil {
		return nil, err
	}
	b, err := fsys.ReadFile(an.path)
	switch {
	case err == nil:
		an.exists, an.before = true, b
		info, serr := fsys.Stat(an.path)
		if serr != nil {
			return nil, fmt.Errorf("cannot inspect %s: %w", an.path, serr)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s is a directory", an.path)
		}
		an.mode = info.Mode().Perm()
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, fmt.Errorf("cannot read %s: %w", an.path, err)
	}
	add := func(id string, s State, detail string) {
		an.arts = append(an.arts, ArtifactState{ID: id, State: s, Detail: detail})
		an.state[id] = s
	}

	// The config directory.
	if _, derr := fsys.Stat(p.ConfigDir); errors.Is(derr, fs.ErrNotExist) {
		an.dirMissing = true
		add(envDirArtifact, StateAbsent, "create "+p.ConfigDir+" (mode 0700)")
	} else {
		add(envDirArtifact, StateOK, p.ConfigDir+" exists")
	}

	doc := config.ParseEnvDoc(an.before)

	// The format: export / whole-value quotes, converted only with consent.
	an.convert = config.ParseEnvDoc(an.before).NormalizeAll()
	if len(an.convert) > 0 {
		add(envFormatArtifact, StateModified, fmt.Sprintf("%d line(s) use `export` or quotes the binary does not read (%s)",
			len(an.convert), strings.Join(an.convert, ", ")))
	} else {
		add(envFormatArtifact, StateOK, "plain KEY=VALUE")
	}

	// The managed keys.
	handled := map[string]bool{}
	for _, w := range an.wants {
		handled[w.Key] = true
		s, detail, note := keyState(doc, w)
		add(envKeyArtifact(w.Key), s, detail)
		if note != nil {
			an.notes = append(an.notes, *note)
		}
	}

	// A line that is not a managed key and the plain format cannot hold: report.
	for _, f := range doc.Findings() {
		if f.Kind == config.FindingUnparseableValue && !handled[f.Key] && f.Key != "" {
			an.notes = append(an.notes, Note{NoteWarn, fmt.Sprintf("%s line %d (%s): %s; left untouched", an.path, f.Line, f.Key, f.Detail)})
		}
	}

	// The mode (AC-29): chmod only.
	if an.exists {
		if an.mode&0o077 != 0 {
			add(envModeArtifact, StateOutdated, fmt.Sprintf("mode %#o, the binary refuses to load it", an.mode))
		} else {
			add(envModeArtifact, StateOK, fmt.Sprintf("mode %#o", an.mode))
		}
	}
	return an, nil
}

// keyState classifies one managed key against the file (no secret reaches
// the detail or the note: only the key name and the line number).
func keyState(doc *config.EnvDoc, w envWant) (State, string, *Note) {
	e, ok := doc.Entry(w.Key)
	if !ok {
		return StateAbsent, "add " + w.Key, nil
	}
	differs := func() (State, string, *Note) {
		switch w.Src {
		case SourceEnv, SourceEnvFile, SourceManifest, "":
			return StateModified, w.Key + " was edited by hand and differs from what install would write", nil
		}
		return StateOutdated, w.Key + " differs from the value chosen in this run", nil
	}
	kept := func() (State, string, *Note) {
		return StateOK, w.Key + " kept as written", &Note{NoteWarn, fmt.Sprintf(
			"%s line %d: the value cannot be written in the plain format; left untouched, fix it by hand", w.Key, e.Line)}
	}

	if w.Target != nil { // the DSN: compare by connection
		needsRecover := e.Unparseable || !e.Plain
		cur, recovered, ok := parseEnvDSN(e.Value, needsRecover)
		if !ok {
			if e.Unparseable || !e.Plain {
				return kept()
			}
			return differs()
		}
		if !cur.SameConnection(*w.Target) {
			return differs()
		}
		if recovered {
			return StateOutdated, w.Key + ": the password is not URL-encoded; re-encode it", nil
		}
		return StateOK, w.Key + " is set", nil
	}
	if e.Unparseable {
		return kept()
	}
	if e.Value != w.Value {
		return differs()
	}
	return StateOK, w.Key + " is set", nil
}

// worstState is the state the table shows for a step (Detection.State).
func worstState(ss ...State) State {
	rank := map[State]int{StateOK: 0, StateOutdated: 1, StateModified: 2, StateAbsent: 3, StateBlocked: 4}
	w := StateOK
	for _, s := range ss {
		if rank[s] > rank[w] {
			w = s
		}
	}
	return w
}

// envResult is the outcome of applying a set of choices to the file.
type envResult struct {
	after      []byte   // the new content (== before when only the mode changes)
	contentChg bool     // the content differs from what is on disk
	setKeys    []string // managed keys written
	converted  []string // keys whose export/quote form was converted
	modified   bool     // an overwritten artifact was `modified` (a backup is kept)
	chmod      bool     // the mode artifact is chosen
	mkdir      bool     // the config directory artifact is chosen
}

// envApply turns the choices into the next file content, using exactly the
// editor Apply uses, so a Plan diff is what Apply writes.
func (an *envAnalysis) envApply(ch Choices) (envResult, error) {
	r := envResult{after: an.before}
	doc := config.ParseEnvDoc(an.before)
	if ch[envFormatArtifact] == ChoiceApply {
		r.converted = doc.NormalizeAll()
		if len(r.converted) > 0 && an.state[envFormatArtifact] == StateModified {
			r.modified = true
		}
	}
	for _, w := range an.wants {
		id := envKeyArtifact(w.Key)
		if ch[id] != ChoiceApply {
			continue
		}
		if _, err := doc.Set(w.Key, w.Value); err != nil {
			return r, fmt.Errorf("%s: %w", w.Key, err)
		}
		r.setKeys = append(r.setKeys, w.Key)
		if an.state[id] == StateModified {
			r.modified = true
		}
	}
	if len(r.setKeys) > 0 || len(r.converted) > 0 {
		r.after = doc.Marshal()
		r.contentChg = !an.exists || string(r.after) != string(an.before)
	}
	r.chmod = ch[envModeArtifact] == ChoiceApply
	r.mkdir = ch[envDirArtifact] == ChoiceApply
	return r, nil
}

// envDir is the directory of the env file.
func (an *envAnalysis) envDir() string { return filepath.Dir(an.path) }

// ---- Seed helper shared by the owners of env-backed fields -----------------

// EnvSeed is the result of SeedEnvValue.
type EnvSeed struct {
	Value  string
	Source Source
	Found  bool
	Notes  []Note
}

// SeedEnvValue implements the Seed order of Design 16 (v0.5, N10) for one
// env-backed key: flag, then the env file's value, then Env (only when the
// file has no usable value). The owner applies its documented default when
// Found is false. A shell value that differs from the file's is a drift note
// and never a source, so a stale `export` cannot rewrite the file on a no-op
// run. same (may be nil) compares two values semantically, e.g. two DSNs;
// the default is string equality. The value is as the file holds it, without
// `export` and whole-value quotes; an unparseable line is not usable.
func SeedEnvValue(st *RunState, env Env, key, flag string, same func(a, b string) bool) EnvSeed {
	if same == nil {
		same = func(a, b string) bool { return a == b }
	}
	shell := env.Get(key)
	if flag != "" {
		return EnvSeed{Value: flag, Source: SourceFlag, Found: true}
	}
	if st.Prior.EnvDoc != nil {
		if v, ok := config.ParseEnvDoc(st.Prior.EnvDoc).Get(key); ok && v != "" {
			s := EnvSeed{Value: v, Source: SourceEnvFile, Found: true}
			if shell != "" && !same(shell, v) {
				s.Notes = append(s.Notes, Note{NoteWarn, fmt.Sprintf(
					"your shell's %s differs from the env file; hooks, MCP and jobs use the file", key)})
			}
			return s
		}
	}
	if shell != "" {
		return EnvSeed{Value: shell, Source: SourceEnv, Found: true}
	}
	return EnvSeed{}
}
