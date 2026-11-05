"""Models: the models the account's AI keys can use, and the model each agent of its machines starts on - chosen for one
agent, or one for every agent, in a team of an organisation when one is named."""

from __future__ import annotations

from support import APITest

from wheelmachine import (Agent, AgentList, AllowedModels, BadRequestError, ConflictError, PermissionDeniedError, Model, ModelIntelligence, ModelList,
                          ModelPrice, NotFoundError, PersonName)

ADA = {"id": "acc_0000000000000000000000ada1", "name": "Ada Lovelace", "email": "ada@example.com", "github_login": None}
ACME = {"id": "acc_00000000000000000000000077", "name": "Acme", "person": ADA, "role": "admin", "teams": None, "limits": None}


class Models(APITest):
    def test_the_models_of_the_keys_are_listed_with_how_each_makers_list_stands(self) -> None:
        listed = self.client.models.list()
        self.assertIsInstance(listed, ModelList)
        self.assertEqual([m.id for m in listed.data], ["openai/gpt-6.1-sol", "deepseek/deepseek-v4-pro"])
        self.assertIsInstance(listed.data[0], Model)
        self.assertEqual((listed.data[1].supplier, listed.data[1].model, listed.data[1].name), ("deepseek", "deepseek-v4-pro", "DeepSeek-V4-Pro"))
        self.assertEqual(listed.data[0].agents, ["codex", "aider"])
        # each with its provider's list price, and its score where the leaderboard has one
        price = listed.data[0].price
        self.assertIsInstance(price, ModelPrice)
        self.assertEqual((price.input_per_million_usd, price.output_per_million_usd, price.source, price.as_of), (2, 10, "OpenAI", "2026-11-02"))
        score = listed.data[0].intelligence
        self.assertIsInstance(score, ModelIntelligence)
        self.assertEqual((score.score, score.source, score.license, score.as_of), (1481, "LMArena text leaderboard", "CC BY 4.0", "2026-10-28"))
        self.assertEqual(listed.data[1].price.output_per_million_usd, 3.96)
        self.assertIsNone(listed.data[1].intelligence)
        self.assertIsNone(Model.from_dict({"id": "x/y"}).price)
        self.assertEqual([(s.id, s.state) for s in listed.suppliers], [("openai", "listed"), ("deepseek", "unreachable")])
        self.assertEqual(listed.suppliers[1].detail, "DeepSeek did not answer; this list is from 40 minutes ago.")
        codex = self.client.models.list(agent="codex", refresh=True)
        self.assertEqual([m.id for m in codex.data], ["openai/gpt-6.1-sol"])
        self.assertEqual(self.api.requests("GET", "/v1/models")[-1].query, {"agent": ["codex"], "refresh": ["true"]})
        with self.assertRaises(NotFoundError):
            self.client.models.list(agent="nosuch")
        with self.assertRaises(ValueError):
            self.client.models.list(agent="Not an agent")

    def test_a_model_is_chosen_for_an_agent_and_for_every_agent_and_taken_away(self) -> None:
        agents = self.client.agents.list()
        self.assertIsInstance(agents, AgentList)
        self.assertEqual([(a.name, a.state, a.starts_on) for a in agents.data], [("codex", "own", None), ("aider", "default", "openai/gpt-6.1-sol")])
        self.assertIsNone(agents.every_agent)
        self.assertEqual(agents.data[1].suppliers[0].default, "gpt-6.1-sol")
        codex = self.client.agents.choose("codex", "openai/gpt-6.1-sol")
        self.assertIsInstance(codex, Agent)
        self.assertEqual((codex.model, codex.chosen_by, codex.state), ("openai/gpt-6.1-sol", "you", "chosen"))
        self.assertEqual(self.api.requests("PUT", "/v1/agents/codex/model")[-1].json(), {"model": "openai/gpt-6.1-sol"})
        every = self.client.agents.choose_for_every("deepseek/deepseek-v4-pro", project="prj_team")
        self.assertEqual(every.every_agent, {"model": "deepseek/deepseek-v4-pro", "chosen_by": "you"})
        self.assertEqual(self.api.requests("PUT", "/v1/agents/model")[-1].query, {"project": ["prj_team"]})
        aider = next(a for a in every.data if a.name == "aider")
        self.assertEqual((aider.chosen_by, aider.state), ("every_agent", "chosen"))
        self.client.agents.reset_every(project="prj_team")
        self.assertEqual(self.api.requests("DELETE", "/v1/agents/model")[-1].query, {"project": ["prj_team"]})
        self.client.agents.choose("aider", "openai/gpt-6.1-sol")
        self.client.agents.reset("aider")
        self.assertTrue(all(a.state != "chosen" for a in self.client.agents.list().data))
        with self.assertRaises(BadRequestError):
            self.client.agents.choose("codex", "deepseek/deepseek-v4-pro")
        with self.assertRaises(ValueError):
            self.client.agents.choose("codex", "gpt-6.1-sol")
        with self.assertRaises(NotFoundError):
            self.client.agents.choose("nosuch", "openai/gpt-6.1-sol")

    def test_an_app_says_its_makers_and_what_it_started_on(self) -> None:
        from wheelmachine import App

        said = App.from_dict({"object": "app", "machine": "m_x", "name": "codex", "title": "Codex", "kind": "agent", "on": True, "running": True,
                              "port": None, "path": None, "url": None, "command": "codex", "suppliers": ["openai", "openrouter"],
                              "model": "openai/gpt-6.1-sol", "model_by": "chosen", "model_changed": True})
        self.assertEqual((said.suppliers, said.model, said.model_by, said.model_changed), (["openai", "openrouter"], "openai/gpt-6.1-sol", "chosen", True))
        before = App.from_dict({"object": "app", "machine": "m_x", "name": "codex", "title": "Codex", "kind": "agent", "on": True, "running": False,
                                "port": None, "path": None, "url": None, "command": "codex"})
        self.assertEqual((before.suppliers, before.model, before.model_by, before.model_changed), (None, None, None, None))


class AllowedModelsTests(APITest):
    def test_owners_and_admins_keep_the_lists_of_the_models_people_may_use(self) -> None:
        with self.assertRaises(ConflictError):
            self.client.models.allowed()
        self.api.org = dict(ACME)
        self.assertEqual(self.client.models.allowed(), [])
        org = self.client.models.allow(["openai/gpt-6.1-sol", "deepseek/deepseek-v4-pro"], organisation=True)
        self.assertIsInstance(org, AllowedModels)
        self.assertEqual((org.level, org.id, org.name, org.models), ("organisation", ACME["id"], "Acme", ["openai/gpt-6.1-sol", "deepseek/deepseek-v4-pro"]))
        self.assertIsInstance(org.set_by, PersonName)
        self.assertEqual(self.api.requests("PUT", "/v1/models/allowed/organisation")[-1].json(), {"models": ["openai/gpt-6.1-sol", "deepseek/deepseek-v4-pro"]})
        role = self.client.models.allow(["openai/gpt-6.1-sol"], role="manager")
        self.assertEqual((role.level, role.id, role.name), ("role", "manager", "Team managers"))
        self.client.models.allow(["deepseek/deepseek-v4-pro"], team="prj_00000000000000000000000001")
        self.client.models.allow(["openai/gpt-6.1-sol"], person="acc_0000000000000000000000bob1")
        self.assertEqual([(x.level, x.id) for x in self.client.models.allowed()],
                         [("organisation", ACME["id"]), ("role", "manager"), ("team", "prj_00000000000000000000000001"), ("person", "acc_0000000000000000000000bob1")])
        self.client.models.allow_any(role="manager")
        self.assertEqual(len(self.api.requests("DELETE", "/v1/models/allowed/roles/manager")), 1)
        self.assertEqual(len(self.client.models.allowed()), 3)
        with self.assertRaises(NotFoundError):
            self.client.models.allow(["openai/gpt-6.1-sol"], role="boss")
        for bad in ({}, {"organisation": True, "team": "prj_00000000000000000000000001"}):
            with self.assertRaises(ValueError):
                self.client.models.allow(["openai/gpt-6.1-sol"], **bad)
        for models in ([], "openai/gpt-6.1-sol", ["openai/gpt-6.1-sol"] * 11, ["gpt-6.1-sol"]):
            with self.assertRaises(ValueError):
                self.client.models.allow(models, organisation=True)
        self.api.org = dict(ACME, role="member", teams=["prj_00000000000000000000000001"])
        with self.assertRaises(PermissionDeniedError):
            self.client.models.allow(["openai/gpt-6.1-sol"], organisation=True)

    def test_a_person_held_to_a_list_chooses_from_it(self) -> None:
        self.api.org = dict(ACME, role="member", teams=["prj_00000000000000000000000001"])
        self.api.held_to = {"object": "allowed_models", "level": "team", "id": "prj_00000000000000000000000001", "name": "Research",
                            "models": ["deepseek/deepseek-v4-pro"], "set_by": None, "set_at": "2026-11-02T09:00:00Z"}
        listed = self.client.models.list(project="prj_00000000000000000000000001")
        self.assertEqual([m.id for m in listed.data], ["deepseek/deepseek-v4-pro"])
        self.assertEqual((listed.allowed.level, listed.allowed.name), ("team", "Research"))
        self.assertIsNone(listed.allowed.set_by)
        self.assertEqual(self.api.requests("GET", "/v1/models")[-1].query, {"project": ["prj_00000000000000000000000001"]})
        agents = self.client.agents.list()
        self.assertEqual(agents.allowed.models, ["deepseek/deepseek-v4-pro"])
        aider = next(a for a in agents.data if a.name == "aider")
        codex = next(a for a in agents.data if a.name == "codex")
        self.assertEqual((aider.chosen_by, aider.starts_on), ("allowed", "deepseek/deepseek-v4-pro"))
        self.assertEqual(codex.state, "not_allowed")
        with self.assertRaises(PermissionDeniedError):
            self.client.agents.choose("aider", "openai/gpt-6.1-sol")
        self.assertEqual(self.client.agents.choose("aider", "deepseek/deepseek-v4-pro").chosen_by, "you")
        self.api.held_to = None
        self.assertIsNone(self.client.models.list().allowed)
