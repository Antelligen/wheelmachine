// Machines given to a teammate in an organisation: forked for them or handed off to them - made with a key, each waits for the
// OK in the dashboard of the person who made the key, then is done or waits for whoever decides it, followed and taken back - a member's
// own fork, which waits for a full manager, asks of a teammate for one of their machines, and a team's rules. Each
// is a create: a retry after a lost answer never makes two.

import assert from "node:assert/strict";
import { test } from "node:test";

import { ConflictError, NotFoundError, PermissionDeniedError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

const ADA = { id: "acc_0000000000000000000000ada1", name: "Ada Lovelace", email: null, github_login: "ada" };
const RAVI = { id: "acc_00000000000000000000000ra1", name: "Ravi", email: "ravi@example.com", github_login: "ravi" };
const ACME = { id: "acc_00000000000000000000000077", name: "Acme", person: ADA, role: "member", teams: ["prj_00000000000000000000000001"], limits: null };

async function inAcme(t) {
  const got = await setup(t);
  got.api.org = { ...ACME };
  got.api.people.set(RAVI.id, { ...RAVI });
  got.box = got.api.addMachine({ name: "box", labels: { env: "dev" } });
  return got;
}

test("a machine forked for a teammate, and handed off to them", async (t) => {
  const { api, client, box } = await inAcme(t);
  const q = await client.machines.forkFor(box.id, { to: "ravi@example.com", why: "for the demo" });
  // made with a key: the OK of the person who made the key first, given signed in to the dashboard
  assert.deepEqual([q.kind, q.way, q.state, q.receiver.name, q.holder.name, q.why, q.machine, q.waiting_on, q.result], ["fork", "give", "waiting",
    "Ravi", "Ada Lovelace", "for the demo", { id: box.id, name: "box" }, { who: "asker", name: "Ada Lovelace" }, null]);
  assert.equal(q.lastResponse.status, 201);
  const sent = api.requests("POST", "/fork-for$").at(-1);
  assert.deepEqual(sent.json(), { to: "ravi@example.com", why: "for the demo" });
  assert.match(sent.headers["idempotency-key"], /^[0-9a-f-]{36}$/, "a request is a create: it carries an Idempotency-Key");
  api.approve(q.id);
  const done = await client.requests.get(q.id);
  assert.deepEqual([done.state, done.waiting_on, done.keys_revoked], ["done", null, 0]);
  const fork = await client.machines.get(done.result);
  assert.deepEqual([fork.name, fork.held_by.name, fork.origin.kind, fork.origin.from.name, fork.origin.machine_name],
    ["box-fork", "Ravi", "fork", "Ada Lovelace", "box"]);

  const handed = await client.machines.handOff(box.id, { to: "ravi", team: api.firstProject });
  assert.deepEqual([handed.kind, handed.state, handed.to_team, handed.waiting_on.who], ["hand_off", "waiting", api.firstProject, "asker"]);
  assert.deepEqual(api.requests("POST", "/hand-off$").at(-1).json(), { to: "ravi", team: api.firstProject });
  api.approve(handed.id);
  assert.equal((await client.requests.get(handed.id)).state, "done");
  const now = await client.machines.get(box.id);
  // a hand-off arrives paused, its memory kept
  assert.deepEqual([now.held_by.id, now.origin.kind, now.origin.from.id, now.state], [RAVI.id, "hand_off", ADA.id, "paused"]);
});

test("a machine handed off is taken back, and given back", async (t) => {
  const { api, client, box } = await inAcme(t);
  const handed = await client.machines.handOff(box.id, { to: "ravi" });
  api.approve(handed.id);
  assert.deepEqual((await client.machines.get(box.id)).given_by, { id: ADA.id, name: "Ada Lovelace" });
  // taken back by the one who handed it off, with a key: the OK of the person who made the key first, then back as it is
  const back = await client.machines.takeBack(box.id);
  assert.deepEqual([back.kind, back.way, back.state, back.waiting_on.who, back.receiver.id, back.lastResponse.status],
    ["hand_off", "back", "waiting", "asker", ADA.id, 201]);
  const sent = api.requests("POST", "/take-back$").at(-1);
  assert.equal(sent.json(), undefined, "nothing named: no body");
  assert.match(sent.headers["idempotency-key"], /^[0-9a-f-]{36}$/);
  api.approve(back.id);
  const now = await client.machines.get(box.id);
  assert.deepEqual([now.held_by.id, now.given_by, now.origin, now.state], [ADA.id, null, null, "paused"]);
  await assert.rejects(client.machines.giveBack(box.id, { team: api.firstProject }),
    (err) => err instanceof ConflictError && /nobody to give it back to/.test(err.message));
  assert.deepEqual(api.requests("POST", "/give-back$").at(-1).json(), { team: api.firstProject });
});

test("what the API refuses of a request is said in its words", async (t) => {
  const { api, client, box } = await inAcme(t);
  await assert.rejects(client.machines.handOff(box.id, { to: ADA.id }), (err) => err instanceof ConflictError && /theirs already/.test(err.message));
  await assert.rejects(client.machines.forkFor(box.id, { to: "nobody@example.com" }), NotFoundError);
  api.rules.set(api.firstProject, { forks: "off" });
  await assert.rejects(client.machines.forkFor(box.id, { to: RAVI.id }), (err) => err instanceof PermissionDeniedError && /Forks: Off in Default/.test(err.message));
  const before = api.seen.length;
  await assert.rejects(client.machines.forkFor(box.id, { to: "" }), TypeError);
  await assert.rejects(client.machines.handOff(box.id, {}), TypeError);
  assert.equal(api.seen.length, before, "a request to nobody is refused before any call");
  api.org = null;
  await assert.rejects(client.machines.handOff(box.id, { to: RAVI.id }), (err) => err instanceof ConflictError && /Only an organisation's machines/.test(err.message));
});

test("a request that waits is followed, and taken back", async (t) => {
  const { api, client, box } = await inAcme(t);
  api.givesWait = true;
  const made = await client.machines.forkFor(box.id, { to: RAVI.id });
  api.approve(made.id);
  const q = await client.requests.get(made.id);
  assert.deepEqual([q.state, q.waiting_on, q.result], ["waiting", { who: "manager", name: null }, null]);
  const open = [];
  for await (const r of client.requests.list({ open: true, machine: box.id })) open.push(r.id);
  assert.deepEqual(open, [q.id]);
  const asked = api.requests("GET", "/v1/requests$").at(-1).query;
  assert.deepEqual([asked.get("open"), asked.get("machine")], ["true", box.id]);
  // the same give again, while it waits, is the one there is
  assert.equal((await client.machines.forkFor(box.id, { to: RAVI.id })).id, q.id);
  assert.equal((await client.requests.takeBack(q.id)).state, "taken_back");
  // sent again - after its answer was lost, say - it is answered as it is
  assert.equal((await client.requests.takeBack(q.id)).state, "taken_back");
  const still = [];
  for await (const r of client.requests.list({ open: true })) still.push(r.id);
  assert.deepEqual(still, []);
  await assert.rejects(client.requests.get("rq_00000000000000000000000000"), NotFoundError);
});

test("a request whose answer was lost is sent again, and makes one", async (t) => {
  const { api, client, box } = await inAcme(t);
  api.fault("POST", "/fork-for$", { drop: "after" });
  const q = await client.machines.forkFor(box.id, { to: RAVI.id });
  assert.equal(api.requests("POST", "/fork-for$").length, 2);
  assert.equal(q.lastResponse.replayed, true, "the second send is the first one's answer, kept");
  assert.equal(api.machineRequests.size, 1);
  api.approve(q.id);
  assert.equal([...api.machines.values()].filter((m) => m.name === "box-fork").length, 1);
});

test("a member's fork waits for a full manager", async (t) => {
  const { api, client, box } = await inAcme(t);
  const q = await client.machines.fork(box.id, { name: "box-2", labels: { stage: "try" } });
  // nothing made: the request, theirs to them, waiting first on the OK of the person who made the key, as one asked with a key does
  assert.deepEqual([q.object, q.kind, q.way, q.state, q.holder.id, q.receiver.id, q.asked_by.id, q.machine, q.waiting_on, q.result],
    ["request", "fork", "give", "waiting", ADA.id, ADA.id, ADA.id, { id: box.id, name: "box" }, { who: "asker", name: "Ada Lovelace" }, null]);
  assert.equal(q.lastResponse.status, 202);
  // of the machine as it is: no backup, and its own size
  assert.deepEqual([q.backup, q.cpus], [null, null]);
  assert.deepEqual(api.requests("POST", "/fork$").at(-1).json(), { name: "box-2", labels: { stage: "try" } });
  const listed = [];
  for await (const m of client.machines.list()) listed.push(m.id);
  assert.deepEqual(listed, [box.id]);
  // asked again while it waits: the one there is
  const again = await client.machines.fork(box.id);
  assert.deepEqual([again.id, again.lastResponse.status], [q.id, 200]);
  // the OK of the person who made the key given in the dashboard, it waits on a full manager
  api.approve(q.id);
  const waiting = await client.requests.get(q.id);
  assert.deepEqual([waiting.state, waiting.waiting_on, waiting.result], ["waiting", { who: "manager", name: null }, null]);
  // where forks are Off, a member's fork is refused
  api.rules.set(api.firstProject, { forks: "off" });
  await assert.rejects(client.machines.fork(box.id), (err) => err instanceof PermissionDeniedError && /Forks: Off in Default/.test(err.message));
});

test("a member's fork from a backup waits too, and says which and how big", async (t) => {
  const { api, client, box } = await inAcme(t);
  await client.machines.backup(box.id, { note: "older" });
  const newest = await client.machines.backup(box.id, { note: "newest" });
  assert.deepEqual((await client.machines.backups(box.id)).map((b) => b.note), ["newest", "older"]);
  const q = await client.machines.fork(box.id, { name: "box-big", backup: "latest", cpus: 8 });
  // nothing made: the request, saying the backup it is made from - the newest, by its id - and the size asked
  assert.deepEqual([q.object, q.kind, q.way, q.state, q.backup, q.cpus, q.waiting_on, q.result, q.lastResponse.status],
    ["request", "fork", "give", "waiting", newest.id, 8, { who: "asker", name: "Ada Lovelace" }, null, 202]);
  assert.deepEqual(api.requests("POST", "/fork$").at(-1).json(), { name: "box-big", backup: "latest", cpus: 8 });
  const listed = [];
  for await (const m of client.machines.list()) listed.push(m.id);
  assert.deepEqual(listed, [box.id]);
  // the OK of the person who made the key given in the dashboard, it waits on a full manager, as it was asked
  api.approve(q.id);
  const waiting = await client.requests.get(q.id);
  assert.deepEqual([waiting.state, waiting.waiting_on, waiting.backup, waiting.cpus], ["waiting", { who: "manager", name: null }, newest.id, 8]);
});

test("a full manager's own fork, and an owner's, are made at once", async (t) => {
  const { api, client, box } = await inAcme(t);
  api.org = { ...ACME, levels: { [api.firstProject]: "full" } };
  const fork = await client.machines.fork(box.id, { name: "box-2" });
  assert.deepEqual([fork.object, fork.name, fork.state, fork.lastResponse.status], ["machine", "box-2", "running", 201]);
  api.org = { ...ACME, role: "owner", teams: null };
  assert.equal((await client.machines.fork(box.id)).object, "machine");
  assert.equal(api.machineRequests.size, 0);
});

test("a teammate asked for one of their machines", async (t) => {
  const { api, client } = await inAcme(t);
  const q = await client.requests.ask({ kind: "fork", of: RAVI.id, why: "the demo box" });
  assert.deepEqual([q.way, q.kind, q.state, q.machine, q.holder.name, q.waiting_on], ["ask", "fork", "waiting", null, "Ravi",
    { who: "asker", name: "Ada Lovelace" }]);
  api.approve(q.id);
  assert.deepEqual((await client.requests.get(q.id)).waiting_on, { who: "holder", name: "Ravi" });
  assert.deepEqual(api.requests("POST", "/v1/requests$").at(-1).json(), { kind: "fork", of: RAVI.id, why: "the demo box" });
  const again = await client.requests.ask({ kind: "fork", of: "ravi@example.com" });
  assert.deepEqual([again.id, again.lastResponse.status], [q.id, 200]);
  assert.equal((await client.requests.ask({ kind: "hand_off", of: "ravi" })).kind, "hand_off");
  const before = api.seen.length;
  await assert.rejects(client.requests.ask({ kind: "copy", of: RAVI.id }), TypeError);
  await assert.rejects(client.requests.ask({ kind: "fork", of: "" }), TypeError);
  assert.equal(api.seen.length, before);
  await assert.rejects(client.requests.ask({ kind: "fork", of: "nobody" }), NotFoundError);
});

test("a team's rules, and the managers' levels", async (t) => {
  const { api, client } = await inAcme(t);
  const rules = await client.projects.rules(api.firstProject);
  assert.deepEqual([rules.hand_offs, rules.forks, rules.gpus, rules.open_internet, rules.month_budget_usd, rules.models, rules.set_by],
    ["ask", "ask", true, true, null, "any", null]);
  // (forks are asked for or off, never free; hand-offs may be free)
  api.rules.set(api.firstProject, { hand_offs: "free", forks: "off", gpus: false, month_budget_usd: 50, spent_usd: 12.5, open_machines: 2,
    set_by: { id: ADA.id, name: "Ada Lovelace" }, set_at: "2026-10-27T07:00:00Z" });
  const set = await client.projects.rules(api.firstProject);
  assert.deepEqual([set.hand_offs, set.forks, set.gpus, set.month_budget_usd, set.spent_usd, set.open_machines, set.set_by.name],
    ["free", "off", false, 50, 12.5, 2, "Ada Lovelace"]);
  // to one of its members, what its machines came to is not said
  api.rules.set(api.firstProject, { spent_usd: null, open_machines: null });
  const member = await client.projects.rules(api.firstProject);
  assert.deepEqual([member.spent_usd, member.open_machines], [null, null]);
  await assert.rejects(client.projects.rules("prj_00000000000000000000000999"), NotFoundError);
  assert.deepEqual((await client.me()).levels, {});
  api.org = { ...ACME, levels: { [api.firstProject]: "full" } };
  assert.deepEqual((await client.me()).levels, { [api.firstProject]: "full" });
  api.org = null;
  await assert.rejects(client.projects.rules(api.firstProject), ConflictError);
});
