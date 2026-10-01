package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSleepStub writes an executable shell script that sleeps for d
// regardless of the arguments it's invoked with, simulating a slow
// extraction run without ever shelling out to the real `claude` CLI.
func writeSleepStub(t *testing.T, d time.Duration) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sleep-stub.sh")
	script := fmt.Sprintf("#!/bin/sh\nsleep %s\n", d)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("failed to write stub script: %v", err)
	}
	return path
}

func TestExtractCmdReturnsBeforeBackgroundProcessCompletes(t *testing.T) {
	stub := writeSleepStub(t, 2*time.Second)
	logPath := filepath.Join(t.TempDir(), "extract.log")

	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("failed to write fixture transcript: %v", err)
	}

	stdin := strings.NewReader(fmt.Sprintf(`{"transcript_path":%q,"cwd":"/tmp","session_id":"session-1"}`, transcriptPath))

	start := time.Now()
	if err := extractCmd(stdin, stub, logPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("expected extractCmd to return within 100ms (hook-added latency, AC-21), took %v", elapsed)
	}
}

func TestExtractCmdNoTranscriptPathIsNoop(t *testing.T) {
	stub := writeSleepStub(t, 0)
	logPath := filepath.Join(t.TempDir(), "extract.log")
	stdin := strings.NewReader(`{"cwd":"/tmp","session_id":"session-1"}`)

	if err := extractCmd(stdin, stub, logPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No transcript_path: nothing should have been launched, and nothing
	// should ever write to the log file.
	if _, err := os.Stat(logPath); err == nil {
		t.Errorf("expected no log file to be created when transcript_path is empty")
	}
}

func TestExtractCmdMalformedStdinIsNoop(t *testing.T) {
	stub := writeSleepStub(t, 0)
	logPath := filepath.Join(t.TempDir(), "extract.log")
	stdin := strings.NewReader(`{not valid json`)

	if err := extractCmd(stdin, stub, logPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
