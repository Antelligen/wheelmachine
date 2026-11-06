"""The credit: what is left, its entries a page at a time, /v1/me saying what is left, and buying credit."""

from __future__ import annotations

from support import APITest

from wheelmachine import CreditBalance, CreditEntry, CreditPurchase, WheelmachineError


class TheCredit(APITest):
    def test_the_credit_and_its_latest_entries(self) -> None:
        self.api.credit_usd = 4.8688
        self.api.credit_entries.insert(0, {
            "id": "ce_" + "t" * 26, "object": "credit_entry", "kind": "machine_time", "amount_usd": -0.1312,
            "reason": "Running time", "machine": "m_" + "1" * 26, "machine_name": "trainer",
            "at": "2026-11-02T09:31:00Z", "updated_at": "2026-11-02T11:09:00Z"})
        credit = self.client.credit.get(limit=1)
        self.assertIsInstance(credit, CreditBalance)
        self.assertEqual((credit.account, credit.balance_usd, credit.empty, credit.buy_open), ("acct_test", 4.8688, False, False))
        self.assertEqual(len(credit.entries), 1)
        entry = credit.entries[0]
        self.assertIsInstance(entry, CreditEntry)
        self.assertEqual((entry.kind, entry.amount_usd, entry.machine_name), ("machine_time", -0.1312, "trainer"))
        self.assertEqual(credit.next, "1")
        self.assertEqual(self.api.seen[-1].query.get("limit"), ["1"])
        self.assertEqual(self.client.me().standing.credit_usd, 4.8688)

    def test_every_entry_page_after_page(self) -> None:
        for n in range(3):
            self.api.credit_entries.insert(0, {
                "id": "ce_" + str(n) * 26, "object": "credit_entry", "kind": "staff_credit", "amount_usd": n + 1.0,
                "reason": "credit %d" % (n + 1), "machine": None, "machine_name": None,
                "at": "2026-11-02T09:30:00Z", "updated_at": "2026-11-02T09:30:00Z"})
        reasons = [e.reason for e in self.client.credit.entries(limit=2)]
        self.assertEqual(reasons, ["credit 3", "credit 2", "credit 1", "Free credit to start with"])
        self.assertEqual([s.query.get("after") for s in self.api.seen if s.path == "/v1/credit"], [None, ["2"]])

    def test_credit_is_bought_in_paddles_checkout(self) -> None:
        with self.assertRaises(WheelmachineError) as closed:
            self.client.credit.buy(25)
        self.assertEqual(closed.exception.code, "not_open")
        self.assertEqual(self.client.credit.get().buy_amounts_usd, [])
        self.api.buy_open = True
        credit = self.client.credit.get()
        self.assertEqual((credit.buy_open, credit.buy_amounts_usd), (True, [10, 25, 50, 100]))
        purchase = self.client.credit.buy(25)
        self.assertIsInstance(purchase, CreditPurchase)
        self.assertEqual((purchase.amount_usd, purchase.account, purchase.checkout.provider, purchase.checkout.environment),
                         (25, "acct_test", "paddle", "sandbox"))
        self.assertEqual(purchase.checkout.url, "https://app.wheelmachine.ai/billing?purchase=" + purchase.id)
        sent = self.api.seen[-1]
        self.assertEqual((sent.method, sent.path, sent.body), ("POST", "/v1/credit/purchases", b'{"amount":25}'))
        self.assertTrue(sent.headers.get("idempotency-key"), "a purchase is a create: it carries an Idempotency-Key")
        with self.assertRaises(WheelmachineError) as wrong:
            self.client.credit.buy(30)
        self.assertEqual(wrong.exception.code, "invalid_request")

    def test_credit_run_out_says_so(self) -> None:
        self.api.credit_usd = -0.04
        credit = self.client.credit.get()
        self.assertTrue(credit.empty)
        self.assertEqual(credit.balance_usd, -0.04)

    def test_an_account_counted_in_pounds_says_and_sets_its_amounts_in_pounds(self) -> None:
        self.api.buy_open = True
        credit = self.client.credit.get()
        self.assertEqual((credit.currency, credit.currency_fixed, credit.balance, credit.buy_amounts), ("USD", False, 5.0, [10, 25, 50, 100]))
        credit = self.client.credit.set_currency("GBP")
        self.assertEqual((credit.currency, credit.balance, credit.balance_usd), ("GBP", 5.0, 6.25))
        sent = self.api.seen[-1]
        self.assertEqual((sent.method, sent.path, sent.json()), ("PUT", "/v1/credit/currency", {"currency": "GBP"}))
        purchase = self.client.credit.buy(25)
        self.assertEqual((purchase.currency, purchase.amount, purchase.amount_usd, purchase.checkout.currency), ("GBP", 25.0, 31.25, "GBP"))
        self.assertEqual(self.api.seen[-1].json(), {"amount": 25})
        limits = self.client.set_month_limit(160)
        self.assertEqual((limits.currency, limits.account.month_cap, limits.account.month_cap_usd, limits.yours[0].limit), ("GBP", 160, 200.0, 160))
        me = self.client.me()
        self.assertEqual((me.standing.currency, me.standing.credit, me.standing.credit_usd), ("GBP", 5.0, 6.25))
        gpus = self.client.gpus()
        self.assertEqual((gpus.currency, gpus.data[0].price_per_hour, gpus.data[0].price_per_hour_usd), ("GBP", 2.024, 2.53))
        usage = self.client.usage()
        self.assertEqual(usage.currency, "GBP")
        self.api.currency_fixed = True
        with self.assertRaises(WheelmachineError) as caught:
            self.client.credit.set_currency("EUR")
        self.assertEqual((caught.exception.status, caught.exception.code), (409, "conflict"))

    def test_an_answer_from_before_accounts_had_currencies_is_in_dollars(self) -> None:
        old = CreditBalance.from_dict({"object": "credit", "account": "acct_test", "balance_usd": 4.25, "low_usd": 1, "empty": False,
                                       "buy": {"open": True, "amounts_usd": [10, 25]}, "free_credit": {"usd": 5, "needs": "github"},
                                       "data": [{"id": "ce_1", "kind": "welcome", "amount_usd": 5}]})
        self.assertEqual((old.currency, old.balance, old.low, old.buy_amounts, old.free_credit, old.entries[0].amount),
                         ("USD", 4.25, 1.0, [10, 25], 5.0, 5.0))
