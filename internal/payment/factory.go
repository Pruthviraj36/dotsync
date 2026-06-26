package payment

import (
	"context"
	"fmt"
	"os"
)

// New returns the payment provider configured via the PAYMENT_PROVIDER env var.
//
//   PAYMENT_PROVIDER=lemonsqueezy   (default — works globally including India)
//   PAYMENT_PROVIDER=paypal         (widest global reach, 200+ countries)
//
// If no payment credentials are configured the server starts normally but
// all billing endpoints return a clear "billing not configured" error
// instead of crashing at boot. Set the credentials and restart to enable
// billing.
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
			// Billing not yet configured — start the server with a no-op provider
			// that returns actionable errors. Nothing else is broken.
			return &unconfiguredProvider{
				name: "lemonsqueezy",
				hint: "Set LEMONSQUEEZY_API_KEY, LEMONSQUEEZY_STORE_ID, " +
					"LS_VARIANT_PRO, LS_VARIANT_TEAM, LS_VARIANT_BUSINESS " +
					"in your environment (see .env for details).",
			}, nil
		}
		return p, nil

	case "paypal":
		p := NewPayPal()
		if p.clientID == "" || p.clientSecret == "" {
			return &unconfiguredProvider{
				name: "paypal",
				hint: "Set PAYPAL_CLIENT_ID, PAYPAL_CLIENT_SECRET, " +
					"PAYPAL_PLAN_PRO, PAYPAL_PLAN_TEAM, PAYPAL_PLAN_BUSINESS " +
					"in your environment (see .env for details).",
			}, nil
		}
		return p, nil

	default:
		return nil, fmt.Errorf(
			"unknown PAYMENT_PROVIDER %q — supported: lemonsqueezy, paypal", name)
	}
}

// unconfiguredProvider is a no-op Provider returned when payment credentials
// are not set. The server boots normally; billing endpoints return a clear
// error pointing operators to the missing env vars.
type unconfiguredProvider struct {
	name string
	hint string
}

func (u *unconfiguredProvider) Name() string { return u.name }

func (u *unconfiguredProvider) GetOrCreateCustomer(_ context.Context, _ CustomerRequest) (string, error) {
	return "", fmt.Errorf("billing not configured — %s", u.hint)
}

func (u *unconfiguredProvider) CreateCheckoutSession(_ context.Context, _ CheckoutRequest) (string, error) {
	return "", fmt.Errorf("billing not configured — %s", u.hint)
}

func (u *unconfiguredProvider) CreatePortalSession(_ context.Context, _ string, _ string) (string, error) {
	return "", fmt.Errorf("billing not configured — %s", u.hint)
}

func (u *unconfiguredProvider) VerifyWebhook(_ []byte, _ string) (*WebhookEvent, error) {
	return nil, fmt.Errorf("billing not configured — %s", u.hint)
}
