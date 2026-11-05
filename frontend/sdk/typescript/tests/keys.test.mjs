// Keys: one with full access needs the code we e-mail its person - asked for as its error says, and carried with the
// same call.

import assert from "node:assert/strict";
import { test } from "node:test";

import { PermissionDeniedError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

test("a key with full access is made with the code we e-mail", async (t) => {
  const { api, client } = await setup(t);
  api.confirmCode = "482913";
  const refused = await client.keys.create({ name: "deploy", scopes: ["cloud:*"] }).catch((e) => e);
  assert.ok(refused instanceof PermissionDeniedError);
  assert.equal(refused.code, "confirm_required");
  assert.deepEqual(refused.confirm, { step: "full_access_key", account: "acc_01jb2xk3n4p5q6r7s8t9v0w1x2" });
  const sent = await client.confirmCodes.send(refused.confirm);
  assert.deepEqual([sent.step, sent.sent_to], ["full_access_key", "a…@example.com"]);
  const wrong = await client.keys.create({ name: "deploy", scopes: ["cloud:*"] }, { confirmCode: "111111" }).catch((e) => e);
  assert.equal(wrong.code, "confirm_wrong");
  const made = await client.keys.create({ name: "deploy", scopes: ["cloud:*"] }, { confirmCode: "482913" });
  assert.ok(made.secret.startsWith("ak_live_"));
  // a key that does less needs none
  await client.keys.create({ name: "reader", scopes: ["cloud:machines:read"] });
});
