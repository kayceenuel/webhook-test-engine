package paystack

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kayceenuel/webhook-test-engine/internal/provider"
)

const (
	// SignatureHeader is the header Paystack uses for webhook signatures.
	SignatureHeader = "X-Paystack-Signature"
	// ChargeSuccessEvent is the only event supported by this milestone.
	ChargeSuccessEvent = "charge.success"
	// SuccessfulStatus is the payment status expected after charge.success.
	SuccessfulStatus = "success"
)

// Event is the minimal Paystack charge.success webhook payload used by the
// engine and the example application.
type Event struct {
	Event string    `json:"event"`
	Data  EventData `json:"data"`
}

// EventData contains the payment fields used by this milestone.
type EventData struct {
	Reference string `json:"reference"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
}

// Adapter builds signed Paystack webhook requests.
type Adapter struct {
	secret string
}

// NewAdapter returns a Paystack adapter using the supplied signing secret.
func NewAdapter(secret string) *Adapter {
	return &Adapter{secret: secret}
}

// Name implements provider.Adapter.
func (a *Adapter) Name() string {
	return "paystack"
}

// Build implements provider.Adapter.
func (a *Adapter) Build(payment provider.Payment) (provider.Request, error) {
	if a.secret == "" {
		return provider.Request{}, fmt.Errorf("paystack signing secret is required")
	}

	status := payment.Status
	if status == "" {
		status = SuccessfulStatus
	}

	event := Event{
		Event: ChargeSuccessEvent,
		Data: EventData{
			Reference: payment.Reference,
			Amount:    payment.Amount,
			Currency:  payment.Currency,
			Status:    status,
		},
	}

	body, err := json.Marshal(event)
	if err != nil {
		return provider.Request{}, fmt.Errorf("marshal paystack event: %w", err)
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set(SignatureHeader, Sign(body, a.secret))

	return provider.Request{Body: body, Headers: headers}, nil
}

// NewChargeSuccess returns a minimal successful payment event.
func NewChargeSuccess(reference string, amount int64, currency string) Event {
	return Event{
		Event: ChargeSuccessEvent,
		Data: EventData{
			Reference: reference,
			Amount:    amount,
			Currency:  currency,
			Status:    SuccessfulStatus,
		},
	}
}

// Sign returns the hex-encoded HMAC-SHA512 signature for body.
func Sign(body []byte, secret string) string {
	return hex.EncodeToString(sign(body, secret))
}

// Verify checks a hex-encoded HMAC-SHA512 signature in constant time.
func Verify(body []byte, signature, secret string) bool {
	got, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return false
	}
	return hmac.Equal(sign(body, secret), got)
}

func sign(body []byte, secret string) []byte {
	mac := hmac.New(sha512.New, []byte(secret))
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}
