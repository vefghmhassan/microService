package providers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Billing reads, one per provider. Every call here is a GET: nothing is paid,
// ordered or changed, the figures are only shown on the provider cards.

// DigitalOcean: GET /customers/my/balance. A custom-scope token needs
// billing:read. account_balance is negative while there is credit.
func (d *DigitalOcean) Billing(ctx context.Context) (*Billing, error) {
	var out struct {
		MonthToDateUsage xzNum `json:"month_to_date_usage"`
		AccountBalance   xzNum `json:"account_balance"`
	}
	if err := d.c.do(ctx, http.MethodGet, "/customers/my/balance", nil, &out); err != nil {
		return nil, err
	}
	return &Billing{
		Currency: "USD",
		Spent:    float64(out.MonthToDateUsage),
		Credit:   creditFromBalance(float64(out.AccountBalance)),
	}, nil
}

// Vultr: GET /account. pending_charges is this month's not yet invoiced use,
// a negative balance is prepaid credit.
func (v *Vultr) Billing(ctx context.Context) (*Billing, error) {
	var out struct {
		Account struct {
			Balance        xzNum `json:"balance"`
			PendingCharges xzNum `json:"pending_charges"`
		} `json:"account"`
	}
	if err := v.c.do(ctx, http.MethodGet, "/account", nil, &out); err != nil {
		return nil, err
	}
	return &Billing{
		Currency: "USD",
		Spent:    float64(out.Account.PendingCharges),
		Credit:   creditFromBalance(float64(out.Account.Balance)),
	}, nil
}

// Linode: GET /account (token scope account:read_only). balance_uninvoiced is
// this month's use; credit is a negative balance plus what promotions still hold.
func (l *Linode) Billing(ctx context.Context) (*Billing, error) {
	var out struct {
		Balance           xzNum `json:"balance"`
		BalanceUninvoiced xzNum `json:"balance_uninvoiced"`
		ActivePromotions  []struct {
			CreditRemaining xzNum `json:"credit_remaining"`
		} `json:"active_promotions"`
	}
	if err := l.c.do(ctx, http.MethodGet, "/account", nil, &out); err != nil {
		return nil, err
	}
	b := &Billing{Currency: "USD", Spent: float64(out.BalanceUninvoiced)}
	credit := 0.0
	if out.Balance < 0 {
		credit = -float64(out.Balance)
	}
	for _, p := range out.ActivePromotions {
		credit += float64(p.CreditRemaining)
	}
	if credit > 0 {
		b.Credit = &credit
	}
	return b, nil
}

// Hetzner Cloud has no billing API, so this month's cost is estimated from the
// servers that exist now: hours since they were created (or since the month
// began) times their hourly price, capped at the monthly price. Servers
// deleted earlier in the month, IPs, volumes and traffic are not counted.
func (h *Hetzner) Billing(ctx context.Context) (*Billing, error) {
	type price struct {
		Location     string `json:"location"`
		PriceHourly  struct{ Gross xzNum } `json:"price_hourly"`
		PriceMonthly struct{ Gross xzNum } `json:"price_monthly"`
	}
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	total := 0.0
	for page := 1; page <= 20; page++ {
		var out struct {
			Servers []struct {
				Created    time.Time `json:"created"`
				ServerType struct {
					Prices []price `json:"prices"`
				} `json:"server_type"`
				Datacenter struct {
					Location struct {
						Name string `json:"name"`
					} `json:"location"`
				} `json:"datacenter"`
			} `json:"servers"`
			Meta struct {
				Pagination struct {
					NextPage *int `json:"next_page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		if err := h.c.do(ctx, http.MethodGet, "/servers?per_page=50&page="+strconv.Itoa(page), nil, &out); err != nil {
			return nil, err
		}
		for _, s := range out.Servers {
			var p *price
			for i := range s.ServerType.Prices {
				if s.ServerType.Prices[i].Location == s.Datacenter.Location.Name {
					p = &s.ServerType.Prices[i]
					break
				}
			}
			if p == nil {
				continue
			}
			from := s.Created
			if from.Before(monthStart) {
				from = monthStart
			}
			hours := math.Ceil(now.Sub(from).Hours())
			if hours < 1 {
				hours = 1
			}
			total += math.Min(hours*float64(p.PriceHourly.Gross), float64(p.PriceMonthly.Gross))
		}
		if out.Meta.Pagination.NextPage == nil {
			break
		}
	}
	return &Billing{
		Currency:  "EUR",
		Spent:     math.Round(total*100) / 100,
		Estimated: true,
		Note:      "Hetzner billing API does not exist; estimated from current servers only",
	}, nil
}

// OVH: GET /cloud/project/{id}/usage/current sums every totalPrice in it;
// the currency comes from GET /me. The consumer key needs both paths.
func (o *OVH) Billing(ctx context.Context) (*Billing, error) {
	var usage any
	if err := o.c.GetWithContext(ctx, o.path("/usage/current"), &usage); err != nil {
		return nil, fmt.Errorf("usage: %w", ovhErr(err))
	}
	b := &Billing{Currency: "EUR", Spent: math.Round(sumTotalPrice(usage)*100) / 100}
	var me struct {
		Currency struct {
			Code string `json:"code"`
		} `json:"currency"`
	}
	if err := o.c.GetWithContext(ctx, "/me", &me); err == nil && me.Currency.Code != "" {
		b.Currency = me.Currency.Code
	}
	return b, nil
}

// X-ZoneServers: GET /account/usage (spend this month, credit) and GET
// /account/balance (credit and currency), both scope read. Servers here are
// hourly, so hourly.charged_this_month is the month's cost; invoices paid this
// month are used only when no hourly figure is given, to avoid counting twice.
func (x *XZone) Billing(ctx context.Context) (*Billing, error) {
	var usage struct {
		Hourly struct {
			ChargedThisMonth xzNum `json:"charged_this_month"`
		} `json:"hourly"`
		Invoices struct {
			PaidThisMonth xzNum `json:"paid_this_month"`
		} `json:"invoices"`
		CreditBalance *xzNum `json:"credit_balance"`
	}
	if err := x.call(ctx, http.MethodGet, "/account/usage", nil, nil, &usage); err != nil {
		return nil, err
	}
	b := &Billing{Currency: "EUR", Spent: float64(usage.Hourly.ChargedThisMonth)}
	if b.Spent == 0 {
		b.Spent = float64(usage.Invoices.PaidThisMonth)
	}
	var bal struct {
		Balance  *xzNum `json:"balance"`
		Currency string `json:"currency"`
	}
	credit := usage.CreditBalance
	if err := x.call(ctx, http.MethodGet, "/account/balance", nil, nil, &bal); err == nil {
		if bal.Currency != "" {
			b.Currency = bal.Currency
		}
		if bal.Balance != nil {
			credit = bal.Balance
		}
	}
	if credit != nil {
		c := float64(*credit)
		b.Credit = &c
	}
	return b, nil
}

// sumTotalPrice adds every numeric "totalPrice" found anywhere in v.
func sumTotalPrice(v any) float64 {
	sum := 0.0
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if f, ok := x.(float64); ok && k == "totalPrice" {
				sum += f
				continue
			}
			sum += sumTotalPrice(x)
		}
	case []any:
		for _, x := range t {
			sum += sumTotalPrice(x)
		}
	}
	return sum
}

// Cherry Servers: GET /teams gives the team's account and promo credit. There
// is no month-to-date spend, so it is estimated like Hetzner's: hours each
// current server has run this month times its hourly price (before VAT).
func (ch *Cherry) Billing(ctx context.Context) (*Billing, error) {
	team, err := ch.teamID(ctx)
	if err != nil {
		return nil, err
	}
	teams, err := ch.teams(ctx)
	if err != nil {
		return nil, err
	}
	b := &Billing{Currency: "EUR", Estimated: true,
		Note: "Cherry Servers reports no monthly spend; estimated from current servers, before VAT"}
	for _, t := range teams {
		if t.ID != team {
			continue
		}
		b.Currency = firstNonEmpty(t.Credit.Account.Currency, t.Billing.Currency, b.Currency)
		if c := t.Credit.Account.Remaining + t.Credit.Promo.Remaining; c > 0 {
			b.Credit = &c
		}
	}
	var servers []cherryServer
	if err := ch.c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/servers", ch.project), nil, &servers); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	total := 0.0
	for _, s := range servers {
		if !strings.EqualFold(s.Pricing.Unit, "hourly") {
			continue
		}
		from := monthStart
		if t, err := time.Parse(time.RFC3339, s.Created); err == nil && t.After(monthStart) {
			from = t
		}
		hours := math.Ceil(now.Sub(from).Hours())
		if hours < 1 {
			hours = 1
		}
		total += hours * s.Pricing.Price
	}
	b.Spent = math.Round(total*100) / 100
	return b, nil
}
