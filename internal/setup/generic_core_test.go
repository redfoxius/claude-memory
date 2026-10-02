package setup

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// Extractability rule: the install engine should stay extractable into its
// own module later. The "generic core" files below must therefore import
// only the stdlib and third-party packages, never a package of this module
// (import paths starting with "claude-memory/"). Add a file to the list
// when it becomes domain-free.
//
// ports.go is intentionally NOT listed because it imports internal/namespace;
// runstate.go is not listed because it holds claude-memory-specific state
// fields.

const projectImportPrefix = "claude-memory/"

var genericCoreFiles = []string{
	"engine.go",
	"manifest.go",
	"prompt.go",
	"render.go",
	"diff.go",
	"redact.go",
	"jsonobj.go",
	"mdblock.go",
	"settings.go",
}

// projectImports parses only the import block of src (or of the file at
// filename when src is nil) and returns the imports of this module.
func projectImports(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(path, projectImportPrefix) {
			bad = append(bad, path)
		}
	}
	return bad, nil
}

func TestGenericCoreHasNoProjectImports(t *testing.T) {
	for _, name := range genericCoreFiles {
		bad, err := projectImports(name, nil)
		if err != nil {
			t.Errorf("%s: cannot parse (listed file missing or invalid?): %v", name, err)
			continue
		}
		for _, path := range bad {
			t.Errorf("%s imports project package %q: generic core files must stay free of %s* imports", name, path, projectImportPrefix)
		}
	}
}

func TestGenericCoreCheckerDetectsViolation(t *testing.T) {
	src := "package x\n\nimport (\n\t\"fmt\"\n\t\"claude-memory/internal/x\"\n)\n"
	bad, err := projectImports("fake.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 1 || bad[0] != "claude-memory/internal/x" {
		t.Fatalf("violation not detected, got %v", bad)
	}
	bad, err = projectImports("ok.go", "package x\n\nimport \"fmt\"\n")
	if err != nil || len(bad) != 0 {
		t.Fatalf("false positive: %v, %v", bad, err)
	}
}
