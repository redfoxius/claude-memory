package postgres

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"claude-memory/internal/setup"
)

// Prober is the setup.DBProber adapter (spec §10, plan WI-S1-4). Probe is
// read-only: it opens one connection whose sessions default to read-only
// transactions, runs catalog SELECTs only, and never applies migrations
// (AC-57). Every error it returns is sanitized: it never contains the DSN or
// its password (AC-30).
type Prober struct {
	// ConnectTimeout bounds connecting when ctx has no earlier deadline;
	// 0 means DefaultConnectTimeout.
	ConnectTimeout time.Duration
}

// DefaultConnectTimeout is the connect timeout of the probe (AC-20: 5 s).
const DefaultConnectTimeout = 5 * time.Second

var _ setup.DBProber = Prober{}

// ProbeApplicationName is the application_name of the probe's session, so
// it can be told apart in pg_stat_activity.
const ProbeApplicationName = "claude-memory-probe"

// Probe connects to dsn without running migrations and reports the ping
// RTT, whether TLS is in use, the server and pgvector versions, and which
// embedded migrations are applied (plan Design 6). When it cannot connect
// the status carries the ErrorClass and the error its sanitized cause. When
// it connected but an introspection query failed, Connected is true,
// Migrations is nil and the error says what failed.
func (p Prober) Probe(ctx context.Context, dsn string) (setup.DBStatus, error) {
	red := dsnRedactor(dsn)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return setup.DBStatus{ErrorClass: setup.DBErrDSN}, parseError(red, dsn, err)
	}
	red.Register(cfg.Password)

	timeout := p.ConnectTimeout
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	cfg.ConnectTimeout = timeout
	// Belt and braces for AC-57: even a write statement would fail.
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["application_name"] = ProbeApplicationName

	connCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(connCtx, cfg)
	if err != nil {
		return setup.DBStatus{ErrorClass: classify(err)}, sanitize(red, "connect", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	st := setup.DBStatus{Connected: true}
	start := time.Now()
	if err := conn.Ping(ctx); err != nil {
		return setup.DBStatus{ErrorClass: classify(err)}, sanitize(red, "ping", err)
	}
	st.RTT = time.Since(start)
	st.ServerVersion = serverVersion(conn.PgConn().ParameterStatus("server_version"))

	tlsInUse, err := sessionTLS(ctx, conn)
	if err != nil {
		return st, sanitize(red, "read pg_stat_ssl", err)
	}
	st.TLS = tlsInUse

	inv, err := readInventory(ctx, conn)
	if err != nil {
		return st, sanitize(red, "read schema", err)
	}
	st.VectorVersion = inv.vectorVersion
	st.Migrations, st.Unknown = inv.evaluate(schemaMigrations)
	return st, nil
}

// Migrate applies the embedded migrations: the same path as postgres.New
// (and `claude-memory migrate`, AC-3), then closes the store. The error is
// sanitized; a missing vector extension that the role may not create gets a
// hint naming the superuser statement.
func (Prober) Migrate(ctx context.Context, dsn string) error {
	s, err := New(ctx, dsn)
	if err != nil {
		red := dsnRedactor(dsn)
		if cfg, perr := pgx.ParseConfig(dsn); perr == nil {
			red.Register(cfg.Password)
		}
		msg := sanitize(red, "migrate", err).Error()
		if classify(err) == setup.DBErrDSN {
			msg = "migrate: " + parseError(red, dsn, err).Error()
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.Contains(pgErr.Message, "extension") {
			msg += " (a superuser must run CREATE EXTENSION vector; in this database first)"
		}
		return errors.New(msg)
	}
	s.Close()
	return nil
}

// LocalServerEvidence reports whether a Postgres server runs on this
// machine: 127.0.0.1:5432 accepts TCP and a local socket or a postgres
// process exists. A forwarded port alone is not evidence (AC-19).
func (Prober) LocalServerEvidence(ctx context.Context) (bool, string) {
	return localServerEvidence(ctx, osLocalEnv())
}

// ---- error classification and sanitizing -----------------------------------

// classify maps a connect/ping error to a setup.DBErrorClass (AC-20) from
// the *pgconn.PgError SQLSTATE and net/deadline errors.
func classify(err error) setup.DBErrorClass {
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		return setup.DBErrDSN
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01":
			return setup.DBErrAuth
		case "28000":
			if strings.Contains(pgErr.Message, "pg_hba.conf") {
				return setup.DBErrHBA
			}
			return setup.DBErrAuth // includes a role that does not exist
		case "3D000":
			return setup.DBErrNoDB
		}
		return setup.DBErrOther
	}
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) {
		return setup.DBErrUnreachable
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return setup.DBErrUnreachable
	}
	return setup.DBErrOther
}

// parseErrRe matches pgx's ParseConfigError prefix, which quotes the
// connection string (password masked, the rest verbatim).
var parseErrRe = regexp.MustCompile("cannot parse `[^`]*`")

// sanitize returns "<op>: <cause>" with the DSN removed: pgx's quoted
// connection string is replaced, and the redactor masks the DSN literal,
// the password literal and any URL userinfo password (AC-30).
func sanitize(red *setup.Redactor, op string, err error) error {
	msg := parseErrRe.ReplaceAllString(err.Error(), "cannot parse dsn")
	return errors.New(op + ": " + red.Redact(msg))
}

// parseError is the error for a DSN that does not parse. The parser's
// detail can quote a fragment of the password (an unencoded '/' in the
// password makes net/url read "user:fragment" as host:port), which no
// redactor can recognise, so the detail is kept only for a DSN without a
// password.
func parseError(red *setup.Redactor, dsn string, err error) error {
	if dsnPassword(dsn) == "" && !strings.Contains(dsn, "password=") {
		return sanitize(red, "parse dsn", err)
	}
	return errors.New("parse dsn: the DSN does not parse (details withheld: they may quote the password); URL-encode any / ? # @ : % in the password")
}

// dsnPassword returns the password of a URL-form DSN: everything between
// the first ':' after the scheme and the last '@' ("" when none).
func dsnPassword(dsn string) string {
	_, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return ""
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return ""
	}
	_, pw, _ := strings.Cut(rest[:at], ":")
	return pw
}

// dsnRedactor returns a Redactor that masks dsn itself and its URL password.
func dsnRedactor(dsn string) *setup.Redactor {
	r := setup.NewRedactor()
	r.Register(dsn)
	r.Register(dsnPassword(dsn))
	return r
}

// ---- catalog reads ---------------------------------------------------------

// serverVersion trims the server_version parameter to its number
// ("16.14 (Ubuntu ...)" → "16.14").
func serverVersion(s string) string {
	v, _, _ := strings.Cut(strings.TrimSpace(s), " ")
	return v
}

// sessionTLS reports whether this session uses TLS, from pg_stat_ssl, and
// from the client connection type if the view returns no row.
func sessionTLS(ctx context.Context, conn *pgx.Conn) (bool, error) {
	var ssl bool
	err := conn.QueryRow(ctx, `SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).Scan(&ssl)
	if errors.Is(err, pgx.ErrNoRows) {
		_, isTLS := conn.PgConn().Conn().(*tls.Conn)
		return isTLS, nil
	}
	return ssl, err
}

// inventory is what the catalog holds of our schema objects.
type inventory struct {
	vectorVersion string
	present       map[schemaObject]bool
	// extra are columns and non-constraint indexes of ourTables, in catalog
	// order, to find objects no migration creates.
	extra []schemaObject
}

// readInventory reads extensions, tables, columns and indexes with catalog
// SELECTs only. pg_catalog is used rather than information_schema because
// the latter hides objects the role has no privilege on.
func readInventory(ctx context.Context, conn *pgx.Conn) (inventory, error) {
	inv := inventory{present: map[schemaObject]bool{}}

	rows, err := conn.Query(ctx, `SELECT extname, extversion FROM pg_extension`)
	if err != nil {
		return inv, err
	}
	for rows.Next() {
		var name, version string
		if err := rows.Scan(&name, &version); err != nil {
			rows.Close()
			return inv, err
		}
		inv.present[ext(name)] = true
		if name == "vector" {
			inv.vectorVersion = version
		}
	}
	if err := rows.Err(); err != nil {
		return inv, err
	}

	rows, err = conn.Query(ctx, `
		SELECT c.relname
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND c.relname = ANY($1)`, ourTables)
	if err != nil {
		return inv, err
	}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return inv, err
		}
		inv.present[table(t)] = true
	}
	if err := rows.Err(); err != nil {
		return inv, err
	}

	rows, err = conn.Query(ctx, `
		SELECT c.relname, a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND c.relname = ANY($1)
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname, a.attnum`, ourTables)
	if err != nil {
		return inv, err
	}
	for rows.Next() {
		var t, col string
		if err := rows.Scan(&t, &col); err != nil {
			rows.Close()
			return inv, err
		}
		o := column(t, col)
		inv.present[o] = true
		inv.extra = append(inv.extra, o)
	}
	if err := rows.Err(); err != nil {
		return inv, err
	}

	// Indexes that back a constraint (primary key, unique) are implied by
	// the table and not part of the inventory.
	rows, err = conn.Query(ctx, `
		SELECT t.relname, i.relname
		FROM pg_index x
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_class t ON t.oid = x.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = current_schema() AND t.relname = ANY($1)
		  AND NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conindid = x.indexrelid)
		ORDER BY t.relname, i.relname`, ourTables)
	if err != nil {
		return inv, err
	}
	for rows.Next() {
		var t, idx string
		if err := rows.Scan(&t, &idx); err != nil {
			rows.Close()
			return inv, err
		}
		o := index(t, idx)
		inv.present[o] = true
		inv.extra = append(inv.extra, o)
	}
	return inv, rows.Err()
}

// evaluate returns each migration's missing objects, in order, and the
// objects of our tables that no migration creates.
func (inv inventory) evaluate(migs []migrationObjects) ([]setup.MigrationStatus, []string) {
	known := map[schemaObject]bool{}
	out := make([]setup.MigrationStatus, 0, len(migs))
	for _, m := range migs {
		ms := setup.MigrationStatus{ID: m.ID}
		for _, o := range m.Objects {
			known[o] = true
			if !inv.present[o] {
				ms.Missing = append(ms.Missing, o.String())
			}
		}
		out = append(out, ms)
	}
	var unknown []string
	for _, o := range inv.extra {
		if !known[o] {
			unknown = append(unknown, o.String())
		}
	}
	return out, unknown
}

// ---- local server evidence -------------------------------------------------

// localEnv is what localServerEvidence observes; tests substitute it.
type localEnv struct {
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	stat    func(string) (fs.FileInfo, error)
	sockets []string
	// processes reports whether a postgres server process is running
	// (false where it cannot tell, e.g. without /proc).
	processes func() bool
}

const localPGAddr = "127.0.0.1:5432"

func osLocalEnv() localEnv {
	d := &net.Dialer{Timeout: 500 * time.Millisecond}
	return localEnv{
		dial: d.DialContext,
		stat: os.Stat,
		sockets: []string{
			"/var/run/postgresql/.s.PGSQL.5432",
			"/run/postgresql/.s.PGSQL.5432",
			"/tmp/.s.PGSQL.5432",
		},
		processes: func() bool { return procHasPostgres("/proc") },
	}
}

func localServerEvidence(ctx context.Context, env localEnv) (bool, string) {
	c, err := env.dial(ctx, "tcp", localPGAddr)
	if err != nil {
		return false, "nothing accepts TCP on " + localPGAddr
	}
	_ = c.Close()
	for _, s := range env.sockets {
		if fi, err := env.stat(s); err == nil && fi.Mode()&fs.ModeSocket != 0 {
			return true, fmt.Sprintf("%s accepts TCP; socket %s", localPGAddr, s)
		}
	}
	if env.processes != nil && env.processes() {
		return true, localPGAddr + " accepts TCP; a postgres process is running"
	}
	return false, localPGAddr + " accepts TCP, but there is no local socket or postgres process (a forwarded port?)"
}

// procHasPostgres reports whether /proc lists a process named postgres or
// postmaster (Linux; false where procfs is absent).
func procHasPostgres(proc string) bool {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join(proc, e.Name(), "comm"))
		if err != nil {
			continue
		}
		if slices.Contains([]string{"postgres", "postmaster"}, strings.TrimSpace(string(b))) {
			return true
		}
	}
	return false
}
