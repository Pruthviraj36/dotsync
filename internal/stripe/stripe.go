package stripehandler

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	stripe "github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/customer"
	"github.com/Pruthviraj36/dotsync/internal/db"
)

// Plan mapping from Stripe price IDs to DotSync plan names
var PriceIDToPlan = map[string]string{
	// Set these to your actual Stripe Price IDs
	"price_pro_monthly":      "pro",
	"price_team_monthly":     "team",
	"price_business_monthly": "business",
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

// POST /api/stripe/webhook — receives all Stripe events
func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 65536))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	// Verify webhook signature — prevents spoofed events
	// In stripe-go v86, ConstructEvent is on the stripe package directly
	event, err := stripe.ConstructEvent(body, r.Header.Get("Stripe-Signature"), h.webhookSecret)
	if err != nil {
		log.Printf("stripe webhook signature failed: %v", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	switch event.Type {
	case "customer.subscription.created", "customer.subscription.updated":
		h.handleSubscriptionUpsert(event)
	case "customer.subscription.deleted":
		h.handleSubscriptionDeleted(event)
	case "invoice.payment_failed":
		h.handlePaymentFailed(event)
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleSubscriptionUpsert(event stripe.Event) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		log.Printf("stripe: unmarshal subscription: %v", err)
		return
	}

	plan := "free"
	if len(sub.Items.Data) > 0 {
		priceID := sub.Items.Data[0].Price.ID
		if p, ok := PriceIDToPlan[priceID]; ok {
			plan = p
		}
	}

	// Only update if subscription is active
	if sub.Status != "active" && sub.Status != "trialing" {
		plan = "free"
	}

	customerID := sub.Customer.ID
	_, err := h.db.Exec(`
		UPDATE users SET plan = $1, stripe_subscription_id = $2, updated_at = NOW()
		WHERE stripe_customer_id = $3`,
		plan, sub.ID, customerID,
	)
	if err != nil {
		log.Printf("stripe: update user plan: %v", err)
	} else {
		log.Printf("stripe: updated customer %s to plan %s", customerID, plan)
	}
}

func (h *Handler) handleSubscriptionDeleted(event stripe.Event) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		return
	}

	_, _ = h.db.Exec(`
		UPDATE users SET plan = 'free', stripe_subscription_id = NULL, updated_at = NOW()
		WHERE stripe_customer_id = $1`, sub.Customer.ID)

	log.Printf("stripe: subscription cancelled for customer %s, downgraded to free", sub.Customer.ID)
}

func (h *Handler) handlePaymentFailed(event stripe.Event) {
	var invoice stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return
	}
	log.Printf("stripe: payment failed for customer %s", invoice.Customer.ID)
	// TODO: send email notification (extend with email service)
}

// CreateOrGetCustomer creates a Stripe customer for a user or returns existing.
func (h *Handler) CreateOrGetCustomer(userID, email, username string) (string, error) {
	// Check if already has stripe customer
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
		return "", err
	}

	_, err = h.db.Exec(
		`UPDATE users SET stripe_customer_id = $1 WHERE id = $2`, c.ID, userID,
	)
	return c.ID, err
}
