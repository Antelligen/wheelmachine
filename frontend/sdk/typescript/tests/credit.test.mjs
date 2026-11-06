// The credit: what is left, its entries a page at a time, /v1/me saying what is left, and buying credit.

import assert from "node:assert/strict";
import { test } from "node:test";

import { setup } from "./fake-api.mjs";

function entry(n, extra = {}) {
  return { id: "ce_" + String(n).repeat(26).slice(0, 26), object: "credit_entry", kind: "staff_credit", amount_usd: n, reason: `credit ${n}`,
    machine: null, machine_name: null, at: "2026-11-02T09:30:00Z", updated_at: "2026-11-02T09:30:00Z", ...extra };
}

test("the credit and its latest entries", async (t) => {
  const { api, client } = await setup(t);
  api.creditUSD = 4.8688;
  api.creditEntries.unshift(entry(1, { kind: "machine_time", amount_usd: -0.1312, reason: "Running time", machine: "m_" + "1".repeat(26),
    machine_name: "trainer" }));
  const credit = await client.credit.get({ limit: 1 });
  assert.deepEqual([credit.account, credit.balance_usd, credit.empty, credit.buy.open, credit.next], ["acct_test", 4.8688, false, false, "1"]);
  assert.deepEqual([credit.data.length, credit.data[0].kind, credit.data[0].machine_name], [1, "machine_time", "trainer"]);
  assert.equal(api.seen.at(-1).query.get("limit"), "1");
  assert.equal((await client.me()).standing.credit_usd, 4.8688);
});

test("every entry, page after page", async (t) => {
  const { api, client } = await setup(t);
  for (const n of [1, 2, 3]) api.creditEntries.unshift(entry(n));
  const reasons = [];
  for await (const e of client.credit.entries({ limit: 2 })) reasons.push(e.reason);
  assert.deepEqual(reasons, ["credit 3", "credit 2", "credit 1", "Free credit to start with"]);
  assert.deepEqual(api.seen.filter((s) => s.path === "/v1/credit").map((s) => s.query.get("after")), [null, "2"]);
});

test("credit run out says so", async (t) => {
  const { api, client } = await setup(t);
  api.creditUSD = -0.04;
  const credit = await client.credit.get();
  assert.deepEqual([credit.empty, credit.balance_usd], [true, -0.04]);
});

test("credit is bought in Paddle's checkout", async (t) => {
  const { api, client } = await setup(t);
  await assert.rejects(client.credit.buy(25), (err) => err.code === "not_open");
  assert.deepEqual((await client.credit.get()).buy, { open: false, amounts: [], amounts_usd: [], paddle: null });
  api.buyOpen = true;
  const credit = await client.credit.get();
  assert.deepEqual([credit.buy.open, credit.buy.amounts_usd, credit.buy.paddle?.environment], [true, [10, 25, 50, 100], "sandbox"]);
  const purchase = await client.credit.buy(25);
  assert.deepEqual([purchase.object, purchase.amount_usd, purchase.account, purchase.checkout.provider, purchase.checkout.environment],
    ["credit_purchase", 25, "acct_test", "paddle", "sandbox"]);
  assert.equal(purchase.checkout.url, "https://app.wheelmachine.ai/billing?purchase=" + purchase.id);
  const sent = api.seen.at(-1);
  assert.deepEqual([sent.method, sent.path], ["POST", "/v1/credit/purchases"]);
  assert.ok(sent.headers["idempotency-key"], "a purchase is a create: it carries an Idempotency-Key");
  await assert.rejects(client.credit.buy(30), (err) => err.code === "invalid_request");
});

test("an account counted in pounds: its credit, a purchase and its limit in pounds, and its currency changed until it is fixed", async (t) => {
  const { api, client } = await setup(t);
  api.buyOpen = true;
  let credit = await client.credit.get();
  assert.deepEqual([credit.currency, credit.currency_fixed, credit.balance, credit.buy.amounts], ["USD", false, 5, [10, 25, 50, 100]]);
  credit = await client.credit.setCurrency("GBP");
  assert.deepEqual([credit.currency, credit.balance, credit.balance_usd], ["GBP", 5, 6.25]);
  assert.deepEqual([api.seen.at(-1).method, api.seen.at(-1).path, api.seen.at(-1).json()], ["PUT", "/v1/credit/currency", { currency: "GBP" }]);
  const purchase = await client.credit.buy(25);
  assert.deepEqual([purchase.currency, purchase.amount, purchase.amount_usd, purchase.checkout.currency, purchase.checkout.prices[1].amount],
    ["GBP", 25, 31.25, "GBP", 25]);
  assert.deepEqual(api.seen.at(-1).json(), { amount: 25 });
  const set = await client.setMonthLimit(160);
  assert.deepEqual([set.currency, set.account.month_cap, set.account.month_cap_usd, set.yours[0].limit], ["GBP", 160, 200, 160]);
  const me = await client.me();
  assert.deepEqual([me.standing.currency, me.standing.credit, me.standing.credit_usd], ["GBP", 5, 6.25]);
  const gpus = await client.gpus();
  assert.deepEqual([gpus.currency, gpus.data[0].price_per_hour, gpus.data[0].price_per_hour_usd], ["GBP", 2.024, 2.53]);
  api.currencyFixed = true;
  await assert.rejects(client.credit.setCurrency("EUR"), (err) => err.status === 409 && err.code === "conflict");
});
