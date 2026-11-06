// The calls, as the API sees them: the key on every call, the answers as they came, errors by family.

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { inspect } from "node:util";

import {
  AuthenticationError,
  BadRequestError,
  ConflictError,
  InternalServerError,
  NotFoundError,
  PaymentRequiredError,
  PermissionDeniedError,
  RateLimitError,
  UnavailableError,
  VERSION,
  WaitTimeoutError,
  Wheelmachine,
  WheelmachineError,
} from "../dist/index.js";
import { KEY, REVOKED_KEY, setup } from "./fake-api.mjs";

// The version is kept in package.json alone: the build writes src/version.ts from it (scripts/version.mjs), and a
// version.ts left behind by a change to package.json fails here.
test("the version is package.json's", async () => {
  const { version } = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
  assert.match(version, /^\d+\.\d+\.\d+/);
  assert.equal(VERSION, version, "src/version.ts is not package.json's version: run npm run build");
});

function withEnv(vars, fn) {
  const before = {};
  for (const [k, v] of Object.entries(vars)) {
    before[k] = process.env[k];
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
  try {
    return fn();
  } finally {
    for (const [k, v] of Object.entries(before)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  }
}

test("every call carries the key and says what it is", async (t) => {
  const { api, client } = await setup(t);
  const me = await client.me();
  const seen = api.seen.at(-1);
  assert.equal(seen.headers.authorization, `Bearer ${KEY}`);
  assert.equal(seen.headers["user-agent"], `wheelmachine-typescript/${VERSION}`);
  assert.equal(me.account.id, "acct_test");
  assert.deepEqual(me.key.scopes, ["cloud:*"]);
  assert.equal(me.standing.can_spend, true);
});

test("an answer says how it came, out of its JSON", async (t) => {
  const { api, client } = await setup(t);
  const me = await client.me();
  assert.deepEqual(me.lastResponse, {
    status: 200,
    requestId: api.seen.at(-1).rid,
    replayed: false,
    rateLimit: { limit: 600, windowSeconds: 60, remaining: 599, resetSeconds: 1 },
  });
  assert.equal("lastResponse" in JSON.parse(JSON.stringify(me)), false);
});

test("key and address come from the environment", async (t) => {
  const { api } = await setup(t);
  const client = withEnv({ WHEELMACHINE_API_KEY: KEY, WHEELMACHINE_BASE_URL: api.url }, () => new Wheelmachine());
  assert.equal((await client.me()).account.id, "acct_test");
  assert.equal(client.baseURL, api.url);
});

test("the address is the API's own by default", () => {
  const client = withEnv({ WHEELMACHINE_BASE_URL: undefined }, () => new Wheelmachine({ apiKey: KEY }));
  assert.equal(client.baseURL, "https://api.wheelmachine.ai");
});

test("no key is refused before any call", () => {
  withEnv({ WHEELMACHINE_API_KEY: undefined }, () => {
    assert.throws(() => new Wheelmachine(), (e) => e instanceof WheelmachineError && /WHEELMACHINE_API_KEY/.test(e.message));
  });
});

test("the key is never shown", async (t) => {
  const { client, make } = await setup(t);
  for (const text of [inspect(client, { depth: 5, showHidden: true }), JSON.stringify(client), String(client)]) {
    assert.equal(text.includes(KEY), false, text);
  }
  const bad = make({ apiKey: REVOKED_KEY });
  const e = await bad.me().then(
    () => assert.fail("no error"),
    (err) => err,
  );
  assert.ok(e instanceof AuthenticationError);
  assert.equal(e.code, "key_revoked");
  for (const text of [String(e), e.stack, inspect(e, { depth: 5 }), JSON.stringify(e.body)]) {
    assert.equal(text.includes(REVOKED_KEY), false);
    assert.equal(text.includes(KEY), false);
  }
});

test("a key that could break a header is refused without being echoed", () => {
  assert.throws(
    () => new Wheelmachine({ apiKey: "ak_live_abc\r\nInjected: yes" }),
    (e) => e instanceof WheelmachineError && !e.message.includes("Injected") && !e.message.includes("ak_live_abc"),
  );
});

test("an address that is not one is refused", () => {
  for (const baseURL of ["ftp://example.com", "not a url", "https://user:pw@example.com", "https://example.com/?a=1"]) {
    assert.throws(() => new Wheelmachine({ apiKey: KEY, baseURL }), WheelmachineError, baseURL);
  }
});

test("it refuses to run in a web page unless told it may", () => {
  globalThis.window = {};
  globalThis.document = {};
  try {
    assert.throws(() => new Wheelmachine({ apiKey: KEY }), (e) => e instanceof WheelmachineError && /web page/.test(e.message));
    assert.ok(new Wheelmachine({ apiKey: KEY, dangerouslyAllowBrowser: true }));
  } finally {
    delete globalThis.window;
    delete globalThis.document;
  }
});

test("each status has its error, with the problem's details", async (t) => {
  const { api, make } = await setup(t);
  const client = make({ maxRetries: 0 });
  const cases = [
    [400, "invalid_request", BadRequestError],
    [401, "key_expired", AuthenticationError],
    [402, "payment_required", PaymentRequiredError],
    [403, "forbidden", PermissionDeniedError],
    [404, "not_found", NotFoundError],
    [408, "timeout", WaitTimeoutError],
    [409, "conflict", ConflictError],
    [411, "invalid_request", BadRequestError],
    [413, "too_large", BadRequestError],
    [422, "invalid_request", ConflictError],
    [429, "rate_limited", RateLimitError],
    [500, "internal", InternalServerError],
    [502, "unavailable", UnavailableError],
    [503, "unavailable", UnavailableError],
    [504, "timeout", UnavailableError],
  ];
  for (const [status, code, Cls] of cases) {
    api.fault("GET", "^/v1/me$", { status, code, detail: `words for ${code}` });
    const e = await client.me().then(
      () => assert.fail(`no error for ${status}`),
      (err) => err,
    );
    assert.ok(e instanceof Cls, `${status}: ${e.name}`);
    assert.ok(e instanceof WheelmachineError);
    assert.equal(e.status, status);
    assert.equal(e.code, code);
    assert.equal(e.detail, `words for ${code}`);
    assert.equal(e.requestId, api.seen.at(-1).rid);
    assert.ok(e.message.includes(`${status} ${code}: words for ${code} (request ${e.requestId})`), e.message);
  }
});

test("an error from the API itself", async (t) => {
  const { client } = await setup(t);
  const e = await client.machines.get(`m_${"9".repeat(26)}`).catch((err) => err);
  assert.ok(e instanceof NotFoundError);
  assert.equal(e.status, 404);
  assert.equal(e.code, "not_found");
  assert.equal(e.detail, "no such machine");
  assert.equal(e.retryAfter, null);
  assert.match(e.message, /^404 not_found: no such machine \(request req_\d+\)$/);
  assert.equal(e.body.type, "https://docs.wheelmachine.ai/errors/not_found");
});

test("retry-after is on the error, and an answer without problem details still has a code", async (t) => {
  const { api, make } = await setup(t);
  const client = make({ maxRetries: 0 });
  api.fault("GET", "^/v1/me$", { status: 503, code: "unavailable", retryAfter: 2.5 });
  assert.equal((await client.me().catch((e) => e)).retryAfter, 3);
  api.fault("GET", "^/v1/me$", { status: 502, raw: ["text/html", "<html>bad gateway</html>"] });
  const e = await client.me().catch((err) => err);
  assert.ok(e instanceof UnavailableError);
  assert.equal(e.code, "unavailable");
});

test("create sends only what was given", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create();
  const seen = api.requests("POST", "^/v1/machines$").at(-1);
  assert.equal(seen.body.length, 0);
  assert.equal(seen.headers["content-length"], "0");
  assert.match(seen.headers["idempotency-key"], /^[0-9a-f-]{36}$/);
  assert.equal(m.state, "running");
});

test("a machine is made and read back", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create({ name: "builder", region: "mumbai", labels: { team: "ml" }, wait_s: 30 });
  assert.deepEqual(api.requests("POST", "^/v1/machines$").at(-1).json(), { name: "builder", region: "mumbai", labels: { team: "ml" }, wait_s: 30 });
  assert.equal(m.name, "builder");
  assert.equal(m.ready_ms, 12.5);
  assert.equal(m.size.cpus, 2);
  assert.equal(m.location.country, "FI");
  assert.equal(m.lastResponse.status, 201);
  const again = await client.machines.get(m.id);
  assert.equal(again.id, m.id);
  assert.equal(again.ready_ms, undefined);
});

test("a machine pauses when idle unless it is made not to", async (t) => {
  const { api, client } = await setup(t);
  let m = await client.machines.create();
  assert.deepEqual([m.sleep_when_idle, m.idle_after_s], [true, 300]);
  m = await client.machines.create({ idle_after_s: 3600 });
  assert.deepEqual(api.requests("POST", "^/v1/machines$").at(-1).json(), { idle_after_s: 3600 });
  assert.equal(m.idle_after_s, 3600);
  m = await client.machines.create({ sleep_when_idle: false });
  assert.deepEqual(api.requests("POST", "^/v1/machines$").at(-1).json(), { sleep_when_idle: false });
  assert.deepEqual([m.sleep_when_idle, m.idle_after_s], [false, null]);
  assert.equal((await client.machines.get(m.id)).sleep_when_idle, false);
});

test("a machine is made at the size asked for", async (t) => {
  const { api, client } = await setup(t);
  let m = await client.machines.create();
  assert.deepEqual([m.cpus, m.memory_gb], [2, 4]);
  m = await client.machines.create({ cpus: 8 });
  assert.deepEqual(api.requests("POST", "^/v1/machines$").at(-1).json(), { cpus: 8 });
  assert.deepEqual([m.cpus, m.memory_gb, m.size.cpus], [8, 16, 8]);
  await assert.rejects(client.machines.create({ cpus: 3 }), (e) => e.code === "invalid_request");
  // 16 is not offered yet, in the API's own words
  await assert.rejects(client.machines.create({ cpus: 16 }), (e) => e.code === "invalid_request" && e.detail === "16 CPUs are not offered yet: choose 2, 4 or 8");
});

test("a machine still starting is followed with wait", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create({ wait_s: 0 });
  assert.equal(m.state, "starting");
  assert.equal(m.lastResponse.status, 202);
  const ready = await client.machines.wait(m.id, "running", { timeout_s: 30 });
  assert.equal(ready.state, "running");
  const q = api.requests("GET", "/wait$").at(-1).query;
  assert.equal(q.get("state"), "running");
  // the time left is sent: a moment after the wait began, a little under the 30 s asked for
  const left = Number(q.get("timeout_s"));
  assert.ok(left > 29 && left <= 30, `timeout_s ${q.get("timeout_s")}`);
});

test("long waits are asked for in steps one call can hold", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create();
  await client.machines.wait(m.id, "running", { timeout_s: 1500 });
  assert.equal(api.requests("GET", "/wait$").at(-1).query.get("timeout_s"), "240");
  // a create asked to wait longer than one call holds: sent with the longest step, then followed with wait
  api.startSlowly = true;
  const made = await client.machines.create({ wait_s: 500 });
  assert.equal(api.requests("POST", "^/v1/machines$").at(-1).json().wait_s, 240);
  assert.equal(made.state, "running");
  const followed = api.requests("GET", `^/v1/machines/${made.id}/wait$`);
  assert.equal(followed.length, 1);
  assert.equal(followed[0].query.get("timeout_s"), "240");
});

test("a state not reached is a wait timeout", async (t) => {
  const { client } = await setup(t);
  const m = await client.machines.create();
  const e = await client.machines.wait(m.id, "archived", { timeout_s: 0.2 }).catch((err) => err);
  assert.ok(e instanceof WaitTimeoutError);
  assert.equal(e.status, 408);
  assert.equal(e.code, "timeout");
});

test("the life of a machine", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create({ labels: { a: "1", b: "2" } });
  for (const [action, state] of [["pause", "paused"], ["start", "running"], ["stop", "stopped"], ["archive", "archived"]]) {
    assert.equal((await client.machines[action](m.id)).state, state, action);
  }
  await client.machines.start(m.id, { wait_s: 5 });
  assert.deepEqual(api.requests("POST", "/start$").at(-1).json(), { wait_s: 5 });
  const renamed = await client.machines.update(m.id, { name: "renamed", labels: { a: null, c: "3" } });
  assert.deepEqual(api.requests("PATCH", "^/v1/machines/").at(-1).json(), { name: "renamed", labels: { a: null, c: "3" } });
  assert.deepEqual([renamed.name, renamed.labels], ["renamed", { b: "2", c: "3" }]);
  const kept = await client.machines.backup(m.id, { note: "before" });
  assert.deepEqual([kept.object, kept.machine, kept.note, kept.lastResponse.status], ["backup", m.id, "before", 201]);
  assert.deepEqual(api.requests("POST", "/backups$").at(-1).json(), { note: "before" });
  assert.ok(api.requests("POST", "/backups$").at(-1).headers["idempotency-key"]);
  const fork = await client.machines.fork(m.id, { name: "the fork" });
  assert.notEqual(fork.id, m.id);
  assert.equal(fork.name, "the fork");
  assert.ok(api.requests("POST", "/fork$").at(-1).headers["idempotency-key"]);
  // copy, fork's name before it was one, is the same call, by the fork route
  assert.equal((await client.machines.copy(m.id, { name: "again" })).name, "again");
  assert.deepEqual(api.requests("POST", "/copy$"), []);
  // a fork from a backup starts afresh from its files, at the size asked
  const restored = await client.machines.fork(m.id, { backup: kept, cpus: 8 });
  assert.deepEqual(api.requests("POST", "/fork$").at(-1).json(), { backup: kept.id, cpus: 8 });
  assert.deepEqual([restored.object, restored.state, restored.cpus, restored.memory_gb], ["machine", "running", 8, 16]);
  assert.equal(await client.machines.delete(m.id), undefined);
  assert.equal((await client.machines.get(m.id)).state, "deleted");
  await client.machines.delete(m.id);
});

test("backups are listed newest first, and a fork is made from one", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create({ name: "box", cpus: 4 });
  const other = await client.machines.create({ name: "other" });
  const first = await client.machines.backup(m.id);
  assert.equal(api.requests("POST", "/backups$").at(-1).body.length, 0);
  const second = await client.machines.backup(m.id, { note: "after the build" });
  const theirs = await client.machines.backup(other.id);
  assert.deepEqual((await client.machines.backups(m.id)).map((b) => b.id), [second.id, first.id]);
  assert.deepEqual((await client.machines.backups()).map((b) => b.id), [theirs.id, second.id, first.id]);
  assert.equal([...api.requests("GET", "^/v1/backups$").at(-1).query].length, 0);
  assert.deepEqual((await client.machines.backups(m.id, { limit: 1 })).map((b) => b.note), ["after the build"]);
  assert.equal(api.requests("GET", "/backups$").at(-1).query.get("limit"), "1");
  assert.deepEqual((await client.machines.backups(undefined, { limit: 2 })).map((b) => b.id), [theirs.id, second.id]);
  // its newest, by name, at the machine's own size
  const fork = await client.machines.fork(m.id, { name: "from the build", backup: "latest" });
  assert.deepEqual(api.requests("POST", "/fork$").at(-1).json(), { name: "from the build", backup: "latest" });
  assert.deepEqual([fork.object, fork.name, fork.cpus], ["machine", "from the build", 4]);
  // what the API refuses: a size without a backup, another machine's backup, the newest of a machine with none
  await assert.rejects(client.machines.fork(m.id, { cpus: 8 }),
    (err) => err instanceof BadRequestError && /cpus is only for a fork from a backup/.test(err.message));
  await assert.rejects(client.machines.fork(m.id, { backup: theirs }), NotFoundError);
  await assert.rejects(client.machines.fork(fork.id, { backup: "latest" }),
    (err) => err instanceof ConflictError && /no backup of it yet/.test(err.message));
  // a deleted machine's backups go with it
  await client.machines.delete(other.id);
  assert.deepEqual(await client.machines.backups(other.id), []);
  assert.deepEqual((await client.machines.backups()).map((b) => b.id), [second.id, first.id]);
  await assert.rejects(client.machines.backups(""), TypeError);
});

test("an id goes in the path as one piece", async (t) => {
  const { api, client } = await setup(t);
  await assert.rejects(client.machines.get("../keys"), NotFoundError);
  assert.equal(api.seen.at(-1).path, "/v1/machines/..%2Fkeys");
  await assert.rejects(client.machines.get(""), TypeError);
});

test("every machine comes, page after page", async (t) => {
  const { api, client } = await setup(t);
  const made = Array.from({ length: 7 }, (_, i) => api.addMachine({ name: String(i) }).id);
  const got = [];
  for await (const m of client.machines.list({ limit: 3 })) got.push(m.id);
  assert.deepEqual(got, [...made].reverse());
  const pages = api.requests("GET", "^/v1/machines$");
  assert.deepEqual(pages.map((p) => p.query.get("after")), [null, made[4], made[1]]);
  assert.ok(pages.every((p) => p.query.get("limit") === "3"));
});

test("a page is asked for only when needed", async (t) => {
  const { api, client } = await setup(t);
  for (let i = 0; i < 7; i++) api.addMachine({ name: String(i) });
  let n = 0;
  for await (const m of client.machines.list({ limit: 2 })) {
    assert.ok(m.id);
    if (++n === 3) break;
  }
  assert.equal(api.requests("GET", "^/v1/machines$").length, 2);
});

test("filters go in the query", async (t) => {
  const { api, client } = await setup(t);
  const other = api.addProject("other").id;
  api.addMachine({ project: other, labels: { team: "ml", tier: "gpu" } });
  api.addMachine({ project: other, labels: { team: "web" } });
  api.addMachine({ labels: { team: "ml", tier: "gpu" } });
  const got = [];
  for await (const m of client.machines.list({ project: other, state: "running", labels: { team: "ml", tier: "gpu" } })) got.push(m);
  assert.equal(got.length, 1);
  const q = api.requests("GET", "^/v1/machines$").at(-1).query;
  assert.equal(q.get("project"), other);
  assert.equal(q.get("state"), "running");
  assert.deepEqual(q.getAll("label"), ["team=ml", "tier=gpu"]);
});

test("projects page too, and live their life", async (t) => {
  const { api, client } = await setup(t);
  for (const name of ["b", "c", "d", "e"]) api.addProject(name);
  const names = [];
  for await (const p of client.projects.list({ limit: 2 })) names.push(p.name);
  assert.deepEqual(names, ["Default", "b", "c", "d", "e"]);
  assert.equal(api.requests("GET", "^/v1/projects$").length, 3);
  const p = await client.projects.create({ name: "research" });
  assert.ok(api.requests("POST", "^/v1/projects$").at(-1).headers["idempotency-key"]);
  assert.equal((await client.projects.get(p.id)).name, "research");
  assert.equal((await client.projects.update(p.id, { name: "lab" })).name, "lab");
  await client.projects.delete(p.id);
  await assert.rejects(client.projects.get(p.id), NotFoundError);
  await client.machines.create();
  const e = await client.projects.delete(api.firstProject).catch((err) => err);
  assert.ok(e instanceof ConflictError);
  assert.equal(e.code, "project_not_empty");
});

test("a command runs and its output comes back", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create();
  const r = await client.machines.exec(m.id, ["echo", "hello"]);
  assert.deepEqual([r.exit_code, r.stdout, r.stderr, r.truncated, r.duration_ms], [0, "hello\n", "", false, 3.2]);
  assert.deepEqual(api.execs.at(-1), { command: ["echo", "hello"] });
  assert.equal(api.requests("POST", "/exec$").at(-1).headers["idempotency-key"], undefined);
  const s = await client.machines.exec(m.id, { shell: "ls | wc -l", env: { A: "1" }, cwd: "/tmp", user: "root" });
  assert.deepEqual(api.execs.at(-1), { shell: "ls | wc -l", env: { A: "1" }, cwd: "/tmp", user: "root" });
  assert.ok(s.stdout.includes("ls | wc -l"));
  const x = await client.machines.exec(m.id, ["exit", "3"]);
  assert.deepEqual([x.exit_code, x.stderr], [3, "bye\n"]);
  const b = await client.machines.exec(m.id, { command: ["cat", "blob"], binary_output: true });
  assert.deepEqual(api.execs.at(-1), { command: ["cat", "blob"], binary_output: true });
  assert.deepEqual([...Buffer.from(b.stdout_base64, "base64")], [0, 1, 255, ...Buffer.from("binary")]);
});

test("a command is the words or a shell line, one of the two", async (t) => {
  const { api, client } = await setup(t);
  const m = await client.machines.create();
  await assert.rejects(client.machines.exec(m.id, {}), TypeError);
  await assert.rejects(client.machines.exec(m.id, { command: ["ls"], shell: "ls" }), TypeError);
  await assert.rejects(client.machines.exec(m.id, { command: "ls -la" }), TypeError);
  assert.deepEqual(api.execs, []);
});

test("regions, GPUs and usage", async (t) => {
  const { api, client } = await setup(t);
  const regions = await client.regions();
  assert.deepEqual(regions[0], { id: "mumbai", city: "Mumbai", country: "IN" });
  const gpus = await client.gpus();
  assert.equal(gpus.for_sale, false);
  assert.deepEqual([gpus.data[0].id, gpus.data[0].model, gpus.data[0].price_per_hour_usd], ["h100x1", "H100 80GB", 2.53]);
  await client.machines.create();
  const usage = await client.usage({ month: new Date(Date.UTC(2026, 9, 15)), project: api.firstProject });
  const q = api.seen.at(-1).query;
  assert.deepEqual([q.get("month"), q.get("project")], ["2026-10", api.firstProject]);
  assert.deepEqual([usage.month, usage.machines.length, usage.total_usd], ["2026-10", 1, 0.01]);
});

test("limits, and the account's own set and taken away", async (t) => {
  const { api, client } = await setup(t);
  const none = await client.limits();
  assert.deepEqual([none.account.limited, none.account.month_cap_usd, none.alert_percents], [false, null, [50, 75, 90, 100]]);
  const set = await client.setMonthLimit(50);
  assert.deepEqual(api.seen.at(-1).json(), { month_cap: 50 });
  assert.deepEqual([set.account.month_cap_usd, set.account.in_force, set.account.percent, set.yours[0].scope], [50, "account", 25, "account"]);
  await client.setMonthLimit(null);
  assert.deepEqual(api.seen.at(-1).json(), { month_cap: null });
});

test("keys, and the secret shown once", async (t) => {
  const { api, client } = await setup(t);
  const made = await client.keys.create({
    name: "ci",
    scopes: ["cloud:machines:read"],
    expires_at: new Date("2026-12-31T23:59:59+05:30"),
    rate_per_minute: 60,
  });
  assert.deepEqual(api.requests("POST", "^/v1/keys$").at(-1).json(), {
    name: "ci",
    scopes: ["cloud:machines:read"],
    expires_at: "2026-12-31T18:29:59Z",
    rate_per_minute: 60,
  });
  assert.equal(made.secret, "ak_live_" + "s".repeat(40));
  assert.deepEqual((await client.keys.list()).map((k) => k.id), [made.id]);
  await client.keys.revoke(made.id);
  assert.deepEqual(await client.keys.list(), []);
});

test("folders: a file of My files put, listed, read, moved and deleted to the Trash", async (t) => {
  const { api, client } = await setup(t);
  const folders = await client.folders();
  assert.deepEqual([folders.on, folders.data.map((f) => [f.id, f.name, f.access])], [true, [["fld_own", "My files", "edit"]]]);
  const e = await client.writeFolderFile("fld_own", "notes/plan.md", new TextEncoder().encode("the plan"));
  assert.deepEqual([e.name, e.size, e.changed_by?.name], ["plan.md", 8, "Ada"]);
  assert.equal(api.seen.at(-1).query.get("path"), "notes/plan.md");
  assert.deepEqual((await client.listFolder("fld_own")).entries.map((x) => x.name), ["notes/plan.md"]);
  // a page at a time: each page asked for from the `next` of the one before
  await client.writeFolderFile("fld_own", "notes/zz.md", new TextEncoder().encode("z"));
  const first = await client.listFolder("fld_own", "", { limit: 1 });
  assert.deepEqual([first.entries.map((x) => x.name), first.next], [["notes/plan.md"], "1"]);
  const all = [];
  for await (const x of client.folderEntries("fld_own", "", { limit: 1 })) all.push(x.name);
  assert.deepEqual(all, ["notes/plan.md", "notes/zz.md"]);
  assert.equal(api.seen.at(-1).query.get("after"), "1");
  await client.deleteFolderFile("fld_own", "notes/zz.md");
  assert.equal(new TextDecoder().decode(await client.readFolderFile("fld_own", "notes/plan.md")), "the plan");
  await client.moveFolderItem("fld_own", "notes/plan.md", "plan.md");
  const gone = await client.deleteFolderFile("fld_own", "plan.md");
  assert.deepEqual([gone.path, gone.goes_at], ["plan.md", "2026-12-04T10:00:00Z"]);
  assert.equal((await client.folders()).data[0].bytes, 0);
});

test("code: the code hosts, what one reaches, and a team given a repository", async (t) => {
  const { api, client } = await setup(t);
  const code = await client.code();
  assert.deepEqual([code.github, code.connections.map((c) => c.name)], [true, ["acme"]]);
  assert.deepEqual(await client.codeRepos("code_1"), ["acme/api"]);
  const g = await client.grantCode("prj_00000000000000000000000001", "code_1", "acme/api", "write");
  assert.deepEqual([g.repo, g.access], ["acme/api", "write"]);
  assert.deepEqual(api.seen.at(-1).json(), { connection: "code_1", repo: "acme/api", access: "write" });
});
