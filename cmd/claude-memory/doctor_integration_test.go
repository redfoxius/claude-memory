//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Integration tests for `migrate` (AC-3) and doctor's pg.* checks (AC-65,
// slice 1 half). Like internal/postgres's suite they use
// MEMORY_TEST_PG_ADMIN_DSN (a superuser DSN of a pgvector-enabled server)
// when it is set, and otherwise start a pgvector/pgvector:pg16 container
// (testcontainers; CI). Each test creates a throwaway database (mt_cmd_*).

// adminDSN returns the superuser DSN to create test databases with.
func adminDSN(t *testing.T) string {
	t.Helper()
	if admin := os.Getenv("MEMORY_TEST_PG_ADMIN_DSN"); admin != "" {
		return admin
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "pgvector/pgvector:pg16",
			ExposedPorts: []string{"5432/tcp"},
			Env:          map[string]string{"POSTGRES_USER": "test", "POSTGRES_PASSWORD": "testpass", "POSTGRES_DB": "postgres"},
			WaitingFor:   wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start postgres container (or set MEMORY_TEST_PG_ADMIN_DSN): %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "5432")
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("postgres://test:testpass@%s:%s/postgres?sslmode=disable", host, port.Port())
}

func scratchDatabase(t *testing.T) string {
	t.Helper()
	admin := adminDSN(t)
	ctx := context.Background()
	var conn *pgx.Conn
	var err error
	for i := 0; i < 30; i++ {
		if conn, err = pgx.Connect(ctx, admin); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	name := "mt_cmd_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		conn.Close(ctx)
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		conn.Close(context.Background())
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("MEMORY_TEST_PG_ADMIN_DSN must be a URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// homeWithEnv returns a HOME whose env file (0600) holds dsn and an Ollama
// URL nothing listens on.
func homeWithEnv(t *testing.T, dsn string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "claude-memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := "MEMORY_PG_DSN=" + dsn + "\nMEMORY_OLLAMA_URL=http://127.0.0.1:1\n"
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

type doctorDoc struct {
	OK     bool `json:"ok"`
	Checks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	} `json:"checks"`
}

func runDoctorJSON(t *testing.T, home string) (map[string]string, map[string]string, int) {
	t.Helper()
	out, code := runChild(t, []string{"HOME=" + home}, "doctor", "--json")
	var doc doctorDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	st, det := map[string]string{}, map[string]string{}
	for _, c := range doc.Checks {
		st[c.ID], det[c.ID] = c.Status, c.Detail
	}
	return st, det, code
}

func TestMigrateAndDoctorPGChecks(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	home := homeWithEnv(t, dsn)

	// Before migrate: connected, but no schema.
	st, det, code := runDoctorJSON(t, home)
	if st["pg.connect"] != "pass" || st["pg.schema"] != "fail" || code != 1 {
		t.Errorf("fresh database: pg.connect %s, pg.schema %s (%s), exit %d", st["pg.connect"], st["pg.schema"], det["pg.schema"], code)
	}
	if st["pg.vector"] != "fail" {
		t.Errorf("fresh database: pg.vector %s (%s), want fail (no extension yet)", st["pg.vector"], det["pg.vector"])
	}

	// AC-3: migrate twice; the second run is a no-op.
	for i := 0; i < 2; i++ {
		out, code := runChild(t, []string{"HOME=" + home}, "migrate")
		if code != 0 || !strings.Contains(out, "schema up to date (migrations: 0001,0002,0003)") {
			t.Fatalf("migrate run %d: exit %d\n%s", i+1, code, out)
		}
	}

	st, det, code = runDoctorJSON(t, home)
	for _, id := range []string{"env.file", "env.perms", "env.format", "pg.connect", "pg.vector", "pg.schema"} {
		if st[id] != "pass" {
			t.Errorf("%s: %s (%s), want pass", id, st[id], det[id])
		}
	}
	if st["pg.latency"] != "pass" && st["pg.latency"] != "warn" {
		t.Errorf("pg.latency: %s (%s)", st["pg.latency"], det["pg.latency"])
	}
	// No Ollama listens on the configured URL: reachable fails, dependents skip.
	if st["ollama.reachable"] != "fail" || st["ollama.model"] != "skip" || st["ollama.embed"] != "skip" {
		t.Errorf("ollama: %s / %s / %s", st["ollama.reachable"], st["ollama.model"], st["ollama.embed"])
	}
	if code != 1 {
		t.Errorf("exit %d, want 1 (ollama fails)", code)
	}
}

func TestDoctorPGWrongDatabase(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	u, _ := url.Parse(dsn)
	u.Path = "/mt_cmd_does_not_exist"
	st, det, _ := runDoctorJSON(t, homeWithEnv(t, u.String()))
	if st["pg.connect"] != "fail" || !strings.Contains(det["pg.connect"], "does not exist") {
		t.Errorf("pg.connect: %s (%s), want fail: database does not exist", st["pg.connect"], det["pg.connect"])
	}
	for _, id := range []string{"pg.latency", "pg.vector", "pg.schema"} {
		if st[id] != "skip" {
			t.Errorf("%s: %s, want skip", id, st[id])
		}
	}
}
