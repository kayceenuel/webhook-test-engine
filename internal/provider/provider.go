// Package provider defines the small boundary between the core engine and a
// webhook provider implementation.
package provider

import "net/http"

// Payment is the application state the engine expects after a webhook is
// processed successfully.
type Payment struct {
	Reference string
	Amount    int64
	Currency  string
	Status    string
}

// Request is a provider-specific webhook request.
type Request struct {
	Body    []byte
	Headers http.Header
}

// Adapter builds provider-specific webhook requests.
type Adapter interface {
	Name() string
	Build(payment Payment) (Request, error)
}
