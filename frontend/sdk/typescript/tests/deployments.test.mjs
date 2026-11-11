// Deploy: a deployment made from each kind of source, an archive sent, a run followed, and what else a deployment can be
// asked. A call while Deploy is shut says it is not open.

import assert from "node:assert/strict";
import { test } from "node:test";

import { APITimeoutError, ConflictError, NotFoundError, WaitTimeoutError, WheelmachineError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

const REPO = { source: { connection: "code_1", repo: "acme/shop" }, start: "npm start", port: 3000 };
const made = (api) => api.requests("POST", "/v1/deployments$");

test("a repository of a connection is deployed and followed to its end", async (t) => {
  const { api, client } = await setup(t);
  const d = await client.deployments.create({ ...REPO, build: "npm ci", name: "shop", branch: "main", cpus: 4, secrets: ["API_KEY"], keep_awake: true });
  const seen = made(api).at(-1);
  assert.deepEqual(seen.json(), { ...REPO, build: "npm ci", name: "shop", branch: "main", cpus: 4, secrets: ["API_KEY"], keep_awake: true });
  assert.ok(seen.headers["idempotency-key"]);
  assert.deepEqual([d.state, d.source.kind, d.source.repo, d.keep_awake, d.lastResponse.status], ["building", "repo", "acme/shop", true, 201]);
  const live = await client.deployments.wait(d.id);
  assert.deepEqual([live.state, live.run.state], ["live", "live"]);
  assert.equal(live.url, `https://3000-${d.machine.slice(2)}.run.wheelmachine.ai`);
});

test("a public address is deployed", async (t) => {
  const { api, client } = await setup(t);
  const d = await client.deployments.create({ source: { url: "https://github.com/acme/shop" }, start: "go run .", port: 8080 });
  assert.deepEqual(made(api).at(-1).json().source, { url: "https://github.com/acme/shop" });
  assert.equal(d.source.kind, "url");
});

test("files are sent as an archive, whole", async (t) => {
  const { api, client } = await setup(t);
  const d = await client.deployments.create({ source: { upload: true }, start: "x", port: 1 });
  assert.equal(d.state, "waiting_for_files");
  const archive = new TextEncoder().encode("a tar archive");
  const sent = await client.deployments.sendFiles(d.id, archive);
  assert.deepEqual([sent.state, sent.lastResponse.status], ["building", 202]);
  assert.equal(api.deploySent.get(d.id).toString(), "a tar archive");
  // new ones, in place of its last: a run of their own
  await client.deployments.wait(d.id);
  const again = await client.deployments.sendFiles(d.id, new TextEncoder().encode("another"));
  assert.equal(again.run.why, "files");
  await assert.rejects(client.deployments.sendFiles(d.id, "text"), TypeError);
});

test("a redeploy is never sent twice, and there is one at a time", async (t) => {
  const { api, client } = await setup(t);
  const d = await client.deployments.create(REPO);
  await client.deployments.wait(d.id);
  const again = await client.deployments.redeploy(d.id);
  assert.deepEqual([again.run.why, again.lastResponse.status], ["redeploy", 202]);
  await assert.rejects(client.deployments.redeploy(d.id), ConflictError);
  assert.equal(api.requests("POST", "/redeploy$").length, 2);
});

test("a wait too long says so", async (t) => {
  const { api, client } = await setup(t);
  const d = await client.deployments.create(REPO);
  api.deployStuck = true;
  await assert.rejects(client.deployments.wait(d.id, { timeout_s: 1 }), WaitTimeoutError);
  assert.equal(api.requests("GET", "/wait$").at(-1).query.get("timeout_s"), "1");
  await assert.rejects(client.deployments.wait(d.id, { timeout_s: -1 }), TypeError);
});

test("it is listed, read, changed and deleted", async (t) => {
  const { api, client } = await setup(t);
  const a = await client.deployments.create({ ...REPO, name: "a", project: "prj_a" });
  const b = await client.deployments.create({ ...REPO, name: "b" });
  assert.deepEqual((await client.deployments.list()).map((d) => d.name), ["b", "a"]);
  assert.deepEqual((await client.deployments.list({ project: "prj_a" })).map((d) => d.id), [a.id]);
  assert.equal((await client.deployments.get(b.id)).name, "b");
  const changed = await client.deployments.update(b.id, { port: 9000, keep_awake: true });
  assert.deepEqual(api.requests("PATCH", "/v1/deployments/").at(-1).json(), { port: 9000, keep_awake: true });
  assert.deepEqual([changed.port, changed.keep_awake], [9000, true]);
  assert.deepEqual((await client.deployments.runs(b.id)).map((r) => r.why), ["first"]);
  const log = await client.deployments.logs(b.id, { kind: "app" });
  assert.deepEqual([log.kind, log.text, log.done], ["app", "built", true]);
  await client.deployments.delete(b.id);
  await assert.rejects(client.deployments.get(b.id), NotFoundError);
});

test("how it builds is asked of a repository, or of files about to be sent", async (t) => {
  const { api, client } = await setup(t);
  const guess = await client.deployments.detect({ source: { connection: "code_1", repo: "acme/shop" }, branch: "main" });
  assert.deepEqual(api.requests("POST", "/detect$").at(-1).json(), { source: { connection: "code_1", repo: "acme/shop" }, branch: "main" });
  assert.deepEqual([guess.runtime, guess.start, guess.port], ["node", "npm start", 3000]);
  await client.deployments.detect({ files: { "package.json": "{}", "yarn.lock": null } });
  assert.deepEqual(api.requests("POST", "/detect$").at(-1).json(), { files: { "package.json": "{}", "yarn.lock": null } });
});

test("what can be deployed is listed", async (t) => {
  const { api, client } = await setup(t);
  api.deployRepos = [{ object: "deploy_repo", connection: "code_1", provider: "github", host: "github.com", repo: "acme/shop", shared: true, updated_at: null }];
  const repos = await client.deployments.repos({ connection: "code_1" });
  assert.deepEqual([repos[0].repo, repos[0].shared], ["acme/shop", true]);
  assert.deepEqual(await client.deployments.repos({ connection: "code_2" }), []);
  const branches = await client.deployments.branches({ connection: "code_1", repo: "acme/shop" });
  assert.deepEqual([branches.default, branches.known, branches.data.map((b) => b.name)], ["main", true, ["main"]]);
});

test("a call while Deploy is shut says so", async (t) => {
  const { api, client } = await setup(t);
  api.deployClosed = true;
  await assert.rejects(client.deployments.list(), (err) => err instanceof WheelmachineError && err.code === "not_open");
});

test("files wait for their machine", async (t) => {
  const { api, client } = await setup(t);
  api.deployFilesAfter = 3;
  const d = await client.deployments.create({ source: { upload: true }, start: "x", port: 1 });
  const sent = await client.deployments.sendFiles(d.id, new TextEncoder().encode("a tar archive"));
  assert.equal(sent.state, "building");
  assert.equal(api.deploySent.get(d.id).toString(), "a tar archive");
  // a machine that takes too long: told, with its id, and nothing sent
  api.deployFilesAfter = 1 << 30;
  const late = await client.deployments.create({ source: { upload: true }, start: "x", port: 1 });
  await assert.rejects(client.deployments.sendFiles(late.id, new TextEncoder().encode("x"), { readyTimeoutS: 0 }), (e) => e instanceof WaitTimeoutError && e.message.includes(late.id));
  assert.equal(api.deploySent.has(late.id), false);
});

test("files going up are given longer than a call usually is", async (t) => {
  const { api, client } = await setup(t, { timeout: 200 });
  const d = await client.deployments.create({ source: { upload: true }, start: "x", port: 1 });
  api.fault("PUT", "/files$", { delay: 500 });
  const sent = await client.deployments.sendFiles(d.id, new TextEncoder().encode("a tar archive"));
  assert.equal(sent.state, "building");
  // and as long as asked, when that is said
  const other = await client.deployments.create({ source: { upload: true }, start: "x", port: 1 });
  api.fault("PUT", "/files$", { delay: 500 });
  await assert.rejects(client.deployments.sendFiles(other.id, new TextEncoder().encode("x"), { timeout: 100, maxRetries: 0 }), APITimeoutError);
});
