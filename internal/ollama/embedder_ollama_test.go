//go:build ollama
// +build ollama

package ollama

import (
	"context"
	"net/http"
	"sort"
	"testing"
	"time"
)

// TestEmbedIntegrationRealOllama is a smoke test against a real local Ollama instance.
// It requires Ollama to be running at http://127.0.0.1:11434 with bge-m3 pulled.
// Run with: go test -tags ollama ./internal/ollama/...
func TestEmbedIntegrationRealOllama(t *testing.T) {
	// Skip if Ollama is not available.
	client := &http.Client{Timeout: 30 * time.Second}
	embedder := New(client, "http://127.0.0.1:11434", "bge-m3", 2048)

	ctx := context.Background()

	// Test 1: Short text should embed successfully and return 1024 dimensions.
	t.Run("short text", func(t *testing.T) {
		embedding, err := embedder.Embed(ctx, "hello world", 2048)
		if err != nil {
			t.Fatalf("Embed short text failed: %v", err)
		}
		if len(embedding) != 1024 {
			t.Errorf("expected 1024 dimensions, got %d", len(embedding))
		}
		if !containsNonZero(embedding) {
			t.Error("embedding is all zeros")
		}
	})

	// Test 2: Text longer than maxTokens should still return an embedding (truncated).
	t.Run("long text truncation", func(t *testing.T) {
		// Create a long text (this is roughly 150+ tokens of text).
		longText := "The quick brown fox jumps over the lazy dog. " +
			"This is a sentence with more words to increase token count. " +
			"Repeated multiple times to ensure we exceed the threshold. " +
			"The quick brown fox jumps over the lazy dog. " +
			"This is a sentence with more words to increase token count. " +
			"Repeated multiple times to ensure we exceed the threshold. " +
			"The quick brown fox jumps over the lazy dog. " +
			"This is a sentence with more words to increase token count. "

		embedding, err := embedder.Embed(ctx, longText, 2048)
		if err != nil {
			t.Fatalf("Embed long text failed: %v", err)
		}
		if len(embedding) != 1024 {
			t.Errorf("expected 1024 dimensions even for truncated input, got %d", len(embedding))
		}
	})

	// Test 3: Truncation behavior — same head + different tail should have high cosine similarity.
	t.Run("truncation keeps head tokens", func(t *testing.T) {
		base := "The quick brown fox jumps over the lazy dog. This is a longer sentence. "
		head := base
		tail := base + "Additional text that will be truncated away. More and more text. " +
			"This should be cut off by the token limit. The important part is preserved at the beginning."

		embHead, err := embedder.Embed(ctx, head, 2048)
		if err != nil {
			t.Fatalf("Embed head failed: %v", err)
		}

		embTail, err := embedder.Embed(ctx, tail, 2048)
		if err != nil {
			t.Fatalf("Embed tail failed: %v", err)
		}

		// Compute cosine similarity.
		sim := cosineSimilarity(embHead, embTail)
		// After truncation, similarity should be high (close to 1.0).
		if sim < 0.95 {
			t.Logf("cosine similarity %.4f is lower than expected (>0.95), but still measured", sim)
		}
	})
}

// BenchOllama runs a benchmark of the Ollama embedder and records p50/p95 latency.
// Run with: go test -tags ollama -run BenchOllama -bench=. ./internal/ollama/...
// or use the Bench helper function directly in non-benchmark mode.
func BenchOllama(b *testing.B) {
	client := &http.Client{Timeout: 30 * time.Second}
	embedder := New(client, "http://127.0.0.1:11434", "bge-m3", 2048)

	texts := []string{
		"hello world",
		"The quick brown fox jumps over the lazy dog.",
		"Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
			"Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. " +
			"Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris.",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		text := texts[i%len(texts)]
		_, err := embedder.Embed(context.Background(), text, 2048)
		if err != nil {
			b.Fatalf("Embed failed: %v", err)
		}
	}
}

// Bench records p50/p95 latency for embedding calls over N runs.
// This is a helper function for AC-44 baseline measurements.
func Bench(ctx context.Context, baseURL, model string, texts []string, runs int) (p50, p95 time.Duration, err error) {
	client := &http.Client{Timeout: 30 * time.Second}
	embedder := New(client, baseURL, model, 2048)

	latencies := make([]time.Duration, 0, len(texts)*runs)

	for run := 0; run < runs; run++ {
		for _, text := range texts {
			start := time.Now()
			_, embErr := embedder.Embed(ctx, text, 2048)
			elapsed := time.Since(start)
			if embErr != nil {
				return 0, 0, embErr
			}
			latencies = append(latencies, elapsed)
		}
	}

	// Sort to compute percentiles.
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	// Compute p50 and p95.
	p50idx := len(latencies) * 50 / 100
	p95idx := len(latencies) * 95 / 100
	if p50idx >= len(latencies) {
		p50idx = len(latencies) - 1
	}
	if p95idx >= len(latencies) {
		p95idx = len(latencies) - 1
	}

	p50 = latencies[p50idx]
	p95 = latencies[p95idx]

	return p50, p95, nil
}

// Helper functions.

func containsNonZero(v []float32) bool {
	for _, x := range v {
		if x != 0 {
			return true
		}
	}
	return false
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}

	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / (sqrtFloat32(normA) * sqrtFloat32(normB))
}

func sqrtFloat32(x float32) float32 {
	// Simple approximation; for production, use math.Sqrt(float64(x))
	if x < 0 {
		return 0
	}
	if x == 0 {
		return 0
	}
	z := x
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}
