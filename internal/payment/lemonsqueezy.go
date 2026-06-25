package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const lsBaseURL = "https://api.lemonsqueezy.com/v1"

// LemonSqueezy implements Provider using the Lemon Squeezy REST API.
// No SDK needed — their API is simple enough to call directly.
//
// Setup:
//   PAYMENT_PROVIDER=lemonsqueezy
//   LEMONSQUEEZY_API_KEY=...        (from app.lemonsqueezy.com → Settings → API)
//   LEMONSQUEEZY_WEBHOOK_SECRET=... (from app.lemonsqueezy.com → Webhooks)
//   LEMONSQUEEZY_STORE_ID=...       (numeric store ID)
//   LS_VARIANT_PRO=...              (variant ID for the Pro plan product)
//   LS_VARIANT_TEAM=...
//   LS_VARIANT_BUSINESS=...
//
// Lemon Squeezy doesn't have a separate "customer" concept during checkout —
// the customer is created automatically on first purchase. We store the
// customer ID from the webhook's first subscription event.
type LemonSqueezy struct {
	apiKey        string
	webhookSecret string
	storeID       string
	httpClient    *http.Client
	variantToPlan map[string]string // variant ID → plan name
}

func NewLemonSqueezy() *LemonSqueezy {
	variantToPlan := map[string]string{}
	for env, plan := range map[string]string{
		"LS_VARIANT_PRO":      "pro",
		"LS_VARIANT_TEAM":     "team",
		"LS_VARIANT_BUSINESS": "business",
	} {
		if id := os.Getenv(env); id != "" {
			variantToPlan[id] = plan
		}
	}

	return &LemonSqueezy{
		apiKey:        os.Getenv("LEMONSQUEEZY_API_KEY"),
		webhookSecret: os.Getenv("LEMONSQUEEZY_WEBHOOK_SECRET"),
		storeID:       os.Getenv("LEMONSQUEEZY_STORE_ID"),
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		variantToPlan: variantToPlan,
	}
}

func (ls *LemonSqueezy) Name() string { return "lemonsqueezy" }

// GetOrCreateCustomer — LS creates customers automatically on checkout,
// so we just return the stored customer ID if we have one. The actual
// customer record is created by LS on the first subscription webhook.
func (ls *LemonSqueezy) GetOrCreateCustomer(_ context.Context, req CustomerRequest) (string, error) {
	// No pre-creation needed. Return empty string — LS handles it at checkout.
	// The customer ID is captured from the first webhook and stored then.
	return "", nil
}

// CreateCheckoutSession creates a Lemon Squeezy hosted checkout URL.
// Docs: https://docs.lemonsqueezy.com/api/checkouts
func (ls *LemonSqueezy) CreateCheckoutSession(_ context.Context, req CheckoutRequest) (string, error) {
	if req.PriceID == "" {
		return "", fmt.Errorf("lemonsqueezy: variant ID (PriceID) is required")
	}

	body := map[string]any{
		"data": map[string]any{
			"type": "checkouts",
			"attributes": map[string]any{
				"checkout_data": map[string]any{
					"custom": map[string]string{
						"user_id":  req.UserID,
						"username": req.Username,
					},
				},
				"product_options": map[string]any{
					"redirect_url": req.SuccessURL,
				},
			},
			"relationships": map[string]any{
				"store": map[string]any{
					"data": map[string]string{"type": "stores", "id": ls.storeID},
				},
				"variant": map[string]any{
					"data": map[string]string{"type": "variants", "id": req.PriceID},
				},
			},
		},
	}

	resp, err := ls.call("POST", "/checkouts", body)
	if err != nil {
		return "", fmt.Errorf("lemonsqueezy checkout: %w", err)
	}

	url, ok := resp["data"].(map[string]any)["attributes"].(map[string]any)["url"].(string)
	if !ok || url == "" {
		return "", fmt.Errorf("lemonsqueezy: no checkout URL in response")
	}
	return url, nil
}

// CreatePortalSession — Lemon Squeezy doesn't have a dedicated portal API.
// Instead, every subscription email contains a "Manage subscription" link.
// We fetch the subscription's customer portal URL directly.
// Docs: https://docs.lemonsqueezy.com/api/subscriptions
func (ls *LemonSqueezy) CreatePortalSession(_ context.Context, customerID string, _ string) (string, error) {
	if customerID == "" {
		return "", fmt.Errorf("no active subscription — subscribe first with: dotsync billing upgrade")
	}

	// Fetch subscriptions for this customer
	resp, err := ls.call("GET", fmt.Sprintf("/subscriptions?filter[customer_id]=%s&page[size]=1", customerID), nil)
	if err != nil {
		return "", fmt.Errorf("lemonsqueezy fetch subscription: %w", err)
	}

	data, _ := resp["data"].([]any)
	if len(data) == 0 {
		return "", fmt.Errorf("no active subscription found for this account")
	}

	attrs, _ := data[0].(map[string]any)["attributes"].(map[string]any)
	portalURL, _ := attrs["urls"].(map[string]any)["customer_portal"].(string)
	if portalURL == "" {
		return "", fmt.Errorf("lemonsqueezy: no customer portal URL found")
	}
	return portalURL, nil
}

// VerifyWebhook verifies the HMAC-SHA256 signature and returns a normalized event.
// Docs: https://docs.lemonsqueezy.com/api/webhooks#webhook-requests
func (ls *LemonSqueezy) VerifyWebhook(payload []byte, signature string) (*WebhookEvent, error) {
	// Verify HMAC-SHA256 signature
	mac := hmac.New(sha256.New, []byte(ls.webhookSecret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return nil, fmt.Errorf("lemonsqueezy: invalid webhook signature")
	}

	// Parse event
	var raw struct {
		Meta struct {
			EventName string `json:"event_name"`
		} `json:"meta"`
		Data struct {
			ID         string `json:"id"` // subscription ID
			Attributes struct {
				Status     string `json:"status"`
				CustomerID int64  `json:"customer_id"`
				VariantID  int64  `json:"variant_id"`
				Urls       struct {
					CustomerPortal string `json:"customer_portal"`
				} `json:"urls"`
			} `json:"attributes"`
		} `json:"data"`
	}

	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("lemonsqueezy: unmarshal webhook: %w", err)
	}

	event := &WebhookEvent{
		SubscriptionID: raw.Data.ID,
		CustomerID:     fmt.Sprintf("%d", raw.Data.Attributes.CustomerID),
		PlanID:         fmt.Sprintf("%d", raw.Data.Attributes.VariantID),
		Status:         raw.Data.Attributes.Status,
		Raw:            payload,
	}

	switch raw.Meta.EventName {
	case "subscription_created":
		event.Type = EventSubscriptionCreated
	case "subscription_updated":
		event.Type = EventSubscriptionUpdated
	case "subscription_cancelled", "subscription_expired":
		event.Type = EventSubscriptionDeleted
	case "subscription_payment_failed":
		event.Type = EventPaymentFailed
	case "subscription_payment_success":
		event.Type = EventPaymentSucceeded
	default:
		event.Type = EventUnknown
	}

	return event, nil
}

// PlanFromVariant maps a LS variant ID to a DotSync plan name.
func (ls *LemonSqueezy) PlanFromVariant(variantID string) string {
	if p, ok := ls.variantToPlan[variantID]; ok {
		return p
	}
	return "free"
}

// call makes an authenticated request to the LS API.
func (ls *LemonSqueezy) call(method, path string, body any) (map[string]any, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, lsBaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+ls.apiKey)
	req.Header.Set("Accept", "application/vnd.api+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/vnd.api+json")
	}

	resp, err := ls.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("lemonsqueezy API %d: %v", resp.StatusCode, result)
	}
	return result, nil
}
