package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"claude-memory/internal/setup"
)

// Every migrations/*.sql file has a schemaMigrations entry, in order, and
// every table, index, extension and added column the file creates is listed
// in that entry (plan Design 6).
func TestSchemaObjectsCoverEveryMigration(t *testing.T) {
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	if len(files) != len(schemaMigrations) {
		t.Fatalf("%d migration files, %d schemaMigrations entries: add an entry to schema_objects.go", len(files), len(schemaMigrations))
	}
	// The SQL postgres.New applies is exactly these files, in order.
	var all []string
	for i, f := range files {
		base := filepath.Base(f)
		m := schemaMigrations[i]
		if m.File != base || !strings.HasPrefix(base, m.ID+"_") {
			t.Errorf("entry %d is %s (%s), file is %s", i, m.ID, m.File, base)
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, string(b))
		for _, want := range objectsCreatedBy(string(b)) {
			if !slices.Contains(m.Objects, want) {
				t.Errorf("%s creates %s, which its schemaMigrations entry does not list", base, want)
			}
		}
	}
	if got := strings.Join(all, ";\n"); got != migrationSQL {
		t.Error("migrationSQL (store.go) is not the concatenation of migrations/*.sql: add the //go:embed line")
	}
	if got := MigrationIDs(); !slices.Equal(got, []string{"0001", "0002", "0003"}) {
		t.Errorf("MigrationIDs() = %v", got)
	}
}

var (
	createTableRe = regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?(\w+)`)
	createIndexRe = regexp.MustCompile(`(?i)CREATE (?:UNIQUE )?INDEX (?:IF NOT EXISTS )?(\w+)\s+ON (\w+)`)
	createExtRe   = regexp.MustCompile(`(?i)CREATE EXTENSION (?:IF NOT EXISTS )?(\w+)`)
	addColumnRe   = regexp.MustCompile(`(?i)ALTER TABLE (\w+) ADD COLUMN (?:IF NOT EXISTS )?(\w+)`)
)

// objectsCreatedBy extracts the objects a migration's SQL creates.
func objectsCreatedBy(sql string) []schemaObject {
	var out []schemaObject
	for _, m := range createTableRe.FindAllStringSubmatch(sql, -1) {
		out = append(out, table(m[1]))
	}
	for _, m := range createIndexRe.FindAllStringSubmatch(sql, -1) {
		out = append(out, index(m[2], m[1]))
	}
	for _, m := range createExtRe.FindAllStringSubmatch(sql, -1) {
		out = append(out, ext(m[1]))
	}
	for _, m := range addColumnRe.FindAllStringSubmatch(sql, -1) {
		out = append(out, column(m[1], m[2]))
	}
	return out
}

func TestSchemaObjectString(t *testing.T) {
	for _, tc := range []struct {
		o    schemaObject
		want string
	}{
		{ext("vector"), "extension vector"},
		{table("records"), "records"},
		{column("records", "namespace"), "records.namespace"},
		{index("records", "idx_records_repo"), "index idx_records_repo"},
	} {
		if got := tc.o.String(); got != tc.want {
			t.Errorf("%#v.String() = %q, want %q", tc.o, got, tc.want)
		}
	}
}

func TestInventoryEvaluate(t *testing.T) {
	all := func() inventory {
		inv := inventory{present: map[schemaObject]bool{}}
		for _, m := range schemaMigrations {
			for _, o := range m.Objects {
				inv.present[o] = true
				if o.Kind == objColumn || o.Kind == objIndex {
					inv.extra = append(inv.extra, o)
				}
			}
		}
		return inv
	}

	t.Run("all applied", func(t *testing.T) {
		migs, unknown := all().evaluate(schemaMigrations)
		if len(migs) != len(schemaMigrations) {
			t.Fatalf("got %d statuses", len(migs))
		}
		for _, m := range migs {
			if !m.Applied() {
				t.Errorf("%s missing %v", m.ID, m.Missing)
			}
		}
		if len(unknown) != 0 {
			t.Errorf("unknown = %v", unknown)
		}
	})

	t.Run("only 0001", func(t *testing.T) {
		inv := all()
		for _, o := range schemaMigrations[1].Objects {
			delete(inv.present, o)
		}
		migs, _ := inv.evaluate(schemaMigrations)
		if !migs[0].Applied() {
			t.Errorf("0001 missing %v", migs[0].Missing)
		}
		want := []string{"records.namespace", "index idx_records_namespace_repo", "index idx_records_namespace_status"}
		if !slices.Equal(migs[1].Missing, want) {
			t.Errorf("0002 missing = %v, want %v", migs[1].Missing, want)
		}
	})

	t.Run("empty database", func(t *testing.T) {
		migs, unknown := inventory{present: map[schemaObject]bool{}}.evaluate(schemaMigrations)
		for i, m := range migs {
			if len(m.Missing) != len(schemaMigrations[i].Objects) {
				t.Errorf("%s: %d missing, want all %d", m.ID, len(m.Missing), len(schemaMigrations[i].Objects))
			}
		}
		if unknown != nil {
			t.Errorf("unknown = %v", unknown)
		}
	})

	t.Run("newer schema", func(t *testing.T) {
		inv := all()
		for _, o := range []schemaObject{column("records", "scope"), index("records", "idx_records_scope")} {
			inv.present[o] = true
			inv.extra = append(inv.extra, o)
		}
		_, unknown := inv.evaluate(schemaMigrations)
		if want := []string{"records.scope", "index idx_records_scope"}; !slices.Equal(unknown, want) {
			t.Errorf("unknown = %v, want %v", unknown, want)
		}
	})
}

// timeoutErr is a net.Error that timed out.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

func TestClassify(t *testing.T) {
	_, parseErr := pgx.ParseConfig("postgres://u:pw@host:notaport/db")
	if parseErr == nil {
		t.Fatal("expected a parse error")
	}
	pg := func(code, msg string) error {
		return fmt.Errorf("failed to connect: %w", &pgconn.PgError{Severity: "FATAL", Code: code, Message: msg})
	}
	for _, tc := range []struct {
		name string
		err  error
		want setup.DBErrorClass
	}{
		{"bad password", pg("28P01", `password authentication failed for user "u"`), setup.DBErrAuth},
		{"no role", pg("28000", `role "nobody" does not exist`), setup.DBErrAuth},
		{"hba no entry", pg("28000", `no pg_hba.conf entry for host "10.0.0.1", user "u", database "d", no encryption`), setup.DBErrHBA},
		{"hba reject", pg("28000", `pg_hba.conf rejects connection for host "127.0.0.1", user "u", database "d", no encryption`), setup.DBErrHBA},
		{"no database", pg("3D000", `database "nope" does not exist`), setup.DBErrNoDB},
		{"too many connections", pg("53300", "sorry, too many clients already"), setup.DBErrOther},
		{"refused", fmt.Errorf("dial: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}), setup.DBErrUnreachable},
		{"dns", &net.DNSError{Err: "no such host", Name: "db.invalid"}, setup.DBErrUnreachable},
		{"net timeout", fmt.Errorf("x: %w", timeoutErr{}), setup.DBErrUnreachable},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), setup.DBErrUnreachable},
		{"dsn", parseErr, setup.DBErrDSN},
		{"other", errors.New("boom"), setup.DBErrOther},
	} {
		if got := classify(tc.err); got != tc.want {
			t.Errorf("%s: classify = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The probe's errors never contain the DSN or its password (AC-30).
func TestProbeErrorsNeverContainDSN(t *testing.T) {
	const pw, frag = "s3cretPassw0rd", "Xy7q"
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		dsn  string
		want setup.DBErrorClass
	}{
		{"unparseable port", "postgres://app:" + pw + "@db.example:notaport/claude_memory", setup.DBErrDSN},
		// net/url reads "app:frag" as host:port and quotes ":frag".
		{"unencoded slash in password", "postgres://app:" + frag + "/" + pw + "@db.example:5432/claude_memory", setup.DBErrDSN},
		{"keyword form", "host=127.0.0.1 port=notaport password=" + pw, setup.DBErrDSN},
		{"refused", "postgres://app:" + pw + "@127.0.0.1:1/claude_memory?sslmode=disable", setup.DBErrUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := Prober{ConnectTimeout: 2 * time.Second}.Probe(ctx, tc.dsn)
			if err == nil {
				t.Fatal("expected an error")
			}
			if st.Connected || st.ErrorClass != tc.want {
				t.Errorf("status = %+v, want class %q", st, tc.want)
			}
			if strings.Contains(err.Error(), pw) || strings.Contains(err.Error(), frag) || strings.Contains(err.Error(), tc.dsn) {
				t.Errorf("error leaks the DSN or password: %v", err)
			}
			t.Logf("%s: %v", st.ErrorClass, err)
		})
	}
}

func TestMigrateErrorNeverContainsDSN(t *testing.T) {
	const pw = "s3cretPassw0rd"
	dsn := "postgres://app:" + pw + "@127.0.0.1:1/claude_memory?sslmode=disable&connect_timeout=2"
	err := Prober{}.Migrate(context.Background(), dsn)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), pw) || strings.Contains(err.Error(), dsn) {
		t.Errorf("error leaks the DSN or password: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "migrate: ") {
		t.Errorf("error = %v", err)
	}
}

// A password with an unencoded "/" makes net/url quote a fragment of it
// ("invalid port \":ab\""); the error must withhold that detail.
func TestMigrateErrorWithholdsParseDetail(t *testing.T) {
	dsn := "postgresql://claude_memory:ab/cd+Efgh12345==@127.0.0.1:54329/db"
	err := Prober{}.Migrate(context.Background(), dsn)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, frag := range []string{":ab", "cd+Efgh", "Efgh12345"} {
		if strings.Contains(err.Error(), frag) {
			t.Errorf("error leaks %q: %v", frag, err)
		}
	}
}

func TestSanitize(t *testing.T) {
	dsn := "postgres://app:hunter2hunter2@db:5432/x"
	red := dsnRedactor(dsn)
	err := sanitize(red, "connect", fmt.Errorf("cannot parse `postgres://app:xxxxx@db:5432/x`: bad; also %s and hunter2hunter2", dsn))
	got := err.Error()
	if strings.Contains(got, "hunter2") || strings.Contains(got, "db:5432") {
		t.Errorf("sanitize = %q", got)
	}
	if !strings.HasPrefix(got, "connect: cannot parse dsn: bad") {
		t.Errorf("sanitize = %q", got)
	}
}

func TestServerVersion(t *testing.T) {
	for in, want := range map[string]string{
		"16.14 (Ubuntu 16.14-0ubuntu0.24.04.1)": "16.14",
		"17.2":                                  "17.2",
		"":                                      "",
	} {
		if got := serverVersion(in); got != want {
			t.Errorf("serverVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeFileInfo is a stat result with a given mode.
type fakeFileInfo struct{ mode fs.FileMode }

func (f fakeFileInfo) Name() string       { return "s" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestLocalServerEvidence(t *testing.T) {
	listening := func(context.Context, string, string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}
	refused := func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	}
	statSocket := func(p string) (fs.FileInfo, error) {
		if p == "/tmp/.s.PGSQL.5432" {
			return fakeFileInfo{mode: fs.ModeSocket | 0o777}, nil
		}
		return nil, fs.ErrNotExist
	}
	statRegular := func(string) (fs.FileInfo, error) { return fakeFileInfo{mode: 0o644}, nil }
	statNone := func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
	sockets := []string{"/var/run/postgresql/.s.PGSQL.5432", "/tmp/.s.PGSQL.5432"}
	yes, no := func() bool { return true }, func() bool { return false }

	for _, tc := range []struct {
		name   string
		env    localEnv
		want   bool
		detail string
	}{
		{"tcp + socket", localEnv{listening, statSocket, sockets, no}, true, "socket /tmp/.s.PGSQL.5432"},
		{"tcp + process", localEnv{listening, statNone, sockets, yes}, true, "postgres process"},
		{"tcp only (forwarded)", localEnv{listening, statNone, sockets, no}, false, "forwarded"},
		{"regular file is no socket", localEnv{listening, statRegular, sockets, nil}, false, "forwarded"},
		{"no tcp", localEnv{refused, statSocket, sockets, yes}, false, "nothing accepts TCP"},
	} {
		ok, detail := localServerEvidence(context.Background(), tc.env)
		if ok != tc.want || !strings.Contains(detail, tc.detail) {
			t.Errorf("%s: got (%v, %q), want (%v, ~%q)", tc.name, ok, detail, tc.want, tc.detail)
		}
	}
}

func TestProcHasPostgres(t *testing.T) {
	proc := t.TempDir()
	write := func(pid, comm string) {
		if err := os.MkdirAll(filepath.Join(proc, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proc, pid, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("1", "init")
	write("self", "postgres") // not a pid
	if procHasPostgres(proc) {
		t.Error("found postgres without a postgres pid")
	}
	write("4242", "postgres")
	if !procHasPostgres(proc) {
		t.Error("missed postgres")
	}
	if procHasPostgres(filepath.Join(proc, "absent")) {
		t.Error("absent procfs reported a process")
	}
}
