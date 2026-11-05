// A machine's ports at web addresses of their own: opened again safely, a link made once, a port checked before
// anything is sent. A link's url is a secret: it is tested, never printed.

import assert from "node:assert/strict";
import { test } from "node:test";

import { APIConnectionError, RateLimitError, UnavailableError } from "../dist/index.js";
import { closedPort, setup } from "./fake-api.mjs";

async function rig(t) {
  const s = await setup(t);
  const machine = await s.client.machines.create();
  return { ...s, machine };
}

const opens = (api) => api.requests("POST", "/ports$");
const links = (api) => api.requests("POST", "/ports/[0-9]+/link$");
const LINK = /^https:\/\/8080-[0-9a-z]{26}\.run\.wheelmachine\.ai\/\.wheelmachine\/link\/[A-Za-z0-9_-]{20,}$/;

test("a port opens private at a web address of its own; opened again, it is made public or private as asked", async (t) => {
  const { api, client, machine } = await rig(t);
  const port = await client.ports.open(machine.id, 8080);
  assert.deepEqual(opens(api).at(-1).json(), { port: 8080 });
  assert.equal(opens(api).at(-1).headers["idempotency-key"], undefined);
  assert.deepEqual(
    { ...port },
    {
      object: "port",
      machine: machine.id,
      port: 8080,
      public: false,
      url: `https://8080-${machine.id.slice(2)}.run.wheelmachine.ai`,
      created_at: "2026-10-23T07:00:00Z",
    },
  );
  assert.equal(port.lastResponse.status, 201);
  const again = await client.ports.open(machine.id, 8080, { public: true });
  assert.deepEqual(opens(api).at(-1).json(), { port: 8080, public: true });
  assert.deepEqual([again.public, again.url, again.lastResponse.status], [true, port.url, 200]);
  const back = await client.ports.open(machine.id, 8080, { public: false });
  assert.deepEqual(opens(api).at(-1).json(), { port: 8080, public: false });
  assert.deepEqual([back.public, back.lastResponse.status], [false, 200]);
  assert.equal(api.openPorts.size, 1);
});

test("open ports come lowest first, and a port is closed whether it is open or not", async (t) => {
  const { api, client, machine } = await rig(t);
  for (const port of [8080, 3000, 1, 65535]) await client.ports.open(machine.id, port);
  const other = await client.machines.create();
  await client.ports.open(other.id, 5000);
  assert.deepEqual((await client.ports.list(machine.id)).map((p) => p.port), [1, 3000, 8080, 65535]);
  assert.equal(await client.ports.close(machine.id, 3000), undefined);
  assert.equal(api.requests("DELETE", "/ports/").at(-1).path, `/v1/machines/${machine.id}/ports/3000`);
  assert.deepEqual((await client.ports.list(machine.id)).map((p) => p.port), [1, 8080, 65535]);
  await client.ports.close(machine.id, 3000); // not open any more: 204 all the same
  assert.deepEqual((await client.ports.list(other.id)).map((p) => p.port), [5000]);
  assert.deepEqual(await client.ports.list((await client.machines.create()).id), []);
});

test("each link is a new one, made with what was asked and nothing else", async (t) => {
  const { api, client, machine } = await rig(t);
  const port = await client.ports.open(machine.id, 8080);
  const link = await client.ports.link(machine.id, 8080);
  const seen = links(api).at(-1);
  assert.equal(seen.path, `/v1/machines/${machine.id}/ports/8080/link`);
  assert.equal(seen.body.length, 0); // nothing asked: the API's own 1800 seconds and "/"
  assert.equal(seen.headers["idempotency-key"], undefined); // its answer is a secret, never kept for a replay
  const { url, ...rest } = link;
  assert.deepEqual(rest, { object: "port_link", machine: machine.id, port: 8080, expires_at: "2026-10-23T07:05:00Z", access_s: 1800 });
  assert.equal(link.lastResponse.status, 201);
  assert.ok(LINK.test(url) && url.startsWith(`${port.url}/`));
  const second = await client.ports.link(machine.id, 8080, { access_s: 600, path: "/app?tab=logs" });
  assert.deepEqual(links(api).at(-1).json(), { access_s: 600, path: "/app?tab=logs" });
  assert.equal(second.access_s, 600);
  assert.ok(second.url !== url);
  assert.deepEqual(api.links, [
    { machine: machine.id, port: 8080, access_s: 1800, path: "/" },
    { machine: machine.id, port: 8080, access_s: 600, path: "/app?tab=logs" },
  ]);
});

test("a port that is not one, or no machine, is refused before anything is sent", async (t) => {
  const { api, client, machine } = await rig(t);
  const before = api.seen.length;
  const refused = (e) => e instanceof TypeError && /1 to 65535/.test(e.message);
  for (const port of [0, -1, 65536, 80.5, NaN, Infinity, "8080", null, undefined]) {
    await assert.rejects(client.ports.open(machine.id, port), refused, `open ${port}`);
    await assert.rejects(client.ports.close(machine.id, port), refused, `close ${port}`);
    await assert.rejects(client.ports.link(machine.id, port), refused, `link ${port}`);
  }
  await assert.rejects(client.ports.open("", 8080), TypeError);
  await assert.rejects(client.ports.list(""), TypeError);
  await assert.rejects(client.ports.close("", 8080), TypeError);
  await assert.rejects(client.ports.link("", 8080), TypeError);
  assert.equal(api.seen.length, before);
});

test("an open whose answer was lost is sent again, and the port is as asked, open once", async (t) => {
  const { api, client, machine, sleeps } = await rig(t);
  api.fault("POST", "/ports$", { drop: "after" });
  const port = await client.ports.open(machine.id, 8080, { public: true });
  assert.deepEqual([port.port, port.public, port.lastResponse.status], [8080, true, 200]); // the second answer: open already
  assert.deepEqual(opens(api).map((s) => s.json()), [{ port: 8080, public: true }, { port: 8080, public: true }]);
  assert.equal(api.openPorts.size, 1);
  assert.deepEqual(sleeps, [250]);
  api.fault("POST", "/ports$", { status: 504, code: "timeout" });
  assert.equal((await client.ports.open(machine.id, 8080)).public, false);
  assert.equal(opens(api).length, 4);
  assert.deepEqual(sleeps, [250, 250]);
});

test("a link that may have reached the API is never sent again, not even one refused for a moment", async (t) => {
  const { api, client, machine, sleeps } = await rig(t);
  await client.ports.open(machine.id, 8080);
  api.fault("POST", "/link$", { drop: "after" });
  const e = await client.ports.link(machine.id, 8080).catch((err) => err);
  assert.ok(e instanceof APIConnectionError, String(e));
  assert.match(e.message, /lost/);
  assert.equal(api.links.length, 1); // made, and its answer lost: sent again, it would make a second
  for (const [status, code, Cls] of [
    [503, "unavailable", UnavailableError],
    [429, "rate_limited", RateLimitError],
    [504, "timeout", UnavailableError],
  ]) {
    api.fault("POST", "/link$", { status, code, retryAfter: 1 });
    await assert.rejects(client.ports.link(machine.id, 8080), Cls, `${status}`);
  }
  assert.equal(links(api).length, 4);
  assert.deepEqual(sleeps, []);
});

test("a link that could not go out at all is tried again", async (t) => {
  const { make, sleeps } = await setup(t);
  const client = make({ baseURL: `http://127.0.0.1:${await closedPort()}`, maxRetries: 2 });
  const e = await client.ports.link(`m_${"0".repeat(26)}`, 8080).catch((err) => err);
  assert.ok(e instanceof APIConnectionError, String(e));
  assert.match(e.message, /could not reach/);
  assert.deepEqual(sleeps, [250, 500]);
});

test("an open or a link refused not_open is not tried again", async (t) => {
  const { api, client, machine, sleeps } = await rig(t);
  api.addressesServed = false;
  for (const call of [() => client.ports.open(machine.id, 8080), () => client.ports.link(machine.id, 8080)]) {
    const e = await call().catch((err) => err);
    assert.ok(e instanceof UnavailableError, String(e));
    assert.deepEqual([e.status, e.code], [503, "not_open"]);
  }
  assert.equal(opens(api).length, 1);
  assert.equal(links(api).length, 1);
  assert.deepEqual(sleeps, []);
});
