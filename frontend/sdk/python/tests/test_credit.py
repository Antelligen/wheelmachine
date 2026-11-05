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
        self.assertEqual((sent.method, sent.path, sent.body), ("POST", "/v1/credit/purchases", b'{"amount_usd":25}'))
        self.assertTrue(sent.headers.get("idempotency-key"), "a purchase is a create: it carries an Idempotency-Key")
        with self.assertRaises(WheelmachineError) as wrong:
            self.client.credit.buy(30)
        self.assertEqual(wrong.exception.code, "invalid_request")

    def test_credit_run_out_says_so(self) -> None:
        self.api.credit_usd = -0.04
        credit = self.client.credit.get()
        self.assertTrue(credit.empty)
        self.assertEqual(credit.balance_usd, -0.04)
