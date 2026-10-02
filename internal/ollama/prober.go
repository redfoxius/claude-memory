package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claude-memory/internal/setup"
)

// Prober is the setup.OllamaProber adapter (spec AC-32, plan WI-S1-5): it
// talks to the Ollama HTTP API at a URL given per call. Version, HasModel
// and EmbedDims are bounded by the caller's context; Pull streams for as
// long as the download takes and stops when ctx is cancelled.
type Prober struct {
	// Client sends the requests. It should have no Timeout (a pull can take
	// minutes); deadlines come from the context. nil = a default client.
	Client *http.Client
	// NumCtx is the num_ctx/num_batch EmbedDims sends. It should match the
	// embedder's (MEMORY_EMBED_MAX_TOKENS): Ollama reloads a model whose
	// options change, which would slow the next hook. 0 = DefaultNumCtx.
	NumCtx int
}

// DefaultNumCtx is the default of MEMORY_EMBED_MAX_TOKENS (internal/config).
const DefaultNumCtx = 2048

// ErrModelNotFound is wrapped by EmbedDims and Pull when Ollama answers 404
// for the model (it is not pulled, or does not exist in the registry).
var ErrModelNotFound = errors.New("ollama: model not found")

var _ setup.OllamaProber = Prober{}

// maxAPIBody bounds the JSON bodies read from version, tags and embed
// responses (one 1024-float embedding is ~20 KiB).
const maxAPIBody = 8 << 20

func (p Prober) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

// Version calls GET /api/version and returns the server version.
func (p Prober) Version(ctx context.Context, url string) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := p.getJSON(ctx, url, "/api/version", &v); err != nil {
		return "", err
	}
	if v.Version == "" {
		return "", errors.New("ollama: /api/version returned no version")
	}
	return v.Version, nil
}

// HasModel reports whether GET /api/tags lists model. A model without a tag
// matches its ":latest" tag ("bge-m3" ≡ "bge-m3:latest").
func (p Prober) HasModel(ctx context.Context, url, model string) (bool, error) {
	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := p.getJSON(ctx, url, "/api/tags", &tags); err != nil {
		return false, err
	}
	want := normalizeModel(model)
	for _, m := range tags.Models {
		if normalizeModel(m.Name) == want || normalizeModel(m.Model) == want {
			return true, nil
		}
	}
	return false, nil
}

// normalizeModel adds the implicit ":latest" tag.
func normalizeModel(m string) string {
	if m == "" {
		return ""
	}
	// A ':' after the last '/' is a tag; one before it is a registry port.
	if !strings.Contains(m[strings.LastIndex(m, "/")+1:], ":") {
		return m + ":latest"
	}
	return m
}

// pullLine is one NDJSON line of the POST /api/pull stream.
type pullLine struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error"`
}

// Pull runs POST /api/pull with streaming and calls progress (may be nil)
// with the bytes done and the total over all layers seen so far, after each
// progress line. It returns nil only when the stream ends with
// status "success"; an "error" line, a stream that ends early, a non-200
// status or a cancelled ctx is an error.
func (p Prober) Pull(ctx context.Context, url, model string, progress func(done, total int64)) error {
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	resp, err := p.do(ctx, http.MethodPost, url, "/api/pull", body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return statusError("/api/pull", model, resp)
	}

	type layer struct{ done, total int64 }
	layers := map[string]layer{}
	var order []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var l pullLine
		if err := json.Unmarshal(line, &l); err != nil {
			return fmt.Errorf("ollama: /api/pull: bad stream line: %w", err)
		}
		if l.Error != "" {
			return fmt.Errorf("ollama: pull %s: %s", model, l.Error)
		}
		if l.Status == "success" {
			return nil
		}
		if l.Digest != "" && l.Total > 0 {
			if _, seen := layers[l.Digest]; !seen {
				order = append(order, l.Digest)
			}
			layers[l.Digest] = layer{done: min(l.Completed, l.Total), total: l.Total}
			if progress != nil {
				var done, total int64
				for _, d := range order {
					done += layers[d].done
					total += layers[d].total
				}
				progress(done, total)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ollama: pull %s: %w", model, err)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("ollama: pull %s: read stream: %w", model, err)
	}
	return fmt.Errorf("ollama: pull %s: stream ended without success", model)
}

// EmbedDims embeds one short text with POST /api/embed (the request the
// embedder sends) and returns the vector length and the request latency.
// It does not check the length: the caller compares it with the schema's
// 1024.
func (p Prober) EmbedDims(ctx context.Context, url, model string) (int, time.Duration, error) {
	numCtx := p.NumCtx
	if numCtx <= 0 {
		numCtx = DefaultNumCtx
	}
	body, err := json.Marshal(embedRequest{
		Model:     model,
		Input:     "claude-memory doctor: embedding dimension check",
		Truncate:  true,
		KeepAlive: -1, // as the embedder; another value would reset the loaded model's keep-alive
		Options:   embedOptions{NumCtx: numCtx, NumBatch: numCtx},
	})
	if err != nil {
		return 0, 0, fmt.Errorf("ollama: marshal request: %w", err)
	}
	start := time.Now()
	resp, err := p.do(ctx, http.MethodPost, url, "/api/embed", body)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, statusError("/api/embed", model, resp)
	}
	var er embedResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIBody)).Decode(&er); err != nil {
		return 0, 0, fmt.Errorf("ollama: /api/embed: parse response: %w", err)
	}
	latency := time.Since(start)
	if len(er.Embeddings) != 1 {
		return 0, latency, fmt.Errorf("ollama: /api/embed: expected 1 embedding, got %d", len(er.Embeddings))
	}
	return len(er.Embeddings[0]), latency, nil
}

// ---- HTTP helpers ----------------------------------------------------------

func (p Prober) do(ctx context.Context, method, base, path string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, r)
	if err != nil {
		return nil, fmt.Errorf("ollama: construct request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: %s %s: %w", method, path, err)
	}
	return resp, nil
}

func (p Prober) getJSON(ctx context.Context, base, path string, v any) error {
	resp, err := p.do(ctx, http.MethodGet, base, path, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return statusError(path, "", resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIBody)).Decode(v); err != nil {
		return fmt.Errorf("ollama: %s: parse response: %w", path, err)
	}
	return nil
}

// statusError describes a non-200 answer, with Ollama's {"error": ...}
// message when there is one. A 404 for a model wraps ErrModelNotFound.
func statusError(path, model string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	msg := strings.TrimSpace(string(b))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	if resp.StatusCode == http.StatusNotFound && model != "" {
		return fmt.Errorf("%w: %s: %s", ErrModelNotFound, model, msg)
	}
	if msg == "" {
		return fmt.Errorf("ollama: %s: unexpected status %d", path, resp.StatusCode)
	}
	return fmt.Errorf("ollama: %s: unexpected status %d: %s", path, resp.StatusCode, msg)
}
