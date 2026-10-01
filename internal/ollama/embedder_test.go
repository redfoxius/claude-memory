package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestEmbedRequestBody verifies the request has the correct shape,
// including truncate=true and options with num_ctx and num_batch.
func TestEmbedRequestBody(t *testing.T) {
	var capturedReq embedRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the request body.
		if err := json.NewDecoder(r.Body).Decode(&capturedReq); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Return a valid response.
		resp := embedResponse{
			Embeddings: [][]float32{make([]float32, 1024)},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	embedder := New(client, server.URL, "bge-m3", 2048)

	ctx := context.Background()
	_, err := embedder.Embed(ctx, "test input", 2048)
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	// Verify request body.
	if capturedReq.Model != "bge-m3" {
		t.Errorf("expected model bge-m3, got %s", capturedReq.Model)
	}
	if capturedReq.Input != "test input" {
		t.Errorf("expected input 'test input', got %s", capturedReq.Input)
	}
	if !capturedReq.Truncate {
		t.Error("expected Truncate=true")
	}
	if capturedReq.Options.NumCtx != 2048 {
		t.Errorf("expected NumCtx=2048, got %d", capturedReq.Options.NumCtx)
	}
	if capturedReq.Options.NumBatch != 2048 {
		t.Errorf("expected NumBatch=2048, got %d", capturedReq.Options.NumBatch)
	}
}

// TestEmbedDimensionCheck verifies that an embedding of exactly 1024 dimensions is validated.
func TestEmbedDimensionCheck(t *testing.T) {
	tests := []struct {
		name    string
		dims    int
		wantErr bool
	}{
		{"valid 1024", 1024, false},
		{"too few dimensions", 512, true},
		{"too many dimensions", 2048, true},
		{"zero dimensions", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				embedding := make([]float32, tt.dims)
				resp := embedResponse{
					Embeddings: [][]float32{embedding},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			client := &http.Client{Timeout: 5 * time.Second}
			embedder := New(client, server.URL, "bge-m3", 2048)

			_, err := embedder.Embed(context.Background(), "test", 2048)
			if (err != nil) != tt.wantErr {
				t.Errorf("Embed error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestEmbedNon200Status verifies error handling for non-200 responses.
func TestEmbedNon200Status(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	embedder := New(client, server.URL, "bge-m3", 2048)

	_, err := embedder.Embed(context.Background(), "test", 2048)
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	if _, ok := err.(interface{ Error() string }); !ok {
		t.Fatalf("expected error interface, got %T", err)
	}
}

// TestEmbedContextDeadline verifies that context deadlines are respected.
func TestEmbedContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow server by sleeping longer than the context deadline.
		time.Sleep(2 * time.Second)
		resp := embedResponse{
			Embeddings: [][]float32{make([]float32, 1024)},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	embedder := New(client, server.URL, "bge-m3", 2048)

	// Create a context with a very short deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := embedder.Embed(ctx, "test", 2048)
	if err == nil {
		t.Fatal("expected context deadline exceeded error")
	}
	// Check that it's a context deadline error.
	if err != context.DeadlineExceeded {
		// The actual error might be wrapped, but it should contain context-related info.
		t.Logf("got error: %v (expected context.DeadlineExceeded)", err)
	}
}

// TestEmbedMalformedResponse verifies error handling for invalid JSON.
func TestEmbedMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	embedder := New(client, server.URL, "bge-m3", 2048)

	_, err := embedder.Embed(context.Background(), "test", 2048)
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
}

// TestEmbedNoEmbeddings verifies error when response has no embeddings.
func TestEmbedNoEmbeddings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{Embeddings: [][]float32{}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	embedder := New(client, server.URL, "bge-m3", 2048)

	_, err := embedder.Embed(context.Background(), "test", 2048)
	if err == nil {
		t.Fatal("expected error for zero embeddings")
	}
}

// TestEmbedMultipleEmbeddings verifies error when response has multiple embeddings.
func TestEmbedMultipleEmbeddings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{
			Embeddings: [][]float32{
				make([]float32, 1024),
				make([]float32, 1024),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	embedder := New(client, server.URL, "bge-m3", 2048)

	_, err := embedder.Embed(context.Background(), "test", 2048)
	if err == nil {
		t.Fatal("expected error for multiple embeddings")
	}
}

// TestEmbedUnreachable verifies error handling when the server is unreachable.
func TestEmbedUnreachable(t *testing.T) {
	// Use an invalid URL that will fail immediately.
	client := &http.Client{Timeout: 5 * time.Second}
	embedder := New(client, "http://127.0.0.1:1", "bge-m3", 2048)

	_, err := embedder.Embed(context.Background(), "test", 2048)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

// TestEmbedMaxTokensPassthrough verifies that the maxTokens parameter is passed to Ollama.
func TestEmbedMaxTokensPassthrough(t *testing.T) {
	var capturedMaxTokens int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		capturedMaxTokens = req.Options.NumCtx
		resp := embedResponse{
			Embeddings: [][]float32{make([]float32, 1024)},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	embedder := New(client, server.URL, "bge-m3", 4096)

	_, err := embedder.Embed(context.Background(), "test", 4096)
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	if capturedMaxTokens != 4096 {
		t.Errorf("expected maxTokens 4096, got %d", capturedMaxTokens)
	}
}
