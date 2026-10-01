package main

import (
	"os"
	"testing"
)

// TestMain isolates the command tests from the developer's real
// ~/.config/claude-memory/namespaces.yaml and MEMORY_NAMESPACE.
func TestMain(m *testing.M) {
	resolveNamespace = func(string) string { return "test-ns" }
	os.Exit(m.Run())
}
