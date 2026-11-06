package api

// The currencies an account is counted in: its credit, its history, its limits and every price it is shown. Pounds
// sterling for an account made in the United Kingdom, euros for one made in the euro area, US dollars for every other;
// India pays for its credit in rupees and is counted in dollars. Every amount is said in the account's currency, with
// the currency beside it; each `*USD` field beside one is the same amount in US dollars at Wheelmachine's rate - the
// very number in an account counted in dollars - kept for clients from before, and deprecated.
const (
	CurrencyUSD = "USD"
	CurrencyGBP = "GBP"
	CurrencyEUR = "EUR"
)

// An answer of an API from before accounts had currencies says no currency, and its amounts in dollars alone: they are
// the account's own then, in dollars - so that what reads the amounts in the account's currency reads them alike.

func (c *Credit) fromBefore() {
	if c.Currency != "" {
		return
	}
	c.Currency, c.Balance, c.Low = CurrencyUSD, c.BalanceUSD, c.LowUSD
	if c.Buy.Amounts == nil {
		c.Buy.Amounts = c.Buy.AmountsUSD
	}
	if c.FreeCredit != nil && c.FreeCredit.Amount == 0 {
		c.FreeCredit.Amount = c.FreeCredit.USD
	}
	for i := range c.Data {
		c.Data[i].Amount = c.Data[i].AmountUSD
	}
}

func (p *CreditPurchase) fromBefore() {
	if p.Currency != "" {
		return
	}
	p.Currency, p.Amount = CurrencyUSD, p.AmountUSD
	p.Checkout.Currency = CurrencyUSD
	for i := range p.Checkout.Prices {
		if p.Checkout.Prices[i].Amount == 0 {
			p.Checkout.Prices[i].Amount = p.Checkout.Prices[i].AmountUSD
		}
	}
}

func (m *Me) fromBefore() {
	if m.Standing.Currency == "" {
		m.Standing.Currency, m.Standing.Balance, m.Standing.Credit = CurrencyUSD, m.Standing.BalanceUSD, m.Standing.CreditUSD
	}
	if m.Limits != nil {
		m.Limits.fromBefore()
	}
}

func (l *PersonLimits) fromBefore() {
	if l.Currency == "" {
		l.Currency, l.MonthCap = CurrencyUSD, l.MonthCapUSD
	}
}

func (u *Usage) fromBefore() {
	if u.Currency != "" {
		return
	}
	u.Currency, u.Total, u.Folders = CurrencyUSD, u.TotalUSD, u.FoldersUSD
	for i := range u.Machines {
		u.Machines[i].Amount = u.Machines[i].USD
	}
}

func (l *UsageLimits) fromBefore() {
	if l.Currency != "" {
		return
	}
	l.Currency = CurrencyUSD
	a := &l.Account
	a.Currency, a.MonthCap, a.InForceAmount, a.Spent, a.Wheelmachine.MonthCap = CurrencyUSD, a.MonthCapUSD, a.InForceUSD, a.SpentUSD, a.Wheelmachine.MonthCapUSD
	for i := range l.Teams {
		l.Teams[i].MonthBudget, l.Teams[i].Spent = l.Teams[i].MonthBudgetUSD, l.Teams[i].SpentUSD
	}
	for i := range l.People {
		l.People[i].MonthCap, l.People[i].Spent = l.People[i].MonthCapUSD, l.People[i].SpentUSD
	}
	for i := range l.Yours {
		l.Yours[i].Limit, l.Yours[i].Spent = l.Yours[i].LimitUSD, l.Yours[i].SpentUSD
	}
}

func (g *GpuList) fromBefore() {
	if g.Currency != "" {
		return
	}
	g.Currency = CurrencyUSD
	for i := range g.Data {
		g.Data[i].Currency, g.Data[i].PricePerHour = CurrencyUSD, g.Data[i].PricePerHourUSD
	}
}

func (r *TeamRules) fromBefore() {
	if r.Currency == "" {
		r.Currency, r.MonthBudget, r.Spent = CurrencyUSD, r.MonthBudgetUSD, r.SpentUSD
	}
}

func (l *ModelList) fromBefore() {
	if l.Currency == "" {
		l.Currency = CurrencyUSD
	}
	if l.Currency == CurrencyUSD && l.Rate == 0 {
		l.Rate = 1
	}
}
