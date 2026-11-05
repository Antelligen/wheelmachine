// A machine's apps: listed, turned on and off, and fresh one-time links into the web ones. A link and an app's own
// token are secrets: they are tested, never printed.

import assert from "node:assert/strict";
import { test } from "node:test";

import { ConflictError, NotFoundError, UnavailableError } from "../dist/index.js";
import { APP_TOKEN, setup } from "./fake-api.mjs";

async function rig(t) {
  const s = await setup(t);
  const machine = await s.client.machines.create();
  return { ...s, machine };
}

const LINK = /^https:\/\/6901-[0-9a-z]{26}\.run\.wheelmachine\.ai\/\.wheelmachine\/link\/[A-Za-z0-9_-]{20,}$/;

test("the apps are listed as the machine says them", async (t) => {
  const { api, client, machine } = await rig(t);
  const apps = await client.apps.list(machine.id);
  assert.equal(api.seen.at(-1).path, `/v1/machines/${machine.id}/apps`);
  const by = Object.fromEntries(apps.map((a) => [a.name, a]));
  assert.deepEqual(
    { ...by.desktop },
    {
      object: "app",
      machine: machine.id,
      name: "desktop",
      title: "Desktop",
      kind: "web",
      port: 6901,
      path: "/vnc.html?autoconnect=1&resize=remote&reconnect=1",
      on: false,
      running: false,
      command: null,
      url: `https://6901-${machine.id.slice(2)}.run.wheelmachine.ai`,
    },
  );
  assert.deepEqual([by.ollama.kind, by.ollama.port, by.ollama.url], ["service", null, null]);
  assert.deepEqual([by.codex.kind, by.codex.command, by.codex.on, by.codex.running], ["agent", "codex", true, false]);
  assert.deepEqual([by.gemini.kind, by.gemini.command, by.gemini.on], ["terminal", "gemini", null]);
});

test("an agent started before the machine's AI keys changed says so", async (t) => {
  const { api, client, machine } = await rig(t);
  const listed = async () => Object.fromEntries((await client.apps.list(machine.id)).map((a) => [a.name, a]));
  assert.equal("keys_changed" in (await listed()).codex, false); // (a machine that does not say it)
  Object.assign(api.apps.get(machine.id).get("codex"), { running: true, keys_changed: true });
  const by = await listed();
  assert.equal(by.codex.keys_changed, true);
  assert.equal("keys_changed" in by.desktop, false);
});

test("a web app turned on answers a link into its private address; turned off, it takes the port it opened", async (t) => {
  const { api, client, machine } = await rig(t);
  const on = await client.apps.turnOn(machine.id, "desktop", { access_s: 600 });
  const sent = api.requests("POST", "/apps/desktop/on$").at(-1);
  assert.deepEqual(sent.json(), { access_s: 600 });
  assert.equal(sent.headers["idempotency-key"], undefined);
  assert.equal(on.lastResponse.status, 200);
  assert.deepEqual([on.on, on.running, on.access_s, on.token], [true, true, 600, undefined]);
  assert.match(on.link, LINK);
  assert.deepEqual((await client.ports.list(machine.id)).map((p) => [p.port, p.public]), [[6901, false]]);
  const off = await client.apps.turnOff(machine.id, "desktop");
  assert.equal(api.requests("POST", "/apps/desktop/off$").at(-1).body.length, 0);
  assert.deepEqual([off.on, off.link], [false, undefined]);
  assert.deepEqual(await client.ports.list(machine.id), []);
  // a service opens nothing, and its turning on carries no body when nothing is asked
  const ollama = await client.apps.turnOn(machine.id, "ollama");
  assert.equal(api.requests("POST", "/apps/ollama/on$").at(-1).body.length, 0);
  assert.deepEqual([ollama.on, ollama.link], [true, undefined]);
  assert.deepEqual(await client.ports.list(machine.id), []);
});

test("an app's own token comes in the answer to turning it on, and in no other", async (t) => {
  const { client, machine } = await rig(t);
  const on = await client.apps.turnOn(machine.id, "openclaw");
  assert.equal(on.token, APP_TOKEN);
  const linked = await client.apps.link(machine.id, "openclaw");
  assert.equal(linked.lastResponse.status, 201);
  assert.equal(linked.token, undefined);
  assert.match(linked.link, /^https:\/\/18789-/);
});

test("a turning off whose answer was lost is sent again; a turning on or a link never is", async (t) => {
  const { api, client, machine, sleeps } = await rig(t);
  api.fault("POST", "/off$", { drop: "after" });
  assert.equal((await client.apps.turnOff(machine.id, "desktop")).on, false);
  assert.equal(api.requests("POST", "/off$").length, 2);
  for (const [call, path] of [
    [() => client.apps.turnOn(machine.id, "desktop"), "/on$"],
    [() => client.apps.link(machine.id, "desktop"), "/link$"],
  ]) {
    api.fault("POST", path, { status: 503, code: "unavailable", retryAfter: 1 });
    await assert.rejects(call(), UnavailableError);
    assert.equal(api.requests("POST", path).length, 1);
  }
  assert.equal(sleeps.length, 1);
});

test("a link is made for a web app that is on, and for no other app", async (t) => {
  const { client, machine } = await rig(t);
  await assert.rejects(client.apps.link(machine.id, "editor"), (e) => e instanceof ConflictError && e.code === "conflict");
  await client.apps.turnOn(machine.id, "editor");
  assert.match((await client.apps.link(machine.id, "editor")).link, /^https:\/\/13337-/);
  await assert.rejects(client.apps.link(machine.id, "ollama"), (e) => e.status === 400);
});

test("a machine with no apps is told so, and an app it does not have is not found", async (t) => {
  const { api, client, machine, sleeps } = await rig(t);
  await assert.rejects(client.apps.turnOn(machine.id, "nope"), (e) => e instanceof NotFoundError && e.detail === "no such app in this machine");
  api.oldMachines.add(machine.id);
  for (const call of [
    () => client.apps.list(machine.id),
    () => client.apps.turnOn(machine.id, "desktop"),
    () => client.apps.turnOff(machine.id, "desktop"),
    () => client.apps.link(machine.id, "desktop"),
  ]) {
    await assert.rejects(call(), (e) => e instanceof ConflictError && e.status === 409 && e.code === "no_apps" && /has no apps/.test(e.detail));
  }
  assert.deepEqual(sleeps, []);
});

test("what is not an app's name is refused before anything is sent", async (t) => {
  const { api, client, machine } = await rig(t);
  const before = api.seen.length;
  for (const bad of ["Desktop", "desk top", "../keys", "", "a".repeat(33), 7]) {
    await assert.rejects(client.apps.turnOn(machine.id, bad), TypeError);
  }
  await assert.rejects(client.apps.list(""), TypeError);
  assert.equal(api.seen.length, before);
});

test("an app is shown on the machine's screen, once it is on", async (t) => {
  const { api, client, machine } = await rig(t);
  await assert.rejects(client.apps.show(machine.id, "openclaw"), (e) => e instanceof ConflictError && /is off/.test(e.message));
  await client.apps.turnOn(machine.id, "openclaw");
  const shown = await client.apps.show(machine.id, "openclaw");
  const sent = api.requests("POST", "/apps/openclaw/show$").at(-1);
  assert.equal(sent.body.length, 0);
  assert.deepEqual([shown.name, shown.on, shown.link, shown.token], ["openclaw", true, undefined, undefined]);
  await assert.rejects(client.apps.show(machine.id, "Not An App"), TypeError);
});
