package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func billingServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("billing must only read, got %s %s", r.Method, r.URL.Path)
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBillingReaders(t *testing.T) {
	ctx := context.Background()

	do := billingServer(t, map[string]string{"/customers/my/balance": `{"month_to_date_balance":"-80.00","account_balance":"-100.50","month_to_date_usage":"20.50"}`})
	b, err := NewDigitalOcean(do.URL, "t").Billing(ctx)
	if err != nil || b.Spent != 20.5 || b.Credit == nil || *b.Credit != 100.5 {
		t.Fatalf("digitalocean: %+v %v", b, err)
	}

	vu := billingServer(t, map[string]string{"/account": `{"account":{"balance":-30,"pending_charges":4.25}}`})
	b, err = NewVultr(vu.URL, "t").Billing(ctx)
	if err != nil || b.Spent != 4.25 || b.Credit == nil || *b.Credit != 30 {
		t.Fatalf("vultr: %+v %v", b, err)
	}

	li := billingServer(t, map[string]string{"/account": `{"balance":0,"balance_uninvoiced":7.5,"active_promotions":[{"credit_remaining":"50.00"}]}`})
	b, err = NewLinode(li.URL, "t").Billing(ctx)
	if err != nil || b.Spent != 7.5 || b.Credit == nil || *b.Credit != 50 {
		t.Fatalf("linode: %+v %v", b, err)
	}

	// A postpaid account with a positive balance has no credit.
	vu2 := billingServer(t, map[string]string{"/account": `{"account":{"balance":12,"pending_charges":12}}`})
	if b, _ = NewVultr(vu2.URL, "t").Billing(ctx); b.Credit != nil {
		t.Fatalf("vultr postpaid credit: %v", *b.Credit)
	}

	xz := billingServer(t, map[string]string{
		"/account/usage":   `{"success":true,"data":{"hourly":{"charged_this_month":"3.20"},"invoices":{"paid_this_month":9},"credit_balance":15}}`,
		"/account/balance": `{"success":true,"data":{"balance":"14.80","currency":"EUR"}}`,
	})
	b, err = NewXZone(xz.URL, "t").Billing(ctx)
	if err != nil || b.Spent != 3.2 || b.Credit == nil || *b.Credit != 14.8 || b.Currency != "EUR" {
		t.Fatalf("xzone: %+v %v", b, err)
	}

	created := time.Now().UTC().Add(-10 * time.Hour).Format(time.RFC3339)
	hz := billingServer(t, map[string]string{"/servers": `{"servers":[{"created":"` + created + `","datacenter":{"location":{"name":"fsn1"}},
		"server_type":{"prices":[{"location":"nbg1","price_hourly":{"gross":"1"},"price_monthly":{"gross":"100"}},
		{"location":"fsn1","price_hourly":{"gross":"0.01"},"price_monthly":{"gross":"5"}}]}}],"meta":{"pagination":{"next_page":null}}}`})
	b, err = NewHetzner(hz.URL, "t").Billing(ctx)
	if err != nil || !b.Estimated || b.Spent < 0.1 || b.Spent > 0.11 {
		t.Fatalf("hetzner: %+v %v", b, err)
	}
}

func TestSumTotalPrice(t *testing.T) {
	v := map[string]any{
		"hourlyUsage":  map[string]any{"instance": []any{map[string]any{"totalPrice": 1.5}, map[string]any{"totalPrice": 2.0}}},
		"monthlyUsage": map[string]any{"instance": []any{map[string]any{"totalPrice": 0.5}}},
	}
	if got := sumTotalPrice(v); got != 4 {
		t.Fatalf("sum = %v", got)
	}
}
