// A program signs in through the browser (as `wm login` does): no key sent, the device code out of JSON and inspect,
// asked about at the API's pace, and the key given once - or the person's refusal, or the code running out, as such.

import assert from "node:assert/strict";
import { test } from "node:test";
import { inspect } from "node:util";

import { DeviceLogin, PermissionDeniedError, WheelmachineError } from "../dist/index.js";
import { KEY, setup } from "./fake-api.mjs";

async function start(api, waits, options = {}) {
  const login = await DeviceLogin.start({ baseURL: api.url, ...options });
  login._sleep = async (ms) => {
    waits.push(ms);
  };
  return login;
}

test("a program is given its key once the person approves", async (t) => {
  const { api } = await setup(t);
  api.decide = (ask) => (ask === 1 ? "pending" : ask === 2 ? "slow" : "approved");
  const waits = [];
  const login = await start(api, waits, { name: "tool on box", scopes: ["cloud:machines:read"] });
  assert.equal(login.userCode, "BCDF-GHJK");
  assert.equal(login.verificationUri, api.url + "/device");
  assert.equal("verificationUriComplete" in login, false, "the code is never carried in an address");
  assert.ok(!JSON.stringify(login).includes("wmd_") && !inspect(login).includes("wmd_"), "the device code is never shown");
  const key = await login.wait();
  assert.equal(key.secret, KEY);
  assert.equal(key.name, "tool on box");
  assert.deepEqual(key.scopes, ["cloud:machines:read"]);
  assert.deepEqual(waits, [5000, 5000, 5000], "every 5 seconds, and the API's own wait when it asked for one");
  const started = api.requests("POST", "/v1/login/devices");
  const asked = api.requests("POST", "/v1/login/devices/token");
  assert.deepEqual(started[0].json(), { name: "tool on box", scopes: ["cloud:machines:read"] });
  assert.equal(asked.length, 3);
  for (const seen of [...started, ...asked]) assert.equal(seen.headers.authorization, undefined, "no key is sent to sign in");
  await assert.rejects(login.poll(), (err) => err instanceof WheelmachineError && err.code === "login_expired", "the key is given once");
});

test("a refusal or a code that ran out is said", async (t) => {
  const { api } = await setup(t);
  api.decide = () => "denied";
  await assert.rejects((await start(api, [])).wait(), (err) => err instanceof PermissionDeniedError && err.code === "login_denied");
  api.decide = () => "expired";
  await assert.rejects((await start(api, [])).wait(), (err) => err instanceof WheelmachineError && err.code === "login_expired");
  // an account that holds as many keys as it may: said as such, not taken for a request to slow down
  api.decide = () => "full";
  await assert.rejects((await start(api, [])).wait(), (err) => err instanceof WheelmachineError && err.code === "limit_reached");
});
