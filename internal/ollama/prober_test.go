package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOllama is an httptest server speaking the subset of the Ollama API the
// prober uses.
type fakeOllama struct {
	version   string
	models    []string // /api/tags names
	dims      int      // /api/embed vector length
	embedWait time.Duration
	pullLines []string // NDJSON lines /api/pull streams
	pullHold  chan struct{}

	mu        sync.Mutex
	embedReqs []embedRequest
	pullReqs  []map[string]any
}

func (f *fakeOllama) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": f.version})
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		type m struct {
			Name  string `json:"name"`
			Model string `json:"model"`
			Size  int64  `json:"size"`
		}
		var ms []m
		for _, n := range f.models {
			ms = append(ms, m{Name: n, Model: n, Size: 1})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": ms})
	})
	mux.HandleFunc("POST /api/embed", func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.embedReqs = append(f.embedReqs, req)
		f.mu.Unlock()
		if !f.has(req.Model) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"model \"%s\" not found, try pulling it first"}`, req.Model)
			return
		}
		select {
		case <-time.After(f.embedWait):
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{make([]float32, f.dims)}})
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.pullReqs = append(f.pullReqs, req)
		f.mu.Unlock()
		if req["model"] == "nosuch" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"pull model manifest: file does not exist"}`))
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		for _, l := range f.pullLines {
			_, _ = w.Write([]byte(l + "\n"))
			fl.Flush()
		}
		if f.pullHold != nil {
			select {
			case <-f.pullHold:
			case <-r.Context().Done():
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeOllama) has(model string) bool {
	for _, m := range f.models {
		if normalizeModel(m) == normalizeModel(model) {
			return true
		}
	}
	return false
}

func TestProberVersion(t *testing.T) {
	url := (&fakeOllama{version: "0.5.7"}).start(t)
	v, err := Prober{}.Version(context.Background(), url+"/") // trailing slash tolerated
	if err != nil || v != "0.5.7" {
		t.Fatalf("Version = %q, %v", v, err)
	}
}

func TestProberVersionErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("not ollama", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		defer srv.Close()
		_, err := Prober{}.Version(ctx, srv.URL)
		if err == nil || !strings.Contains(err.Error(), "404") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("empty version", func(t *testing.T) {
		url := (&fakeOllama{}).start(t)
		if _, err := (Prober{}).Version(ctx, url); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		if _, err := (Prober{}).Version(ctx, url); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		block := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-block:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(block)
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := Prober{}.Version(ctx, srv.URL)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want deadline exceeded", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Error("Version ignored the context deadline")
		}
	})
}

func TestProberHasModel(t *testing.T) {
	url := (&fakeOllama{models: []string{"bge-m3:latest", "llama3.2:3b", "registry.local:5000/team/embed:latest"}}).start(t)
	ctx := context.Background()
	for model, want := range map[string]bool{
		"bge-m3":                         true,
		"bge-m3:latest":                  true,
		"llama3.2:3b":                    true,
		"llama3.2":                       false, // only :3b is pulled
		"nomic-embed-text":               false,
		"registry.local:5000/team/embed": true,
	} {
		got, err := Prober{}.HasModel(ctx, url, model)
		if err != nil || got != want {
			t.Errorf("HasModel(%q) = %v, %v; want %v", model, got, err, want)
		}
	}
}

func TestProberEmbedDims(t *testing.T) {
	f := &fakeOllama{models: []string{"bge-m3:latest"}, dims: 1024, embedWait: 20 * time.Millisecond}
	url := f.start(t)
	dims, latency, err := Prober{}.EmbedDims(context.Background(), url, "bge-m3")
	if err != nil || dims != 1024 {
		t.Fatalf("EmbedDims = %d, %v", dims, err)
	}
	if latency < 20*time.Millisecond {
		t.Errorf("latency = %v, want >= 20ms", latency)
	}
	// The request is the embedder's: same keep_alive and options, so the
	// probe does not make Ollama reload or unload the model.
	req := f.embedReqs[0]
	if req.Model != "bge-m3" || !req.Truncate || req.KeepAlive != -1 || req.Options.NumCtx != DefaultNumCtx || req.Options.NumBatch != DefaultNumCtx || req.Input == "" {
		t.Errorf("request = %+v", req)
	}

	if _, _, err := (Prober{NumCtx: 512}).EmbedDims(context.Background(), url, "bge-m3"); err != nil {
		t.Fatal(err)
	}
	if got := f.embedReqs[1].Options.NumCtx; got != 512 {
		t.Errorf("num_ctx = %d, want 512", got)
	}
}

func TestProberEmbedDimsWrongDims(t *testing.T) {
	url := (&fakeOllama{models: []string{"nomic-embed-text"}, dims: 768}).start(t)
	dims, _, err := Prober{}.EmbedDims(context.Background(), url, "nomic-embed-text")
	if err != nil || dims != 768 {
		t.Fatalf("EmbedDims = %d, %v; want 768 and no error (the caller compares)", dims, err)
	}
}

func TestProberEmbedDimsModelNotFound(t *testing.T) {
	url := (&fakeOllama{models: []string{"bge-m3"}, dims: 1024}).start(t)
	_, _, err := Prober{}.EmbedDims(context.Background(), url, "nosuch")
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("err = %v, want ErrModelNotFound", err)
	}
	if !strings.Contains(err.Error(), "try pulling it first") {
		t.Errorf("err = %v, want Ollama's message", err)
	}
}

func TestProberEmbedDimsTimeout(t *testing.T) {
	url := (&fakeOllama{models: []string{"bge-m3"}, dims: 1024, embedWait: 5 * time.Second}).start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := Prober{}.EmbedDims(ctx, url, "bge-m3")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("EmbedDims ignored the context deadline")
	}
}

func TestProberEmbedDimsBadResponses(t *testing.T) {
	for name, body := range map[string]string{
		"no embeddings":  `{"embeddings":[]}`,
		"two embeddings": `{"embeddings":[[1],[2]]}`,
		"not json":       `<html>`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		if _, _, err := (Prober{}).EmbedDims(context.Background(), srv.URL, "bge-m3"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		srv.Close()
	}
}

// pullStream is a realistic /api/pull stream: two layers with progress.
var pullStream = []string{
	`{"status":"pulling manifest"}`,
	`{"status":"pulling daec91ffb5dd","digest":"sha256:daec","total":1000,"completed":0}`,
	`{"status":"pulling daec91ffb5dd","digest":"sha256:daec","total":1000,"completed":400}`,
	`{"status":"pulling a406579cd136","digest":"sha256:a406","total":200,"completed":200}`,
	`{"status":"pulling daec91ffb5dd","digest":"sha256:daec","total":1000,"completed":1000}`,
	`{"status":"verifying sha256 digest"}`,
	`{"status":"writing manifest"}`,
	`{"status":"success"}`,
}

func TestProberPull(t *testing.T) {
	f := &fakeOllama{pullLines: pullStream}
	url := f.start(t)
	type pt struct{ done, total int64 }
	var got []pt
	err := Prober{}.Pull(context.Background(), url, "bge-m3", func(done, total int64) {
		got = append(got, pt{done, total})
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	want := []pt{{0, 1000}, {400, 1000}, {600, 1200}, {1200, 1200}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("progress = %v, want %v", got, want)
	}
	if req := f.pullReqs[0]; req["model"] != "bge-m3" || req["stream"] != true {
		t.Errorf("request = %v", req)
	}
	// A nil progress callback is allowed.
	if err := (Prober{}).Pull(context.Background(), url, "bge-m3", nil); err != nil {
		t.Errorf("Pull(nil progress): %v", err)
	}
}

func TestProberPullErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("error line", func(t *testing.T) {
		url := (&fakeOllama{pullLines: []string{`{"status":"pulling manifest"}`, `{"error":"max retries exceeded: unexpected EOF"}`}}).start(t)
		err := Prober{}.Pull(ctx, url, "bge-m3", nil)
		if err == nil || !strings.Contains(err.Error(), "max retries exceeded") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("stream ends without success", func(t *testing.T) {
		url := (&fakeOllama{pullLines: pullStream[:3]}).start(t)
		err := Prober{}.Pull(ctx, url, "bge-m3", nil)
		if err == nil || !strings.Contains(err.Error(), "without success") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("bad line", func(t *testing.T) {
		url := (&fakeOllama{pullLines: []string{"garbage"}}).start(t)
		if err := (Prober{}).Pull(ctx, url, "bge-m3", nil); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("404", func(t *testing.T) {
		url := (&fakeOllama{}).start(t)
		err := Prober{}.Pull(ctx, url, "nosuch", nil)
		if !errors.Is(err, ErrModelNotFound) || !strings.Contains(err.Error(), "file does not exist") {
			t.Errorf("err = %v", err)
		}
	})
}

// Cancelling the context stops a pull that is still streaming.
func TestProberPullCancel(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	url := (&fakeOllama{pullLines: pullStream[:3], pullHold: hold}).start(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	var once sync.Once
	go func() {
		errc <- Prober{}.Pull(ctx, url, "bge-m3", func(done, total int64) {
			if done == 400 {
				once.Do(cancel) // cancel mid-stream
			}
		})
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pull did not return after cancel")
	}
}

func TestNormalizeModel(t *testing.T) {
	for in, want := range map[string]string{
		"bge-m3":                "bge-m3:latest",
		"bge-m3:latest":         "bge-m3:latest",
		"bge-m3:567m":           "bge-m3:567m",
		"host:5000/ns/model":    "host:5000/ns/model:latest",
		"host:5000/ns/model:v1": "host:5000/ns/model:v1",
		"":                      "",
	} {
		if got := normalizeModel(in); got != want {
			t.Errorf("normalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}
