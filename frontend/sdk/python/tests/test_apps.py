"""A machine's apps: listed, turned on and off, and fresh one-time links into the web ones."""

from __future__ import annotations

import unittest

from fake_api import APP_TOKEN
from support import APITest

from wheelmachine import App, BadRequestError, ConflictError, NotFoundError, UnavailableError


class OnAMachine(APITest):
    """A machine made in the stand-in itself, so that no call is seen before a test's own."""

    def setUp(self) -> None:
        super().setUp()
        self.machine = self.api.add_machine()["id"]


class Apps(OnAMachine):
    def test_the_apps_are_listed_as_the_machine_says_them(self) -> None:
        apps = self.client.apps.list(self.machine)
        seen = self.api.seen[-1]
        self.assertEqual((seen.method, seen.path), ("GET", "/v1/machines/{}/apps".format(self.machine)))
        self.assertTrue(all(isinstance(a, App) for a in apps))
        by = {a.name: a for a in apps}
        desktop = by["desktop"]
        self.assertEqual((desktop.machine, desktop.title, desktop.kind, desktop.port, desktop.on, desktop.running),
                         (self.machine, "Desktop", "web", 6901, False, False))
        self.assertEqual(desktop.url, "https://6901-{}.run.wheelmachine.ai".format(self.machine[2:]))
        self.assertEqual(desktop.path, "/vnc.html?autoconnect=1&resize=remote&reconnect=1")
        self.assertIsNone(desktop.link)
        self.assertEqual((by["ollama"].kind, by["ollama"].port, by["ollama"].url), ("service", None, None))
        self.assertEqual((by["codex"].kind, by["codex"].command, by["codex"].on, by["codex"].running), ("agent", "codex", True, False))
        self.assertEqual((by["gemini"].kind, by["gemini"].command, by["gemini"].on), ("terminal", "gemini", None))
        self.assertEqual(desktop.raw["object"], "app")

    def test_an_agent_started_before_the_machines_keys_changed_says_so(self) -> None:
        by = {a.name: a for a in self.client.apps.list(self.machine)}
        self.assertIsNone(by["codex"].keys_changed)  # (a machine that does not say it)
        self.api.apps[self.machine]["codex"].update(running=True, keys_changed=True)
        by = {a.name: a for a in self.client.apps.list(self.machine)}
        self.assertIs(by["codex"].keys_changed, True)
        self.assertIsNone(by["desktop"].keys_changed)

    def test_a_web_app_turned_on_answers_a_link_into_its_private_address(self) -> None:
        on = self.client.apps.turn_on(self.machine, "desktop", access_s=600)
        seen = self.api.seen[-1]
        self.assertEqual((seen.method, seen.path, seen.json()), ("POST", "/v1/machines/{}/apps/desktop/on".format(self.machine), {"access_s": 600}))
        self.assertNotIn("idempotency-key", seen.headers)
        self.assertEqual((on.on, on.running, on.access_s, on.expires_at), (True, True, 600, "2026-10-23T07:05:00Z"))
        self.assertTrue(on.link and on.link.startswith(on.url + "/.wheelmachine/link/"))
        self.assertIsNone(on.token)
        self.assertEqual(on.last_response.status if on.last_response else None, 200)
        # the link is never in what the object prints
        self.assertNotIn(on.link, repr(on))
        self.assertEqual([p.port for p in self.client.ports.list(self.machine)], [6901])
        self.client.apps.turn_on(self.machine, "ollama")
        self.assertIsNone(self.api.seen[-1].json())
        self.assertEqual([p.port for p in self.client.ports.list(self.machine)], [6901])

    def test_an_app_is_shown_on_the_machines_screen(self) -> None:
        with self.assertRaises(ConflictError) as off:
            self.client.apps.show(self.machine, "openclaw")
        self.assertIn("is off", str(off.exception))
        self.client.apps.turn_on(self.machine, "openclaw")
        shown = self.client.apps.show(self.machine, "openclaw")
        seen = self.api.seen[-1]
        self.assertEqual((seen.method, seen.path, seen.json()), ("POST", "/v1/machines/{}/apps/openclaw/show".format(self.machine), None))
        self.assertEqual((shown.name, shown.on, shown.link, shown.token), ("openclaw", True, None, None))
        with self.assertRaises(ValueError):
            self.client.apps.show(self.machine, "Not An App")

    def test_an_apps_own_token_comes_once_and_is_never_printed(self) -> None:
        on = self.client.apps.turn_on(self.machine, "openclaw")
        self.assertEqual(on.token, APP_TOKEN)
        self.assertNotIn(APP_TOKEN, repr(on))
        self.assertIsNone(self.client.apps.link(self.machine, "openclaw").token)

    def test_a_web_app_turned_off_takes_the_port_turning_it_on_opened(self) -> None:
        self.client.apps.turn_on(self.machine, "desktop")
        off = self.client.apps.turn_off(self.machine, "desktop")
        seen = self.api.seen[-1]
        self.assertEqual((seen.method, seen.path, seen.body), ("POST", "/v1/machines/{}/apps/desktop/off".format(self.machine), b""))
        self.assertEqual((off.on, off.link), (False, None))
        self.assertEqual(self.client.ports.list(self.machine), [])

    def test_a_turning_off_whose_answer_was_lost_is_sent_again(self) -> None:
        self.api.fault("POST", "/off$", drop="after")
        self.assertFalse(self.client.apps.turn_off(self.machine, "desktop").on)
        self.assertEqual(len(self.api.requests("POST", "/off$")), 2)

    def test_a_turning_on_or_a_link_that_may_have_reached_the_api_is_never_sent_again(self) -> None:
        for call, path in ((lambda: self.client.apps.turn_on(self.machine, "desktop"), "/on$"),
                           (lambda: self.client.apps.link(self.machine, "desktop"), "/link$")):
            self.api.fault("POST", path, status=503, code="unavailable", retry_after=1)
            with self.assertRaises(UnavailableError):
                call()
            self.assertEqual(len(self.api.requests("POST", path)), 1)
        self.assertEqual(self.sleeps, [])

    def test_a_link_is_made_for_a_web_app_that_is_on(self) -> None:
        with self.assertRaises(ConflictError) as caught:
            self.client.apps.link(self.machine, "editor")
        self.assertEqual(caught.exception.code, "conflict")
        self.client.apps.turn_on(self.machine, "editor")
        made = self.client.apps.link(self.machine, "editor")
        self.assertEqual(made.last_response.status if made.last_response else None, 201)
        self.assertTrue(made.link and made.link.startswith("https://13337-"))
        self.assertIsNone(self.api.seen[-1].json())
        self.assertEqual(len(self.api.links), 2)
        with self.assertRaises(BadRequestError):
            self.client.apps.link(self.machine, "ollama")

    def test_a_machine_made_before_apps_came_is_told_so(self) -> None:
        self.api.old_machines.add(self.machine)
        for call in (lambda: self.client.apps.list(self.machine), lambda: self.client.apps.turn_on(self.machine, "desktop"),
                     lambda: self.client.apps.turn_off(self.machine, "desktop"), lambda: self.client.apps.link(self.machine, "desktop")):
            with self.assertRaises(ConflictError) as caught:
                call()
            e = caught.exception
            self.assertEqual((e.status, e.code), (409, "no_apps"))
            self.assertIn("has no apps", e.detail or "")
        self.assertEqual(self.sleeps, [])

    def test_an_app_the_machine_does_not_have_is_not_found(self) -> None:
        with self.assertRaises(NotFoundError) as caught:
            self.client.apps.turn_on(self.machine, "nope")
        self.assertEqual(caught.exception.detail, "no such app in this machine")

    def test_what_is_not_an_apps_name_is_refused_before_any_call(self) -> None:
        before = len(self.api.seen)
        for bad in ("Desktop", "desk top", "../keys", "", "a" * 33, 7):
            with self.assertRaises(ValueError):
                self.client.apps.turn_on(self.machine, bad)  # type: ignore[arg-type]
        with self.assertRaises(ValueError):
            self.client.apps.list("")
        self.assertEqual(len(self.api.seen), before)


if __name__ == "__main__":
    unittest.main()
