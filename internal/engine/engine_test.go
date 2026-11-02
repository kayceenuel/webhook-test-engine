package engine_test

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

func assertStatuses(t *testing.T, result engine.Result, want ...int) {
	t.Helper()
	if len(result.HTTPStatuses) != len(want) {
		t.Fatalf("HTTP statuses = %v, want %v", result.HTTPStatuses, want)
	}
	for i := range want {
		if result.HTTPStatuses[i] != want[i] {
			t.Fatalf("HTTP statuses = %v, want %v", result.HTTPStatuses, want)
		}
	}
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
	assertStatuses(t, result, http.StatusOK)

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

	assertStatuses(t, result, http.StatusOK)
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
	assertStatuses(t, result, http.StatusOK)
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
	assertStatuses(t, result, http.StatusInternalServerError)
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
	if len(result.HTTPStatuses) != 0 {
		t.Fatalf("HTTP statuses = %v, want none (webhook must not be sent)", result.HTTPStatuses)
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

func TestRunPassesWithTwoDeliveries(t *testing.T) {
	app, dbPath := newTestApp(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		app.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()

	expected := expectedPayment("ref_two_deliveries")
	scenario := testScenario(server.URL+"/webhooks/paystack", dbPath, expected, 5*time.Second)
	scenario.Deliveries = 2
	result := runTest(t, scenario)

	if !result.Passed {
		t.Fatalf("result = %+v, want PASS", result)
	}
	assertStatuses(t, result, http.StatusOK, http.StatusOK)
	if requests.Load() != 2 {
		t.Fatalf("webhook requests = %d, want 2", requests.Load())
	}

	db := openDatabase(t, dbPath)
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM payments WHERE reference = ?`,
		expected.Reference,
	).Scan(&count); err != nil {
		t.Fatalf("count stored payments: %v", err)
	}
	if count != 1 {
		t.Fatalf("payment count = %d, want 1", count)
	}
}

func TestRunReusesSameRequestForEveryDelivery(t *testing.T) {
	app, dbPath := newTestApp(t)

	var mu sync.Mutex
	var bodies [][]byte
	var signatures []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		signatures = append(signatures, r.Header.Get(paystack.SignatureHeader))
		mu.Unlock()

		r.Body = io.NopCloser(bytes.NewReader(body))
		app.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()

	scenario := testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expectedPayment("ref_same_request"),
		5*time.Second,
	)
	scenario.Deliveries = 3
	result := runTest(t, scenario)

	if !result.Passed {
		t.Fatalf("result = %+v, want PASS", result)
	}
	assertStatuses(t, result, http.StatusOK, http.StatusOK, http.StatusOK)

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("webhook requests = %d, want 3", len(bodies))
	}
	if signatures[0] == "" {
		t.Fatal("signature header is empty, want the signed request header")
	}
	for i := 1; i < len(bodies); i++ {
		if !bytes.Equal(bodies[0], bodies[i]) {
			t.Fatalf("delivery %d body = %q, want the same body as delivery 1 %q",
				i+1, bodies[i], bodies[0])
		}
		if signatures[i] != signatures[0] {
			t.Fatalf("delivery %d signature = %q, want %q",
				i+1, signatures[i], signatures[0])
		}
	}
}

func TestRunDefaultsToOneDelivery(t *testing.T) {
	app, dbPath := newTestApp(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		app.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()

	expected := expectedPayment("ref_default_delivery")
	result := runTest(t, testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expected,
		5*time.Second,
	))

	if !result.Passed {
		t.Fatalf("result = %+v, want PASS", result)
	}
	assertStatuses(t, result, http.StatusOK)
	if requests.Load() != 1 {
		t.Fatalf("webhook requests = %d, want 1", requests.Load())
	}
}

func TestRunFailsWhenDuplicateDeliveryCreatesExtraRecords(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "duplicates.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})
	if _, err := db.Exec(`CREATE TABLE payments (
		reference   TEXT NOT NULL,
		amount      INTEGER NOT NULL,
		currency    TEXT NOT NULL,
		status      TEXT NOT NULL,
		received_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create payments table: %v", err)
	}

	expected := expectedPayment("ref_duplicate_records")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := db.Exec(
			`INSERT INTO payments (reference, amount, currency, status, received_at)
			 VALUES (?, ?, ?, ?, ?)`,
			expected.Reference, expected.Amount, expected.Currency, expected.Status,
			time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			t.Errorf("insert payment: %v", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	scenario := testScenario(server.URL+"/webhooks/paystack", dbPath, expected, 5*time.Second)
	scenario.Deliveries = 2
	result := runTest(t, scenario)

	if result.Passed {
		t.Fatal("result passed with duplicate payment records, want failure")
	}
	assertStatuses(t, result, http.StatusOK, http.StatusOK)
	if !strings.Contains(result.Failure, `found 2 records for reference "ref_duplicate_records"`) {
		t.Fatalf("failure = %q, want a duplicate-record message", result.Failure)
	}
}

func TestRunFailsWhenSecondDeliveryReturns500(t *testing.T) {
	app, dbPath := newTestApp(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 2 {
			http.Error(w, "handler reported failure", http.StatusInternalServerError)
			return
		}
		app.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()

	scenario := testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expectedPayment("ref_second_delivery_500"),
		5*time.Second,
	)
	scenario.Deliveries = 2
	result := runTest(t, scenario)

	if result.Passed {
		t.Fatal("result passed on a failed second delivery, want failure")
	}
	assertStatuses(t, result, http.StatusOK, http.StatusInternalServerError)
	if !strings.Contains(result.Failure, "delivery 2 of 2 returned HTTP 500") {
		t.Fatalf("failure = %q, want the delivery number and status", result.Failure)
	}
	if requests.Load() != 2 {
		t.Fatalf("webhook requests = %d, want the run to stop after 2", requests.Load())
	}
}

func TestRunFailsWhenFirstDeliveryDoesNotPersist(t *testing.T) {
	app, dbPath := newTestApp(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		app.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()

	scenario := testScenario(
		server.URL+"/webhooks/paystack",
		dbPath,
		expectedPayment("ref_first_delivery_ignored"),
		1*time.Second,
	)
	scenario.Deliveries = 2
	result := runTest(t, scenario)

	if result.Passed {
		t.Fatal("result passed although the first delivery was ignored, want failure")
	}
	assertStatuses(t, result, http.StatusOK)
	if requests.Load() != 1 {
		t.Fatalf("webhook requests = %d, want the run to stop after the first delivery", requests.Load())
	}
	if !strings.Contains(result.Failure, "not found before timeout") {
		t.Fatalf("failure = %q, want a missing-state message", result.Failure)
	}
}

func TestRunRejectsNegativeDeliveryCount(t *testing.T) {
	_, dbPath := newTestApp(t)
	scenario := testScenario(
		"http://127.0.0.1:1/webhooks/paystack",
		dbPath,
		expectedPayment("ref_negative_deliveries"),
		time.Second,
	)
	scenario.Deliveries = -1

	_, err := engine.Run(context.Background(), paystack.NewAdapter(testSecret), scenario)
	if err == nil {
		t.Fatal("engine.Run() error = nil, want an invalid delivery count error")
	}
	if !strings.Contains(err.Error(), "deliveries must not be negative") {
		t.Fatalf("error = %q, want a delivery count message", err)
	}
}
