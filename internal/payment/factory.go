package payment

import (
	"fmt"
	"os"
)

// New returns the payment provider configured via the PAYMENT_PROVIDER env var.
//
//   PAYMENT_PROVIDER=lemonsqueezy   (default — works globally including India)
//   PAYMENT_PROVIDER=paypal         (widest global reach, 200+ countries)
//   PAYMENT_PROVIDER=stripe         (if you get Stripe access later)
//
// Add new providers by implementing the Provider interface and adding a
// case here — nothing else in the codebase needs to change.
func New() (Provider, error) {
	name := os.Getenv("PAYMENT_PROVIDER")
	if name == "" {
		name = "lemonsqueezy" // sensible default
	}

	switch name {
	case "lemonsqueezy", "ls":
		p := NewLemonSqueezy()
		if p.apiKey == "" {
			return nil, fmt.Errorf("LEMONSQUEEZY_API_KEY not set")
		}
		return p, nil

	case "paypal":
		p := NewPayPal()
		if p.clientID == "" || p.clientSecret == "" {
			return nil, fmt.Errorf("PAYPAL_CLIENT_ID and PAYPAL_CLIENT_SECRET must be set")
		}
		return p, nil

	default:
		return nil, fmt.Errorf(
			"unknown PAYMENT_PROVIDER %q — supported: lemonsqueezy, paypal", name)
	}
}
