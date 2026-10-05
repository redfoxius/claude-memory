package main

import (
	"os"
	"testing"
)

// runMainEnv makes the test binary act as claude-memory itself (see
// runChild in dispatch_test.go): with it set, TestMain runs main() with the
// child's os.Args instead of the tests.
const runMainEnv = "CLAUDE_MEMORY_TEST_RUN_MAIN"

// TestMain isolates the command tests from the developer's real
// ~/.config/claude-memory/namespaces.yaml and MEMORY_NAMESPACE.
func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	resolveNamespace = func(string) string { return "test-ns" }
	resolveNamespaceQuiet = func(string) string { return "test-ns" }
	os.Exit(m.Run())
}
