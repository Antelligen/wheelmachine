// A machine's internet setting through the library: a machine's internet policy is set on create and changed on update, and the
// machine shows it.

import assert from "node:assert/strict";
import { test } from "node:test";

import { BadRequestError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

test("a machine is made with a policy and shows it", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create({ network: { internet: "allowed", allow: ["api.github.com", "10.0.0.0/8"] } });
  assert.equal(m.network.internet, "allowed");
  assert.deepEqual(m.network.allow, ["api.github.com", "10.0.0.0/8"]);
  const post = api.requests("POST", "^/v1/machines$").at(-1);
  assert.deepEqual(post.json().network, { internet: "allowed", allow: ["api.github.com", "10.0.0.0/8"] });
});

test("an open machine sends no policy and reads back open", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create({});
  assert.equal(m.network.internet, "open");
  const post = api.requests("POST", "^/v1/machines$").at(-1);
  assert.equal(post.json()?.network, undefined);
});

test("a policy is changed on update, without a restart", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create({});
  const changed = await client.machines.update(m.id, { network: { internet: "closed", allow: [] } });
  assert.equal(changed.network.internet, "closed");
  const patch = api.requests("PATCH", `^/v1/machines/${m.id}$`).at(-1);
  assert.deepEqual(patch.json().network, { internet: "closed", allow: [] });
});

test("a list with a closed policy is refused by the API", async (t) => {
  const { client } = await setup(t);
  await assert.rejects(() => client.machines.create({ network: { internet: "closed", allow: ["x.com"] } }), BadRequestError);
});
