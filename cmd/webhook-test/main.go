package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/kayceenuel/webhook-test-engine/internal/engine"
	"github.com/kayceenuel/webhook-test-engine/internal/paystack"
	"github.com/kayceenuel/webhook-test-engine/internal/provider"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("webhook-test", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr,
			"Usage: webhook-test --target URL --db PATH --secret SECRET [flags]")
		flags.PrintDefaults()
	}

	target := flags.String("target", "", "webhook endpoint URL (required)")
	databasePath := flags.String("db", "", "path to the application SQLite database (required)")
	secret := flags.String("secret", "", "Paystack webhook signing secret (required; use a development-only value)")
	reference := flags.String("reference", "", "payment reference (default: generated)")
	amount := flags.Int64("amount", 50000, "amount in minor currency units, such as kobo")
	currency := flags.String("currency", "NGN", "payment currency")
	timeout := flags.Duration("timeout", 2*time.Second, "total scenario timeout covering preflight, all deliveries, and state verification")
	deliveries := flags.Int("deliveries", 1, "number of times to deliver the same webhook sequentially")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "unexpected positional arguments")
		flags.Usage()
		return 2
	}

	if *target == "" || *databasePath == "" || *secret == "" {
		_, _ = fmt.Fprintln(stderr, "--target, --db, and --secret are required")
		flags.Usage()
		return 2
	}
	if *amount <= 0 {
		_, _ = fmt.Fprintln(stderr, "--amount must be greater than zero")
		return 2
	}
	if *currency == "" {
		_, _ = fmt.Fprintln(stderr, "--currency is required")
		return 2
	}
	if *timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "--timeout must be greater than zero")
		return 2
	}
	if *deliveries < 1 {
		_, _ = fmt.Fprintln(stderr, "--deliveries must be at least 1")
		return 2
	}
	if parsed, err := url.Parse(*target); err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		_, _ = fmt.Fprintln(stderr, "--target must be an absolute http or https URL")
		return 2
	}

	if *reference == "" {
		generated, err := newReference()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "generate reference: %v\n", err)
			return 1
		}
		*reference = generated
	}

	scenario := engine.Scenario{
		TargetURL:    *target,
		DatabasePath: *databasePath,
		Timeout:      *timeout,
		Deliveries:   *deliveries,
		Expected: provider.Payment{
			Reference: *reference,
			Amount:    *amount,
			Currency:  *currency,
			Status:    paystack.SuccessfulStatus,
		},
	}

	result, err := engine.Run(context.Background(), paystack.NewAdapter(*secret), scenario)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid scenario: %v\n", err)
		return 2
	}

	_, _ = fmt.Fprintln(stdout, result.Summary())
	if !result.Passed {
		return 1
	}
	return 0
}

func newReference() (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "whe_" + hex.EncodeToString(random), nil
}
