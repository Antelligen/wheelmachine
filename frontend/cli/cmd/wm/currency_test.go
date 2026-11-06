package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"wheelmachine/frontend/cli/api"
)

// An account counted in pounds or euros is said in them everywhere wm says money: the credit, its entries and the free
// credit; a purchase, bought by its amount in that currency; the limits and a limit set; the usage; the GPUs' prices; and
// a team's budget - never a dollar sign for one of them. wm credit currency says the currency and changes it until your first purchase, or until we change it for you.
func TestAnAccountInPoundsIsSaidInPounds(t *testing.T) {
	h := newHarness(t)
	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	trainer, machine := "trainer", "m_00000000000000000000000007"
	h.api.Credit = api.Credit{Currency: api.CurrencyGBP, Balance: 4.1, BalanceUSD: 5.125, Low: 1, LowUSD: 1.25,
		FreeCredit: &api.FreeCredit{Amount: 5, USD: 6.25, Needs: "github"},
		Data: []api.CreditEntry{
			{ID: "ce_2", Object: "credit_entry", Kind: api.EntryMachineTime, Amount: -0.9, AmountUSD: -1.125, Reason: "Running time", Machine: &machine,
				MachineName: &trainer, At: at.Add(time.Minute), UpdatedAt: at.Add(time.Hour)},
			{ID: "ce_1", Object: "credit_entry", Kind: api.EntryWelcome, Amount: 5, AmountUSD: 6.25, Reason: "Free credit to start with", At: at, UpdatedAt: at},
		}}
	h.api.Credit.Buy = api.Buying{Open: true, Amounts: []int64{10, 25, 50, 100}, AmountsUSD: []int64{10, 25, 50, 100}}
	h.api.Credit.Buy.Paddle = &struct {
		Environment string `json:"environment"`
		ClientToken string `json:"client_token"`
	}{"sandbox", "test_madeup0123"}
	r := h.wm("", "credit").must(t, 0)
	for _, want := range []string{"balance: £4.10\n", "currency: pounds sterling (GBP); wm credit currency changes it until your first purchase, or until we change it for you\n",
		"free credit: £5.00 once you link", "trainer -£0.9000", "+£5.00"} {
		if !strings.Contains(spaced(r.stdout), want) {
			t.Fatalf("credit lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "$") {
		t.Fatalf("a dollar sign in an account counted in pounds:\n%s", r.stdout)
	}
	if r := h.wm("", "credit", "-q").must(t, 0); r.stdout != "4.100000\n" {
		t.Fatalf("quiet %q", r.stdout)
	}
	// wm me says the credit in pounds too
	if r := h.wm("", "me").must(t, 0); !strings.Contains(r.stdout, "credit:") || !strings.Contains(r.stdout, "£4.10") || !strings.Contains(r.stdout, "£10.50 left this month") {
		t.Fatalf("me:\n%s", r.stdout)
	}
	// a purchase, by its amount in pounds
	r = h.wm("", "credit", "buy", "£25").must(t, 0)
	if !strings.Contains(r.stdout, "amount:  £25.00\n") {
		t.Fatalf("buy:\n%s", r.stdout)
	}
	if req, _ := h.api.Last(http.MethodPost, "/v1/credit/purchases"); string(req.Body) != `{"amount":25}` {
		t.Fatalf("asked %s", req.Body)
	}
	// the currency, and a change of it until your first purchase, or until we change it for you
	if r := h.wm("", "credit", "currency").must(t, 0); !strings.Contains(spaced(r.stdout), "currency: pounds sterling (GBP)\n") ||
		!strings.Contains(r.stdout, "wm credit currency <USD|GBP|EUR>, until your first purchase, or until we change it for you") {
		t.Fatalf("currency:\n%s", r.stdout)
	}
	if r := h.wm("", "credit", "currency", "eur").must(t, 0); !strings.Contains(spaced(r.stdout), "currency: euros (EUR)\n") || !strings.Contains(r.stdout, "€4.10") {
		t.Fatalf("changed:\n%s", r.stdout)
	}
	if req, _ := h.api.Last(http.MethodPut, "/v1/credit/currency"); string(req.Body) != `{"currency":"EUR"}` {
		t.Fatalf("asked %s", req.Body)
	}
	if r := h.wm("", "credit", "currency", "-q").must(t, 0); r.stdout != "EUR\n" {
		t.Fatalf("quiet %q", r.stdout)
	}
	var said api.Credit
	if r := h.wm("", "credit", "currency", "--json").must(t, 0); json.Unmarshal([]byte(r.stdout), &said) != nil || said.Currency != api.CurrencyEUR {
		t.Fatalf("json %s", r.stdout)
	}
	for _, words := range [][]string{{"credit", "currency", "INR"}, {"credit", "currency", "GBP", "EUR"}} {
		if r := h.wm("", words...).must(t, 2); r.stderr == "" {
			t.Fatalf("%v: nothing said", words)
		}
	}
	h.api.Credit.CurrencyFixed = true
	if r := h.wm("", "credit", "currency", "GBP").must(t, 1); !strings.Contains(r.stderr, "currency is fixed") {
		t.Fatalf("fixed: %q", r.stderr)
	}
	if r := h.wm("", "credit").must(t, 0); !strings.Contains(spaced(r.stdout), "currency: euros (EUR)\n") {
		t.Fatalf("fixed:\n%s", r.stdout)
	}
	if r := h.wm("", "credit", "currency").must(t, 0); !strings.Contains(spaced(r.stdout), "change: fixed\n") {
		t.Fatalf("fixed, its currency read:\n%s", r.stdout)
	}
}

// The limits, the usage, the GPUs and a team's budget in the account's currency; a limit set by its amount in it.
func TestLimitsUsageAndPricesInTheAccountsCurrency(t *testing.T) {
	h := newHarness(t)
	cap, capUSD, spent, spentUSD, pct := 160.0, 200.0, 110.08, 137.6, 68
	h.api.Limits = api.UsageLimits{Currency: api.CurrencyGBP, Account: api.AccountLimit{Name: "Lab", Kind: "org", Currency: api.CurrencyGBP,
		MonthCap: &cap, MonthCapUSD: &capUSD, InForceAmount: &cap, InForceUSD: &capUSD, InForce: "account", Spent: &spent, SpentUSD: &spentUSD,
		Percent: &pct, Limited: true}}
	r := h.wm("", "limits").must(t, 0)
	if !strings.Contains(r.stdout, "£110.08 of £160.00") || strings.Contains(r.stdout, "$") {
		t.Fatalf("limits:\n%s", r.stdout)
	}
	if r := h.wm("", "limits", "-q").must(t, 0); r.stdout != "160.00\n" {
		t.Fatalf("quiet %q", r.stdout)
	}
	if r := h.wm("", "limits", "set", "month", "£250").must(t, 0); !strings.Contains(r.stdout, "£250.00 a month now") {
		t.Fatalf("set:\n%s", r.stdout)
	}
	if req, _ := h.api.Last(http.MethodPut, "/v1/usage/limits/account"); string(req.Body) != `{"month_cap":250}` {
		t.Fatalf("body %s", req.Body)
	}
	folders := 0.0329
	h.api.Usage = api.Usage{Currency: api.CurrencyEUR, Total: 1.134, TotalUSD: 1.26, Folders: &folders,
		Machines: []api.UsageLine{{Machine: "m_00000000000000000000000007", Project: "prj_1", RunningSeconds: 3725, Amount: 1.134, USD: 1.26}}}
	if r := h.wm("", "usage").must(t, 0); !strings.Contains(r.stdout, "€1.13") || !strings.Contains(r.stdout, "€0.0329") || strings.Contains(r.stdout, "$") {
		t.Fatalf("usage:\n%s", r.stdout)
	}
	h.api.GPUs = api.GpuList{Object: "list", Currency: api.CurrencyEUR, ForSale: true, Data: []api.GpuOffer{{ID: "h100-80", Model: "H100 80GB",
		Count: 1, MemoryGB: 80, Currency: api.CurrencyEUR, PricePerHour: 2.241, PricePerHourUSD: 2.49, Available: true}}}
	if r := h.wm("", "gpus").must(t, 0); !strings.Contains(r.stdout, "€2.24/h") {
		t.Fatalf("gpus:\n%s", r.stdout)
	}
}

// A provider's price in an account counted in pounds is its dollars, with about what that is in pounds at Wheelmachine's
// rate beside them, and said to be the provider's; in one counted in dollars, the dollars alone.
func TestAProvidersPriceIsItsDollarsWithAboutThatInPounds(t *testing.T) {
	h := newHarness(t)
	h.api.Models.Currency, h.api.Models.Rate = api.CurrencyGBP, 0.8
	r := h.wm("", "models").must(t, 0)
	if !strings.Contains(r.stdout, "$2 / $10 (about £1.60 / £8)") || !strings.Contains(r.stderr, "charges it in US dollars; about: what that is in pounds sterling (GBP)") {
		t.Fatalf("models:\n%s\n%s", r.stdout, r.stderr)
	}
	h.api.Models.Currency, h.api.Models.Rate = "", 0
	if r := h.wm("", "models").must(t, 0); strings.Contains(r.stdout, "about") || strings.Contains(r.stderr, "about:") {
		t.Fatalf("dollars:\n%s\n%s", r.stdout, r.stderr)
	}
}

// Words with each run of spaces said as one: a table's or a detail's columns, read as a sentence.
func spaced(s string) string {
	out := strings.Builder{}
	for _, line := range strings.Split(s, "\n") {
		out.WriteString(strings.Join(strings.Fields(line), " ") + "\n")
	}
	return out.String()
}
