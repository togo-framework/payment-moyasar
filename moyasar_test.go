package moyasar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/togo-framework/payment"
)

func newTestProvider(h http.HandlerFunc) (*provider, *httptest.Server) {
	srv := httptest.NewServer(h)
	return &provider{key: "sk_test", base: srv.URL, hc: srv.Client()}, srv
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]string{"paid": "succeeded", "captured": "succeeded", "failed": "failed", "initiated": "pending", "": "pending"}
	for in, want := range cases {
		if got := status(in); got != want {
			t.Errorf("status(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCreateCharge(t *testing.T) {
	p, srv := newTestProvider(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payments" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if u, _, _ := r.BasicAuth(); u != "sk_test" {
			t.Errorf("basic auth user = %q", u)
		}
		_ = r.ParseForm()
		if r.Form.Get("amount") != "1500" || r.Form.Get("currency") != "SAR" || r.Form.Get("source[token]") != "tok_1" {
			t.Errorf("bad form: %v", r.Form)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "pay_1", "status": "paid", "amount": 1500})
	})
	defer srv.Close()

	ch, err := p.CreateCharge(context.Background(), payment.ChargeRequest{
		Amount: payment.Money{Amount: 1500, Currency: "SAR"}, Token: "tok_1", Description: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.ID != "pay_1" || ch.Status != "succeeded" || ch.Provider != "moyasar" {
		t.Errorf("got %+v", ch)
	}
}

func TestCreateChargeRequiresToken(t *testing.T) {
	p := &provider{key: "x", base: "http://x"}
	if _, err := p.CreateCharge(context.Background(), payment.ChargeRequest{Amount: payment.Money{Amount: 100}}); err == nil {
		t.Error("expected error without token")
	}
}

func TestRefund(t *testing.T) {
	p, srv := newTestProvider(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payments/pay_1/refund" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "pay_1", "status": "refunded"})
	})
	defer srv.Close()
	if err := p.Refund(context.Background(), payment.RefundRequest{ChargeID: "pay_1"}); err != nil {
		t.Fatal(err)
	}
}

func TestErrorResponse(t *testing.T) {
	p, srv := newTestProvider(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"message": "invalid token"})
	})
	defer srv.Close()
	_, err := p.CreateCharge(context.Background(), payment.ChargeRequest{Amount: payment.Money{Amount: 1}, Token: "bad"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleWebhook(t *testing.T) {
	p := &provider{}
	ev, err := p.HandleWebhook(context.Background(), nil, []byte(`{"type":"payment_paid","data":{"id":"pay_9","status":"paid"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "payment_paid" || ev.ID != "pay_9" || ev.Provider != "moyasar" {
		t.Errorf("got %+v", ev)
	}
}

func TestCustomerAndSubscriptionUnsupported(t *testing.T) {
	p := &provider{}
	if _, err := p.CreateCustomer(context.Background(), payment.Customer{}); err == nil {
		t.Error("expected unsupported customer error")
	}
	if _, err := p.CreateSubscription(context.Background(), payment.SubscriptionRequest{}); err == nil {
		t.Error("expected unsupported subscription error")
	}
}
