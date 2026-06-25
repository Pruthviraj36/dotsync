package stripehandler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/Pruthviraj36/dotsync/internal/db"
	stripe "github.com/stripe/stripe-go/v86"
	checkoutsession "github.com/stripe/stripe-go/v86/checkout/session"
	portalsession "github.com/stripe/stripe-go/v86/billingportal/session"
	"github.com/stripe/stripe-go/v86/customer"
)

// PriceIDToPlan maps Stripe Price IDs (set via env vars) to DotSync plan names.
// Use STRIPE_PRICE_PRO, STRIPE_PRICE_TEAM, STRIPE_PRICE_BUSINESS env vars.
var PriceIDToPlan map[string]string

func init() {
	PriceIDToPlan = map[string]string{}
	for env, plan := range map[string]string{
		"STRIPE_PRICE_PRO":      "pro",
		"STRIPE_PRICE_TEAM":     "team",
		"STRIPE_PRICE_BUSINESS": "business",
	} {
		if id := os.Getenv(env); id != "" {
			PriceIDToPlan[id] = plan
		}
	}
}

type Handler struct {
	db            *db.DB
	webhookSecret string
}

func New(database *db.DB) *Handler {
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")
	return &Handler{
		db:            database,
		webhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
	}
}

// POST /api/stripe/webhook — receives all Stripe events.
// Raw body required — do NOT use body-parsing middleware before this.
func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 65536))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	event, err := stripe.ConstructEvent(body, r.Header.Get("Stripe-Signature"), h.webhookSecret)
	if err != nil {
		log.Printf("stripe webhook signature failed: %v", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var procErr error
	switch event.Type {
	case "customer.subscription.created", "customer.subscription.updated":
		procErr = h.handleSubscriptionUpsert(event)
	case "customer.subscription.deleted":
		procErr = h.handleSubscriptionDeleted(event)
	case "invoice.payment_failed":
		procErr = h.handlePaymentFailed(event)
	case "invoice.payment_succeeded":
		procErr = h.handlePaymentSucceeded(event)
	default:
		// Unhandled event types — acknowledge receipt so Stripe doesn't retry
		log.Printf("stripe: unhandled event type %s", event.Type)
	}

	if procErr != nil {
		// Return 500 so Stripe retries — a transient DB error here would
		// permanently drop a subscription state update.
		log.Printf("stripe: processing %s failed: %v", event.Type, procErr)
		http.Error(w, "processing failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleSubscriptionUpsert(event stripe.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		return fmt.Errorf("unmarshal subscription: %w", err)
	}

	plan := "free"
	if len(sub.Items.Data) > 0 {
		priceID := sub.Items.Data[0].Price.ID
		if p, ok := PriceIDToPlan[priceID]; ok {
			plan = p
		} else {
			log.Printf("stripe: unknown price ID %s — defaulting to free. Add STRIPE_PRICE_* env vars.", priceID)
		}
	}

	if sub.Status != stripe.SubscriptionStatusActive && sub.Status != stripe.SubscriptionStatusTrialing {
		plan = "free"
	}

	customerID := sub.Customer.ID
	result, err := h.db.Exec(`
		UPDATE users SET plan = $1, stripe_subscription_id = $2, updated_at = NOW()
		WHERE stripe_customer_id = $3`,
		plan, sub.ID, customerID,
	)
	if err != nil {
		return fmt.Errorf("update user plan: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		log.Printf("stripe: no user found for customer %s — webhook may have arrived before user record created", customerID)
	} else {
		log.Printf("stripe: customer %s → plan %s (sub %s)", customerID, plan, sub.ID)
	}
	return nil
}

func (h *Handler) handleSubscriptionDeleted(event stripe.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		return fmt.Errorf("unmarshal subscription: %w", err)
	}

	_, err := h.db.Exec(`
		UPDATE users SET plan = 'free', stripe_subscription_id = NULL, updated_at = NOW()
		WHERE stripe_customer_id = $1`, sub.Customer.ID)
	if err != nil {
		return fmt.Errorf("downgrade user plan: %w", err)
	}
	log.Printf("stripe: subscription cancelled for customer %s → free", sub.Customer.ID)
	return nil
}

func (h *Handler) handlePaymentFailed(event stripe.Event) error {
	var invoice stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return fmt.Errorf("unmarshal invoice: %w", err)
	}
	log.Printf("stripe: payment FAILED for customer %s, invoice %s", invoice.Customer.ID, invoice.ID)
	// TODO: send email notification
	return nil
}

func (h *Handler) handlePaymentSucceeded(event stripe.Event) error {
	var invoice stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return fmt.Errorf("unmarshal invoice: %w", err)
	}
	log.Printf("stripe: payment succeeded for customer %s, invoice %s", invoice.Customer.ID, invoice.ID)
	return nil
}

// CreateOrGetCustomer creates a Stripe customer for a user, or returns the existing one.
func (h *Handler) CreateOrGetCustomer(userID, email, username string) (string, error) {
	var stripeID string
	err := h.db.QueryRow(
		`SELECT COALESCE(stripe_customer_id, '') FROM users WHERE id = $1`, userID,
	).Scan(&stripeID)
	if err == nil && stripeID != "" {
		return stripeID, nil
	}

	params := &stripe.CustomerParams{
		Email: stripe.String(email),
		Metadata: map[string]string{
			"user_id":  userID,
			"username": username,
		},
	}
	c, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe customer.New: %w", err)
	}

	_, err = h.db.Exec(
		`UPDATE users SET stripe_customer_id = $1 WHERE id = $2`, c.ID, userID,
	)
	if err != nil {
		return "", fmt.Errorf("save stripe customer id: %w", err)
	}
	log.Printf("stripe: created customer %s for user %s", c.ID, username)
	return c.ID, nil
}

// CreateCheckoutSession creates a Stripe Checkout session for upgrading a plan.
// The user is redirected to this URL in their browser to complete payment.
func (h *Handler) CreateCheckoutSession(customerID, priceID, successURL, cancelURL string) (string, error) {
	params := &stripe.CheckoutSessionParams{
		Customer: stripe.String(customerID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceID),
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: stripe.String(successURL),
		CancelURL:  stripe.String(cancelURL),
		// Allow updating existing subscription (upgrade/downgrade)
		SubscriptionData: &stripe.CheckoutSessionSubscriptionDataParams{
			Metadata: map[string]string{
				"source": "dotsync-cli",
			},
		},
	}

	s, err := checkoutsession.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe checkout.session.New: %w", err)
	}
	return s.URL, nil
}

// CreatePortalSession creates a Stripe Customer Portal session.
// Used for: cancel subscription, update payment method, download invoices.
func (h *Handler) CreatePortalSession(customerID, returnURL string) (string, error) {
	params := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(returnURL),
	}
	s, err := portalsession.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe portal.session.New: %w", err)
	}
	return s.URL, nil
}
