package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"claude-memory/internal/namespace"
)

const namespacesUsage = `usage:
  claude-memory namespaces init [--default NAME] [--force] [NAME=GLOB ...]
  claude-memory namespaces add NAME GLOB [GLOB ...]
  claude-memory namespaces which [DIR]

  init   create ~/.config/claude-memory/namespaces.yaml (part of installation)
  add    map more project paths to a namespace (creates it if new)
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
