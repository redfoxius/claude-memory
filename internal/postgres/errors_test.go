package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"
)

func TestIsUnavailable(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"connect error", &pgconn.ConnectError{}, true},
		{"connect error wrapping deadline", fmt.Errorf("ping: %w", &pgconn.ConnectError{}), true},
		{"dial error", fmt.Errorf("x: %w", dial), true},
		{"dns error", &net.DNSError{Err: "no such host"}, true},
		{"08006", &pgconn.PgError{Code: "08006"}, true},
		{"57P01", &pgconn.PgError{Code: "57P01"}, true},
		{"57P03", &pgconn.PgError{Code: "57P03"}, true},
		{"closed pool", fmt.Errorf("x: %w", puddle.ErrClosedPool), true},
		{"undefined table", &pgconn.PgError{Code: "42P01"}, false},
		{"constraint", &pgconn.PgError{Code: "23514"}, false},
		{"bare deadline", context.DeadlineExceeded, false},
		{"canceled", context.Canceled, false},
		{"other", errors.New("boom"), false},
	}
	for _, tc := range cases {
		if got := IsUnavailable(tc.err); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
