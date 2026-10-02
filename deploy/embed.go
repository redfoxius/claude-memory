// Package deploy embeds the server-side deployment assets the installer
// shares with the Docker server stack (spec AC-21, AC-34): the initdb
// scripts and the .env template. A directory pattern skips dotfiles, so
// .env.example is named explicitly. Slice 2 adds initdb/app-role.psql (the
// shared bootstrap SQL); slice 3 the compose bundle.
package deploy

import "embed"

// FS holds the embedded deployment assets, at their paths relative to this
// directory.
//
//go:embed initdb .env.example
var FS embed.FS

// Asset paths inside FS; embed_test.go asserts every one of them exists.
const (
	InitDBDir     = "initdb"
	InitDBAppRole = "initdb/01-app-role.sh"
	// InitDBAppRolePSQL is the shared psql-variable script: the compose
	// stack's 01-app-role.sh runs it, and the installer renders bootstrap.sql
	// from it.
	InitDBAppRolePSQL = "initdb/app-role.psql"
	EnvExample        = ".env.example"
)
