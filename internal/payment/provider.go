package payment

import "context"

// Provider is the interface every payment backend must implement.
// Swap providers by changing the PAYMENT_PROVIDER env var — no other
// code changes needed. handler.go and main.go only ever see this interface.
type Provider interface {
	// CreateCheckoutSession creates a hosted payment page for the given plan.
	// Returns the URL to redirect the user to.
	CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (string, error)

	// CreatePortalSession creates a self-serve billing management URL.
	// Used for: cancel, update card, download invoices.
	CreatePortalSession(ctx context.Context, customerID string, returnURL string) (string, error)

	// GetOrCreateCustomer returns an existing provider customer ID for the
	// user, or creates a new one if none exists yet.
	GetOrCreateCustomer(ctx context.Context, req CustomerRequest) (string, error)

	// VerifyWebhook parses and verifies a raw webhook payload.
	// Returns a normalized WebhookEvent regardless of which provider sent it.
	VerifyWebhook(payload []byte, signature string) (*WebhookEvent, error)

	// Name returns the provider identifier ("lemonsqueezy", "paypal", etc.)
	// Used in logs.
	Name() string
}

// CheckoutRequest contains everything needed to start a checkout session.
type CheckoutRequest struct {
	CustomerID string // provider customer ID
	PriceID    string // provider-specific price/variant ID
	SuccessURL string
	CancelURL  string
	UserID     string // dotsync user ID, stored in metadata
	Username   string
}

// CustomerRequest contains what we know about the user when creating a customer.
type CustomerRequest struct {
	DotSyncUserID string
	Email         string
	Username      string
}

// WebhookEvent is a normalized event from any provider.
// Each provider's VerifyWebhook maps their native event to this struct.
type WebhookEvent struct {
	Type           EventType
	CustomerID     string // provider customer ID
	SubscriptionID string // provider subscription ID
	PlanID         string // provider price/variant/plan ID
	Status         string // "active" | "trialing" | "cancelled" | "past_due"
	Raw            []byte // original payload for debugging
}

type EventType string

const (
	EventSubscriptionCreated EventType = "subscription.created"
	EventSubscriptionUpdated EventType = "subscription.updated"
	EventSubscriptionDeleted EventType = "subscription.deleted"
	EventPaymentFailed       EventType = "payment.failed"
	EventPaymentSucceeded    EventType = "payment.succeeded"
	EventUnknown             EventType = "unknown"
)
