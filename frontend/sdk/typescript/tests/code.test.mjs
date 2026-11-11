// What a machine reaches of its team's repositories, and a pull request from it: opened on GitHub, and on GitLab the push
// that opens it.

import assert from "node:assert/strict";
import { test } from "node:test";

import { NotFoundError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

const REPOSITORIES = [
  { object: "machine_repository", provider: "github", host: "github.com", repository: "acme/api", access: "write", linked: true, can_push: true, push: null },
  { object: "machine_repository", provider: "gitlab", host: "gitlab.com", repository: "team/site", access: "write", linked: false, can_push: false, push: null },
  { object: "machine_repository", provider: "github", host: "github.com", repository: "acme/docs", access: "read", linked: true, can_push: false, push: null },
];

test("a machine says what it reaches, and opens a pull request", async (t) => {
  const { api, client } = await setup(t);
  const m = api.addMachine({ name: "box" });
  api.repositories = REPOSITORIES;
  const seen = await client.machines.repositories(m.id);
  assert.deepEqual(seen.data.map((r) => [r.repository, r.access, r.linked, r.can_push]),
    [["acme/api", "write", true, true], ["team/site", "write", false, false], ["acme/docs", "read", true, false]]);
  assert.deepEqual(seen.protected_branches, ["main", "master", "release/**"]);

  const pull = await client.machines.openPullRequest(m.id, { repository: "acme/api", head: "agent/a", base: "main", title: "A change", body: "Why." });
  assert.equal(pull.opened, true);
  assert.equal(pull.run, null);
  assert.ok(pull.url.endsWith(`/acme/api/pull/${pull.number}`));
  const sent = api.requests("POST", "/pull-requests$").at(-1);
  assert.deepEqual(sent.json(), { repository: "acme/api", head: "agent/a", base: "main", title: "A change", body: "Why." });
  assert.ok(sent.headers["idempotency-key"], "a retry never makes two");

  // on GitLab, the push that opens it
  const lab = await client.machines.openPullRequest(m.id, { repository: "team/site", host: "gitlab.com", head: "agent/a", base: "main", title: "A change", draft: true });
  assert.equal(lab.opened, false);
  assert.equal(lab.number, null);
  assert.equal(lab.draft, true);
  assert.match(lab.run, /merge_request\.create/);
  assert.deepEqual(api.requests("POST", "/pull-requests$").at(-1).json(), { repository: "team/site", host: "gitlab.com", head: "agent/a", base: "main", title: "A change", draft: true });

  await assert.rejects(client.machines.openPullRequest(m.id, { repository: "acme/docs", head: "agent/a", base: "main", title: "A change" }), NotFoundError);
});
