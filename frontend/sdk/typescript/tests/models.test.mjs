// Models: the models the account's AI keys can use, and the model each agent of its machines starts on - chosen for one
// agent, or one for every agent, in a team of an organisation when one is named.

import assert from "node:assert/strict";
import { test } from "node:test";

import { BadRequestError, ConflictError, NotFoundError, PermissionDeniedError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

test("the models of the keys are listed with how each provider's list stands", async (t) => {
  const { api, client } = await setup(t);
  const listed = await client.models.list();
  assert.deepEqual(listed.data.map((m) => m.id), ["openai/gpt-6.1-sol", "deepseek/deepseek-v4-pro"]);
  assert.deepEqual(listed.data[0].agents, ["codex", "aider"]);
  // each with its provider's list price, and its score where the leaderboard has one
  assert.deepEqual([listed.data[0].price.input_per_million_usd, listed.data[0].price.output_per_million_usd, listed.data[0].price.source], [2, 10, "OpenAI"]);
  assert.deepEqual([listed.data[0].intelligence.score, listed.data[0].intelligence.license, listed.data[0].intelligence.as_of], [1481, "CC BY 4.0", "2026-10-28"]);
  assert.equal(listed.data[1].price.output_per_million_usd, 3.96);
  assert.equal(listed.data[1].intelligence, null);
  assert.deepEqual(listed.suppliers.map((s) => [s.id, s.state]), [["openai", "listed"], ["deepseek", "unreachable"]]);
  const codex = await client.models.list({ agent: "codex", refresh: true });
  assert.deepEqual(codex.data.map((m) => m.id), ["openai/gpt-6.1-sol"]);
  const asked = api.requests("GET", "^/v1/models$").at(-1);
  assert.equal(asked.query.get("agent"), "codex");
  assert.equal(asked.query.get("refresh"), "true");
  await assert.rejects(client.models.list({ agent: "nosuch" }), NotFoundError);
  await assert.rejects(client.models.list({ agent: "Not an agent" }), TypeError);
});

test("a model is chosen for an agent and for every agent, and taken away", async (t) => {
  const { api, client } = await setup(t);
  const agents = await client.agents.list();
  assert.deepEqual(agents.data.map((a) => [a.name, a.state, a.starts_on]), [["codex", "own", null], ["aider", "default", "openai/gpt-6.1-sol"]]);
  assert.equal(agents.every_agent, null);
  const codex = await client.agents.choose("codex", "openai/gpt-6.1-sol");
  assert.deepEqual([codex.model, codex.chosen_by, codex.state], ["openai/gpt-6.1-sol", "you", "chosen"]);
  assert.deepEqual(api.requests("PUT", "^/v1/agents/codex/model$").at(-1).json(), { model: "openai/gpt-6.1-sol" });
  const every = await client.agents.chooseForEvery("deepseek/deepseek-v4-pro", { project: "prj_team" });
  assert.deepEqual(every.every_agent, { model: "deepseek/deepseek-v4-pro", chosen_by: "you" });
  assert.equal(api.requests("PUT", "^/v1/agents/model$").at(-1).query.get("project"), "prj_team");
  assert.equal(every.data.find((a) => a.name === "aider").chosen_by, "every_agent");
  await client.agents.resetEvery({ project: "prj_team" });
  assert.equal(api.requests("DELETE", "^/v1/agents/model$").at(-1).query.get("project"), "prj_team");
  await client.agents.choose("aider", "openai/gpt-6.1-sol");
  await client.agents.reset("aider");
  assert.ok((await client.agents.list()).data.every((a) => a.state !== "chosen"));
  await assert.rejects(client.agents.choose("codex", "deepseek/deepseek-v4-pro"), BadRequestError);
  await assert.rejects(client.agents.choose("codex", "gpt-6.1-sol"), TypeError);
  await assert.rejects(client.agents.choose("nosuch", "openai/gpt-6.1-sol"), NotFoundError);
});

const ACME = { id: "acc_00000000000000000000000077", name: "Acme", role: "admin", teams: null, limits: null,
  person: { id: "acc_0000000000000000000000ada1", name: "Ada Lovelace", email: "ada@example.com", github_login: null } };

test("owners and admins keep the lists of the models people may use", async (t) => {
  const { api, client } = await setup(t);
  await assert.rejects(client.models.allowed(), ConflictError);
  api.org = { ...ACME };
  assert.deepEqual(await client.models.allowed(), []);
  const org = await client.models.allow(["openai/gpt-6.1-sol", "deepseek/deepseek-v4-pro"], { organisation: true });
  assert.deepEqual([org.level, org.id, org.name, org.models], ["organisation", ACME.id, "Acme", ["openai/gpt-6.1-sol", "deepseek/deepseek-v4-pro"]]);
  assert.deepEqual(org.set_by, { id: ACME.person.id, name: "Ada Lovelace" });
  assert.deepEqual(api.requests("PUT", "^/v1/models/allowed/organisation$").at(-1).json(), { models: ["openai/gpt-6.1-sol", "deepseek/deepseek-v4-pro"] });
  const role = await client.models.allow(["openai/gpt-6.1-sol"], { role: "manager" });
  assert.deepEqual([role.level, role.id, role.name], ["role", "manager", "Team managers"]);
  await client.models.allow(["deepseek/deepseek-v4-pro"], { team: "prj_00000000000000000000000001" });
  await client.models.allow(["openai/gpt-6.1-sol"], { person: "acc_0000000000000000000000bob1" });
  assert.deepEqual((await client.models.allowed()).map((x) => [x.level, x.id]),
    [["organisation", ACME.id], ["role", "manager"], ["team", "prj_00000000000000000000000001"], ["person", "acc_0000000000000000000000bob1"]]);
  await client.models.allowAny({ role: "manager" });
  assert.equal(api.requests("DELETE", "^/v1/models/allowed/roles/manager$").length, 1);
  assert.equal((await client.models.allowed()).length, 3);
  await assert.rejects(client.models.allow(["openai/gpt-6.1-sol"], { role: "boss" }), NotFoundError);
  await assert.rejects(client.models.allow(["openai/gpt-6.1-sol"], {}), TypeError);
  await assert.rejects(client.models.allow(["openai/gpt-6.1-sol"], { organisation: true, team: "prj_00000000000000000000000001" }), TypeError);
  for (const models of [[], Array(11).fill("openai/gpt-6.1-sol"), ["gpt-6.1-sol"]]) {
    await assert.rejects(client.models.allow(models, { organisation: true }), TypeError);
  }
  api.org = { ...ACME, role: "member", teams: ["prj_00000000000000000000000001"] };
  await assert.rejects(client.models.allow(["openai/gpt-6.1-sol"], { organisation: true }), PermissionDeniedError);
});

test("a person limited to a list chooses from it", async (t) => {
  const { api, client } = await setup(t);
  api.org = { ...ACME, role: "member", teams: ["prj_00000000000000000000000001"] };
  api.heldTo = { object: "allowed_models", level: "team", id: "prj_00000000000000000000000001", name: "Research", models: ["deepseek/deepseek-v4-pro"],
    set_by: null, set_at: "2026-11-02T09:00:00Z" };
  const listed = await client.models.list({ project: "prj_00000000000000000000000001" });
  assert.deepEqual(listed.data.map((m) => m.id), ["deepseek/deepseek-v4-pro"]);
  assert.deepEqual([listed.allowed.level, listed.allowed.name], ["team", "Research"]);
  assert.equal(api.requests("GET", "^/v1/models$").at(-1).query.get("project"), "prj_00000000000000000000000001");
  const agents = await client.agents.list();
  assert.deepEqual(agents.allowed.models, ["deepseek/deepseek-v4-pro"]);
  const aider = agents.data.find((a) => a.name === "aider");
  assert.deepEqual([aider.chosen_by, aider.starts_on], ["allowed", "deepseek/deepseek-v4-pro"]);
  assert.equal(agents.data.find((a) => a.name === "codex").state, "not_allowed");
  await assert.rejects(client.agents.choose("aider", "openai/gpt-6.1-sol"), PermissionDeniedError);
  assert.equal((await client.agents.choose("aider", "deepseek/deepseek-v4-pro")).chosen_by, "you");
  api.heldTo = null;
  assert.equal((await client.models.list()).allowed, null);
});
