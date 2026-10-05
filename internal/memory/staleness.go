package memory

import (
	"context"
	"log/slog"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redfoxius/claude-memory/internal/record"
)

// maxStaleChecksPerSearch bounds how many of a search's top results are
// staleness-checked.
const maxStaleChecksPerSearch = 10

// maxStaleFiles bounds the files handed to git per record.
const maxStaleFiles = 20

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Checkout identifies the git working tree the process operates in: its top
// level directory and the repo name (basename of that directory) that
// records are scoped by.
type Checkout struct {
	Dir  string
	Repo string
}

// CodeHistory is the port through which the service asks git about the
// working tree. The concrete adapter (internal/gitlog) is wired in
// cmd/claude-memory/main.go; the service never runs git itself.
type CodeHistory interface {
	// Resolve finds the checkout containing cwd and its current HEAD in one
	// call. ok is false when cwd is not inside a checkout. head is empty on
	// an unborn HEAD.
	Resolve(ctx context.Context, cwd string) (co Checkout, head string, ok bool, err error)

	// Head returns the current HEAD commit of the checkout at dir.
	Head(ctx context.Context, dir string) (string, error)

	// Changed reports whether any of files (already normalized,
	// dir-relative) differs between commit sha and head. commits is the
	// number of commits touching them in sha..head (0 = unknown, capped at
	// 100). An error means "unchecked".
	Changed(ctx context.Context, dir, head, sha string, files []string) (changed bool, commits int, err error)

	// Dirty reports whether any of files has uncommitted changes (modified,
	// staged or untracked) relative to HEAD.
	Dirty(ctx context.Context, dir string, files []string) (bool, error)
}

// StaleHint marks a record whose files changed since it was recorded.
// Commits is the number of commits touching them (0 = unknown).
type StaleHint struct {
	Commits int
}

// NormalizeFiles turns a record's file list into the dir-relative paths git
// is asked about: a trailing ":N" / ":N-M" line suffix is stripped, absolute
// paths under dir become relative, paths outside dir (or escaping it) and
// empty entries are dropped, duplicates removed, at most 20 kept. It is pure
// (no I/O).
func NormalizeFiles(dir string, files []string) []string {
	out := make([]string, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, f := range files {
		f = stripLineSuffix(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		if filepath.IsAbs(f) {
			rel, err := filepath.Rel(dir, f)
			if err != nil {
				continue
			}
			f = rel
		}
		f = path.Clean(filepath.ToSlash(f))
		if f == "." || f == ".." || strings.HasPrefix(f, "../") || strings.HasPrefix(f, "/") {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
		if len(out) == maxStaleFiles {
			break
		}
	}
	return out
}

// stripLineSuffix removes a trailing ":N" or ":N-M".
func stripLineSuffix(f string) string {
	i := strings.LastIndex(f, ":")
	if i <= 0 {
		return f
	}
	tail := f[i+1:]
	if lo, hi, ok := strings.Cut(tail, "-"); ok {
		if isDigits(lo) && isDigits(hi) {
			return f[:i]
		}
		return f
	}
	if isDigits(tail) {
		return f[:i]
	}
	return f
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

// --- Service wiring (copy-on-write, like WithNamespace) ---

// WithCheckout returns a copy of the service that knows its git checkout.
func (s *Service) WithCheckout(co Checkout) *Service {
	c := *s
	c.checkout = &co
	return &c
}

// WithPinnedHead returns a copy that uses head as HEAD for every check
// instead of reading it per call (one-shot processes such as the hook).
func (s *Service) WithPinnedHead(head string) *Service {
	c := *s
	c.pinnedHead = head
	c.headPinned = true // even when empty (unborn HEAD): no re-read
	return &c
}

// WithCodeHistory returns a copy that checks staleness through h, with
// ceiling as the per-call time budget.
func (s *Service) WithCodeHistory(h CodeHistory, ceiling time.Duration) *Service {
	c := *s
	c.history = h
	c.staleCeiling = ceiling
	return &c
}

// WithStaleDeadline returns a copy whose staleness checks never run past t
// (the hook's overall latency budget). The zero time means no extra bound.
func (s *Service) WithStaleDeadline(t time.Time) *Service {
	c := *s
	c.staleDeadline = t
	return &c
}

// staleInputs reports whether rec can be staleness-checked at all and, if
// so, the normalized files. Decided here, without calling the adapter.
func (s *Service) staleInputs(repo, sha string, files []string) ([]string, bool) {
	if s.history == nil || s.checkout == nil {
		return nil, false
	}
	if repo == "" || repo == "*" || repo != s.checkout.Repo || !shaRe.MatchString(sha) {
		return nil, false
	}
	norm := NormalizeFiles(s.checkout.Dir, files)
	return norm, len(norm) > 0
}

// currentHead returns the pinned HEAD, or reads it from git once.
func (s *Service) currentHead(ctx context.Context) string {
	if s.headPinned {
		return s.pinnedHead
	}
	h, err := s.history.Head(ctx, s.checkout.Dir)
	if err != nil {
		return ""
	}
	return h
}

// deadline is the instant by which this call's checks must finish.
func (s *Service) staleDeadlineFor(now time.Time) time.Time {
	d := now.Add(s.staleCeiling)
	if !s.staleDeadline.IsZero() && s.staleDeadline.Before(d) {
		d = s.staleDeadline
	}
	return d
}

type staleResult struct {
	idx     int
	changed bool
	commits int
	checked bool
}

// annotateStale sets Stale on the top results whose files changed since
// they were recorded. Best-effort: anything that cannot be checked in time
// is left unflagged; it never returns an error.
func (s *Service) annotateStale(ctx context.Context, recs []*SearchRecord, minSimilarity float64) {
	if s.history == nil || s.checkout == nil || len(recs) == 0 {
		return
	}
	if len(recs) > maxStaleChecksPerSearch {
		recs = recs[:maxStaleChecksPerSearch]
	}

	type job struct {
		idx   int
		files []string
	}
	var jobs []job
	for i, r := range recs {
		// Results the caller will not show (below its similarity floor)
		// are never checked: no git for a result that cannot become a card.
		if r.Similarity < minSimilarity {
			continue
		}
		if files, ok := s.staleInputs(r.Repo, r.CommitSHA, r.Files); ok {
			jobs = append(jobs, job{i, files})
		}
	}
	if len(jobs) == 0 {
		return
	}

	deadline := s.staleDeadlineFor(time.Now())
	cctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	head := s.currentHead(cctx)
	if head == "" {
		return
	}

	results := make(chan staleResult, len(jobs))
	for _, j := range jobs {
		go func(j job) {
			changed, commits, err := s.history.Changed(cctx, s.checkout.Dir, head, recs[j.idx].CommitSHA, j.files)
			if err != nil {
				changed = false
			}
			results <- staleResult{idx: j.idx, changed: changed, commits: commits, checked: err == nil}
		}(j)
	}

	// Wait for every check, or give up shortly after the deadline even if an
	// adapter ignores its context.
	wait := time.Until(deadline)
	if wait < 0 {
		wait = 0 // budget already spent: still collect results that are ready (cache hits)
	}
	grace := time.NewTimer(wait + 10*time.Millisecond)
	defer grace.Stop()
	for range jobs {
		select {
		case res := <-results:
			recs[res.idx].StaleChecked = res.checked
			if res.changed {
				recs[res.idx].Stale = &StaleHint{Commits: res.commits}
			}
		case <-grace.C:
			return
		}
	}
}

// StaleHint checks a single record (memory_get). It returns nil when the
// record is fresh or cannot be checked.
func (s *Service) StaleHint(ctx context.Context, rec *record.Record) *StaleHint {
	sha := ""
	if rec.CommitSHA != nil {
		sha = *rec.CommitSHA
	}
	files, ok := s.staleInputs(rec.Repo, sha, rec.Files)
	if !ok {
		return nil
	}
	cctx, cancel := context.WithDeadline(ctx, s.staleDeadlineFor(time.Now()))
	defer cancel()
	head := s.currentHead(cctx)
	if head == "" {
		return nil
	}
	changed, commits, err := s.history.Changed(cctx, s.checkout.Dir, head, sha, files)
	if err != nil || !changed {
		return nil
	}
	return &StaleHint{Commits: commits}
}

// baselineSHA returns the commit to record as a record's baseline, or "" when
// none may be stamped: there must be a checkout and history, the record's
// repo must be the checkout's repo, at least one usable file must exist, HEAD
// must be known, and the listed files must have no uncommitted changes
// (otherwise HEAD predates the code the record describes and the first
// commit would flag it stale). Any failure leaves the baseline empty.
func (s *Service) baselineSHA(ctx context.Context, repo string, files []string) string {
	if s.history == nil || s.checkout == nil || repo == "" || repo == "*" || repo != s.checkout.Repo {
		return ""
	}
	norm := NormalizeFiles(s.checkout.Dir, files)
	if len(norm) == 0 {
		return ""
	}
	head := s.currentHead(ctx)
	if head == "" || !shaRe.MatchString(head) {
		return ""
	}
	dirty, err := s.history.Dirty(ctx, s.checkout.Dir, norm)
	if err != nil || dirty {
		slog.DebugContext(ctx, "commit baseline not stamped", "dirty", dirty, "error", err)
		return ""
	}
	return head
}

// ValidCommitSHA reports whether sha is acceptable as a stored commit_sha.
func ValidCommitSHA(sha string) bool { return shaRe.MatchString(sha) }

// Checkout reports the checkout the service was given, if any.
func (s *Service) Checkout() (Checkout, bool) {
	if s.checkout == nil {
		return Checkout{}, false
	}
	return *s.checkout, true
}
