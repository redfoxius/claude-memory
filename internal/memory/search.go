package memory

import (
	"context"
	"log/slog"
)

// embedQueryText embeds a search query string using the embedding provider.
// If embedding fails, it returns nil, signaling the caller to fall back to full-text-only (AC-7).
// This function is called by Search when no pre-computed embedding is provided.
func (s *Service) embedQueryText(ctx context.Context, text string) []float32 {
	embedding, err := s.embeddingProvider.Embed(ctx, text, s.cfg.EmbedMaxTokens)
	if err != nil {
		// Embedding failed; return nil so the store falls back to full-text-only.
		slog.WarnContext(ctx, "embedding provider unavailable", "error", err)
		return nil
	}
	return embedding
}
