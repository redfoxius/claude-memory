//go:build eval
// +build eval

package evalset

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"claude-memory/internal/config"
	"claude-memory/internal/memory"
	"claude-memory/internal/ollama"
	"claude-memory/internal/postgres"
	"claude-memory/internal/scrub"
)

// clockImpl is a simple clock implementation for testing.
type clockImpl struct{}

func (c *clockImpl) Now() time.Time {
	return time.Now().UTC()
}

// TestRetrievalEval runs the full evaluation harness against real Postgres and Ollama.
func TestRetrievalEval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Start testcontainers for Postgres.
	req := testcontainers.ContainerRequest{
		Image:        "pgvector/pgvector:pg16",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_DB":       "claude_memory",
			"POSTGRES_USER":     "eval_user",
			"POSTGRES_PASSWORD": "eval_password",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections"),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	}()

	// Get container connection details.
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		t.Fatalf("get container port: %v", err)
	}

	dsn := fmt.Sprintf("postgres://eval_user:eval_password@%s:%s/claude_memory?sslmode=disable",
		host, port.Port())

	// Wait for the database to accept connections and run migrations.
	var store *postgres.Store
	for i := 0; i < 30; i++ {
		s, err := postgres.New(ctx, dsn)
		if err == nil {
			store = s
			break
		}
		if i == 29 {
			t.Fatalf("failed to connect and initialize database after 30 seconds: %v", err)
		}
		time.Sleep(1 * time.Second)
	}
	if store == nil {
		t.Fatalf("failed to create postgres store")
	}

	// Construct the memory service with real adapters, using the real
	// config defaults (config.Load) rather than a hand-copied set of
	// thresholds that silently drifts from what production runs with.
	t.Setenv("MEMORY_PG_DSN", dsn)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Create HTTP client for Ollama.
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	embedder := ollama.New(httpClient, cfg.OllamaURL, cfg.OllamaModel, cfg.EmbedMaxTokens)
	scrubber := scrub.NewAdapter(scrub.New())
	clock := &clockImpl{}

	svc := memory.New(store, embedder, scrubber, clock, cfg)

	// Determine fixture paths (assumes testdata/evalset/ relative to this package).
	fixturePath := "../../testdata/evalset"

	// Run the evaluation.
	outFile, err := os.Create("../../docs/specs/memory-mvp/eval-results.md")
	if err != nil {
		t.Fatalf("create eval-results.md: %v", err)
	}
	if err := Run(ctx, svc, fixturePath, outFile); err != nil {
		_ = outFile.Close() // best effort; the Run error is what matters
		t.Fatalf("run evaluation: %v", err)
	}

	// Verify the file was written.
	info, err := outFile.Stat()
	if err != nil {
		_ = outFile.Close() // best effort; the Stat error is what matters
		t.Fatalf("stat eval-results.md: %v", err)
	}
	if err := outFile.Close(); err != nil {
		t.Fatalf("close eval-results.md: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("eval-results.md is empty")
	}

	t.Logf("Evaluation complete. Results written to eval-results.md (%d bytes)", info.Size())
}
