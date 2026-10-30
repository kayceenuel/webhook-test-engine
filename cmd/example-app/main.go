package main

import (
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kayceenuel/webhook-test-engine/internal/exampleapp"
)

const developmentSecret = "dev-secret"

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	dbPath := flag.String("db", "example.db", "path to the SQLite database")
	secret := flag.String("secret", developmentSecret, "development-only webhook signing secret")
	flag.Parse()

	app, err := exampleapp.Open(*dbPath, *secret)
	if err != nil {
		slog.Error("failed to start example app", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := app.Close(); err != nil {
			slog.Error("failed to close database", "error", err)
		}
	}()

	if *secret == developmentSecret {
		slog.Warn("using development-only webhook secret")
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	slog.Info("example app listening", "addr", *addr, "db", *dbPath)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
