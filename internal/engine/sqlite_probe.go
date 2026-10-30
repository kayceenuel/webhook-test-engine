package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/kayceenuel/webhook-test-engine/internal/provider"

	_ "modernc.org/sqlite"
)

type sqliteProbe struct {
	db *sql.DB
}

func openSQLiteProbe(path string) (*sqliteProbe, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}

	urlPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	dsn := (&url.URL{Scheme: "file", Path: urlPath}).String() + "?mode=ro"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	return &sqliteProbe{db: db}, nil
}

func (p *sqliteProbe) Close() error {
	return p.db.Close()
}

func (p *sqliteProbe) Lookup(ctx context.Context, reference string) (provider.Payment, bool, error) {
	payment := provider.Payment{Reference: reference}
	err := p.db.QueryRowContext(ctx,
		`SELECT amount, currency, status FROM payments WHERE reference = ?`,
		reference,
	).Scan(&payment.Amount, &payment.Currency, &payment.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return provider.Payment{}, false, nil
	}
	if err != nil {
		return provider.Payment{}, false, fmt.Errorf("query payment state: %w", err)
	}
	return payment, true, nil
}
