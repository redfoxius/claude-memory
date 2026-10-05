package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/namespace"
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
	return resolveNamespaceWith(dir, slog.Warn)
}

// resolveNamespaceQuiet is resolveNamespace without any log output, for
// callers that must stay silent on stderr (the hook's build-failure path).
// It is a variable so tests can substitute it.
var resolveNamespaceQuiet = quietResolveNamespace

func quietResolveNamespace(dir string) string {
	return resolveNamespaceWith(dir, func(string, ...any) {})
}

func resolveNamespaceWith(dir string, warn func(msg string, args ...any)) string {
	override := os.Getenv(config.NamespaceEnv)
	nsCfg, err := namespace.Load(namespacesFile())
	if err != nil {
		warn("namespaces.yaml unusable; using fallback namespace", "error", err)
		nsCfg = &namespace.Config{}
	}
	ns, err := nsCfg.ForDir(override, dir)
	if err != nil {
		warn("invalid "+config.NamespaceEnv+"; using namespaces.yaml resolution", "error", err)
		ns = nsCfg.Resolve(dir)
	}
	return ns
}

// loadNamespaces loads namespaces.yaml, degrading to an empty config (with a
// warning) when it is unreadable.
func loadNamespaces() *namespace.Config {
	nsCfg, err := namespace.Load(namespacesFile())
	if err != nil {
		slog.Warn("namespaces.yaml unusable; using fallback namespace", "error", err)
		return &namespace.Config{}
	}
	return nsCfg
}

// explainNamespace is resolveNamespace plus how the result was chosen
// (env, rule <glob>, default, fallback), for `namespaces which`.
func explainNamespace(dir string) (ns, why string) {
	nsCfg := loadNamespaces()
	if override := os.Getenv(config.NamespaceEnv); override != "" {
		if got, err := nsCfg.ForDir(override, dir); err == nil {
			return got, "env " + config.NamespaceEnv
		}
	}
	return nsCfg.Explain(dir)
}

// warnIfFallback logs once per call site when a background writer (session
// extraction, PR ingest) had to use the built-in global fallback because no
// mapping exists for dir — a hint to run `claude-memory namespaces add`.
// Records written this way can be re-homed later (see DEPLOY.md).
func warnIfFallback(dir, ns string) {
	if _, why := explainNamespace(dir); why == namespace.WhyFallback {
		slog.Warn("no namespace mapping for directory; writing to the shared global namespace (see `claude-memory namespaces add`)", "dir", dir, "namespace", ns)
	}
}
