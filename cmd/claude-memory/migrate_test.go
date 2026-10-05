package main

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/redfoxius/claude-memory/internal/postgres"
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

func TestMigrateRejectsArguments(t *testing.T) {
	t.Parallel()
	if err := cmdMigrate(nil, []string{"extra"}); exitCode(err) != 2 {
		t.Errorf("exit code = %d, want 2 (err %v)", exitCode(err), err)
	}
}
