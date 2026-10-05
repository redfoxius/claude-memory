package postgres

import (
	"errors"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"
)

// IsUnavailable reports whether err means the database could not be reached
// or went away: a failed connect (also one that wrapped a deadline), a
// network error, SQLSTATE class 08 (connection exception) or 57P01..57P03
// (admin shutdown, crash shutdown, cannot connect now), or a closed pool. A
// bare context deadline or cancellation is not. An error of another shape
// (e.g. a connection dropped mid-query) is not recognised and counts as
// internal by the caller.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "57P01", "57P02", "57P03":
			return true
		}
		return len(pgErr.Code) >= 2 && pgErr.Code[:2] == "08"
	}
	// Specific net types, not net.Error: context.DeadlineExceeded satisfies
	// net.Error and must not be taken for an outage.
	var opErr *net.OpError
	var dnsErr *net.DNSError
	return errors.As(err, &opErr) || errors.As(err, &dnsErr) || errors.Is(err, puddle.ErrClosedPool)
}
