// Tried again when it is safe and worth it, never so that one call makes two; kept connections; stopping a call.

import assert from "node:assert/strict";
import { test } from "node:test";

import { APIConnectionError, APITimeoutError, ConflictError, RateLimitError, UnavailableError } from "../dist/index.js";
import { closedPort, setup } from "./fake-api.mjs";

const UUID4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const creates = (api) => api.requests("POST", "^/v1/machines$");
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

test("a 503 is tried again after its Retry-After, with the same Idempotency-Key", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.fault("POST", "^/v1/machines$", { status: 503, code: "unavailable", retryAfter: 2 });
  const m = await client.machines.create({ name: "once" });
  assert.equal(m.state, "running");
  assert.deepEqual(sleeps, [2000]);
  const keys = creates(api).map((s) => s.headers["idempotency-key"]);
  assert.equal(keys.length, 2);
  assert.equal(keys[0], keys[1]);
  assert.match(keys[0], UUID4);
  assert.equal(api.machines.size, 1);
});

test("each create has its own key, and a key given is the key sent", async (t) => {
  const { api, client } = await setup(t);
  await client.machines.create();
  await client.machines.create();
  assert.equal(new Set(creates(api).map((s) => s.headers["idempotency-key"])).size, 2);
  await client.projects.create({ name: "p" }, { idempotencyKey: "my-own-key-1" });
  assert.equal(api.requests("POST", "^/v1/projects$").at(-1).headers["idempotency-key"], "my-own-key-1");
});

test("a create whose answer was lost is given back, not made twice", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.fault("POST", "^/v1/machines$", { drop: "after" });
  const m = await client.machines.create({ name: "once" });
  assert.deepEqual([...api.machines.keys()], [m.id]);
  const keys = creates(api).map((s) => s.headers["idempotency-key"]);
  assert.equal(keys.length, 2);
  assert.equal(keys[0], keys[1]);
  assert.equal(m.lastResponse.replayed, true);
  assert.deepEqual(sleeps, [250]); // backoff: up to 0.5 s, the jitter at a half
});

test("a create still running is waited for, without spending the retries", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.fault("POST", "^/v1/machines$", { status: 409, code: "conflict", retryAfter: 1, times: 4 });
  const m = await client.machines.create();
  assert.equal(m.state, "running");
  assert.deepEqual(sleeps, [1000, 1000, 1000, 1000]);
  assert.equal(new Set(creates(api).map((s) => s.headers["idempotency-key"])).size, 1);
});

test("a conflict without Retry-After is the answer", async (t) => {
  const { api, client, sleeps } = await setup(t);
  const m = await client.machines.create();
  api.fault("POST", "/pause$", { status: 409, code: "conflict" });
  await assert.rejects(client.machines.pause(m.id), ConflictError);
  assert.deepEqual(sleeps, []);
});

test("a command whose answer was lost is not sent again", async (t) => {
  const { api, client, sleeps } = await setup(t);
  const m = await client.machines.create();
  api.fault("POST", "/exec$", { drop: "after" });
  const e = await client.machines.exec(m.id, ["echo", "once"]).catch((err) => err);
  assert.ok(e instanceof APIConnectionError, String(e));
  assert.ok(!(e instanceof APITimeoutError));
  assert.match(e.message, /lost/);
  assert.equal(api.execs.length, 1);
  assert.deepEqual(sleeps, []);
});

test("a command refused with 504 is not sent again; with 503 it is", async (t) => {
  const { api, client, sleeps } = await setup(t);
  const m = await client.machines.create();
  api.fault("POST", "/exec$", { status: 504, code: "timeout", times: 5 });
  await assert.rejects(client.machines.exec(m.id, ["sleep", "999"]), UnavailableError);
  assert.equal(api.requests("POST", "/exec$").length, 1);
  api.faults.length = 0;
  api.fault("POST", "/exec$", { status: 503, code: "unavailable", retryAfter: 1 });
  const r = await client.machines.exec(m.id, ["echo", "hi"]);
  assert.equal(r.stdout, "hi\n");
  assert.deepEqual(sleeps, [1000]);
  assert.equal(api.execs.length, 1);
});

test("a read that lost its answer, or met a 504, is tried again", async (t) => {
  const { api, client, sleeps } = await setup(t);
  const m = await client.machines.create();
  api.fault("GET", "^/v1/machines/m_[0-9]+$", { drop: "before" });
  api.fault("GET", "^/v1/machines/m_[0-9]+$", { status: 504, code: "timeout" });
  assert.equal((await client.machines.get(m.id)).id, m.id);
  assert.deepEqual(sleeps, [250, 500]);
});

test("a 429 is tried again up to maxRetries", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.fault("GET", "^/v1/me$", { status: 429, code: "rate_limited", retryAfter: 1, times: 10 });
  const e = await client.me().catch((err) => err);
  assert.ok(e instanceof RateLimitError);
  assert.deepEqual([e.status, e.code, e.retryAfter], [429, "rate_limited", 1]);
  assert.equal(e.requestId, api.seen.at(-1).rid);
  assert.deepEqual(sleeps, [1000, 1000, 1000]);
  assert.equal(api.requests("GET", "^/v1/me$").length, 4);
});

test("the backoff grows when no Retry-After is given", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.fault("GET", "^/v1/me$", { status: 503, code: "unavailable", times: 10 });
  await assert.rejects(client.me(), UnavailableError);
  assert.deepEqual(sleeps, [250, 500, 1000]);
});

test("a 503 not_open is not tried again, whatever the call: what is not served yet will not be in a moment", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.fault("GET", "^/v1/me$", { status: 503, code: "not_open", times: 5 });
  api.fault("POST", "^/v1/machines$", { status: 503, code: "not_open", retryAfter: 1, times: 5 });
  const e = await client.me().catch((err) => err);
  assert.ok(e instanceof UnavailableError);
  assert.deepEqual([e.status, e.code], [503, "not_open"]);
  await assert.rejects(client.machines.create(), (err) => err instanceof UnavailableError && err.code === "not_open");
  assert.equal(api.requests("GET", "^/v1/me$").length, 1);
  assert.equal(creates(api).length, 1);
  assert.deepEqual(sleeps, []);
});

test("a long Retry-After is not waited out, and the error keeps the key to try again with", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.fault("POST", "^/v1/machines$", { status: 503, code: "unavailable", retryAfter: 120 });
  const e = await client.machines.create().catch((err) => err);
  assert.ok(e instanceof UnavailableError);
  assert.equal(e.retryAfter, 120);
  assert.deepEqual(sleeps, []);
  assert.match(e.idempotencyKey, UUID4);
});

test("a limit of the account's is not tried again", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.fault("POST", "^/v1/machines$", { status: 429, code: "limit_reached" });
  const e = await client.machines.create().catch((err) => err);
  assert.ok(e instanceof RateLimitError);
  assert.equal(e.code, "limit_reached");
  assert.deepEqual(sleeps, []);
});

test("no retries when asked for none, for the client or for one call", async (t) => {
  const { api, make, sleeps } = await setup(t);
  api.fault("GET", "^/v1/me$", { status: 503, code: "unavailable", retryAfter: 1, times: 2 });
  await assert.rejects(make({ maxRetries: 0 }).me(), UnavailableError);
  await assert.rejects(make().me({ maxRetries: 0 }), UnavailableError);
  assert.deepEqual(sleeps, []);
});

test("an address that cannot be reached: nothing was sent, so even a command is tried again", async (t) => {
  const { make, sleeps } = await setup(t);
  const client = make({ baseURL: `http://127.0.0.1:${await closedPort()}`, maxRetries: 2 });
  const e = await client.me().catch((err) => err);
  assert.ok(e instanceof APIConnectionError, String(e));
  assert.match(e.message, /could not reach/);
  assert.deepEqual(sleeps, [250, 500]);
  sleeps.length = 0;
  await assert.rejects(client.machines.exec(`m_${"0".repeat(26)}`, ["true"]), APIConnectionError);
  assert.equal(sleeps.length, 2);
});

test("no answer in time is a timeout", async (t) => {
  const { api, make } = await setup(t);
  api.fault("GET", "^/v1/me$", { delay: 1500 });
  const e = await make({ timeout: 300, maxRetries: 0 }).me().catch((err) => err);
  assert.ok(e instanceof APITimeoutError, String(e));
  assert.match(e.message, /within 0\.3 seconds/);
});

test("a call stopped by its signal ends at once, and is not tried again", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.fault("GET", "^/v1/me$", { delay: 2000 });
  const controller = new AbortController();
  setTimeout(() => controller.abort(), 100);
  const began = Date.now();
  const e = await client.me({ signal: controller.signal }).catch((err) => err);
  assert.equal(e.name, "AbortError");
  assert.ok(Date.now() - began < 1500);
  assert.deepEqual(sleeps, []);
});

test("calls share kept connections", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create();
  for (let i = 0; i < 8; i++) await client.machines.get(m.id);
  await client.machines.writeFile(m.id, "work/a", new Uint8Array([1]));
  await client.machines.readFile(m.id, "work/a");
  // eleven calls; Node's fetch may open a second connection while it puts the first back, never one a call
  assert.ok(api.connections <= 2, `${api.connections} connections`);
});

test("a connection the server closed is not used again", async (t) => {
  const { api, client, sleeps } = await setup(t);
  api.closeAfterAnswer = true;
  const m = await client.machines.create();
  await pause(50); // the server closes the idle connection meanwhile
  await client.machines.exec(m.id, ["echo", "1"]);
  await pause(50);
  await client.machines.exec(m.id, ["echo", "2"]);
  assert.equal(api.execs.length, 2);
  assert.deepEqual(sleeps, []);
  assert.equal(api.connections, 3);
});
