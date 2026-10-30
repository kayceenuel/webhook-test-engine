package engine_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kayceenuel/webhook-test-engine/internal/engine"
	"github.com/kayceenuel/webhook-test-engine/internal/exampleapp"
	"github.com/kayceenuel/webhook-test-engine/internal/paystack"
	"github.com/kayceenuel/webhook-test-engine/internal/provider"

	_ "modernc.org/sqlite"
)

const testSecret = "test-secret"

func newTestApp(t *testing.T) (*exampleapp.App, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	app, err := exampleapp.Open(dbPath, testSecret)
	if err != nil {
		t.Fatalf("exampleapp.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Errorf("exampleapp.Close() error = %v", err)
		}
	})
	return app, dbPath
}

func expectedPayment(reference string) provider.Payment {
	return provider.Payment{
		Reference: reference,
		Amount:    50000,
		Currency:  "NGN",
		Status:    paystack.SuccessfulStatus,
	}
}

func testScenario(target, dbPath string, expected provider.Payment, timeout time.Duration) engine.Scenario {
	return engine.Scenario{
		TargetURL:    target,
		DatabasePath: dbPath,
		Timeout:      timeout,
		Expected:     expected,
	}
}

func runTest(t *testing.T, scenario engine.Scenario) engine.Result {
	t.Helper()
	result, err := engine.Run(context.Background(), paystack.NewAdapter(testSecret), scenario)
	if err != nil {
		t.Fatalf("engine.Run() error = %v", err)
	}
	return result
}

func openDatabase(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})
	return db
}

func insertPayment(t *testing.T, db *sql.DB, payment provider.Payment) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO payments (reference, amount, currency, status, received_at)
		 VALUES (?, ?, ?, ?, ?)`,
		payment.Reference, payment.Amount, payment.Currency, payment.Status,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insert payment: %v", err)
	}
}

func TestRunPassesWhenStateMatches(t *testing.T) {
	app, dbPath := newTestApp(t)
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	expected := expectedPayment("ref_pass")
	result := runTest(t, testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expected,
		2*time.Second,
	))

	if !result.Passed {
		t.Fatalf("result = %+v, want PASS", result)
	}
	if result.HTTPStatus != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", result.HTTPStatus)
	}

	db := openDatabase(t, dbPath)
	stored := provider.Payment{Reference: expected.Reference}
	err := db.QueryRow(
		`SELECT amount, currency, status FROM payments WHERE reference = ?`,
		expected.Reference,
	).Scan(&stored.Amount, &stored.Currency, &stored.Status)
	if err != nil {
		t.Fatalf("query stored payment: %v", err)
	}
	if stored != expected {
		t.Fatalf("stored payment = %+v, want %+v", stored, expected)
	}
}

func TestRunFailsWhenHTTP200DoesNotChangeState(t *testing.T) {
	_, dbPath := newTestApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result := runTest(t, testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expectedPayment("ref_200_only"),
		// The timeout also covers the read-only SQLite preflight query. SQLite
		// calls on this development environment have been measured stalling
		// for 139-196 ms, so a 150 ms budget intermittently expired before the
		// HTTP response arrived.
		1*time.Second,
	))

	if result.HTTPStatus != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200; failure = %q", result.HTTPStatus, result.Failure)
	}
	if result.Passed {
		t.Fatal("result passed from HTTP 200 alone, want failure")
	}
	if result.Actual != nil {
		t.Fatalf("result actual = %+v, want nil", result.Actual)
	}
	if !strings.Contains(result.Failure, "not found before timeout") {
		t.Fatalf("failure = %q, want a missing-state message", result.Failure)
	}

	db := openDatabase(t, dbPath)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&count); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if count != 0 {
		t.Fatalf("payment count = %d, want 0", count)
	}
}

func TestRunFailsWhenPersistedAmountIsWrong(t *testing.T) {
	_, dbPath := newTestApp(t)
	expected := expectedPayment("ref_wrong_amount")
	db := openDatabase(t, dbPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := db.Exec(
			`INSERT INTO payments (reference, amount, currency, status, received_at)
			 VALUES (?, ?, ?, ?, ?)`,
			expected.Reference, expected.Amount+1, expected.Currency, expected.Status,
			time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			t.Errorf("insert wrong payment: %v", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result := runTest(t, testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expected,
		2*time.Second,
	))

	if result.Passed {
		t.Fatal("result passed with the wrong amount, want failure")
	}
	if result.HTTPStatus != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", result.HTTPStatus)
	}
	if result.Actual == nil || result.Actual.Amount != expected.Amount+1 {
		t.Fatalf("actual = %+v, want the persisted wrong amount", result.Actual)
	}
	if !strings.Contains(result.Failure, "state mismatch") {
		t.Fatalf("failure = %q, want a state mismatch message", result.Failure)
	}
}

func TestRunFailsOnNon2xxEvenWhenStateMatches(t *testing.T) {
	_, dbPath := newTestApp(t)
	expected := expectedPayment("ref_http_500")
	db := openDatabase(t, dbPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := db.Exec(
			`INSERT INTO payments (reference, amount, currency, status, received_at)
			 VALUES (?, ?, ?, ?, ?)`,
			expected.Reference, expected.Amount, expected.Currency, expected.Status,
			time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			t.Errorf("insert payment: %v", err)
		}
		http.Error(w, "handler reported failure", http.StatusInternalServerError)
	}))
	defer server.Close()

	result := runTest(t, testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expected,
		2*time.Second,
	))

	if result.Passed {
		t.Fatal("result passed on an HTTP 500 response, want failure")
	}
	if result.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("HTTP status = %d, want 500", result.HTTPStatus)
	}
	if result.Actual != nil {
		t.Fatalf("actual = %+v, want nil; state must not be accepted after a non-2xx response", result.Actual)
	}
	if !strings.Contains(result.Failure, "target returned HTTP 500") {
		t.Fatalf("failure = %q, want a non-2xx failure message", result.Failure)
	}

	stored := provider.Payment{Reference: expected.Reference}
	err := db.QueryRow(
		`SELECT amount, currency, status FROM payments WHERE reference = ?`,
		expected.Reference,
	).Scan(&stored.Amount, &stored.Currency, &stored.Status)
	if err != nil {
		t.Fatalf("query stored payment: %v", err)
	}
	if stored != expected {
		t.Fatalf("stored payment = %+v, want %+v", stored, expected)
	}
}

func TestRunRejectsExistingReference(t *testing.T) {
	_, dbPath := newTestApp(t)
	expected := expectedPayment("ref_existing")
	insertPayment(t, openDatabase(t, dbPath), expected)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result := runTest(t, testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expected,
		1*time.Second,
	))

	if result.Passed {
		t.Fatal("result passed for an existing reference, want failure")
	}
	if result.HTTPStatus != 0 {
		t.Fatalf("HTTP status = %d, want 0 (webhook must not be sent)", result.HTTPStatus)
	}
	if requests.Load() != 0 {
		t.Fatalf("webhook requests = %d, want 0", requests.Load())
	}
	if !strings.Contains(result.Failure, "already exists") {
		t.Fatalf("failure = %q, want an existing-reference message", result.Failure)
	}
}

func TestRunReportsDatabaseOpenFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "app.db")
	result := runTest(t, testScenario(
		"http://127.0.0.1:1/webhooks/paystack",
		missing,
		expectedPayment("ref_missing_db"),
		500*time.Millisecond,
	))

	if result.Passed {
		t.Fatal("result passed with a missing database, want failure")
	}
	if !strings.Contains(result.Failure, "open database") {
		t.Fatalf("failure = %q, want an open-database message", result.Failure)
	}
}

func TestRunReportsQueryFailureWhenSchemaIsMissing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	result := runTest(t, testScenario(
		"http://127.0.0.1:1/webhooks/paystack",
		dbPath,
		expectedPayment("ref_missing_schema"),
		500*time.Millisecond,
	))

	if result.Passed {
		t.Fatal("result passed without a payments table, want failure")
	}
	if !strings.Contains(result.Failure, "database query failed") {
		t.Fatalf("failure = %q, want a database query message", result.Failure)
	}
}

func TestRunReportsHTTPTimeout(t *testing.T) {
	_, dbPath := newTestApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// The scenario timeout also covers the read-only SQLite preflight. Keep it
	// comfortably above the 139-196 ms stalls observed on this environment so
	// this test exercises HTTP delivery rather than a database timeout.
	start := time.Now()
	result := runTest(t, testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expectedPayment("ref_timeout"),
		1*time.Second,
	))
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("run took %v, want it bounded by the configured timeout", elapsed)
	}

	if result.Passed {
		t.Fatal("result passed after an HTTP timeout, want failure")
	}
	if !strings.Contains(result.Failure, "delivery failed") {
		t.Fatalf("failure = %q, want an HTTP delivery failure, not a database timeout", result.Failure)
	}
}
