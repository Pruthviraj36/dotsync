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

	"github.com/Pruthviraj36/dotsync/internal/model"
)

// PayPal implements Provider using PayPal's Orders API v2 for a single
// one-time purchase — the $500 on-premise license. (dotsync itself has no
// recurring subscription anymore; hosted use is free.)
//
// PayPal has the widest global reach — available in 200+ countries/regions
// including India — making it the best fallback when Stripe or LS aren't
// available locally.
//
// Setup:
//
//	PAYMENT_PROVIDER=paypal
//	PAYPAL_CLIENT_ID=...      (from developer.paypal.com → My Apps)
//	PAYPAL_CLIENT_SECRET=...
//	PAYPAL_WEBHOOK_ID=...     (from the webhook you create in the dashboard)
//	PAYPAL_ENV=sandbox        (or "live")
//
// Docs: https://developer.paypal.com/docs/api/orders/v2/
type PayPal struct {
	clientID     string
	clientSecret string
	webhookID    string
	baseURL      string
	httpClient   *http.Client
}

func NewPayPal() *PayPal {
	baseURL := "https://api-m.paypal.com"
	if os.Getenv("PAYPAL_ENV") == "sandbox" {
		baseURL = "https://api-m.sandbox.paypal.com"
	}

	return &PayPal{
		clientID:     os.Getenv("PAYPAL_CLIENT_ID"),
		clientSecret: os.Getenv("PAYPAL_CLIENT_SECRET"),
		webhookID:    os.Getenv("PAYPAL_WEBHOOK_ID"),
		baseURL:      baseURL,
		httpClient:   &http.Client{Timeout: 20 * time.Second},
	}
}

func (pp *PayPal) Name() string { return "paypal" }

// GetOrCreateCustomer — PayPal doesn't require pre-creating customers.
// The subscriber is identified by their PayPal account at checkout.
// We store the subscription ID (which contains payer info) from webhooks.
func (pp *PayPal) GetOrCreateCustomer(_ context.Context, req CustomerRequest) (string, error) {
	return req.DotSyncUserID, nil // use our own user ID as the customer key
}

// CreateCheckoutSession creates a one-time PayPal order for the on-premise
// license and returns the approval URL — the user clicks this to log into
// PayPal and approve the $500 payment. This is a single fixed-price
// purchase, not a subscription, so it uses the Orders v2 API rather than
// Subscriptions v2.
// Docs: https://developer.paypal.com/docs/api/orders/v2/#orders_create
func (pp *PayPal) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (string, error) {
	token, err := pp.getAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("paypal auth: %w", err)
	}

	body := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{
			{
				"custom_id":   req.UserID, // stored as custom_id, returned in webhooks
				"description": "dotsync on-premise license (one-time)",
				"amount": map[string]any{
					"currency_code": "USD",
					"value":         fmt.Sprintf("%d.00", model.OnPremisePriceUSD),
				},
			},
		},
		"application_context": map[string]any{
			"brand_name":          "DotSync",
			"locale":              "en-US",
			"shipping_preference": "NO_SHIPPING",
			"user_action":         "PAY_NOW",
			"return_url":          req.SuccessURL,
			"cancel_url":          req.CancelURL,
		},
	}

	b, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", pp.baseURL+"/v2/checkout/orders", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("PayPal-Request-Id", req.UserID+"-onpremise") // idempotency key

	resp, err := pp.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("paypal create order: %w", err)
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

// CreatePortalSession — there's no subscription to manage since the
// on-premise license is a one-time purchase. Point people at support instead.
func (pp *PayPal) CreatePortalSession(_ context.Context, customerID string, _ string) (string, error) {
	return "", fmt.Errorf("the on-premise license is a one-time purchase — there's no subscription to manage. Need a receipt or help? Email support@dotsync.dev")
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

	// Parse the actual event. One-time orders carry the buyer's user ID in
	// resource.purchase_units[].custom_id rather than resource.custom_id
	// directly (that field is subscription-shaped) — check both.
	var raw struct {
		EventType string `json:"event_type"`
		Resource  struct {
			ID            string `json:"id"`
			PlanID        string `json:"plan_id"`
			CustomID      string `json:"custom_id"`
			Status        string `json:"status"`
			PurchaseUnits []struct {
				CustomID string `json:"custom_id"`
			} `json:"purchase_units"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("paypal: unmarshal event: %w", err)
	}

	customID := raw.Resource.CustomID
	if customID == "" && len(raw.Resource.PurchaseUnits) > 0 {
		customID = raw.Resource.PurchaseUnits[0].CustomID
	}

	event := &WebhookEvent{
		SubscriptionID: raw.Resource.ID,
		CustomerID:     customID, // we stored our user_id here
		PlanID:         raw.Resource.PlanID,
		Status:         strings.ToLower(raw.Resource.Status),
		Raw:            payload,
	}

	switch raw.EventType {
	case "CHECKOUT.ORDER.APPROVED", "PAYMENT.CAPTURE.COMPLETED":
		event.Type = EventOrderCompleted
	case "PAYMENT.CAPTURE.DENIED", "PAYMENT.CAPTURE.DECLINED":
		event.Type = EventPaymentFailed
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

// PlanFromPayPalPlan always returns "onpremise" — there's only one
// purchasable product now, so no plan-ID lookup table is needed.
func (pp *PayPal) PlanFromPayPalPlan(planID string) string {
	return "onpremise"
}
