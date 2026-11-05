// Organisations and the audit: who a key's person is and what they may do in an organisation, the organisations they
// are in, who made each key and machine there and whose each machine is, and the audit - read page after page, narrowed as the API takes it, and
// exported as a file, whole.

import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { BadRequestError, DeviceLogin, PermissionDeniedError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

const ADA = { id: "acc_0000000000000000000000ada1", name: "Ada Lovelace", email: null, github_login: "ada" };
const ACME = { id: "acc_00000000000000000000000077", name: "Acme", person: ADA, role: "member", teams: ["prj_00000000000000000000000001"],
  limits: { max_machines: 5, max_vcpus: null, gpus: false, month_cap_usd: 200 } };

/** n events of the audit, the newest first, a minute apart. */
function events(n) {
  const base = Date.parse("2026-10-23T11:00:00Z");
  return Array.from({ length: n }, (_, i) => ({ id: String(n - i), object: "audit_event",
    at: new Date(base - i * 60_000).toISOString().replace(/\.\d{3}Z$/, "Z"), action: "machine.create", target: "m_" + "1".padStart(26, "0"),
    person: { id: ADA.id, name: ADA.name }, by: "key_00000000000000a1", address: "203.0.113.7", request_id: `req_${i}`, detail: {} }));
}

test("me in a person's own account, and in an organisation", async (t) => {
  const { api, client } = await setup(t);
  let me = await client.me();
  assert.deepEqual([me.account.kind, me.role, me.teams, me.limits, me.scopes], ["person", "owner", null, null, ["cloud:*"]]);
  api.org = { ...ACME };
  me = await client.me();
  assert.deepEqual(me.account, { id: ACME.id, kind: "org", name: "Acme" });
  assert.deepEqual([me.person, me.role, me.teams, me.limits], [ADA, "member", ACME.teams, ACME.limits]);
});

test("the organisations of a key's person, and none for an organisation's key", async (t) => {
  const { api, client } = await setup(t);
  assert.deepEqual(await client.orgs.list(), []);
  api.orgs = [{ id: ACME.id, object: "org", name: "Acme", role: "owner", primary_owner: true, created_at: "2026-10-01T00:00:00Z", people: 3 }];
  assert.deepEqual(await client.orgs.list(), api.orgs);
  api.org = { ...ACME };
  await assert.rejects(client.orgs.list(), (err) => err instanceof PermissionDeniedError && /does not list the other organisations/.test(err.message));
});

test("keys, machines and usage say who made them, and whose they are, in an organisation", async (t) => {
  const { api, client } = await setup(t);
  const own = api.addMachine({ name: "web" });
  assert.equal((await client.machines.get(own.id)).made_by, null);
  assert.equal((await client.machines.get(own.id)).held_by, null);
  api.org = { ...ACME };
  const box = api.addMachine({ name: "box" });
  assert.equal((await client.machines.get(box.id)).made_by, ADA.id);
  assert.deepEqual((await client.machines.get(box.id)).held_by, { id: ADA.id, name: "Ada Lovelace", role: "member", level: null });
  assert.equal((await client.keys.create({ name: "ci", scopes: ["cloud:machines:read"] })).made_by, ADA.id);
  const usage = await client.usage();
  assert.deepEqual(Object.fromEntries(usage.machines.map((l) => [l.machine, l.made_by])), { [own.id]: null, [box.id]: ADA.id });
  assert.deepEqual(Object.fromEntries(usage.machines.map((l) => [l.machine, [l.name, l.held_by?.name ?? null]])),
    { [own.id]: ["web", null], [box.id]: ["box", "Ada Lovelace"] });
});

test("a program asks for its key in an organisation", async (t) => {
  const { api } = await setup(t);
  await DeviceLogin.start({ baseURL: api.url, name: "tool", account: ACME.id });
  assert.deepEqual(api.requests("POST", "/v1/login/devices").at(-1).json(), { name: "tool", account: ACME.id });
  const before = api.seen.length;
  for (const bad of ["Acme", "acc_123", "ACC_00000000000000000000000077"]) {
    await assert.rejects(DeviceLogin.start({ baseURL: api.url, account: bad }), TypeError);
  }
  assert.equal(api.seen.length, before, "an account that is not one is refused before any call");
});

test("the audit comes newest first, page after page", async (t) => {
  const { api, client } = await setup(t);
  api.audit = events(5);
  Object.assign(api.audit[1], { person: null, by: "system", address: "" });
  const got = [];
  for await (const e of client.audit.list({ limit: 2 })) got.push(e);
  assert.deepEqual(got.map((e) => e.id), ["5", "4", "3", "2", "1"]);
  assert.deepEqual([got[1].person, got[1].by], [null, "system"]);
  assert.deepEqual(api.requests("GET", "/v1/audit$").map((s) => s.query.get("after")), [null, "4", "2"]);
});

test("the audit is narrowed by person, action and time", async (t) => {
  const { api, client } = await setup(t);
  api.audit = events(3);
  const asked = () => Object.fromEntries(api.requests("GET", "/v1/audit$").at(-1).query);
  const ids = async (params) => {
    const out = [];
    for await (const e of client.audit.list(params)) out.push(e.id);
    return out;
  };
  await ids({ person: ADA.id, action: "machine.", since: "2026-10-23T10:59:00Z", until: new Date(Date.UTC(2026, 9, 23, 11, 0, 0)) });
  assert.deepEqual(asked(), { person: ADA.id, action: "machine.", since: "2026-10-23T10:59:00Z", until: "2026-10-23T11:00:00Z" });
  assert.deepEqual(await ids({ since: "2026-10-23T10:59:00Z" }), ["3", "2"]);
  await assert.rejects(ids({ since: new Date("not a date") }), TypeError);
  await assert.rejects(ids({ since: "yesterday" }), BadRequestError);
});

test("owners and admins alone read the audit", async (t) => {
  const { api, client } = await setup(t);
  api.org = { ...ACME, role: "admin" };
  for await (const e of client.audit.list()) assert.fail(`no events: ${e.id}`);
  api.org = { ...ACME, role: "member" };
  await assert.rejects(client.audit.list()[Symbol.asyncIterator]().next(), PermissionDeniedError);
  await assert.rejects(client.audit.export(), PermissionDeniedError);
});

test("the audit is exported as a file, whole", async (t) => {
  const { api, client } = await setup(t);
  api.audit = events(2);
  api.audit[0].person.name = "=2+3";
  api.audit[0].detail = { user: "root", app: "desktop" };
  const csv = await client.audit.export();
  assert.deepEqual(new TextDecoder().decode(csv).split("\r\n").slice(0, 2), [
    '"id","at","action","target","person","person_name","by","address","request_id","detail"',
    `"2","2026-10-23T11:00:00Z","machine.create","m_00000000000000000000000001","${ADA.id}","'=2+3","key_00000000000000a1","203.0.113.7","req_0","app=desktop; user=root"`,
  ]);
  const seen = api.requests("GET", "/v1/audit/export$").at(-1);
  assert.deepEqual([Object.fromEntries(seen.query), seen.headers.accept], [{ format: "csv" }, "text/csv"]);

  const json = JSON.parse(new TextDecoder().decode(await client.audit.export({ format: "json", action: "machine.", since: new Date(Date.UTC(2026, 9, 23, 10, 59, 30)) })));
  assert.deepEqual(json.map((e) => e.id), ["2"]);
  assert.deepEqual(Object.fromEntries(api.requests("GET", "/v1/audit/export$").at(-1).query), { format: "json", action: "machine.", since: "2026-10-23T10:59:30Z" });

  const folder = await mkdtemp(join(tmpdir(), "wm-audit-"));
  t.after(() => rm(folder, { recursive: true, force: true }));
  const path = join(folder, "audit.csv");
  assert.equal(await client.audit.export({}, { to: path }), csv.byteLength);
  assert.deepEqual(new Uint8Array(await readFile(path)), csv);
  if (process.platform !== "win32") assert.equal((await stat(path)).mode & 0o777, 0o600, "a new file of the audit is its owner's alone");
  const parts = [];
  const sink = new WritableStream({ write: (piece) => void parts.push(piece) });
  assert.equal(await client.audit.export({}, { to: sink }), csv.byteLength);
  assert.deepEqual(new Uint8Array(Buffer.concat(parts)), csv);

  await assert.rejects(client.audit.export({ since: "2025-01-01T00:00:00Z" }), (err) => err instanceof BadRequestError && /a year at most/.test(err.message));
  const before = api.seen.length;
  await assert.rejects(client.audit.export({ format: "xml" }), TypeError);
  assert.equal(api.seen.length, before);
});
