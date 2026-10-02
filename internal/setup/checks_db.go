package setup

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Doctor checks pg.connect, pg.latency, pg.vector, pg.schema (AC-58). One
// read-only DBProber.Probe (postgres.Open: never migrates, AC-57) feeds all
// four; the three dependents only read its result.

func (d *doctor) checkPGConnect(ctx context.Context) (Status, string, string) {
	dsn, _ := d.setting("MEMORY_PG_DSN", "")
	desc, _ := describeDSN(dsn)
	st, err := d.DB.Probe(ctx, dsn)
	d.dbStatus, d.dbErr = st, err
	if !st.Connected {
		cause := "unknown error"
		if err != nil {
			cause = d.redact(err.Error())
		}
		switch st.ErrorClass {
		case DBErrDSN:
			return StatusFail, "the DSN does not parse: " + cause,
				"URL-encode the password in MEMORY_PG_DSN (/ → %2F, @ → %40, : → %3A), or use a URL-safe one (openssl rand -hex 32)"
		case DBErrAuth:
			return StatusFail, "authentication failed (" + desc + "): wrong password, or the role does not exist; " + cause,
				"check the password in MEMORY_PG_DSN; on the server: ALTER ROLE <user> PASSWORD '…' (DEPLOY.md)"
		case DBErrNoDB:
			return StatusFail, "the database does not exist (" + desc + "): " + cause,
				"create the database on the server (DEPLOY.md, deploy/initdb), or fix the database name in MEMORY_PG_DSN"
		case DBErrHBA:
			return StatusFail, "rejected by the server's pg_hba.conf (" + desc + "): " + cause,
				"allow this client in pg_hba.conf (DEPLOY.md: the Tailscale range 100.64.0.0/10, scram-sha-256)"
		case DBErrUnreachable:
			return StatusFail, "server unreachable or timed out (" + desc + "): " + cause,
				"check `tailscale status`, that Postgres listens on that address, and the server firewall (DEPLOY.md)"
		default:
			return StatusFail, "cannot connect (" + desc + "): " + cause,
				"check MEMORY_PG_DSN and the server logs"
		}
	}
	tls := "no TLS"
	if st.TLS {
		tls = "TLS"
	}
	detail := fmt.Sprintf("connected (%s; PostgreSQL %s, %s)", desc, orDash(st.ServerVersion), tls)
	if err != nil {
		detail += "; reading the catalog failed: " + d.redact(err.Error())
	}
	return pass(detail)
}

func (d *doctor) checkPGLatency(context.Context) (Status, string, string) {
	rtt := d.dbStatus.RTT
	if rtt > PGLatencyWarn {
		return StatusWarn, fmt.Sprintf("ping %s (> %s); the prompt hook's 800 ms budget includes this round trip", roundDur(rtt), PGLatencyWarn),
			"check the network path to the server (`tailscale ping <host>`: a relayed DERP path is slow)"
	}
	return pass("ping " + roundDur(rtt))
}

// catalogUnread is the result for a dependent check when the probe connected
// but could not read the catalog.
func (d *doctor) catalogUnread() (Status, string, string, bool) {
	if d.dbStatus.Migrations != nil {
		return "", "", "", false
	}
	cause := "no catalog data"
	if d.dbErr != nil {
		cause = d.redact(d.dbErr.Error())
	}
	return StatusFail, "could not read the catalog: " + cause, "check that the role in MEMORY_PG_DSN may read pg_catalog", true
}

func (d *doctor) checkPGVector(context.Context) (Status, string, string) {
	if st, det, rem, unread := d.catalogUnread(); unread {
		return st, det, rem
	}
	if d.dbStatus.VectorVersion == "" {
		return StatusFail, "the vector extension is not installed in this database",
			"as a superuser on the server, in this database: CREATE EXTENSION vector; (the app role cannot), then `claude-memory migrate`"
	}
	return pass("pgvector " + d.dbStatus.VectorVersion)
}

func (d *doctor) checkPGSchema(context.Context) (Status, string, string) {
	if st, det, rem, unread := d.catalogUnread(); unread {
		return st, det, rem
	}
	var applied, behind []string
	recordsMissing := false
	for _, m := range d.dbStatus.Migrations {
		if m.Applied() {
			applied = append(applied, m.ID)
			continue
		}
		for _, o := range m.Missing {
			if o == "records" {
				recordsMissing = true
			}
		}
		behind = append(behind, m.ID+" (missing "+strings.Join(capList(m.Missing, 3), ", ")+")")
	}
	switch {
	case recordsMissing:
		return StatusFail, "no claude-memory schema: table records is missing",
			"run `claude-memory migrate` (needs the vector extension, see pg.vector)"
	case len(behind) > 0:
		return StatusWarn, "schema behind: " + strings.Join(behind, "; "),
			"run `claude-memory migrate`"
	}
	detail := "migrations " + strings.Join(applied, ", ") + " applied"
	if len(d.dbStatus.Unknown) > 0 {
		return StatusInfo, detail + "; objects no migration of this binary creates (a newer binary?): " +
			strings.Join(capList(d.dbStatus.Unknown, 5), ", "), ""
	}
	return pass(detail)
}

// capList returns at most n items, with a "+k more" marker.
func capList(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	out := append([]string(nil), items[:n]...)
	return append(out, fmt.Sprintf("+%d more", len(items)-n))
}

// roundDur formats a duration for humans: "<1 ms", ms below a second,
// else tenths of a second.
func roundDur(d time.Duration) string {
	if d < time.Millisecond {
		return "<1 ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}
