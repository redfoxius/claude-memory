package main

import (
	"context"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"

	"claude-memory/internal/config"
	"claude-memory/internal/mcpserver"
	"claude-memory/internal/memory"
)

// serveCmd starts the claude-memory MCP server on stdio only (AC-19,
// AC-40): logging goes to stderr via zerolog, stdout carries only MCP
// protocol frames. cfg is accepted to match the composition root's other
// subcommand entry points; all tunables it holds are consumed by the
// memory.Service built in main.go, not by this transport layer.
func serveCmd(ctx context.Context, _ *config.Config, svc *memory.Service) error {
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	logger.Info().Msg("claude-memory MCP server starting (stdio)")

	srv := mcpserver.New(svc, logger)

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		logger.Error().Err(err).Msg("claude-memory MCP server exited with error")
		return err
	}
	return nil
}
