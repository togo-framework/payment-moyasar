// Package moyasar is a Moyasar (moyasar.com, KSA) driver for togo payment.
// Set PAYMENT_DRIVER=moyasar + MOYASAR_SECRET_KEY. Optional MOYASAR_BASE_URL
// (defaults to the live API) for testing against a mock server.
//
// Auth is HTTP Basic with the secret key as the username (empty password).
package moyasar

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/togo-framework/payment"
	"github.com/togo-framework/togo"
)

const defaultAPI = "https://api.moyasar.com/v1"

func init() {
	payment.RegisterDriver("moyasar", func(k *togo.Kernel) (payment.PaymentProvider, error) {
		key := os.Getenv("MOYASAR_SECRET_KEY")
		if key == "" {
			return nil, errors.New("payment-moyasar: MOYASAR_SECRET_KEY not set")
		}
		base := os.Getenv("MOYASAR_BASE_URL")
		if base == "" {
			base = defaultAPI
		}
		return &provider{key: key, base: strings.TrimRight(base, "/"), webhookSecret: os.Getenv("MOYASAR_WEBHOOK_SECRET"), hc: &http.Client{Timeout: 20 * time.Second}}, nil
	})
}

type provider struct {
	key           string
	base          string
	webhookSecret string
	hc            *http.Client
}

// post sends a form-encoded request with Basic auth and decodes the JSON body.
func (p *provider) post(ctx context.Context, path string, form url.Values) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.key, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(b) > 0 {
		_ = json.Unmarshal(b, &m)
	}
	if resp.StatusCode >= 300 {
		msg := resp.Status
		if m != nil {
			if e, ok := m["message"].(string); ok && e != "" {
				msg = e
			}
		}
		return m, fmt.Errorf("moyasar: %s", msg)
	}
	return m, nil
}

// status maps Moyasar payment status → the togo charge status vocabulary.
func status(s string) string {
	switch s {
	case "paid", "captured", "authorized":
		return "succeeded"
	case "failed":
		return "failed"
	default:
		return "pending"
	}
}

func (p *provider) CreateCharge(ctx context.Context, r payment.ChargeRequest) (*payment.Charge, error) {
	if r.Token == "" {
		return nil, errors.New("moyasar: ChargeRequest.Token (a Moyasar source token) is required")
	}
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(r.Amount.Amount, 10))
	form.Set("currency", orDefault(r.Amount.Currency, "SAR"))
	form.Set("description", r.Description)
	form.Set("source[type]", "token")
	form.Set("source[token]", r.Token)
	for k, v := range r.Metadata {
		form.Set("metadata["+k+"]", v)
	}
	m, err := p.post(ctx, "/payments", form)
	if err != nil {
		return nil, err
	}
	return &payment.Charge{
		ID:       str(m["id"]),
		Status:   status(str(m["status"])),
		Amount:   r.Amount,
		Provider: "moyasar",
		Raw:      m,
	}, nil
}

func (p *provider) Refund(ctx context.Context, r payment.RefundRequest) error {
	if r.ChargeID == "" {
		return errors.New("moyasar: RefundRequest.ChargeID is required")
	}
	form := url.Values{}
	if r.Amount != nil {
		form.Set("amount", strconv.FormatInt(r.Amount.Amount, 10))
	}
	_, err := p.post(ctx, "/payments/"+url.PathEscape(r.ChargeID)+"/refund", form)
	return err
}

func (p *provider) CreateCheckoutSession(ctx context.Context, r payment.CheckoutRequest) (*payment.CheckoutSession, error) {
	// Moyasar hosted checkout = an Invoice with a payment URL.
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(total(r), 10))
	form.Set("currency", orDefault(r.Amount.Currency, "SAR"))
	form.Set("description", descFromItems(r))
	if r.SuccessURL != "" {
		form.Set("success_url", r.SuccessURL)
		form.Set("callback_url", r.SuccessURL)
	}
	if r.CancelURL != "" {
		form.Set("back_url", r.CancelURL)
	}
	for k, v := range r.Metadata {
		form.Set("metadata["+k+"]", v)
	}
	m, err := p.post(ctx, "/invoices", form)
	if err != nil {
		return nil, err
	}
	return &payment.CheckoutSession{ID: str(m["id"]), URL: str(m["url"])}, nil
}

// Moyasar has no first-class Customer or Subscription API.
func (p *provider) CreateCustomer(context.Context, payment.Customer) (string, error) {
	return "", errors.New("moyasar: customers are not supported by the Moyasar API")
}

func (p *provider) CreateSubscription(context.Context, payment.SubscriptionRequest) (*payment.Subscription, error) {
	return nil, errors.New("moyasar: native subscriptions are not supported — use the togo subscriptions plugin")
}

// HandleWebhook parses a Moyasar webhook ({type, data:{...}}).
func (p *provider) HandleWebhook(_ context.Context, _ map[string]string, body []byte) (*payment.WebhookEvent, error) {
	var env struct {
		Type        string         `json:"type"`
		SecretToken string         `json:"secret_token"`
		Data        map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("moyasar: bad webhook body: %w", err)
	}
	// Moyasar puts the webhook's secret_token in the payload. When a secret is
	// configured (MOYASAR_WEBHOOK_SECRET) verify it in constant time and reject
	// forgeries; with no secret set we stay parse-only for dev (back-compat).
	if p.webhookSecret != "" && subtle.ConstantTimeCompare([]byte(env.SecretToken), []byte(p.webhookSecret)) != 1 {
		return nil, errors.New("moyasar: webhook secret_token mismatch")
	}
	id := ""
	if env.Data != nil {
		id = str(env.Data["id"])
	}
	return &payment.WebhookEvent{Type: env.Type, ID: id, Provider: "moyasar", Raw: map[string]any{"type": env.Type, "data": env.Data}}, nil
}

// ── helpers ────────────────────────────────────────────────────────────────

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func total(r payment.CheckoutRequest) int64 {
	if len(r.Items) == 0 {
		return r.Amount.Amount
	}
	var t int64
	for _, it := range r.Items {
		q := it.Quantity
		if q == 0 {
			q = 1
		}
		t += it.Amount.Amount * q
	}
	return t
}

func descFromItems(r payment.CheckoutRequest) string {
	if len(r.Items) == 0 {
		return "Checkout"
	}
	names := make([]string, 0, len(r.Items))
	for _, it := range r.Items {
		names = append(names, it.Name)
	}
	return strings.Join(names, ", ")
}
