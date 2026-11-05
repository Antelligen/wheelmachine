// What a machine used: asked for from a time or how long ago, in steps, as the API gives it - null for a number its
// server could not read.

import assert from "node:assert/strict";
import { test } from "node:test";

import { BadRequestError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

const POINTS = [
  { at: "2026-10-23T12:03:00Z", cpu_percent: 12.5, memory_bytes: 536870912, memory_limit_bytes: 4294967296, disk_read_bps: 1536,
    disk_write_bps: 300, net_rx_bps: 10240, net_tx_bps: 2048 },
  { at: "2026-10-23T12:04:00Z", cpu_percent: null, memory_bytes: 1073741824, memory_limit_bytes: null, disk_read_bps: null,
    disk_write_bps: null, net_rx_bps: null, net_tx_bps: null },
];

test("a machine's points come as the API gives them", async (t) => {
  const { api, client } = await setup(t);
  const m = api.addMachine({ name: "web" });
  api.metrics.set(m.id, POINTS);
  const got = await client.machines.metrics(m.id);
  assert.equal(got.machine, m.id);
  assert.equal(got.step_s, 60);
  assert.equal(got.cpus, 2);
  assert.equal(got.kept_since, "2026-10-23T12:03:00Z");
  assert.deepEqual(got.points, POINTS);
  assert.equal(got.lastResponse.status, 200);
  assert.equal([...api.requests("GET", "/metrics$").at(-1).query].length, 0);
  assert.deepEqual((await client.machines.metrics(api.addMachine({ name: "idle" }).id)).points, []);
});

test("from when and in what steps", async (t) => {
  const { api, client } = await setup(t);
  const m = api.addMachine({ name: "web" });
  const asked = () => Object.fromEntries(api.requests("GET", "/metrics$").at(-1).query);
  await client.machines.metrics(m.id, { since: "6h", step: 300 });
  assert.deepEqual(asked(), { since: "6h", step: "300" });
  await client.machines.metrics(m.id, { since: new Date(Date.UTC(2026, 9, 23, 9, 0, 0)) });
  assert.deepEqual(asked(), { since: "2026-10-23T09:00:00Z" });
  await assert.rejects(client.machines.metrics(m.id, { since: new Date("not a date") }), TypeError);
  await assert.rejects(client.machines.metrics(m.id, { step: 1.5 }), TypeError);
  await assert.rejects(client.machines.metrics(m.id, { step: 45 }), BadRequestError);
});
