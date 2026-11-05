// Secrets: the value goes in the body of the call that sets it and nowhere else - never in an answer, an object, an
// error or another call - and machines are given them by name.

import assert from "node:assert/strict";
import { test } from "node:test";
import { inspect } from "node:util";

import { BadRequestError, SECRET_VALUE_MAX, UnavailableError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

// A made-up value, nobody's.
const VALUE = "made-up-secret-9a2d";

function carriedElsewhere(api, value) {
  for (const s of api.seen) {
    if (s.path.includes(value) || s.query.toString().includes(value) || JSON.stringify(s.headers).includes(value)) return true;
    if (!(s.method === "PUT" && s.path.startsWith("/v1/secrets/")) && s.body.toString().includes(value)) return true;
  }
  return false;
}

test("a value is set in the body alone and never comes back", async (t) => {
  const { api, client } = await setup(t);
  const s = await client.secrets.set("API_TOKEN", VALUE);
  assert.equal(s.name, "API_TOKEN");
  assert.deepEqual(s.machines, []);
  assert.equal(s.lastResponse.status, 201);
  const put = api.requests("PUT", "^/v1/secrets/API_TOKEN$").at(-1);
  assert.deepEqual(put.json(), { value: VALUE });
  assert.equal([...put.query].length, 0);
  assert.equal(api.secrets.get("API_TOKEN").value, VALUE);
  assert.equal((await client.secrets.set("API_TOKEN", VALUE + "-2")).lastResponse.status, 200);
  const listed = await client.secrets.list();
  assert.deepEqual(listed.map((x) => x.name), ["API_TOKEN"]);
  for (const shown of [JSON.stringify(s), inspect(s, { depth: 9 }), JSON.stringify(listed), inspect(client, { depth: 9 })]) {
    assert.ok(!shown.includes(VALUE));
  }
  assert.ok(!carriedElsewhere(api, VALUE));
});

test("machines are given secrets by name", async (t) => {
  const { api, client } = await setup(t);
  await client.secrets.set("API_TOKEN", VALUE);
  await client.secrets.set("DATABASE_URL", "made-up-db");
  const m = await client.machines.create({ name: "web", secrets: ["DATABASE_URL", "API_TOKEN"] });
  assert.deepEqual(api.requests("POST", "^/v1/machines$").at(-1).json().secrets, ["DATABASE_URL", "API_TOKEN"]);
  assert.deepEqual(m.secrets, ["API_TOKEN", "DATABASE_URL"]);
  assert.deepEqual((await client.secrets.list()).map((x) => x.machines), [[m.id], [m.id]]);
  const fork = await client.machines.fork(m.id);
  assert.deepEqual(fork.secrets, ["API_TOKEN", "DATABASE_URL"]);
  // said whole: the names take the place of the ones it had; [] takes them all away
  assert.deepEqual((await client.machines.update(m.id, { secrets: ["API_TOKEN"] })).secrets, ["API_TOKEN"]);
  assert.deepEqual((await client.machines.update(m.id, { secrets: [] })).secrets, []);
  assert.deepEqual(api.requests("PATCH", `^/v1/machines/${m.id}$`).at(-1).json(), { secrets: [] });
  await assert.rejects(client.machines.create({ secrets: ["NOT_SET"] }), (e) => e instanceof BadRequestError && /set it first/.test(e.message));
  // deleted, and taken off the machines given it
  await client.secrets.delete("API_TOKEN");
  assert.deepEqual((await client.machines.get(fork.id)).secrets, ["DATABASE_URL"]);
  assert.ok(!carriedElsewhere(api, VALUE));
});

test("what is not right is refused before any call and says nothing of the value", async (t) => {
  const { api, client } = await setup(t);
  const calls = api.seen.length;
  for (const name of ["", "api_token", "1ST", "A-B", "A/B", "A".repeat(65)]) {
    await assert.rejects(client.secrets.set(name, VALUE), TypeError);
    await assert.rejects(client.secrets.delete(name), TypeError);
  }
  for (const value of ["", "a\0" + VALUE, VALUE + "\ud800", "\udc00" + VALUE, VALUE + "x".repeat(SECRET_VALUE_MAX)]) {
    await assert.rejects(client.secrets.set("API_TOKEN", value), (e) => e instanceof TypeError && !inspect(e).includes(VALUE));
  }
  // a pair whole is text: it is sent
  await client.secrets.set("EMOJI", "made-up-😀");
  await assert.rejects(client.secrets.set("API_TOKEN", Buffer.from(VALUE)), TypeError);
  await assert.rejects(client.machines.create({ secrets: "API_TOKEN" }), TypeError);
  await assert.rejects(client.machines.update("m_x", { secrets: ["lower"] }), TypeError);
  assert.equal(api.seen.length, calls + 1);
});

test("a refused set is tried again and its error says nothing of the value", async (t) => {
  const { api, client } = await setup(t);
  api.fault("PUT", "^/v1/secrets/API_TOKEN$", { status: 503, code: "unavailable", times: 10 });
  await assert.rejects(client.secrets.set("API_TOKEN", VALUE), (e) => {
    assert.ok(e instanceof UnavailableError);
    assert.ok(!inspect(e, { depth: 9 }).includes(VALUE) && !JSON.stringify(e).includes(VALUE) && !String(e).includes(VALUE));
    return true;
  });
  assert.equal(api.requests("PUT", "^/v1/secrets/API_TOKEN$").length, client.maxRetries + 1);
});

test("an AI key is kept outside every machine by its name", async (t) => {
  const { api, client } = await setup(t);
  const s = await client.secrets.set("ANTHROPIC_API_KEY", VALUE);
  assert.deepEqual(api.requests("PUT", "^/v1/secrets/ANTHROPIC_API_KEY$").at(-1).json(), { value: VALUE });
  assert.equal(s.every_machine, true);
  assert.equal(s.outside.preset, "anthropic");
  assert.deepEqual(s.outside.hosts, ["api.anthropic.com"]);
  assert.deepEqual(s.outside.headers.map((h) => h.name), ["x-api-key", "authorization"]);
  const m = await client.machines.create({ name: "box" });
  assert.deepEqual(m.ai_keys, ["ANTHROPIC_API_KEY"]);
  const shut = await client.machines.create({ name: "shut", network: { internet: "closed" } });
  assert.deepEqual(shut.ai_keys, []);
  // kept inside, or only on the machines given it: said in the call's body
  const inside = await client.secrets.set("ANTHROPIC_API_KEY", VALUE, { outside: false });
  assert.deepEqual(api.requests("PUT", "^/v1/secrets/ANTHROPIC_API_KEY$").at(-1).json(), { value: VALUE, outside: false });
  assert.equal(inside.outside, null);
  assert.equal(inside.every_machine, false);
  const given = await client.secrets.set("ANTHROPIC_API_KEY", VALUE, { every_machine: false });
  assert.deepEqual(api.requests("PUT", "^/v1/secrets/ANTHROPIC_API_KEY$").at(-1).json(), { value: VALUE, every_machine: false });
  assert.equal(given.outside.preset, "anthropic");
  assert.deepEqual((await client.machines.get(m.id)).ai_keys, []);
  const own = await client.secrets.set("LLM_TOKEN", VALUE, { outside: { hosts: ["llm.example.com"], header: "Authorization" } });
  assert.equal(own.outside.preset, null);
  await assert.rejects(client.secrets.set("DATABASE_URL", VALUE, { outside: true }), (e) => e instanceof BadRequestError);
  await assert.rejects(client.secrets.set("DATABASE_URL", VALUE, { outside: "yes" }), TypeError);
  await assert.rejects(client.secrets.set("DATABASE_URL", VALUE, { every_machine: "yes" }), TypeError);
  for (const shown of [JSON.stringify(s), inspect(inside, { depth: 9 }), JSON.stringify(await client.secrets.list())]) {
    assert.ok(!shown.includes(VALUE));
  }
  assert.ok(!carriedElsewhere(api, VALUE));
});

test("the presets say the names and where each key goes", async (t) => {
  const { client } = await setup(t);
  const presets = await client.secrets.presets();
  assert.deepEqual(presets.map((p) => p.id), ["anthropic", "gemini"]);
  assert.deepEqual(presets[1].names, ["GEMINI_API_KEY", "GOOGLE_API_KEY"]);
  assert.deepEqual(presets[1].query, ["key"]);
});
