package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"claude-memory/internal/config"
	"claude-memory/internal/namespace"
)

// namespacesFile is the per-user path→namespace mapping.
func namespacesFile() string {
	return filepath.Join(os.Getenv("HOME"), ".config", "claude-memory", "namespaces.yaml")
}

// resolveNamespace picks the namespace for a project directory: an explicit
// MEMORY_NAMESPACE wins, else the most specific rule in namespaces.yaml,
// else its `default:`, else the built-in fallback. Errors (unreadable file,
// invalid names) are logged and degrade to the fallback, so a broken config
// never takes memory offline — but never to another namespace's data.
// It is a variable so tests can substitute it.
var resolveNamespace = func(dir string) string {
	override := os.Getenv(config.NamespaceEnv)
	nsCfg, err := namespace.Load(namespacesFile())
	if err != nil {
		slog.Warn("namespaces.yaml unusable; using fallback namespace", "error", err)
		nsCfg = &namespace.Config{}
	}
	ns, err := nsCfg.ForDir(override, dir)
	if err != nil {
		slog.Warn("invalid "+config.NamespaceEnv+"; using namespaces.yaml resolution", "error", err)
		ns = nsCfg.Resolve(dir)
	}
	return ns
}
