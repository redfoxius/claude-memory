//go:build integration

package postgres

import (
	"context"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"claude-memory/internal/setup"
)

// These tests need MEMORY_TEST_PG_ADMIN_DSN: a superuser DSN of a
// pgvector-enabled server (see startScratchDatabase). They create throwaway
// databases and roles named cmprobe_* / mt_* and drop them afterwards.
//
// The wrong-password and pg_hba cases need the server to enforce them for
// the test roles; with an all-trust pg_hba.conf they are skipped. A local
// test server enables them with, ahead of the trust lines:
//
//	host  all             /^cmprobe_pw_  127.0.0.1/32  scram-sha-256
//	host  /^cmprobe_hba_  all            127.0.0.1/32  reject

func probeAdminDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MEMORY_TEST_PG_ADMIN_DSN")
	if dsn == "" {
		t.Skip("MEMORY_TEST_PG_ADMIN_DSN is not set")
	}
	return dsn
}

func probeAdminConn(t *testing.T, ctx context.Context) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(ctx, probeAdminDSN(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// scratchDB creates an empty database and returns its superuser DSN.
func scratchDB(t *testing.T, ctx context.Context) string {
	t.Helper()
	dsn, cleanup := startScratchDatabase(t, ctx, probeAdminDSN(t))
	t.Cleanup(cleanup)
	return dsn
}

func suffix() string { return strings.ReplaceAll(uuid.New().String(), "-", "")[:12] }

// withURL returns dsn with its user, password and/or database replaced
// ("" keeps the current value), and optionally over TCP to 127.0.0.1.
func withURL(t *testing.T, dsn, user, password, db string, tcp bool) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		if password != "" {
			u.User = url.UserPassword(user, password)
		} else {
			u.User = url.User(user)
		}
	}
	if db != "" {
		u.Path = "/" + db
	}
	q := u.Query()
	if tcp {
		port := q.Get("port")
		if port == "" {
			port = u.Port()
		}
		if port == "" {
			port = "5432"
		}
		q.Del("host")
		q.Del("port")
		u.Host = net.JoinHostPort("127.0.0.1", port)
	}
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

// createRole creates a LOGIN role (password may be "") and drops it at
// cleanup. Create roles before the databases they own: cleanups run LIFO.
func createRole(t *testing.T, ctx context.Context, admin *pgx.Conn, name, password string) {
	t.Helper()
	stmt := "CREATE ROLE " + name + " LOGIN"
	if password != "" {
		stmt += " PASSWORD '" + password + "'"
	}
	if _, err := admin.Exec(ctx, stmt); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+name); err != nil {
			t.Logf("drop role %s: %v", name, err)
		}
	})
}

func dbName(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(u.Path, "/")
}

func execOn(t *testing.T, ctx context.Context, dsn, sql string) {
	t.Helper()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, sql); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func migrationByID(st setup.DBStatus, id string) setup.MigrationStatus {
	for _, m := range st.Migrations {
		if m.ID == id {
			return m
		}
	}
	return setup.MigrationStatus{ID: id, Missing: []string{"<no status>"}}
}

func assertAllApplied(t *testing.T, st setup.DBStatus) {
	t.Helper()
	if len(st.Migrations) != len(schemaMigrations) {
		t.Fatalf("migrations = %+v", st.Migrations)
	}
	for _, m := range st.Migrations {
		if !m.Applied() {
			t.Errorf("migration %s missing %v", m.ID, m.Missing)
		}
	}
}

// AC-3, AC-58 pg.*: a migrated database reports every object; a second
// Migrate is a no-op; schema_migrations stays empty.
func TestProbeIntegrationMigratedDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDB(t, ctx)
	p := Prober{}
	for i := 0; i < 2; i++ {
		if err := p.Migrate(ctx, dsn); err != nil {
			t.Fatalf("migrate #%d: %v", i+1, err)
		}
	}
	st, err := p.Probe(ctx, dsn)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("status: %+v", st)
	if !st.Connected || st.ErrorClass != "" || st.RTT <= 0 {
		t.Errorf("status = %+v", st)
	}
	if st.ServerVersion == "" || st.ServerVersion[0] < '1' || st.ServerVersion[0] > '9' || strings.Contains(st.ServerVersion, " ") {
		t.Errorf("ServerVersion = %q", st.ServerVersion)
	}
	if st.VectorVersion == "" {
		t.Error("VectorVersion empty on a migrated database")
	}
	if st.TLS {
		t.Error("TLS reported for sslmode=disable / a socket connection")
	}
	assertAllApplied(t, st)
	if len(st.Unknown) != 0 {
		t.Errorf("Unknown = %v", st.Unknown)
	}

	var rows int
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if err := c.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("schema_migrations rows = %d, %v (nothing writes it; Design 6)", rows, err)
	}
}

// A fresh, empty database: no vector extension, every object missing.
func TestProbeIntegrationFreshDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDB(t, ctx)
	st, err := Prober{}.Probe(ctx, dsn)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !st.Connected || st.VectorVersion != "" {
		t.Errorf("status = %+v", st)
	}
	m1, m2 := migrationByID(st, "0001"), migrationByID(st, "0002")
	if len(m1.Missing) != len(schemaMigrations[0].Objects) || len(m2.Missing) != len(schemaMigrations[1].Objects) {
		t.Errorf("missing: 0001 %v; 0002 %v", m1.Missing, m2.Missing)
	}
	for _, want := range []string{"extension vector", "records", "records.embedding", "index idx_records_embedding_hnsw"} {
		if !slices.Contains(m1.Missing, want) {
			t.Errorf("0001 missing %v lacks %q", m1.Missing, want)
		}
	}
	if len(st.Unknown) != 0 {
		t.Errorf("Unknown = %v", st.Unknown)
	}
}

// A non-superuser role owning a fresh database cannot create the vector
// extension: Migrate fails with a clear permission error and the hint, and
// succeeds once a superuser created the extension (spec AC-21 path).
func TestProbeIntegrationMigrateNeedsSuperuserForExtension(t *testing.T) {
	ctx := context.Background()
	admin := probeAdminConn(t, ctx)
	role := "cmprobe_app_" + suffix()
	createRole(t, ctx, admin, role, "")
	adminDB := scratchDB(t, ctx)
	db := dbName(t, adminDB)
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+db+" OWNER TO "+role); err != nil {
		t.Fatal(err)
	}
	appDSN := withURL(t, adminDB, role, "", "", false)

	err := Prober{}.Migrate(ctx, appDSN)
	if err == nil {
		t.Fatal("Migrate as a non-superuser created the vector extension")
	}
	t.Logf("migrate error: %v", err)
	for _, want := range []string{"permission denied to create extension", "42501", "superuser must run CREATE EXTENSION vector"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), appDSN) {
		t.Errorf("error contains the DSN: %v", err)
	}

	execOn(t, ctx, adminDB, "CREATE EXTENSION vector")
	if err := (Prober{}).Migrate(ctx, appDSN); err != nil {
		t.Fatalf("migrate as owner after CREATE EXTENSION: %v", err)
	}
	st, err := Prober{}.Probe(ctx, appDSN)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	assertAllApplied(t, st)
}

// Only 0001 applied: 0002's three objects are missing. Then a newer
// binary's objects show up as Unknown (info), not as missing.
func TestProbeIntegrationPartialAndNewerSchema(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDB(t, ctx)
	execOn(t, ctx, dsn, migrationInitSQL)

	st, err := Prober{}.Probe(ctx, dsn)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if m := migrationByID(st, "0001"); !m.Applied() {
		t.Errorf("0001 missing %v", m.Missing)
	}
	want := []string{"records.namespace", "index idx_records_namespace_repo", "index idx_records_namespace_status"}
	if m := migrationByID(st, "0002"); !slices.Equal(m.Missing, want) {
		t.Errorf("0002 missing = %v, want %v", m.Missing, want)
	}

	if err := (Prober{}).Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	execOn(t, ctx, dsn, "ALTER TABLE records ADD COLUMN scope TEXT; CREATE INDEX idx_records_scope ON records(scope)")
	st, err = Prober{}.Probe(ctx, dsn)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	assertAllApplied(t, st)
	if want := []string{"records.scope", "index idx_records_scope"}; !slices.Equal(st.Unknown, want) {
		t.Errorf("Unknown = %v, want %v", st.Unknown, want)
	}
}

// AC-20: error classes from real server responses, with no DSN or password
// in the error.
func TestProbeIntegrationErrorClasses(t *testing.T) {
	ctx := context.Background()
	admin := probeAdminConn(t, ctx)
	adminDSN := probeAdminDSN(t)
	p := Prober{ConnectTimeout: 2 * time.Second}

	check := func(t *testing.T, dsn string, want setup.DBErrorClass) (setup.DBStatus, error) {
		t.Helper()
		st, err := p.Probe(ctx, dsn)
		if err == nil {
			t.Fatalf("probe succeeded, want %q", want)
		}
		t.Logf("%s: %v", st.ErrorClass, err)
		if strings.Contains(err.Error(), dsn) {
			t.Errorf("error contains the DSN: %v", err)
		}
		if st.Connected {
			t.Errorf("Connected = true")
		}
		return st, err
	}

	t.Run("nodb", func(t *testing.T) {
		dsn := withURL(t, adminDSN, "", "", "cmprobe_nodb_"+suffix(), false)
		if st, _ := check(t, dsn, setup.DBErrNoDB); st.ErrorClass != setup.DBErrNoDB {
			t.Errorf("class = %q", st.ErrorClass)
		}
	})

	t.Run("missing role is auth", func(t *testing.T) {
		dsn := withURL(t, adminDSN, "cmprobe_norole_"+suffix(), "", "", false)
		if st, _ := check(t, dsn, setup.DBErrAuth); st.ErrorClass != setup.DBErrAuth {
			t.Errorf("class = %q", st.ErrorClass)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		role := "cmprobe_pw_" + suffix()
		const good, bad = "right-password-123", "wrong-password-456"
		createRole(t, ctx, admin, role, good)
		dsn := withURL(t, adminDSN, role, bad, "postgres", true)
		st, err := p.Probe(ctx, dsn)
		if err == nil {
			t.Skip("the server does not enforce passwords for cmprobe_pw_* over TCP (all-trust pg_hba.conf)")
		}
		t.Logf("%s: %v", st.ErrorClass, err)
		if st.ErrorClass != setup.DBErrAuth {
			t.Errorf("class = %q", st.ErrorClass)
		}
		if strings.Contains(err.Error(), bad) || strings.Contains(err.Error(), dsn) {
			t.Errorf("error leaks the password: %v", err)
		}
		if _, err := p.Probe(ctx, withURL(t, adminDSN, role, good, "postgres", true)); err != nil {
			t.Errorf("right password: %v", err)
		}
	})

	t.Run("hba reject", func(t *testing.T) {
		dsn := withURL(t, adminDSN, "", "", "cmprobe_hba_"+suffix(), true)
		st, err := p.Probe(ctx, dsn)
		if err == nil || st.ErrorClass == setup.DBErrNoDB {
			t.Skip("no pg_hba.conf reject line for cmprobe_hba_* databases")
		}
		t.Logf("%s: %v", st.ErrorClass, err)
		if st.ErrorClass != setup.DBErrHBA {
			t.Errorf("class = %q", st.ErrorClass)
		}
	})

	t.Run("refused port", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close() // nothing listens there now
		dsn := "postgres://u:s3cretPassw0rd@" + addr + "/db?sslmode=disable"
		if st, _ := check(t, dsn, setup.DBErrUnreachable); st.ErrorClass != setup.DBErrUnreachable {
			t.Errorf("class = %q", st.ErrorClass)
		}
	})

	t.Run("hanging server times out", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close() // accept, never answer
			}
		}()
		hp := Prober{ConnectTimeout: 300 * time.Millisecond}
		start := time.Now()
		st, err := hp.Probe(ctx, "postgres://u:s3cretPassw0rd@"+l.Addr().String()+"/db?sslmode=disable")
		if err == nil || st.ErrorClass != setup.DBErrUnreachable {
			t.Errorf("got %q, %v", st.ErrorClass, err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("probe took %v with a 300ms timeout", d)
		}
	})

	t.Run("dsn", func(t *testing.T) {
		if st, _ := check(t, "postgres://u:ab/s3cretPassw0rd@127.0.0.1:5432/db", setup.DBErrDSN); st.ErrorClass != setup.DBErrDSN {
			t.Errorf("class = %q", st.ErrorClass)
		}
	})
}

// catalogFingerprint hashes the schema and row counts of dsn's database.
func catalogFingerprint(t *testing.T, ctx context.Context, dsn string) string {
	t.Helper()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var fp string
	err = c.QueryRow(ctx, `
		SELECT md5(
		  (SELECT string_agg(c.oid || ':' || c.relname || ':' || c.relkind::text || ':' || c.relfilenode, ',' ORDER BY c.oid)
		     FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast'))
		  || (SELECT string_agg(attrelid || ':' || attname || ':' || atttypid, ',' ORDER BY attrelid, attnum) FROM pg_attribute WHERE attrelid >= 16384)
		  || (SELECT string_agg(extname || ':' || extversion, ',' ORDER BY extname) FROM pg_extension)
		  || (SELECT count(*) FROM records)::text
		  || (SELECT count(*) FROM schema_migrations)::text)`).Scan(&fp)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// AC-57: the probe writes nothing. It runs as a role with no privileges on
// any table (a write would fail; the catalog reads must still work), in a
// session whose transactions default to read-only, and the catalog and row
// counts are unchanged afterwards.
func TestProbeIntegrationMakesNoWrites(t *testing.T) {
	ctx := context.Background()
	admin := probeAdminConn(t, ctx)
	role := "cmprobe_ro_" + suffix()
	createRole(t, ctx, admin, role, "")
	dsn := scratchDB(t, ctx)
	if err := (Prober{}).Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	roDSN := withURL(t, dsn, role, "", "", false)

	before := catalogFingerprint(t, ctx, dsn)
	st, err := Prober{}.Probe(ctx, roDSN)
	if err != nil {
		t.Fatalf("probe as an unprivileged role: %v", err)
	}
	assertAllApplied(t, st)
	if st.VectorVersion == "" {
		t.Error("VectorVersion empty")
	}
	if after := catalogFingerprint(t, ctx, dsn); after != before {
		t.Error("the catalog or row counts changed during the probe")
	}

	// The probe's session settings make any write fail even for a superuser.
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, "CREATE TABLE cmprobe_should_fail (x int)"); err == nil || !strings.Contains(err.Error(), "read-only transaction") {
		t.Errorf("write in a read-only session: %v", err)
	}
}
