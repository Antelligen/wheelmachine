"""Secrets: the value goes in the body of the call that sets it and nowhere else - never in an answer, an object,
an error or another call - and machines are given them by name."""

from __future__ import annotations

from support import APITest

from wheelmachine import SECRET_VALUE_MAX, BadRequestError, Machine, Secret, UnavailableError

#: A made-up value, nobody's.
VALUE = "made-up-secret-3b7f"


class Secrets(APITest):
    def carried_elsewhere(self, value: str) -> bool:
        """Whether any call but the ones that set a secret carried the value, anywhere."""
        for seen in self.api.seen:
            if value in seen.path or value in repr(seen.query) or any(value in v for v in seen.headers.values()):
                return True
            if not (seen.method == "PUT" and seen.path.startswith("/v1/secrets/")) and value.encode() in seen.body:
                return True
        return False

    def test_a_value_is_set_in_the_body_alone_and_never_comes_back(self) -> None:
        s = self.client.secrets.set("API_TOKEN", VALUE)
        self.assertIsInstance(s, Secret)
        self.assertEqual((s.name, s.machines), ("API_TOKEN", []))
        self.assertEqual(s.last_response.status if s.last_response else None, 201)
        put = self.api.requests("PUT", "/v1/secrets/API_TOKEN")[-1]
        self.assertEqual(put.json(), {"value": VALUE})
        self.assertEqual(put.query, {})
        self.assertEqual(self.api.secrets["API_TOKEN"]["value"], VALUE)
        self.assertEqual(self.client.secrets.set("API_TOKEN", VALUE + "-2").last_response.status, 200)  # type: ignore[union-attr]
        listed = self.client.secrets.list()
        self.assertEqual([x.name for x in listed], ["API_TOKEN"])
        for shown in (repr(s), repr(listed), repr(s.raw), str(listed[0].raw)):
            self.assertNotIn(VALUE, shown)
        self.assertFalse(self.carried_elsewhere(VALUE))

    def test_machines_are_given_secrets_by_name(self) -> None:
        self.client.secrets.set("API_TOKEN", VALUE)
        self.client.secrets.set("DATABASE_URL", "made-up-db")
        m = self.client.machines.create(name="web", secrets=["DATABASE_URL", "API_TOKEN"])
        self.assertEqual(self.api.requests("POST", "/v1/machines")[-1].json()["secrets"], ["DATABASE_URL", "API_TOKEN"])
        self.assertEqual(m.secrets, ["API_TOKEN", "DATABASE_URL"])
        self.assertEqual([x.machines for x in self.client.secrets.list()], [[m.id], [m.id]])
        fork = self.client.machines.fork(m.id)
        assert isinstance(fork, Machine)
        self.assertEqual(fork.secrets, ["API_TOKEN", "DATABASE_URL"])
        # said whole: the names take the place of the ones it had; [] takes them all away
        self.assertEqual(self.client.machines.update(m.id, secrets=["API_TOKEN"]).secrets, ["API_TOKEN"])
        self.assertEqual(self.client.machines.update(m.id, secrets=[]).secrets, [])
        self.assertEqual(self.api.requests("PATCH", "/v1/machines/" + m.id)[-1].json(), {"secrets": []})
        with self.assertRaises(BadRequestError) as caught:
            self.client.machines.create(secrets=["NOT_SET"])
        self.assertIn("set it first", str(caught.exception))
        # deleted, and taken off the machines given it
        self.client.secrets.delete("API_TOKEN")
        self.assertEqual(self.client.machines.get(fork.id).secrets, ["DATABASE_URL"])
        self.assertFalse(self.carried_elsewhere(VALUE))

    def test_what_is_not_right_is_refused_before_any_call_and_says_nothing_of_the_value(self) -> None:
        calls = len(self.api.seen)
        for name in ("", "api_token", "1ST", "A-B", "A/B", "A" * 65):
            with self.assertRaises(ValueError):
                self.client.secrets.set(name, VALUE)
            with self.assertRaises(ValueError):
                self.client.secrets.delete(name)
        for value, why in (("", "empty"), ("a\x00" + VALUE, "NUL"), (VALUE + "\ud800", "surrogate"),
                           (VALUE + "x" * SECRET_VALUE_MAX, "32 KiB")):
            with self.assertRaises(ValueError) as caught:
                self.client.secrets.set("API_TOKEN", value)
            self.assertNotIn(VALUE, str(caught.exception) + repr(caught.exception), why)
        with self.assertRaises(TypeError):
            self.client.secrets.set("API_TOKEN", VALUE.encode())  # type: ignore[arg-type]
        with self.assertRaises(TypeError):
            self.client.machines.create(secrets="API_TOKEN")
        with self.assertRaises(ValueError):
            self.client.machines.update("m_x", secrets=["lower"])
        self.assertEqual(len(self.api.seen), calls)

    def test_a_refused_set_is_tried_again_and_its_error_says_nothing_of_the_value(self) -> None:
        self.api.fault("PUT", r"^/v1/secrets/API_TOKEN$", status=503, code="unavailable", times=10)
        with self.assertRaises(UnavailableError) as caught:
            self.client.secrets.set("API_TOKEN", VALUE)
        self.assertEqual(len(self.api.requests("PUT", "/v1/secrets/API_TOKEN")), self.max_retries + 1)
        self.assertNotIn(VALUE, str(caught.exception) + repr(caught.exception) + repr(vars(caught.exception)))

    def test_an_ai_key_is_kept_outside_every_machine_by_its_name(self) -> None:
        s = self.client.secrets.set("ANTHROPIC_API_KEY", VALUE)
        self.assertEqual(self.api.requests("PUT", "/v1/secrets/ANTHROPIC_API_KEY")[-1].json(), {"value": VALUE})
        self.assertTrue(s.ai_key)
        self.assertTrue(s.every_machine)
        assert s.outside is not None
        self.assertEqual((s.outside.preset, s.outside.hosts, s.outside.header), ("anthropic", ["api.anthropic.com"], "x-api-key"))
        self.assertEqual([(h.name, h.format) for h in s.outside.headers], [("x-api-key", "{value}"), ("authorization", "Bearer {value}")])
        m = self.client.machines.create(name="box")
        self.assertEqual(m.ai_keys, ["ANTHROPIC_API_KEY"])
        closed = self.client.machines.create(name="shut", network={"internet": "closed"})
        self.assertEqual(closed.ai_keys, [])
        # kept inside, or only on the machines given it: said in the call's body
        inside = self.client.secrets.set("ANTHROPIC_API_KEY", VALUE, outside=False)
        self.assertEqual(self.api.requests("PUT", "/v1/secrets/ANTHROPIC_API_KEY")[-1].json(), {"value": VALUE, "outside": False})
        self.assertEqual((inside.outside, inside.every_machine, inside.ai_key), (None, False, False))
        given = self.client.secrets.set("ANTHROPIC_API_KEY", VALUE, every_machine=False)
        self.assertEqual(self.api.requests("PUT", "/v1/secrets/ANTHROPIC_API_KEY")[-1].json(), {"value": VALUE, "every_machine": False})
        self.assertEqual((given.ai_key, given.every_machine), (True, False))
        self.assertEqual(self.client.machines.get(m.id).ai_keys, [])
        own = self.client.secrets.set("LLM_TOKEN", VALUE, outside={"hosts": ["llm.example.com"], "header": "Authorization"})
        assert own.outside is not None
        self.assertEqual((own.outside.preset, own.outside.hosts, own.ai_key), (None, ["llm.example.com"], False))
        with self.assertRaises(BadRequestError):
            self.client.secrets.set("DATABASE_URL", VALUE, outside=True)
        with self.assertRaises(TypeError):
            self.client.secrets.set("DATABASE_URL", VALUE, outside="yes")  # type: ignore[arg-type]
        with self.assertRaises(TypeError):
            self.client.secrets.set("DATABASE_URL", VALUE, every_machine="yes")  # type: ignore[arg-type]
        for shown in (repr(s), repr(inside), repr(given), repr(own), repr(self.client.secrets.list())):
            self.assertNotIn(VALUE, shown)
        self.assertFalse(self.carried_elsewhere(VALUE))

    def test_the_presets_say_the_names_and_where_each_key_goes(self) -> None:
        presets = self.client.secrets.presets()
        self.assertEqual([p.id for p in presets], ["anthropic", "gemini"])
        gemini = presets[1]
        self.assertEqual((gemini.title, gemini.names, gemini.query), ("Gemini", ["GEMINI_API_KEY", "GOOGLE_API_KEY"], ["key"]))
        self.assertEqual(gemini.headers[0].name, "x-goog-api-key")
