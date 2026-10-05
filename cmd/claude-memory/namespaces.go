package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/redfoxius/claude-memory/internal/namespace"
)

const namespacesUsage = `usage:
  claude-memory namespaces init [--default NAME] [--force] [NAME=GLOB ...]
  claude-memory namespaces add NAME GLOB [GLOB ...]
  claude-memory namespaces list [--json]
  claude-memory namespaces which [DIR]

  init   create ~/.config/claude-memory/namespaces.yaml (part of installation)
  add    map more project paths to a namespace (creates it if new)
  list   show every namespace with its path globs and the default
  which  show which namespace a directory resolves to and why (default: current dir)

With no mappings, "init" creates a file whose default is "global": every
project shares one namespace until you add mappings. GLOB examples:
  ~/work/acme/**   $PWD/**   ~/src/pet-game`

// cmdNamespaces implements the "namespaces" subcommand. It needs no
// database or Ollama, so main dispatches it before loading the full config.
func cmdNamespaces(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", namespacesUsage)
	}
	path := namespacesFile()

	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("namespaces init", flag.ContinueOnError)
		def := fs.String("default", "", "default namespace for unmatched directories (default: global)")
		force := fs.Bool("force", false, "overwrite an existing file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var rules []namespace.Rule
		for _, m := range fs.Args() {
			name, glob, ok := strings.Cut(m, "=")
			if !ok || name == "" || glob == "" {
				return fmt.Errorf("mapping %q must look like NAME=GLOB", m)
			}
			rules = append(rules, namespace.Rule{Namespace: name, Paths: []string{glob}})
		}
		if err := namespace.Init(path, *def, rules, *force); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", path)
		return nil

	case "add":
		if len(args) < 3 {
			return fmt.Errorf("%s", namespacesUsage)
		}
		if err := namespace.Add(path, args[1], args[2:]...); err != nil {
			return err
		}
		fmt.Printf("updated %s\n", path)
		return nil

	case "list":
		fs := flag.NewFlagSet("namespaces list", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "machine-readable output")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return listNamespaces(os.Stdout, path, *asJSON)

	case "which":
		dir := ""
		if len(args) > 1 {
			dir = args[1]
		} else {
			dir, _ = os.Getwd()
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		ns, why := explainNamespace(dir)
		fmt.Printf("%s\t(%s)\n", ns, why)
		return nil
	}
	return fmt.Errorf("unknown namespaces command %q\n%s", args[0], namespacesUsage)
}

// namespacesListJSON is the stable --json shape of `namespaces list`.
type namespacesListJSON struct {
	Default    string            `json:"default"`
	Namespaces []namespace.Entry `json:"namespaces"`
}

// listNamespaces prints the namespaces in the file at path to w, as a table
// or (asJSON) as JSON. A missing file is fine: only the built-in global
// namespace is shown, with a hint to run `namespaces init`.
func listNamespaces(w io.Writer, path string, asJSON bool) error {
	cfg, err := namespace.Load(path)
	if err != nil {
		return err
	}
	entries := cfg.List()
	def := cfg.EffectiveDefault()
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(namespacesListJSON{Default: def, Namespaces: entries})
	}

	tw := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
	fmt.Fprintln(tw, "NAMESPACE\tPATHS")
	for _, e := range entries {
		switch {
		case len(e.Paths) > 0:
			fmt.Fprintf(tw, "%s\t%s\n", e.Namespace, e.Paths[0])
			for _, g := range e.Paths[1:] {
				fmt.Fprintf(tw, "\t%s\n", g)
			}
		case e.Namespace == namespace.Fallback:
			fmt.Fprintf(tw, "%s\t(shared; searched together with every namespace)\n", e.Namespace)
		default:
			fmt.Fprintf(tw, "%s\t(default for unmatched directories)\n", e.Namespace)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if cfg.Default == "" {
		fmt.Fprintf(w, "default: %s (built-in)\n", def)
	} else {
		fmt.Fprintf(w, "default: %s\n", def)
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		fmt.Fprintf(w, "no %s yet; run `claude-memory namespaces init` to create it\n", path)
	}
	return nil
}
