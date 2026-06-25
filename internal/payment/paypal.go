package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// PayPal implements Provider using PayPal's Subscriptions API v2.
//
// PayPal has the widest global reach — available in 200+ countries/regions
// including India — making it the best fallback when Stripe or LS aren't
// available locally.
//
// Setup:
//   PAYMENT_PROVIDER=paypal
//   PAYPAL_CLIENT_ID=...      (from developer.paypal.com → My Apps)
//   PAYPAL_CLIENT_SECRET=...
//   PAYPAL_WEBHOOK_ID=...     (from the webhook you create in the dashboard)
//   PAYPAL_ENV=sandbox        (or "live")
//   PAYPAL_PLAN_PRO=...       (PayPal Plan ID, e.g. P-8ML...)
//   PAYPAL_PLAN_TEAM=...
//   PAYPAL_PLAN_BUSINESS=...
//
// Note: PayPal subscriptions use "Plan IDs" (not price IDs). Create these
// in the PayPal dashboard under Catalog → Subscription Plans.
type PayPal struct {
	clientID     string
	clientSecret string
	webhookID    string
	baseURL      string
	httpClient   *http.Client
	planToDotSync map[string]string // PayPal plan ID → dotsync plan name
}

func NewPayPal() *PayPal {
	baseURL := "https://api-m.paypal.com"
	if os.Getenv("PAYPAL_ENV") == "sandbox" {
		baseURL = "https://api-m.sandbox.paypal.com"
	}

	planToDotSync := map[string]string{}
	for env, plan := range map[string]string{
		"PAYPAL_PLAN_PRO":      "pro",
		"PAYPAL_PLAN_TEAM":     "team",
		"PAYPAL_PLAN_BUSINESS": "business",
	} {
		if id := os.Getenv(env); id != "" {
			planToDotSync[id] = plan
		}
	}

	return &PayPal{
		clientID:      os.Getenv("PAYPAL_CLIENT_ID"),
		clientSecret:  os.Getenv("PAYPAL_CLIENT_SECRET"),
		webhookID:     os.Getenv("PAYPAL_WEBHOOK_ID"),
		baseURL:       baseURL,
		httpClient:    &http.Client{Timeout: 20 * time.Second},
		planToDotSync: planToDotSync,
	}
}

func (pp *PayPal) Name() string { return "paypal" }

// GetOrCreateCustomer — PayPal doesn't require pre-creating customers.
// The subscriber is identified by their PayPal account at checkout.
// We store the subscription ID (which contains payer info) from webhooks.
func (pp *PayPal) GetOrCreateCustomer(_ context.Context, req CustomerRequest) (string, error) {
	return req.DotSyncUserID, nil // use our own user ID as the customer key
}

// CreateCheckoutSession creates a PayPal subscription and returns the
// approval URL — the user clicks this to log into PayPal and approve.
// Docs: https://developer.paypal.com/docs/api/subscriptions/v1/#subscriptions_create
func (pp *PayPal) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (string, error) {
	token, err := pp.getAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("paypal auth: %w", err)
	}

	body := map[string]any{
		"plan_id": req.PriceID, // PriceID holds the PayPal Plan ID for this provider
		"subscriber": map[string]any{
			"name": map[string]string{
				"given_name": req.Username,
			},
		},
		"application_context": map[string]any{
			"brand_name":          "DotSync",
			"locale":              "en-US",
			"shipping_preference": "NO_SHIPPING",
			"user_action":         "SUBSCRIBE_NOW",
			"return_url":          req.SuccessURL,
			"cancel_url":          req.CancelURL,
		},
		"custom_id": req.UserID, // stored as custom_id, returned in webhooks
	}

	b, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", pp.baseURL+"/v1/billing/subscriptions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("PayPal-Request-Id", req.UserID+"-"+req.PriceID) // idempotency key

	resp, err := pp.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("paypal create subscription: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ID    string `json:"id"`
		Links []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("paypal decode: %w", err)
	}

	for _, link := range result.Links {
		if link.Rel == "approve" {
			return link.Href, nil
		}
	}
	return "", fmt.Errorf("paypal: no approval URL in response")
}

// CreatePortalSession — PayPal doesn't have a managed portal.
// We return a deep link to the PayPal subscription management page.
func (pp *PayPal) CreatePortalSession(_ context.Context, customerID string, _ string) (string, error) {
	// customerID here is the PayPal subscription ID (captured from webhook)
	if customerID == "" {
		return "", fmt.Errorf("no active PayPal subscription found")
	}
	// PayPal's subscription management deep link
	return "https://www.paypal.com/myaccount/autopay/", nil
}

// VerifyWebhook verifies a PayPal webhook using their verification API.
// PayPal requires an API call to verify (unlike HMAC-based providers).
// Docs: https://developer.paypal.com/api/webhooks/v1/#verify-webhook-signature_post
func (pp *PayPal) VerifyWebhook(payload []byte, signature string) (*WebhookEvent, error) {
	// PayPal passes multiple headers for verification — signature here is
	// a JSON-encoded map of the required headers (set by the handler).
	var headers map[string]string
	if err := json.Unmarshal([]byte(signature), &headers); err != nil {
		return nil, fmt.Errorf("paypal: invalid signature header format")
	}

	ctx := context.Background()
	token, err := pp.getAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("paypal auth for webhook verify: %w", err)
	}

	verifyBody := map[string]any{
		"auth_algo":         headers["PAYPAL-AUTH-ALGO"],
		"cert_url":          headers["PAYPAL-CERT-URL"],
		"transmission_id":   headers["PAYPAL-TRANSMISSION-ID"],
		"transmission_sig":  headers["PAYPAL-TRANSMISSION-SIG"],
		"transmission_time": headers["PAYPAL-TRANSMISSION-TIME"],
		"webhook_id":        pp.webhookID,
		"webhook_event":     json.RawMessage(payload),
	}
	b, _ := json.Marshal(verifyBody)

	req, _ := http.NewRequestWithContext(ctx, "POST",
		pp.baseURL+"/v1/notifications/verify-webhook-signature", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := pp.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paypal webhook verify request: %w", err)
	}
	defer resp.Body.Close()

	var verifyResult struct {
		VerificationStatus string `json:"verification_status"`
	}
	json.NewDecoder(resp.Body).Decode(&verifyResult)
	if verifyResult.VerificationStatus != "SUCCESS" {
		return nil, fmt.Errorf("paypal: webhook verification failed: %s", verifyResult.VerificationStatus)
	}

	// Parse the actual event
	var raw struct {
		EventType string `json:"event_type"`
		Resource  struct {
			ID       string `json:"id"`   // subscription ID
			PlanID   string `json:"plan_id"`
			CustomID string `json:"custom_id"` // our user_id
			Status   string `json:"status"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("paypal: unmarshal event: %w", err)
	}

	event := &WebhookEvent{
		SubscriptionID: raw.Resource.ID,
		CustomerID:     raw.Resource.CustomID, // we stored user_id here
		PlanID:         raw.Resource.PlanID,
		Status:         strings.ToLower(raw.Resource.Status),
		Raw:            payload,
	}

	switch raw.EventType {
	case "BILLING.SUBSCRIPTION.CREATED":
		event.Type = EventSubscriptionCreated
	case "BILLING.SUBSCRIPTION.UPDATED", "BILLING.SUBSCRIPTION.ACTIVATED":
		event.Type = EventSubscriptionUpdated
	case "BILLING.SUBSCRIPTION.CANCELLED", "BILLING.SUBSCRIPTION.EXPIRED",
		"BILLING.SUBSCRIPTION.SUSPENDED":
		event.Type = EventSubscriptionDeleted
	case "PAYMENT.SALE.DENIED", "BILLING.SUBSCRIPTION.PAYMENT.FAILED":
		event.Type = EventPaymentFailed
	case "PAYMENT.SALE.COMPLETED":
		event.Type = EventPaymentSucceeded
	default:
		event.Type = EventUnknown
	}

	return event, nil
}

// getAccessToken fetches a short-lived OAuth2 token from PayPal.
func (pp *PayPal) getAccessToken(ctx context.Context) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, "POST",
		pp.baseURL+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(pp.clientID, pp.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := pp.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.AccessToken == "" {
		return "", fmt.Errorf("paypal token response: %s", string(body))
	}
	return result.AccessToken, nil
}

// PlanFromPayPalPlan maps a PayPal plan ID to a dotsync plan name.
func (pp *PayPal) PlanFromPayPalPlan(planID string) string {
	if p, ok := pp.planToDotSync[planID]; ok {
		return p
	}
	return "free"
}
