package main

import (
	"context"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/mcpserver"
	"github.com/redfoxius/claude-memory/internal/memory"
)

// serveCmd starts the claude-memory MCP server on stdio only (AC-19,
// AC-40): logging goes to stderr via zerolog, stdout carries only MCP
// protocol frames. cfg is accepted to match the composition root's other
// subcommand entry points; all tunables it holds are consumed by the
// memory.Service built in main.go, not by this transport layer.
func serveCmd(ctx context.Context, _ *config.Config, svc *memory.Service, opts ...mcpserver.Option) error {
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	checkout := "none"
	if co, ok := svc.Checkout(); ok {
		checkout = co.Dir
	}
	logger.Info().Str("namespace", svc.Namespace()).Str("checkout", checkout).Msg("claude-memory MCP server starting (stdio)")

	srv := mcpserver.New(svc, logger, opts...)

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		logger.Error().Err(err).Msg("claude-memory MCP server exited with error")
		return err
	}
	return nil
}
