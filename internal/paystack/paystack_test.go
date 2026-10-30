package paystack

import (
	"encoding/json"
	"testing"

	"github.com/kayceenuel/webhook-test-engine/internal/provider"
)

const knownBody = `{"event":"charge.success","data":{"reference":"ref_fixed","amount":50000,"currency":"NGN","status":"success"}}`

// This vector was computed independently with HMAC-SHA512 using the key
// "dev-secret" and the exact body above.
const knownSignature = "165c80d5ed4bce14193a198a8eeb8d9ab97a3c14513f7711c2c2c84e69fee03650d0f357df59c0012b144cdaae18f9337dc53776780d671176bcd651d0876ad6"

func TestSignMatchesKnownVector(t *testing.T) {
	if got := Sign([]byte(knownBody), "dev-secret"); got != knownSignature {
		t.Fatalf("Sign() = %q, want %q", got, knownSignature)
	}
}

func TestVerifyRejectsChangedBody(t *testing.T) {
	changed := []byte(knownBody + " ")
	if Verify(changed, knownSignature, "dev-secret") {
		t.Fatal("Verify() accepted a changed body")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	if Verify([]byte(knownBody), knownSignature, "wrong-secret") {
		t.Fatal("Verify() accepted the wrong secret")
	}
}

func TestAdapterBuildsChargeSuccessEvent(t *testing.T) {
	payment := provider.Payment{
		Reference: "ref_adapter",
		Amount:    25000,
		Currency:  "NGN",
		Status:    SuccessfulStatus,
	}
	request, err := NewAdapter("dev-secret").Build(payment)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := request.Headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	signature := request.Headers.Get(SignatureHeader)
	if !Verify(request.Body, signature, "dev-secret") {
		t.Fatal("generated request signature is invalid")
	}

	var event Event
	if err := json.Unmarshal(request.Body, &event); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if event.Event != ChargeSuccessEvent {
		t.Fatalf("event = %q, want %q", event.Event, ChargeSuccessEvent)
	}
	if event.Data != (EventData{
		Reference: payment.Reference,
		Amount:    payment.Amount,
		Currency:  payment.Currency,
		Status:    payment.Status,
	}) {
		t.Fatalf("event data = %+v, want %+v", event.Data, payment)
	}
}

func TestAdapterRequiresSecret(t *testing.T) {
	_, err := NewAdapter("").Build(provider.Payment{Reference: "ref"})
	if err == nil {
		t.Fatal("Build() error = nil, want an error")
	}
}
