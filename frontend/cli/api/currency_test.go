package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The credit's currency is changed by a PUT of the new one, and the credit answered in it; an answer of an API from
// before accounts had currencies is read as an account's in dollars - its `*_usd` amounts its own - so that what reads
// the new fields reads it alike; an answer in pounds is read as it says.
func TestTheCurrencyIsChangedAndAnAnswerFromBeforeIsInDollars(t *testing.T) {
	var asked, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		asked, body = r.Method+" "+r.URL.Path, string(raw)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/credit/currency":
			_, _ = w.Write([]byte(`{"object":"credit","currency":"GBP","currency_fixed":false,"balance":4,"balance_usd":5,"buy":{"open":false,` +
				`"amounts":[],"amounts_usd":[]},"data":[]}`))
		case "/v1/credit":
			_, _ = w.Write([]byte(`{"object":"credit","balance_usd":4.25,"low_usd":1,"buy":{"open":true,"amounts_usd":[10,25]},` +
				`"free_credit":{"usd":5,"needs":"github"},"data":[{"id":"ce_1","kind":"welcome","amount_usd":5}]}`))
		case "/v1/usage/limits":
			_, _ = w.Write([]byte(`{"object":"usage_limits","account":{"month_cap_usd":200,"in_force_usd":200,"spent_usd":12},` +
				`"teams":[{"month_budget_usd":80,"spent_usd":7}],"people":[],"yours":[{"limit_usd":200,"spent_usd":12}]}`))
		}
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	ctx := context.Background()
	got, err := c.SetCurrency(ctx, CurrencyGBP)
	if err != nil || asked != "PUT /v1/credit/currency" || body != `{"currency":"GBP"}` || got.Currency != CurrencyGBP || got.Balance != 4 {
		t.Fatalf("%s %s: %+v %v", asked, body, got, err)
	}
	old, err := c.Credit(ctx, ListParams{})
	if err != nil || old.Currency != CurrencyUSD || old.Balance != 4.25 || old.Low != 1 || len(old.Buy.Amounts) != 2 || old.FreeCredit.Amount != 5 ||
		old.Data[0].Amount != 5 {
		t.Fatalf("from before: %+v %v", old, err)
	}
	l, err := c.Limits(ctx)
	if err != nil || l.Currency != CurrencyUSD || *l.Account.MonthCap != 200 || *l.Account.InForceAmount != 200 || *l.Account.Spent != 12 ||
		*l.Teams[0].MonthBudget != 80 || *l.Yours[0].Limit != 200 {
		t.Fatalf("limits from before: %+v %v", l, err)
	}
}
