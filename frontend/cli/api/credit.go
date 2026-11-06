package api

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The account's credit: prepaid usage credit for Wheelmachine alone, counted in the account's currency (Currency: US
// dollars, pounds sterling or euros). A person's own account starts with 5 of its currency in free credit, and a new
// organisation with none; machines' running time is taken from it once a minute, on one entry a machine a day; at zero
// or below the account cannot spend. Credit is bought in 10, 25, 50 or 100 of the account's currency, paid in
// Paddle's checkout (BuyCredit). The currency can be changed until your first purchase, or until we change it for you (SetCurrency). In an
// organisation the credit is the organisation's, read by its owners, admins and
// billing and bought by its owners and billing; anyone else is refused (CodeForbidden), and so is a key limited to some
// projects.

// The kinds of the credit's entries.
const (
	EntryWelcome     = "welcome"      // the free credit a person's own account starts with: 5 of its currency
	EntryMachineTime = "machine_time" // a machine's running time of a day (UTC), which grows until the day ends
	EntryFolders     = "folders"      // what the account's folders kept a day, which grows as the day's most does
	EntryPurchase    = "purchase"     // credit bought, paid through Paddle (and a disputed payment that came back)
	EntryRefund      = "refund"       // credit refunded through Paddle, or disputed with a bank
	EntryStaffCredit = "staff_credit" // credit put in by Wheelmachine's staff, with why
	EntryStaffDebit  = "staff_debit"  // credit removed by Wheelmachine's staff, with why
)

// Credit is the account's credit and a page of its entries, the newest first.
type Credit struct {
	Object  string `json:"object"`
	Account string `json:"account"`
	// the currency it is counted in, and whether it is fixed, by the account's first purchase or by Wheelmachine's support
	// (until then SetCurrency changes it)
	Currency      string `json:"currency"`
	CurrencyFixed bool   `json:"currency_fixed"`
	// in the account's currency, to the millionth; a little below zero when it ran out mid-minute
	Balance float64 `json:"balance"`
	// Deprecated: the balance in US dollars at Wheelmachine's rate; read Balance.
	BalanceUSD float64 `json:"balance_usd"`
	Empty      bool    `json:"empty"` // zero or below: the account's machines are paused, and none starts
	Buy        Buying  `json:"buy"`
	Low        float64 `json:"low"` // below it the credit is running low, and its buyers are e-mailed
	// Deprecated: Low in US dollars at Wheelmachine's rate.
	LowUSD float64 `json:"low_usd"`
	// whether its use is taken now: "on"; "from" an account from before billing, not before From; "off" never
	Charging struct {
		State string  `json:"state"`
		From  *string `json:"from"`
	} `json:"charging"`
	// the free credit your own account is still to get, and what it waits for: a GitHub account linked on the Code page
	FreeCredit *FreeCredit   `json:"free_credit"`
	Data       []CreditEntry `json:"data"`
	Next       *string       `json:"next"`
}

// FreeCredit is the free credit your own account is still to get: once a GitHub account at least GitHubMinDays old is
// linked on the dashboard's Code page (TooNewUntil: the one linked is old enough then).
type FreeCredit struct {
	Amount float64 `json:"amount"` // 5 of the account's currency
	// Deprecated: Amount in US dollars at Wheelmachine's rate.
	USD           float64 `json:"usd"`
	Needs         string  `json:"needs"`
	GitHubMinDays int     `json:"github_min_days"`
	TooNewUntil   *string `json:"too_new_until"`
}

// Buying says whether credit can be bought here, in which amounts (of the account's currency, one a time), and what
// Paddle's checkout is opened with in a browser: Paddle's environment and its client-side token, made to be given to
// browsers.
type Buying struct {
	Open    bool    `json:"open"`
	Amounts []int64 `json:"amounts"`
	// Deprecated: the same numbers as Amounts, as they were named while every account was in dollars.
	AmountsUSD []int64 `json:"amounts_usd"`
	Paddle     *struct {
		Environment string `json:"environment"`
		ClientToken string `json:"client_token"`
	} `json:"paddle"`
}

// CreditPurchase is a checkout started to buy credit: nothing is charged until it is paid in Paddle's checkout -
// Checkout.URL opens it on the dashboard's Billing page, for a person signed in there - and the credit is there once
// Paddle says the payment is complete.
type CreditPurchase struct {
	Object   string  `json:"object"`
	ID       string  `json:"id"`
	Account  string  `json:"account"`
	Currency string  `json:"currency"`
	Amount   float64 `json:"amount"` // the credit it gives once paid, in the account's currency
	// Deprecated: Amount in US dollars at Wheelmachine's rate.
	AmountUSD float64 `json:"amount_usd"`
	Checkout  struct {
		Provider    string `json:"provider"`
		Environment string `json:"environment"`
		ClientToken string `json:"client_token"`
		Currency    string `json:"currency"`
		// Paddle's id for the purchase's transaction, or nil while it has none: a purchase can have one before it is paid
		TransactionID *string `json:"transaction_id"`
		// the purchase's price at Paddle, the four amounts' (for the checkout to move between), and what its payment names
		PriceID    string            `json:"price_id"`
		Prices     []CreditPrice     `json:"prices"`
		CustomData map[string]string `json:"custom_data"`
		URL        string            `json:"url"`
	} `json:"checkout"`
	CreatedAt time.Time `json:"created_at"`
}

// One of the amounts of credit - 10, 25, 50 or 100 of the account's currency - and its price at Paddle.
type CreditPrice struct {
	Amount float64 `json:"amount"`
	// Deprecated: the same number as Amount, as it was named while every account was in dollars.
	AmountUSD float64 `json:"amount_usd"`
	PriceID   string  `json:"price_id"`
}

// BuyCredit starts a checkout for `amount` of the account's currency in credit - 10, 25, 50 or 100 (CodeNotOpen where
// buying credit is not set up). Sent again after a failure, it carries the same Idempotency-Key, so one press is one
// purchase.
func (c *Client) BuyCredit(ctx context.Context, amount int) (*CreditPurchase, error) {
	var out CreditPurchase
	cl := call{method: http.MethodPost, path: "/v1/credit/purchases", in: map[string]int{"amount": amount}, create: true, timeout: callTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetCurrency changes the currency the account's credit is counted in - CurrencyUSD, CurrencyGBP or CurrencyEUR -
// until your first purchase, or until we change it for you (CodeConflict then). The credit keeps its value at Wheelmachine's rates but
// the free credit, which becomes 5 of the new currency. In your own account, you change it; in an organisation, its
// owners and billing. The credit as it is then.
func (c *Client) SetCurrency(ctx context.Context, currency string) (*Credit, error) {
	var out Credit
	cl := call{method: http.MethodPut, path: "/v1/credit/currency", in: map[string]string{"currency": currency}, timeout: callTimeout}
	if err := c.decode(ctx, cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreditEntry is credit put in (Amount above zero) or taken (below).
type CreditEntry struct {
	ID     string  `json:"id"`
	Object string  `json:"object"`
	Kind   string  `json:"kind"`
	Amount float64 `json:"amount"` // in the account's currency, to the millionth
	// Deprecated: Amount in US dollars at Wheelmachine's rate.
	AmountUSD   float64   `json:"amount_usd"`
	Reason      string    `json:"reason"`
	Machine     *string   `json:"machine"`      // the machine whose running time it is; nil for any other entry
	MachineName *string   `json:"machine_name"` // its name, a deleted machine's too
	At          time.Time `json:"at"`
	UpdatedAt   time.Time `json:"updated_at"` // a day's running time grows until the day ends; no other entry changes
}

// Credit is the account's credit with one page of its entries, the newest first.
func (c *Client) Credit(ctx context.Context, p ListParams) (*Credit, error) {
	q := url.Values{}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.After != "" {
		q.Set("after", p.After)
	}
	var out Credit
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/credit", query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AllCreditEntries walks every entry of the credit, the newest first, asking for each page only when the one before
// it is used up (`limit` entries a page; 0: the API's 50). Stop early by breaking out of the loop.
func (c *Client) AllCreditEntries(ctx context.Context, limit int) iter.Seq2[CreditEntry, error] {
	return all("", func(after string) (*Page[CreditEntry], error) {
		cr, err := c.Credit(ctx, ListParams{Limit: limit, After: after})
		if err != nil {
			return nil, err
		}
		return &Page[CreditEntry]{Object: "list", Data: cr.Data, Next: cr.Next}, nil
	})
}
