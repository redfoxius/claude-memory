package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"claude-memory/internal/postgres"
)

// TestMigrationIDsMatchEmbeddedFiles checks that the IDs the "migrate"
// summary prints (postgres.MigrationIDs) are exactly the migration files.
func TestMigrationIDsMatchEmbeddedFiles(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "..", "internal", "postgres", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob migrations: %v (%d files)", err, len(files))
	}
	var ids []string
	for _, f := range files {
		id, _, _ := strings.Cut(filepath.Base(f), "_")
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, postgres.MigrationIDs()) {
		t.Errorf("postgres.MigrationIDs() = %v, migrations dir has %v", postgres.MigrationIDs(), ids)
	}
}

func TestDSNPassword(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"postgresql://u:secret@h:5432/db":         "secret",
		"postgres://u:p%40ss@h/db":                "p%40ss",
		"postgres://u:ab/c+d==@h/db?sslmode=x":    "ab/c+d==",
		"postgres://u@h/db":                       "",
		"host=h user=u password=x":                "",
		"postgres://u:pw-with-@-inside@h:5432/db": "pw-with-@-inside",
	}
	for dsn, want := range cases {
		if got := dsnPassword(dsn); got != want {
			t.Errorf("dsnPassword(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestMigrateErrorIsRedacted(t *testing.T) {
	t.Parallel()
	const pw = "S3ntinel-pw-long"
	dsn := "postgresql://u:" + pw + "@h/db"
	got := redactWithDSN(dsn).RedactError(errors.New("cannot parse `" + dsn + "`: failed; password " + pw))
	if strings.Contains(got, pw) {
		t.Errorf("password leaked: %q", got)
	}
}

func TestMigrateRejectsArguments(t *testing.T) {
	t.Parallel()
	if err := cmdMigrate(nil, []string{"extra"}); exitCode(err) != 2 {
		t.Errorf("exit code = %d, want 2 (err %v)", exitCode(err), err)
	}
}
