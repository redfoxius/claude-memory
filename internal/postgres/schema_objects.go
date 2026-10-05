package postgres

// Schema objects per embedded migration (plan Design 6). schema_migrations
// exists but nothing writes to it, so the probe detects which migrations are
// applied by introspection: a migration is applied when every object it
// creates is present. probe_test.go fails when a migrations/*.sql file has no
// entry here, or when a table, column or index a migration creates is
// missing from its entry.
//
// Keep this list in step with migrationSQL in store.go: a new migration file
// needs both a //go:embed line there and an entry here.

// schemaObjectKind is what kind of catalog object a schemaObject names.
type schemaObjectKind int

const (
	objExtension schemaObjectKind = iota // pg_extension.extname
	objTable                             // a table in current_schema()
	objColumn                            // a column of a table in current_schema()
	objIndex                             // an index in current_schema()
)

// schemaObject is one catalog object a migration creates.
type schemaObject struct {
	Kind  schemaObjectKind
	Table string // the table (objTable, objColumn, objIndex); "" for objExtension
	Name  string // extension, column or index name; "" for objTable
}

// String is the object's name in MigrationStatus.Missing and DBStatus.Unknown:
// "extension vector", "records", "records.namespace", "index idx_records_repo".
func (o schemaObject) String() string {
	switch o.Kind {
	case objExtension:
		return "extension " + o.Name
	case objTable:
		return o.Table
	case objColumn:
		return o.Table + "." + o.Name
	default:
		return "index " + o.Name
	}
}

// migrationObjects is one embedded migration and the objects it creates.
type migrationObjects struct {
	ID      string // the file name's numeric prefix, e.g. "0001"
	File    string // the file under migrations/
	Objects []schemaObject
}

func ext(name string) schemaObject       { return schemaObject{Kind: objExtension, Name: name} }
func table(name string) schemaObject     { return schemaObject{Kind: objTable, Table: name} }
func column(t, name string) schemaObject { return schemaObject{Kind: objColumn, Table: t, Name: name} }
func index(t, name string) schemaObject  { return schemaObject{Kind: objIndex, Table: t, Name: name} }
func columns(t string, names ...string) []schemaObject {
	out := make([]schemaObject, len(names))
	for i, n := range names {
		out[i] = column(t, n)
	}
	return out
}

// schemaMigrations lists every embedded migration in order with the objects
// it creates. Primary-key indexes (records_pkey, schema_migrations_pkey) and
// constraints are implied by their tables and not listed.
var schemaMigrations = []migrationObjects{
	{
		ID:   "0001",
		File: "0001_init.sql",
		Objects: concat(
			[]schemaObject{ext("vector"), table("schema_migrations"), table("records")},
			columns("schema_migrations", "version", "applied_at"),
			columns("records",
				"id", "kind", "title", "content", "repo", "files", "commit_sha",
				"ticket", "tags", "status", "deprecation_reason", "superseded_by",
				"source", "confidence", "seen_count", "used_count", "created_at",
				"updated_at", "last_used_at", "embedding", "tsvector_content"),
			[]schemaObject{
				index("records", "idx_records_repo"),
				index("records", "idx_records_status"),
				index("records", "idx_records_source"),
				index("records", "idx_records_created_at"),
				index("records", "idx_records_updated_at"),
				index("records", "idx_records_last_used_at"),
				index("records", "idx_records_embedding_hnsw"),
				index("records", "idx_records_tsvector_gin"),
			},
		),
	},
	{
		ID:   "0002",
		File: "0002_namespaces.sql",
		Objects: []schemaObject{
			column("records", "namespace"),
			index("records", "idx_records_namespace_repo"),
			index("records", "idx_records_namespace_status"),
		},
	},
	{
		ID:   "0003",
		File: "0003_events.sql",
		Objects: concat(
			[]schemaObject{table("events")},
			columns("events",
				"id", "at", "namespace", "type", "record_id", "related_id", "source",
				"status", "outcome", "via", "similarity", "stale", "stale_commits",
				"session_id"),
			[]schemaObject{
				index("events", "idx_events_at"),
				index("events", "idx_events_type_at"),
				index("events", "idx_events_record"),
			},
		),
	},
	{
		ID:   "0004",
		File: "0004_mgmt_import.sql",
		Objects: []schemaObject{
			column("records", "import_key"),
			index("records", "idx_records_import_key"),
		},
	},
	{
		ID:      "0005",
		File:    "0005_reliability.sql",
		Objects: []schemaObject{column("events", "error_class")},
	},
}

// ourTables are the tables whose columns and indexes the probe inventories
// to report objects no embedded migration creates (DBStatus.Unknown).
var ourTables = []string{"records", "schema_migrations", "events"}

// MigrationIDs returns the IDs of the embedded migrations, in order.
func MigrationIDs() []string {
	ids := make([]string, len(schemaMigrations))
	for i, m := range schemaMigrations {
		ids[i] = m.ID
	}
	return ids
}

func concat(parts ...[]schemaObject) []schemaObject {
	var out []schemaObject
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
