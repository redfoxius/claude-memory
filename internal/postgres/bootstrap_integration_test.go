//go:build integration

package postgres

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/redfoxius/claude-memory/deploy"
	"github.com/redfoxius/claude-memory/internal/setup"
)

// WI-S2-4b / AC-21 / AC-65: the bootstrap.sql the installer renders (three
// \set lines + deploy/initdb/app-role.psql verbatim) is run by psql as a
// superuser, twice, and afterwards the app role can run the migrations
// (postgres.New) and the read-only probe finds everything in place (B-6).
//
// By default psql runs INSIDE a pgvector testcontainer through Exec, so the
// host needs only Docker. With MEMORY_TEST_PG_ADMIN_DSN set and psql on the
// host, it runs there against that server instead (throwaway role/database,
// dropped afterwards). Skipped when neither is available.

const bootstrapTestPassword = "S3ntinel-pw_.~x9" // inside ^[A-Za-z0-9_.~-]{8,256}$

func renderBootstrapForTest(t *testing.T, user, db, pw string) []byte {
	t.Helper()
	body, err := deploy.FS.ReadFile(deploy.InitDBAppRolePSQL)
	if err != nil {
		t.Fatal(err)
	}
	out, err := setup.RenderBootstrap(body, user, db, pw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// bootstrapRunner runs a rendered bootstrap.sql as the superuser, and says how
// to reach the resulting database as the app role.
type bootstrapRunner struct {
	run    func(t *testing.T, sql []byte) string // returns psql's combined output; fails the test on a non-zero exit
	appDSN func(user, pw, db string) string
}

func hostPSQLRunner(t *testing.T) (bootstrapRunner, bool) {
	admin := os.Getenv("MEMORY_TEST_PG_ADMIN_DSN")
	if admin == "" {
		return bootstrapRunner{}, false
	}
	psql, err := exec.LookPath("psql")
	if err != nil {
		return bootstrapRunner{}, false
	}
	return bootstrapRunner{
		run: func(t *testing.T, sql []byte) string {
			f := filepath.Join(t.TempDir(), "bootstrap.sql")
			if err := os.WriteFile(f, sql, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(psql, "-v", "ON_ERROR_STOP=1", "-d", admin, "-f", f).CombinedOutput()
			if err != nil {
				t.Fatalf("psql: %v\n%s", err, out)
			}
			return string(out)
		},
		appDSN: func(user, pw, db string) string { return withURL(t, admin, user, pw, db, true) },
	}, true
}

func containerPSQLRunner(t *testing.T, ctx context.Context) bootstrapRunner {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		t.Skipf("Docker is not available: %v", err)
	}
	if err := provider.Health(ctx); err != nil {
		t.Skipf("Docker is not available: %v", err)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "pgvector/pgvector:pg16",
			ExposedPorts: []string{"5432/tcp"},
			Env:          map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": "adminpass", "POSTGRES_DB": "postgres"},
			// The entrypoint starts a temporary server first, then the real one.
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("cannot start the pgvector container (is the image available?): %v", err)
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
	return bootstrapRunner{
		run: func(t *testing.T, sql []byte) string {
			const path = "/tmp/bootstrap.sql"
			if err := c.CopyToContainer(ctx, sql, path, 0o600); err != nil {
				t.Fatalf("copy bootstrap.sql into the container: %v", err)
			}
			code, r, err := c.Exec(ctx, []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-f", path})
			if err != nil {
				t.Fatalf("exec psql: %v", err)
			}
			out, _ := io.ReadAll(r)
			if code != 0 {
				t.Fatalf("psql exited %d:\n%s", code, out)
			}
			return string(out)
		},
		appDSN: func(user, pw, db string) string {
			u := url.URL{Scheme: "postgres", User: url.UserPassword(user, pw), Host: fmt.Sprintf("%s:%s", host, port.Port()), Path: "/" + db}
			return u.String()
		},
	}
}

func TestBootstrapSQLRunsTwiceAndTheAppRoleCanMigrate(t *testing.T) {
	ctx := context.Background()
	r, onHost := hostPSQLRunner(t)
	if !onHost {
		r = containerPSQLRunner(t, ctx)
	}
	sfx := suffix()
	user, db := "cmboot_u_"+sfx, "cmboot_d_"+sfx
	if onHost {
		t.Cleanup(func() { dropBootstrapObjects(t, user, db) })
	}
	sql := renderBootstrapForTest(t, user, db, bootstrapTestPassword)

	first := r.run(t, sql)
	second := r.run(t, sql) // idempotent: the second run must also succeed
	for _, out := range []string{first, second} {
		if strings.Contains(out, "ERROR") {
			t.Errorf("psql reported an error:\n%s", out)
		}
	}

	appDSN := r.appDSN(user, bootstrapTestPassword, db)
	// B-6: the app role runs the migrations ...
	store, err := New(ctx, appDSN)
	if err != nil {
		t.Fatalf("postgres.New as the app role: %v", err)
	}
	store.Close()

	// ... and the read-only probe sees pgvector, every migration applied, and a
	// plain (non-superuser) role.
	st, err := (Prober{}).Probe(ctx, appDSN)
	if err != nil || !st.Connected {
		t.Fatalf("probe: %+v %v", st, err)
	}
	if st.VectorVersion == "" {
		t.Error("the vector extension is not installed in the app database")
	}
	for _, m := range st.Migrations {
		if !m.Applied() {
			t.Errorf("migration %s is missing %v", m.ID, m.Missing)
		}
	}
	conn, err := pgx.Connect(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var super bool
	if err := conn.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil || super {
		t.Errorf("the app role must not be a superuser: super=%v err=%v", super, err)
	}

	// The password survived the second run (an existing role keeps it).
	if _, err := pgx.Connect(ctx, r.appDSN(user, "wrong-password-1", db)); err == nil {
		t.Error("a wrong password connected")
	}
}

// dropBootstrapObjects removes the throwaway role and database from an
// MEMORY_TEST_PG_ADMIN_DSN server.
func dropBootstrapObjects(t *testing.T, user, db string) {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, os.Getenv("MEMORY_TEST_PG_ADMIN_DSN"))
	if err != nil {
		t.Logf("cleanup: %v", err)
		return
	}
	defer admin.Close(ctx)
	_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{db}.Sanitize()+` WITH (FORCE)`)
	_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS `+pgx.Identifier{user}.Sanitize())
}
