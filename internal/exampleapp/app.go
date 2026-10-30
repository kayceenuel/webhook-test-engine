package exampleapp

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kayceenuel/webhook-test-engine/internal/paystack"

	_ "modernc.org/sqlite"
)

// MaxBodyBytes limits the webhook request body size.
const MaxBodyBytes = 1 << 20

const schema = `
CREATE TABLE IF NOT EXISTS payments (
	reference   TEXT PRIMARY KEY,
	amount      INTEGER NOT NULL,
	currency    TEXT NOT NULL,
	status      TEXT NOT NULL,
	received_at TEXT NOT NULL
);`

// App is the local SQLite-backed example webhook receiver.
type App struct {
	db     *sql.DB
	secret string
}

// Open initializes the example application database and returns the app.
func Open(dbPath, secret string) (*App, error) {
	if dbPath == "" {
		return nil, errors.New("database path is required")
	}
	if secret == "" {
		return nil, errors.New("webhook secret is required")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	return &App{db: db, secret: secret}, nil
}

// Close closes the database.
func (a *App) Close() error {
	return a.db.Close()
}

// Handler returns the example application HTTP handler.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/paystack", a.handlePaystack)
	return mux
}

func (a *App) handlePaystack(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if !paystack.Verify(body, r.Header.Get(paystack.SignatureHeader), a.secret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var event paystack.Event
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}
	if event.Event != paystack.ChargeSuccessEvent {
		http.Error(w, "unsupported event", http.StatusUnprocessableEntity)
		return
	}

	payment := event.Data
	if payment.Reference == "" || payment.Amount <= 0 || payment.Currency == "" ||
		payment.Status != paystack.SuccessfulStatus {
		http.Error(w, "invalid payment event", http.StatusUnprocessableEntity)
		return
	}

	receivedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := a.db.ExecContext(r.Context(),
		`INSERT INTO payments (reference, amount, currency, status, received_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(reference) DO NOTHING`,
		payment.Reference, payment.Amount, payment.Currency, payment.Status, receivedAt,
	)
	if err != nil {
		http.Error(w, "failed to persist payment", http.StatusInternalServerError)
		return
	}

	inserted, err := result.RowsAffected()
	if err != nil {
		http.Error(w, "failed to persist payment", http.StatusInternalServerError)
		return
	}
	if inserted == 0 {
		var amount int64
		var currency, status string
		err := a.db.QueryRowContext(r.Context(),
			`SELECT amount, currency, status FROM payments WHERE reference = ?`,
			payment.Reference,
		).Scan(&amount, &currency, &status)
		if err != nil {
			http.Error(w, "failed to load existing payment", http.StatusInternalServerError)
			return
		}
		if amount != payment.Amount || currency != payment.Currency || status != payment.Status {
			http.Error(w, "payment reference already exists with different details", http.StatusConflict)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}
