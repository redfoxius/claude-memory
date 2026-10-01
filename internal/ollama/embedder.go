// Package ollama provides the Ollama embedding provider adapter.
// It implements the memory.EmbeddingProvider interface for bge-m3 embeddings.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Embedder implements memory.EmbeddingProvider using a local Ollama instance.
type Embedder struct {
	client    *http.Client
	baseURL   string
	model     string
	maxTokens int
}

// New constructs a new Ollama embedder.
// The client is assumed to be properly configured with timeouts.
// baseURL should be the Ollama service URL (e.g., http://127.0.0.1:11434).
// model is the model name (e.g., bge-m3).
// maxTokens is the maximum token count to pass to Ollama (num_ctx, num_batch).
func New(client *http.Client, baseURL, model string, maxTokens int) *Embedder {
	return &Embedder{
		client:    client,
		baseURL:   baseURL,
		model:     model,
		maxTokens: maxTokens,
	}
}

// embedRequest is the request body for the Ollama /api/embed endpoint.
type embedRequest struct {
	Model   string      `json:"model"`
	Input   string      `json:"input"`
	Truncate bool       `json:"truncate"`
	Options embedOptions `json:"options"`
}

// embedOptions configures embedding truncation and context size.
type embedOptions struct {
	NumCtx   int `json:"num_ctx"`
	NumBatch int `json:"num_batch"`
}

// embedResponse is the response body from the Ollama /api/embed endpoint.
type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed returns a 1024-dimensional embedding for the given text.
// If the text exceeds maxTokens, Ollama truncates it keeping the leading tokens.
// Returns an error if the provider is unreachable, returns a non-200 status,
// returns a malformed response, or does not return exactly one 1024-dimensional embedding.
func (e *Embedder) Embed(ctx context.Context, text string, maxTokens int) ([]float32, error) {
	// Respect the provided context deadline.
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/api/embed", e.baseURL), nil)
	if err != nil {
		return nil, fmt.Errorf("ollama: construct request: %w", err)
	}

	// Build the request body.
	body := embedRequest{
		Model:    e.model,
		Input:    text,
		Truncate: true,
		Options: embedOptions{
			NumCtx:   maxTokens,
			NumBatch: maxTokens,
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	// Send the request.
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Check the HTTP status code.
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the response.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ollama: read response: %w", err)
	}

	var embResp embedResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("ollama: parse response: %w", err)
	}

	// Validate: exactly one embedding.
	if len(embResp.Embeddings) != 1 {
		return nil, fmt.Errorf("ollama: expected 1 embedding, got %d", len(embResp.Embeddings))
	}

	embedding := embResp.Embeddings[0]

	// Validate: exactly 1024 dimensions.
	if len(embedding) != 1024 {
		return nil, fmt.Errorf("ollama: expected 1024-dim embedding, got %d", len(embedding))
	}

	return embedding, nil
}
