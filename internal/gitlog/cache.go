package gitlog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"claude-memory/internal/memory"
)

// Verdict is a cached staleness result.
type Verdict string

const (
	VerdictFresh     Verdict = "fresh"
	VerdictStale     Verdict = "stale"
	VerdictUnchecked Verdict = "unchecked"
)

// Entry is one cached result; N is the commit count (0 = unknown).
type Entry struct {
	V Verdict `json:"v"`
	N int     `json:"n,omitempty"`
}

// Store holds cached entries. Keys are built by Cached.
type Store interface {
	Get(key string) (Entry, bool)
	Put(key string, e Entry)
}

// budgetSlack is how far below the ceiling a context's remaining time must
// be before the timeout is attributed to the caller's budget, not to git.
const budgetSlack = 5 * time.Millisecond

const keySep = "\x00"

func cacheKey(dir, head, sha string, files []string) string {
	fs := append([]string(nil), files...)
	sort.Strings(fs)
	return strings.Join([]string{dir, head, sha, strings.Join(fs, "\n")}, keySep)
}

// keyHead extracts H from a key.
func keyHead(key string) string {
	parts := strings.SplitN(key, keySep, 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[1]
}

// MapCache is an in-process Store (serve; hook without a usable session id).
type MapCache struct {
	mu sync.Mutex
	m  map[string]Entry
}

func NewMapCache() *MapCache { return &MapCache{m: map[string]Entry{}} }

func (c *MapCache) Get(key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	return e, ok
}

func (c *MapCache) Put(key string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = e
}

// FileCache is a per-session Store persisted as JSON (0600). It loads once
// (a corrupt file reads as empty), writes back atomically only when a new
// entry is added, and keeps only entries for the HEAD of the entry being
// written, so the file cannot grow across commits.
type FileCache struct {
	path string
	mu   sync.Mutex
	m    map[string]Entry
	load bool
}

func NewFileCache(path string) *FileCache { return &FileCache{path: path} }

func (c *FileCache) ensure() {
	if c.load {
		return
	}
	c.load = true
	c.m = map[string]Entry{}
	if b, err := os.ReadFile(c.path); err == nil {
		var m map[string]Entry
		if json.Unmarshal(b, &m) == nil {
			c.m = m
		}
	}
}

func (c *FileCache) Get(key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	e, ok := c.m[key]
	return e, ok
}

func (c *FileCache) Put(key string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	c.m[key] = e
	head := keyHead(key)
	for k := range c.m {
		if keyHead(k) != head {
			delete(c.m, k)
		}
	}
	c.flush()
}

// flush writes the file atomically; failures are ignored (cache only).
func (c *FileCache) flush() {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(c.m)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".stale-*.json")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return
	}
	if tmp.Close() != nil || os.Chmod(tmp.Name(), 0o600) != nil {
		return
	}
	_ = os.Rename(tmp.Name(), c.path)
}

// Cached wraps inner so Changed results are memoized in store. Resolve, Head
// and Dirty pass through (they are cheap or must be live).
//
// Negative caching: errors and ceiling timeouts are cached as unchecked, so
// a doomed git walk is not repeated on every prompt of a session — except a
// timeout whose context had less than ceiling left when the call began
// (shortened by the caller's overall budget), which proves nothing about
// git's speed and is not cached. An already-expired context returns cache
// hits and an uncached error for misses, without starting git.
func Cached(inner memory.CodeHistory, store Store, ceiling time.Duration) memory.CodeHistory {
	return &cached{inner: inner, store: store, ceiling: ceiling}
}

type cached struct {
	inner   memory.CodeHistory
	store   Store
	ceiling time.Duration
}

func (c *cached) Resolve(ctx context.Context, cwd string) (memory.Checkout, string, bool, error) {
	return c.inner.Resolve(ctx, cwd)
}
func (c *cached) Head(ctx context.Context, dir string) (string, error) {
	return c.inner.Head(ctx, dir)
}
func (c *cached) Dirty(ctx context.Context, dir string, files []string) (bool, error) {
	return c.inner.Dirty(ctx, dir, files)
}

func (c *cached) Changed(ctx context.Context, dir, head, sha string, files []string) (bool, int, error) {
	key := cacheKey(dir, head, sha, files)
	if e, ok := c.store.Get(key); ok {
		switch e.V {
		case VerdictFresh:
			return false, 0, nil
		case VerdictStale:
			return true, e.N, nil
		default:
			return false, 0, ErrUnchecked
		}
	}
	if err := ctx.Err(); err != nil {
		return false, 0, err // budget spent: cache hits only, nothing cached
	}

	// The caller computes its deadline a moment before calling, so allow a
	// little slack before calling the budget "shortened".
	budgetShortened := false
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < c.ceiling-budgetSlack {
		budgetShortened = true
	}

	changed, n, err := c.inner.Changed(ctx, dir, head, sha, files)
	switch {
	case err == nil && changed:
		c.store.Put(key, Entry{V: VerdictStale, N: n})
	case err == nil:
		c.store.Put(key, Entry{V: VerdictFresh})
	default:
		timedOut := errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
		if !(timedOut && budgetShortened) {
			c.store.Put(key, Entry{V: VerdictUnchecked})
		}
	}
	return changed, n, err
}
