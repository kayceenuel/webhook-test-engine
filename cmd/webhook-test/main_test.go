package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunReturnsUsageExitCodeForMissingFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--target, --db, and --secret are required") {
		t.Fatalf("stderr = %q, want a required-flags message", stderr.String())
	}
}

func TestRunReturnsUsageExitCodeForInvalidTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--target", "not-a-url",
		"--db", "example.db",
		"--secret", "dev-secret",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "absolute http or https URL") {
		t.Fatalf("stderr = %q, want a target URL message", stderr.String())
	}
}
