package exampleapp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kayceenuel/webhook-test-engine/internal/paystack"
	"github.com/kayceenuel/webhook-test-engine/internal/provider"
)

const testSecret = "test-secret"

func newTestApp(t *testing.T) (*App, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	app, err := Open(dbPath, testSecret)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return app, dbPath
}

func signedRequest(t *testing.T, payment provider.Payment) *http.Request {
	t.Helper()
	request, err := paystack.NewAdapter(testSecret).Build(payment)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/paystack", bytes.NewReader(request.Body))
	for name, values := range request.Headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return req
}

func TestValidEventIsPersisted(t *testing.T) {
	app, _ := newTestApp(t)
	expected := provider.Payment{
		Reference: "ref_valid",
		Amount:    50000,
		Currency:  "NGN",
		Status:    paystack.SuccessfulStatus,
	}

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, signedRequest(t, expected))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", recorder.Code, recorder.Body.String())
	}

	var amount int64
	var currency, status, receivedAt string
	err := app.db.QueryRow(
		`SELECT amount, currency, status, received_at FROM payments WHERE reference = ?`,
		expected.Reference,
	).Scan(&amount, &currency, &status, &receivedAt)
	if err != nil {
		t.Fatalf("query stored payment: %v", err)
	}
	if amount != expected.Amount || currency != expected.Currency || status != expected.Status {
		t.Fatalf("stored payment = amount %d, currency %s, status %s; want %+v",
			amount, currency, status, expected)
	}
	if _, err := time.Parse(time.RFC3339Nano, receivedAt); err != nil {
		t.Fatalf("received_at %q is not RFC3339Nano: %v", receivedAt, err)
	}
}

func storedPayment(t *testing.T, app *App, reference string) (int64, string, string, string) {
	t.Helper()
	var amount int64
	var currency, status, receivedAt string
	err := app.db.QueryRow(
		`SELECT amount, currency, status, received_at FROM payments WHERE reference = ?`,
		reference,
	).Scan(&amount, &currency, &status, &receivedAt)
	if err != nil {
		t.Fatalf("query stored payment: %v", err)
	}
	return amount, currency, status, receivedAt
}

func paymentCount(t *testing.T, app *App) int {
	t.Helper()
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&count); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	return count
}

func TestDuplicateEventIsIdempotent(t *testing.T) {
	app, _ := newTestApp(t)
	payment := provider.Payment{
		Reference: "ref_duplicate",
		Amount:    50000,
		Currency:  "NGN",
		Status:    paystack.SuccessfulStatus,
	}

	for delivery := 1; delivery <= 2; delivery++ {
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, signedRequest(t, payment))
		if recorder.Code != http.StatusOK {
			t.Fatalf("delivery %d status = %d, want 200; body = %q",
				delivery, recorder.Code, recorder.Body.String())
		}
	}

	if count := paymentCount(t, app); count != 1 {
		t.Fatalf("payment count = %d, want 1", count)
	}
}

func TestConflictingEventIsRejectedWithoutChangingOriginal(t *testing.T) {
	changes := map[string]func(*provider.Payment){
		"amount":   func(payment *provider.Payment) { payment.Amount++ },
		"currency": func(payment *provider.Payment) { payment.Currency = "USD" },
	}

	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			app, _ := newTestApp(t)
			original := provider.Payment{
				Reference: "ref_conflict",
				Amount:    50000,
				Currency:  "NGN",
				Status:    paystack.SuccessfulStatus,
			}

			first := httptest.NewRecorder()
			app.Handler().ServeHTTP(first, signedRequest(t, original))
			if first.Code != http.StatusOK {
				t.Fatalf("first delivery status = %d, want 200; body = %q",
					first.Code, first.Body.String())
			}
			originalAmount, originalCurrency, originalStatus, originalReceivedAt :=
				storedPayment(t, app, original.Reference)

			conflicting := original
			change(&conflicting)
			second := httptest.NewRecorder()
			app.Handler().ServeHTTP(second, signedRequest(t, conflicting))
			if second.Code != http.StatusConflict {
				t.Fatalf("conflicting delivery status = %d, want 409; body = %q",
					second.Code, second.Body.String())
			}

			amount, currency, status, receivedAt := storedPayment(t, app, original.Reference)
			if amount != originalAmount || currency != originalCurrency ||
				status != originalStatus || receivedAt != originalReceivedAt {
				t.Fatalf("stored payment changed to amount %d, currency %s, status %s, received_at %s; want %d, %s, %s, %s",
					amount, currency, status, receivedAt,
					originalAmount, originalCurrency, originalStatus, originalReceivedAt)
			}
			if count := paymentCount(t, app); count != 1 {
				t.Fatalf("payment count = %d, want 1", count)
			}
		})
	}
}

func TestInvalidSignatureIsRejected(t *testing.T) {
	app, _ := newTestApp(t)
	req := signedRequest(t, provider.Payment{
		Reference: "ref_bad_signature",
		Amount:    50000,
		Currency:  "NGN",
		Status:    paystack.SuccessfulStatus,
	})
	req.Header.Set(paystack.SignatureHeader, "invalid")

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&count); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if count != 0 {
		t.Fatalf("payment count = %d, want 0", count)
	}
}

func TestMalformedInputIsRejected(t *testing.T) {
	app, _ := newTestApp(t)
	body := []byte(`{"event":`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/paystack", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(paystack.SignatureHeader, paystack.Sign(body, testSecret))

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %q", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&count); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if count != 0 {
		t.Fatalf("payment count = %d, want 0", count)
	}
}

func TestUnsupportedEventIsRejected(t *testing.T) {
	app, _ := newTestApp(t)
	body, err := json.Marshal(paystack.Event{
		Event: "transfer.success",
		Data: paystack.EventData{
			Reference: "ref_unsupported",
			Amount:    50000,
			Currency:  "NGN",
			Status:    paystack.SuccessfulStatus,
		},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/paystack", bytes.NewReader(body))
	req.Header.Set(paystack.SignatureHeader, paystack.Sign(body, testSecret))

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
}
