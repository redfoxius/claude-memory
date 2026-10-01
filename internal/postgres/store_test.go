package postgres

import (
	"context"
	"strings"
	"testing"
)

// TestUpdate_RejectsNonWhitelistedColumn exercises Store.Update's column
// whitelist (store.go's allowedColumns map) without needing a real
// Postgres connection: the whitelist check runs and returns before the
// method ever touches s.pool, so a zero-value *Store (nil pool) is safe to
// use here as long as the updates map contains at least one disallowed
// key (the only path that returns before reaching the pool).
func TestUpdate_RejectsNonWhitelistedColumn(t *testing.T) {
	s := &Store{} // pool intentionally nil; must never be dereferenced below.

	cases := []struct {
		name    string
		updates map[string]interface{}
	}{
		{"sql-injection-shaped column name", map[string]interface{}{"id = '1'; DROP TABLE records; --": "x"}},
		{"unrelated internal marker key", map[string]interface{}{"content_changed": true}},
		{"arbitrary unknown column", map[string]interface{}{"not_a_real_column": "value"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Update(context.Background(), "some-id", tc.updates)
			if err == nil {
				t.Fatalf("expected Update to reject a non-whitelisted column, got nil error")
			}
			if !strings.Contains(err.Error(), "not allowed") {
				t.Errorf("expected a 'column not allowed' error, got: %v", err)
			}
		})
	}
}

