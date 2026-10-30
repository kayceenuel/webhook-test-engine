package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kayceenuel/webhook-test-engine/internal/provider"
)

const pollInterval = 50 * time.Millisecond

// Scenario describes one webhook test.
type Scenario struct {
	TargetURL    string
	DatabasePath string
	Timeout      time.Duration
	Expected     provider.Payment
}

// Result describes the outcome of one webhook test.
type Result struct {
	Passed     bool
	Provider   string
	Reference  string
	HTTPStatus int
	Actual     *provider.Payment
	Failure    string
}

// Summary returns a concise human-readable result line.
func (r Result) Summary() string {
	if r.Passed {
		return fmt.Sprintf("PASS %s reference=%s http=%d state=verified",
			r.Provider, r.Reference, r.HTTPStatus)
	}
	if r.HTTPStatus != 0 {
		return fmt.Sprintf("FAIL %s reference=%s http=%d: %s",
			r.Provider, r.Reference, r.HTTPStatus, r.Failure)
	}
	return fmt.Sprintf("FAIL %s reference=%s: %s", r.Provider, r.Reference, r.Failure)
}

// Run delivers one webhook and verifies the expected application state.
// Returned errors indicate invalid scenario configuration. Operational
// failures, including HTTP and database failures, are reported in Result.
func Run(ctx context.Context, adapter provider.Adapter, scenario Scenario) (Result, error) {
	if adapter == nil {
		return Result{}, errors.New("provider adapter is required")
	}
	if err := validateScenario(scenario); err != nil {
		return Result{}, err
	}

	result := Result{
		Provider:  adapter.Name(),
		Reference: scenario.Expected.Reference,
	}

	runCtx, cancel := context.WithTimeout(ctx, scenario.Timeout)
	defer cancel()

	request, err := adapter.Build(scenario.Expected)
	if err != nil {
		result.Failure = fmt.Sprintf("build webhook: %v", err)
		return result, nil
	}

	probe, err := openSQLiteProbe(scenario.DatabasePath)
	if err != nil {
		result.Failure = fmt.Sprintf("open database: %v", err)
		return result, nil
	}
	defer probe.Close()

	existing, found, err := probe.Lookup(runCtx, scenario.Expected.Reference)
	if err != nil {
		result.Failure = probeFailure(runCtx, err)
		return result, nil
	}
	if found {
		result.Actual = &existing
		result.Failure = fmt.Sprintf("reference %q already exists; refusing to send", scenario.Expected.Reference)
		return result, nil
	}

	status, err := deliver(runCtx, scenario.TargetURL, request)
	if err != nil {
		result.Failure = fmt.Sprintf("delivery failed: %v", err)
		return result, nil
	}
	result.HTTPStatus = status
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		result.Failure = fmt.Sprintf("target returned HTTP %d", status)
		return result, nil
	}

	expected := scenario.Expected
	for {
		if runCtx.Err() != nil {
			setTimeoutFailure(&result, expected)
			return result, nil
		}

		actual, found, err := probe.Lookup(runCtx, expected.Reference)
		if err != nil {
			if runCtx.Err() != nil {
				setTimeoutFailure(&result, expected)
			} else {
				result.Failure = probeFailure(runCtx, err)
			}
			return result, nil
		}
		if found {
			result.Actual = &actual
			if actual == expected {
				result.Passed = true
				return result, nil
			}
		}

		select {
		case <-runCtx.Done():
			setTimeoutFailure(&result, expected)
			return result, nil
		case <-time.After(pollInterval):
		}
	}
}

func validateScenario(scenario Scenario) error {
	if scenario.TargetURL == "" {
		return errors.New("target URL is required")
	}
	target, err := url.Parse(scenario.TargetURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return fmt.Errorf("target URL must be an absolute http or https URL")
	}
	if scenario.DatabasePath == "" {
		return errors.New("database path is required")
	}
	if scenario.Timeout <= 0 {
		return errors.New("timeout must be greater than zero")
	}
	if scenario.Expected.Reference == "" {
		return errors.New("expected reference is required")
	}
	if scenario.Expected.Amount <= 0 {
		return errors.New("expected amount must be greater than zero")
	}
	if scenario.Expected.Currency == "" {
		return errors.New("expected currency is required")
	}
	if scenario.Expected.Status == "" {
		return errors.New("expected status is required")
	}
	return nil
}

func deliver(ctx context.Context, target string, request provider.Request) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(request.Body))
	if err != nil {
		return 0, err
	}
	for name, values := range request.Headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

func setTimeoutFailure(result *Result, expected provider.Payment) {
	if result.Actual != nil {
		result.Failure = fmt.Sprintf("state mismatch: got %s; want %s",
			formatPayment(*result.Actual), formatPayment(expected))
		return
	}
	result.Failure = fmt.Sprintf("expected payment %q not found before timeout", expected.Reference)
}

func probeFailure(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "timed out while querying database"
	}
	return fmt.Sprintf("database query failed: %v", err)
}

func formatPayment(payment provider.Payment) string {
	return fmt.Sprintf("reference=%s amount=%d currency=%s status=%s",
		payment.Reference, payment.Amount, payment.Currency, payment.Status)
}
