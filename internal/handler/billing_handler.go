package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/Pruthviraj36/dotsync/internal/auth"
	"github.com/Pruthviraj36/dotsync/internal/db"
	"github.com/Pruthviraj36/dotsync/internal/model"
	"github.com/Pruthviraj36/dotsync/internal/payment"
)

// BillingHandler handles all payment-related HTTP routes.
// It talks only to the payment.Provider interface — swapping providers
// (LemonSqueezy ↔ PayPal ↔ Stripe) requires only an env var change.
type BillingHandler struct {
	provider payment.Provider
	db       *db.DB
}

func NewBillingHandler(p payment.Provider, database *db.DB) *BillingHandler {
	return &BillingHandler{provider: p, db: database}
}

// GET /api/billing/plans — public, no auth required
func (h *BillingHandler) Plans(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"provider": h.provider.Name(),
		"plans": []map[string]any{
			{
				"id": "free", "name": "Free", "price_usd": 0,
				"max_projects": 1, "max_members": 3, "history_days": 7,
				"audit_logs": false, "leak_detect": false,
			},
			{
				"id": "pro", "name": "Pro", "price_usd": 9,
				"max_projects": -1, "max_members": 5, "history_days": 30,
				"audit_logs": false, "leak_detect": true,
				"price_id": providerPriceID("pro"),
			},
			{
				"id": "team", "name": "Team", "price_usd": 29,
				"max_projects": -1, "max_members": 10, "history_days": 90,
				"audit_logs": false, "leak_detect": true,
				"price_id": providerPriceID("team"),
			},
			{
				"id": "business", "name": "Business", "price_usd": 79,
				"max_projects": -1, "max_members": -1, "history_days": 365,
				"audit_logs": true, "leak_detect": true,
				"price_id": providerPriceID("business"),
			},
		},
	})
}

// POST /api/billing/checkout
// Body: {"plan": "pro"|"team"|"business"}
func (h *BillingHandler) Checkout(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())

	var req struct {
		Plan string `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Plan == "" {
		writeError(w, http.StatusBadRequest, "plan required (pro, team, business)")
		return
	}

	priceID := providerPriceID(req.Plan)
	if priceID == "" {
		writeError(w, http.StatusBadRequest, "invalid plan or billing not configured for this plan")
		return
	}

	var email, username string
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT email, username FROM users WHERE id = $1`, claims.UserID,
	).Scan(&email, &username)

	// Get or create provider customer
	customerID, err := h.provider.GetOrCreateCustomer(r.Context(), payment.CustomerRequest{
		DotSyncUserID: claims.UserID,
		Email:         email,
		Username:      username,
	})
	if err != nil {
		log.Printf("billing checkout: get/create customer: %v", err)
		writeError(w, http.StatusInternalServerError, "billing setup failed")
		return
	}

	// Persist customerID if provider returned one (LemonSqueezy returns "" — that's fine)
	if customerID != "" {
		_, _ = h.db.ExecContext(r.Context(),
			`UPDATE users SET stripe_customer_id = $1 WHERE id = $2`, customerID, claims.UserID)
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "https://dotsync.onrender.com"
	}

	checkoutURL, err := h.provider.CreateCheckoutSession(r.Context(), payment.CheckoutRequest{
		CustomerID: customerID,
		PriceID:    priceID,
		SuccessURL: frontendURL + "/billing/success?plan=" + req.Plan,
		CancelURL:  frontendURL + "/billing/cancel",
		UserID:     claims.UserID,
		Username:   username,
	})
	if err != nil {
		log.Printf("billing checkout: create session: %v", err)
		writeError(w, http.StatusInternalServerError, "checkout session failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"checkout_url": checkoutURL,
		"provider":     h.provider.Name(),
	})
}

// POST /api/billing/portal
func (h *BillingHandler) Portal(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())

	var customerID string
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT COALESCE(stripe_customer_id, '') FROM users WHERE id = $1`, claims.UserID,
	).Scan(&customerID)

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "https://dotsync.onrender.com"
	}

	portalURL, err := h.provider.CreatePortalSession(r.Context(), customerID, frontendURL+"/billing")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"portal_url": portalURL,
		"provider":   h.provider.Name(),
	})
}

// GET /api/billing/status
func (h *BillingHandler) Status(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromCtx(r.Context())

	var plan, subID string
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT plan, COALESCE(stripe_subscription_id, '') FROM users WHERE id = $1`, claims.UserID,
	).Scan(&plan, &subID)

	limits := model.Plans[plan]
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":             plan,
		"provider":         h.provider.Name(),
		"has_subscription": subID != "",
		"limits": map[string]any{
			"max_projects": limits.MaxProjects,
			"max_members":  limits.MaxMembers,
			"history_days": limits.HistoryDays,
			"audit_logs":   limits.HasAuditLogs,
			"leak_detect":  limits.HasLeakDetect,
		},
	})
}

// POST /api/payment/webhook — unified webhook endpoint for all providers.
// Register this URL in your payment provider's dashboard.
// For PayPal: the signature param is a JSON-encoded map of PayPal headers.
func WebhookHandler(p payment.Provider, database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := readBody(r, 65536)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		// Each provider uses a different signature header
		sig := providerSignatureHeader(r, p.Name())

		event, err := p.VerifyWebhook(payload, sig)
		if err != nil {
			log.Printf("webhook [%s] verify failed: %v", p.Name(), err)
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}

		if err := processWebhookEvent(r.Context(), database, p, event); err != nil {
			log.Printf("webhook [%s] process %s failed: %v", p.Name(), event.Type, err)
			http.Error(w, "processing failed", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

func processWebhookEvent(ctx context.Context, database *db.DB, p payment.Provider, event *payment.WebhookEvent) error {
	switch event.Type {
	case payment.EventSubscriptionCreated, payment.EventSubscriptionUpdated:
		plan := planFromEvent(p, event)
		if event.Status != "active" && event.Status != "trialing" {
			plan = "free"
		}

		// Try matching by customer ID first, then by user_id in custom field
		result, err := database.ExecContext(ctx, `
			UPDATE users SET plan = $1, stripe_subscription_id = $2, updated_at = NOW()
			WHERE stripe_customer_id = $3`,
			plan, event.SubscriptionID, event.CustomerID,
		)
		if err != nil {
			return fmt.Errorf("update plan by customer: %w", err)
		}
		rows, _ := result.RowsAffected()

		// PayPal stores our user_id in CustomerID — try direct user ID match
		if rows == 0 && event.CustomerID != "" {
			_, err = database.ExecContext(ctx, `
				UPDATE users SET plan = $1, stripe_subscription_id = $2, updated_at = NOW()
				WHERE id = $3`,
				plan, event.SubscriptionID, event.CustomerID,
			)
			if err != nil {
				return fmt.Errorf("update plan by user id: %w", err)
			}
		}
		log.Printf("webhook [%s]: customer %s → plan %s", p.Name(), event.CustomerID, plan)

	case payment.EventSubscriptionDeleted:
		_, err := database.ExecContext(ctx, `
			UPDATE users SET plan = 'free', stripe_subscription_id = NULL, updated_at = NOW()
			WHERE stripe_customer_id = $1 OR id = $1`, event.CustomerID)
		if err != nil {
			return fmt.Errorf("downgrade plan: %w", err)
		}
		log.Printf("webhook [%s]: subscription cancelled for %s → free", p.Name(), event.CustomerID)

	case payment.EventPaymentFailed:
		log.Printf("webhook [%s]: payment FAILED for customer %s", p.Name(), event.CustomerID)

	case payment.EventPaymentSucceeded:
		log.Printf("webhook [%s]: payment succeeded for customer %s", p.Name(), event.CustomerID)

	case payment.EventUnknown:
		// acknowledged but not processed

	}
	return nil
}

// providerPriceID reads the right env var for the given plan + active provider.
func providerPriceID(plan string) string {
	provider := os.Getenv("PAYMENT_PROVIDER")
	if provider == "" {
		provider = "lemonsqueezy"
	}
	switch provider {
	case "lemonsqueezy", "ls":
		return map[string]string{
			"pro": os.Getenv("LS_VARIANT_PRO"), "team": os.Getenv("LS_VARIANT_TEAM"),
			"business": os.Getenv("LS_VARIANT_BUSINESS"),
		}[plan]
	case "paypal":
		return map[string]string{
			"pro": os.Getenv("PAYPAL_PLAN_PRO"), "team": os.Getenv("PAYPAL_PLAN_TEAM"),
			"business": os.Getenv("PAYPAL_PLAN_BUSINESS"),
		}[plan]
	}
	return ""
}

// planFromEvent maps a provider-specific plan/variant ID to a dotsync plan.
func planFromEvent(p payment.Provider, event *payment.WebhookEvent) string {
	switch v := p.(type) {
	case *payment.LemonSqueezy:
		return v.PlanFromVariant(event.PlanID)
	case *payment.PayPal:
		return v.PlanFromPayPalPlan(event.PlanID)
	}
	return "free"
}

// providerSignatureHeader returns the webhook signature string for the provider.
// PayPal requires multiple headers bundled together.
func providerSignatureHeader(r *http.Request, providerName string) string {
	switch providerName {
	case "paypal":
		// Bundle all PayPal verification headers as JSON
		headers := map[string]string{
			"PAYPAL-AUTH-ALGO":         r.Header.Get("PAYPAL-AUTH-ALGO"),
			"PAYPAL-CERT-URL":          r.Header.Get("PAYPAL-CERT-URL"),
			"PAYPAL-TRANSMISSION-ID":   r.Header.Get("PAYPAL-TRANSMISSION-ID"),
			"PAYPAL-TRANSMISSION-SIG":  r.Header.Get("PAYPAL-TRANSMISSION-SIG"),
			"PAYPAL-TRANSMISSION-TIME": r.Header.Get("PAYPAL-TRANSMISSION-TIME"),
		}
		b, _ := json.Marshal(headers)
		return string(b)
	default: // lemonsqueezy
		return r.Header.Get("X-Signature")
	}
}

func readBody(r *http.Request, limit int64) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(io.LimitReader(r.Body, limit))
}
