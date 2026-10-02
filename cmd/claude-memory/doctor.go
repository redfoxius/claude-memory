package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// doctorOptions are the doctor flags (AC-4).
type doctorOptions struct {
	JSON     bool          // --json: one JSON object (AC-60)
	Strict   bool          // --strict: a warn also exits 1 (AC-59)
	Timeout  time.Duration // --timeout: per check (default 3s)
	Deadline time.Duration // --deadline: whole run (default 10s)
}

// deferredDoctorFlags exit 2 with a pointer to the spec (AC-4, §12.1).
var deferredDoctorFlags = map[string]string{
	"latency": "doctor --latency is deferred (spec §12.1, AC-61)",
}

// parseDoctorFlags parses the doctor command line. Every error it returns is
// a usage error (exit 2).
func parseDoctorFlags(args []string, stderr io.Writer) (doctorOptions, error) {
	for _, a := range args {
		name := strings.TrimLeft(a, "-")
		name, _, _ = strings.Cut(name, "=")
		if msg, ok := deferredDoctorFlags[name]; ok && strings.HasPrefix(a, "-") {
			return doctorOptions{}, usageError(errors.New(msg))
		}
	}
	var o doctorOptions
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.JSON, "json", false, "print one JSON object instead of text")
	fs.BoolVar(&o.Strict, "strict", false, "exit 1 on any warn as well as on a fail")
	fs.DurationVar(&o.Timeout, "timeout", 3*time.Second, "time limit per check")
	fs.DurationVar(&o.Deadline, "deadline", 10*time.Second, "time limit for the whole run")
	if err := fs.Parse(args); err != nil {
		return doctorOptions{}, usageError(err)
	}
	if fs.NArg() > 0 {
		return doctorOptions{}, usageError(fmt.Errorf("doctor takes no arguments, got %q", fs.Args()))
	}
	if o.Timeout <= 0 || o.Deadline <= 0 {
		return doctorOptions{}, usageError(errors.New("--timeout and --deadline must be positive"))
	}
	return o, nil
}

// cmdDoctor runs the read-only health check (AC-57..AC-60). main.go parses
// the flags and builds deps (read-only FS and Runner) before calling it.
//
// Slice 1 phase 1: dispatch, flags and adapters exist; the check registry and
// the renderers land with WI-S1-11/WI-S1-12.
func cmdDoctor(_ context.Context, _ doctorOptions, _ setupDeps) error {
	return errors.New("doctor: not implemented yet")
}
