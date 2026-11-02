package main

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kayceenuel/webhook-test-engine/internal/exampleapp"
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

func TestRunRejectsInvalidDeliveryCount(t *testing.T) {
	for _, count := range []string{"0", "-1"} {
		t.Run(count, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{
				"--target", "http://127.0.0.1:8080/webhooks/paystack",
				"--db", "example.db",
				"--secret", "dev-secret",
				"--deliveries", count,
			}, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("run() = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), "--deliveries must be at least 1") {
				t.Fatalf("stderr = %q, want a delivery-count message", stderr.String())
			}
		})
	}
}

func TestRunSendsOneDeliveryByDefault(t *testing.T) {
	server, dbPath := newTestServer(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--target", server.URL + "/webhooks/paystack",
		"--db", dbPath,
		"--secret", "dev-secret",
		"--timeout", "5s",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "http=200 state=verified") {
		t.Fatalf("stdout = %q, want a single-delivery PASS line", stdout.String())
	}
}

func TestRunWithTwoDeliveries(t *testing.T) {
	server, dbPath := newTestServer(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--target", server.URL + "/webhooks/paystack",
		"--db", dbPath,
		"--secret", "dev-secret",
		"--deliveries", "2",
		"--timeout", "5s",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "http=200,200 state=verified") {
		t.Fatalf("stdout = %q, want both delivery statuses", stdout.String())
	}
}

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	app, err := exampleapp.Open(dbPath, "dev-secret")
	if err != nil {
		t.Fatalf("exampleapp.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Errorf("exampleapp.Close() error = %v", err)
		}
	})

	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	return server, dbPath
}
