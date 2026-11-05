"""The calls, as the API sees them: the key on every call, the answers read into objects, errors by family."""

from __future__ import annotations

import itertools
import os
import unittest
from datetime import date, datetime, timedelta, timezone
from unittest import mock

from fake_api import KEY, REVOKED_KEY
from support import APITest

import wheelmachine
from wheelmachine import (
    AuthenticationError,
    Backup,
    BadRequestError,
    ConflictError,
    InternalServerError,
    Machine,
    NotFoundError,
    PaymentRequiredError,
    PermissionDeniedError,
    RateLimitError,
    UnavailableError,
    WaitTimeoutError,
    Wheelmachine,
    WheelmachineError,
)


class TheKey(APITest):
    def test_every_call_carries_the_key_and_says_what_it_is(self) -> None:
        me = self.client.me()
        seen = self.api.seen[-1]
        self.assertEqual(seen.headers["authorization"], "Bearer " + KEY)
        self.assertEqual(seen.headers["user-agent"], "wheelmachine-python/" + wheelmachine.__version__)
        self.assertEqual(me.account.id, "acct_test")
        self.assertEqual(me.key.scopes, ["cloud:*"])
        self.assertTrue(me.standing.can_spend)
        self.assertEqual(me.standing.balance_usd, 10.0)

    def test_the_answer_says_how_it_came(self) -> None:
        me = self.client.me()
        info = me.last_response
        assert info is not None
        self.assertEqual(info.status, 200)
        self.assertTrue(info.request_id and info.request_id.startswith("req_"))
        self.assertFalse(info.replayed)
        assert info.rate_limit is not None
        self.assertEqual((info.rate_limit.limit, info.rate_limit.window_seconds), (600, 60))
        self.assertEqual((info.rate_limit.remaining, info.rate_limit.reset_seconds), (599, 1))
        self.assertEqual(me.raw["object"], "me")

    def test_key_and_address_come_from_the_environment(self) -> None:
        with mock.patch.dict(os.environ, {"WHEELMACHINE_API_KEY": KEY, "WHEELMACHINE_BASE_URL": self.api.url}):
            client = Wheelmachine()
        self.addCleanup(client.close)
        self.assertEqual(client.me().account.id, "acct_test")
        self.assertEqual(client.base_url, self.api.url)

    def test_the_address_is_the_apis_own_by_default(self) -> None:
        env = {k: v for k, v in os.environ.items() if k != "WHEELMACHINE_BASE_URL"}
        with mock.patch.dict(os.environ, env, clear=True):
            client = Wheelmachine(api_key=KEY)
        self.assertEqual(client.base_url, "https://api.wheelmachine.ai")
        self.assertEqual(wheelmachine.DEFAULT_BASE_URL, "https://api.wheelmachine.ai")

    def test_no_key_is_refused_before_any_call(self) -> None:
        env = {k: v for k, v in os.environ.items() if k != "WHEELMACHINE_API_KEY"}
        with mock.patch.dict(os.environ, env, clear=True):
            with self.assertRaises(WheelmachineError) as caught:
                Wheelmachine(base_url=self.api.url)
        self.assertIn("WHEELMACHINE_API_KEY", str(caught.exception))
        self.assertEqual(self.api.seen, [])

    def test_the_key_is_never_shown(self) -> None:
        self.assertNotIn(KEY, repr(self.client))
        self.assertNotIn(KEY, str(vars(self.client)))
        bad = self.make_client(api_key=REVOKED_KEY)
        with self.assertRaises(AuthenticationError) as caught:
            bad.me()
        e = caught.exception
        self.assertEqual((e.status, e.code), (401, "key_revoked"))
        for text in (str(e), repr(e), str(e.body), str(e.args)):
            self.assertNotIn(REVOKED_KEY, text)
            self.assertNotIn(KEY, text)

    def test_a_key_that_could_break_a_header_is_refused_without_being_echoed(self) -> None:
        with self.assertRaises(WheelmachineError) as caught:
            Wheelmachine(api_key="ak_live_abc\r\nInjected: yes", base_url=self.api.url)
        self.assertNotIn("Injected", str(caught.exception))
        self.assertNotIn("ak_live_abc", str(caught.exception))

    def test_an_address_that_is_not_one_is_refused(self) -> None:
        for url in ("ftp://example.com", "https://", "https://user:pw@example.com", "https://example.com/?a=1"):
            with self.assertRaises(WheelmachineError):
                Wheelmachine(api_key=KEY, base_url=url)


class Errors(APITest):
    max_retries = 0

    def cases(self):  # type: ignore[no-untyped-def]
        return [
            (400, "invalid_request", BadRequestError),
            (401, "key_expired", AuthenticationError),
            (402, "payment_required", PaymentRequiredError),
            (403, "forbidden", PermissionDeniedError),
            (404, "not_found", NotFoundError),
            (408, "timeout", WaitTimeoutError),
            (409, "conflict", ConflictError),
            (411, "invalid_request", BadRequestError),
            (413, "too_large", BadRequestError),
            (422, "invalid_request", ConflictError),
            (429, "rate_limited", RateLimitError),
            (500, "internal", InternalServerError),
            (502, "unavailable", UnavailableError),
            (503, "unavailable", UnavailableError),
            (504, "timeout", UnavailableError),
        ]

    def test_each_status_has_its_error_with_the_problems_details(self) -> None:
        for status, code, cls in self.cases():
            with self.subTest(status=status):
                self.api.fault("GET", "^/v1/me$", status=status, code=code, detail="words for " + code)
                with self.assertRaises(cls) as caught:
                    self.client.me()
                e = caught.exception
                self.assertIsInstance(e, WheelmachineError)
                self.assertEqual((e.status, e.code, e.detail), (status, code, "words for " + code))
                self.assertEqual(e.request_id, self.api.seen[-1].rid)
                self.assertIn("{} {}: words for {}".format(status, code, code), str(e))
                self.assertIn(e.request_id or "-", str(e))

    def test_a_refusal_from_the_api_itself(self) -> None:
        with self.assertRaises(NotFoundError) as caught:
            self.client.machines.get("m_" + "9" * 26)
        e = caught.exception
        self.assertEqual((e.status, e.code, e.detail, e.retry_after), (404, "not_found", "no such machine", None))
        self.assertTrue(str(e).startswith("404 not_found: no such machine (request req_"))
        assert e.body is not None
        self.assertEqual(e.body["type"], "https://docs.wheelmachine.ai/errors/not_found")

    def test_retry_after_is_on_the_error(self) -> None:
        self.api.fault("GET", "^/v1/me$", status=503, code="unavailable", retry_after=2.5)
        with self.assertRaises(UnavailableError) as caught:
            self.client.me()
        self.assertEqual(caught.exception.retry_after, 3.0)  # the header, whole seconds rounded up

    def test_an_answer_without_problem_details_still_has_a_code(self) -> None:
        self.api.fault("GET", "^/v1/me$", status=502, raw=("text/html", b"<html>bad gateway</html>"))
        with self.assertRaises(UnavailableError) as caught:
            self.client.me()
        self.assertEqual((caught.exception.status, caught.exception.code), (502, "unavailable"))

    def test_a_project_with_machines_is_not_deleted(self) -> None:
        self.client.machines.create(name="keeps it")
        with self.assertRaises(ConflictError) as caught:
            self.client.projects.delete(self.api.first_project)
        self.assertEqual(caught.exception.code, "project_not_empty")


class Machines(APITest):
    def test_create_sends_only_what_was_given(self) -> None:
        m = self.client.machines.create()
        seen = self.api.requests("POST", "^/v1/machines$")[-1]
        self.assertEqual(seen.body, b"")
        self.assertEqual(seen.headers["content-length"], "0")
        self.assertTrue(seen.headers.get("idempotency-key"))
        self.assertIsInstance(m, Machine)
        self.assertEqual(m.state, "running")

    def test_a_machine_is_made_and_read_back(self) -> None:
        m = self.client.machines.create(name="builder", region="mumbai", labels={"team": "ml"}, wait_s=30)
        body = self.api.requests("POST", "^/v1/machines$")[-1].json()
        self.assertEqual(body, {"name": "builder", "region": "mumbai", "labels": {"team": "ml"}, "wait_s": 30})
        self.assertEqual((m.name, m.state, m.labels, m.files_sealed, m.ready_ms), ("builder", "running", {"team": "ml"}, True, 12.5))
        assert m.size is not None and m.location is not None
        self.assertEqual((m.size.cpus, m.size.memory_gb, m.location.country), (2, 4, "FI"))
        self.assertEqual(m.last_response.status if m.last_response else None, 201)
        again = self.client.machines.get(m.id)
        self.assertEqual(again.id, m.id)
        self.assertIsNone(again.ready_ms)

    def test_a_machine_pauses_when_idle_unless_it_is_made_not_to(self) -> None:
        m = self.client.machines.create()
        self.assertEqual((m.sleep_when_idle, m.idle_after_s), (True, 300))
        m = self.client.machines.create(idle_after_s=3600)
        self.assertEqual(self.api.requests("POST", "^/v1/machines$")[-1].json(), {"idle_after_s": 3600})
        self.assertEqual((m.sleep_when_idle, m.idle_after_s), (True, 3600))
        m = self.client.machines.create(sleep_when_idle=False)
        self.assertEqual(self.api.requests("POST", "^/v1/machines$")[-1].json(), {"sleep_when_idle": False})
        self.assertEqual((m.sleep_when_idle, m.idle_after_s), (False, None))
        self.assertEqual(self.client.machines.get(m.id).sleep_when_idle, False)

    def test_a_machine_is_made_at_the_size_asked_for(self) -> None:
        m = self.client.machines.create()
        self.assertEqual((m.cpus, m.memory_gb), (2, 4))
        m = self.client.machines.create(cpus=8)
        self.assertEqual(self.api.requests("POST", "^/v1/machines$")[-1].json(), {"cpus": 8})
        self.assertEqual((m.cpus, m.memory_gb), (8, 16))
        assert m.size is not None
        self.assertEqual(m.size.cpus, 8)
        with self.assertRaises(BadRequestError) as caught:
            self.client.machines.create(cpus=3)
        self.assertEqual(caught.exception.code, "invalid_request")
        # 16 is not offered yet, in the API's own words
        with self.assertRaises(BadRequestError) as caught:
            self.client.machines.create(cpus=16)
        self.assertEqual(caught.exception.detail, "16 CPUs are not offered yet: choose 2, 4 or 8")

    def test_a_machine_still_starting_is_followed_with_wait(self) -> None:
        m = self.client.machines.create(wait_s=0)
        self.assertEqual(m.state, "starting")
        self.assertEqual(m.last_response.status if m.last_response else None, 202)
        ready = self.client.machines.wait(m.id, "running", timeout_s=30)
        self.assertEqual(ready.state, "running")
        seen = self.api.requests("GET", "/wait$")[-1]
        self.assertEqual(seen.query, {"state": ["running"], "timeout_s": ["30"]})

    def test_a_long_wait_is_asked_for_in_the_apis_longest_steps(self) -> None:
        m = self.client.machines.create()
        self.client.machines.wait(m.id, timeout_s=1500)
        self.assertEqual(self.api.requests("GET", "/wait$")[-1].query["timeout_s"], ["600"])

    def test_a_state_not_reached_is_a_wait_timeout(self) -> None:
        m = self.client.machines.create()
        with self.assertRaises(WaitTimeoutError) as caught:
            self.client.machines.wait(m.id, "archived", timeout_s=0.2)
        self.assertEqual((caught.exception.status, caught.exception.code), (408, "timeout"))

    def test_the_life_of_a_machine(self) -> None:
        m = self.client.machines.create(labels={"a": "1", "b": "2"})
        for action, state in (("pause", "paused"), ("start", "running"), ("stop", "stopped"), ("archive", "archived")):
            got = getattr(self.client.machines, action)(m.id)
            self.assertEqual(got.state, state, action)
        self.client.machines.start(m.id, wait_s=5)
        self.assertEqual(self.api.requests("POST", "/start$")[-1].json(), {"wait_s": 5})
        renamed = self.client.machines.update(m.id, name="renamed", labels={"a": None, "c": "3"})
        self.assertEqual(self.api.requests("PATCH", "^/v1/machines/")[-1].json(), {"name": "renamed", "labels": {"a": None, "c": "3"}})
        self.assertEqual((renamed.name, renamed.labels), ("renamed", {"b": "2", "c": "3"}))
        kept = self.client.machines.backup(m.id, note="before")
        self.assertIsInstance(kept, Backup)
        self.assertEqual((kept.machine, kept.note, kept.raw["object"]), (m.id, "before", "backup"))
        self.assertEqual(self.api.requests("POST", "/backups$")[-1].json(), {"note": "before"})
        self.assertTrue(self.api.requests("POST", "/backups$")[-1].headers.get("idempotency-key"))
        # in a person's own account a fork is made at once: the machine, never a request that waits
        fork = self.client.machines.fork(m.id, name="the fork")
        assert isinstance(fork, Machine)
        self.assertNotEqual(fork.id, m.id)
        self.assertEqual(fork.name, "the fork")
        self.assertTrue(self.api.requests("POST", "/fork$")[-1].headers.get("idempotency-key"))
        # copy, fork's name before it was one, is the same call, by the fork route
        with self.assertWarns(DeprecationWarning):
            again = self.client.machines.copy(m.id, name="again")
        assert isinstance(again, Machine)
        self.assertEqual(again.name, "again")
        self.assertEqual(self.api.requests("POST", "/copy$"), [])
        # a fork from a backup starts afresh from its files, at the size asked
        restored = self.client.machines.fork(m.id, backup=kept, cpus=8)
        self.assertEqual(self.api.requests("POST", "/fork$")[-1].json(), {"backup": kept.id, "cpus": 8})
        assert isinstance(restored, Machine)
        self.assertEqual((restored.state, restored.cpus, restored.memory_gb), ("running", 8, 16))
        self.client.machines.delete(m.id)
        self.assertEqual(self.client.machines.get(m.id).state, "deleted")
        self.client.machines.delete(m.id)

    def test_backups_are_listed_the_newest_first_and_a_fork_is_made_from_one(self) -> None:
        m = self.client.machines.create(name="box", cpus=4)
        other = self.client.machines.create(name="other")
        first = self.client.machines.backup(m.id)
        self.assertEqual(self.api.requests("POST", "/backups$")[-1].body, b"")
        second = self.client.machines.backup(m.id, note="after the build")
        theirs = self.client.machines.backup(other.id)
        self.assertEqual([b.id for b in self.client.machines.backups(m.id)], [second.id, first.id])
        self.assertEqual([b.id for b in self.client.machines.backups()], [theirs.id, second.id, first.id])
        self.assertEqual(self.api.requests("GET", "^/v1/backups$")[-1].query, {})
        self.assertEqual([b.note for b in self.client.machines.backups(m.id, limit=1)], ["after the build"])
        self.assertEqual(self.api.requests("GET", "/backups$")[-1].query, {"limit": ["1"]})
        # its newest, by name, at the machine's own size
        fork = self.client.machines.fork(m.id, backup="latest", name="from the build")
        self.assertEqual(self.api.requests("POST", "/fork$")[-1].json(), {"name": "from the build", "backup": "latest"})
        assert isinstance(fork, Machine)
        self.assertEqual((fork.name, fork.cpus), ("from the build", 4))
        # what the API refuses: a size without a backup, another machine's backup, the newest of a machine with none
        with self.assertRaises(BadRequestError) as size:
            self.client.machines.fork(m.id, cpus=8)
        self.assertIn("cpus is only for a fork from a backup", str(size.exception))
        with self.assertRaises(NotFoundError):
            self.client.machines.fork(m.id, backup=theirs)
        with self.assertRaises(ConflictError) as none:
            self.client.machines.fork(fork.id, backup="latest")
        self.assertIn("no backup of it yet", str(none.exception))
        # a deleted machine's backups go with it
        self.client.machines.delete(other.id)
        self.assertEqual(self.client.machines.backups(other.id), [])
        self.assertEqual([b.id for b in self.client.machines.backups()], [second.id, first.id])
        with self.assertRaises(ValueError):
            self.client.machines.backups("")

    def test_an_id_goes_in_the_path_as_one_piece(self) -> None:
        with self.assertRaises(NotFoundError):
            self.client.machines.get("../keys")
        self.assertEqual(self.api.seen[-1].path, "/v1/machines/..%2Fkeys")
        with self.assertRaises(ValueError):
            self.client.machines.get("")


class Paging(APITest):
    def test_every_machine_comes_page_after_page(self) -> None:
        made = [self.api.add_machine(name=str(i))["id"] for i in range(7)]
        got = [m.id for m in self.client.machines.list(limit=3)]
        self.assertEqual(got, list(reversed(made)))
        pages = self.api.requests("GET", "^/v1/machines$")
        self.assertEqual([p.query.get("after") for p in pages], [None, [made[4]], [made[1]]])
        self.assertTrue(all(p.query["limit"] == ["3"] for p in pages))

    def test_a_page_is_asked_for_only_when_needed(self) -> None:
        for i in range(7):
            self.api.add_machine(name=str(i))
        first = list(itertools.islice(self.client.machines.list(limit=2), 3))
        self.assertEqual(len(first), 3)
        self.assertEqual(len(self.api.requests("GET", "^/v1/machines$")), 2)

    def test_filters_go_in_the_query(self) -> None:
        other = self.api.add_project("other")["id"]
        self.api.add_machine(project=other, labels={"team": "ml", "tier": "gpu"})
        self.api.add_machine(project=other, labels={"team": "web"})
        self.api.add_machine(labels={"team": "ml", "tier": "gpu"})
        got = list(self.client.machines.list(project=other, state="running", labels={"team": "ml", "tier": "gpu"}))
        self.assertEqual(len(got), 1)
        q = self.api.requests("GET", "^/v1/machines$")[-1].query
        self.assertEqual(q, {"project": [other], "state": ["running"], "label": ["team=ml", "tier=gpu"]})

    def test_an_empty_list(self) -> None:
        self.assertEqual(list(self.client.machines.list()), [])

    def test_projects_page_too(self) -> None:
        for name in ("b", "c", "d", "e"):
            self.api.add_project(name)
        names = [p.name for p in self.client.projects.list(limit=2)]
        self.assertEqual(names, ["Default", "b", "c", "d", "e"])
        self.assertEqual(len(self.api.requests("GET", "^/v1/projects$")), 3)


class Exec(APITest):
    def test_a_command_runs_and_its_output_comes_back(self) -> None:
        m = self.client.machines.create()
        r = self.client.machines.exec(m.id, ["echo", "hello"])
        self.assertEqual((r.exit_code, r.stdout, r.stderr, r.truncated), (0, "hello\n", "", False))
        self.assertEqual(r.duration_ms, 3.2)
        self.assertEqual(self.api.execs[-1], {"command": ["echo", "hello"]})
        self.assertNotIn("idempotency-key", self.api.requests("POST", "/exec$")[-1].headers)

    def test_a_shell_line_with_its_settings(self) -> None:
        m = self.client.machines.create()
        r = self.client.machines.exec(m.id, shell="ls | wc -l", env={"A": "1"}, cwd="/tmp", user="root")
        self.assertEqual(self.api.execs[-1], {"shell": "ls | wc -l", "env": {"A": "1"}, "cwd": "/tmp", "user": "root"})
        self.assertIn("ls | wc -l", r.stdout)

    def test_an_exit_code_is_an_answer_not_an_error(self) -> None:
        m = self.client.machines.create()
        r = self.client.machines.exec(m.id, ["exit", "3"])
        self.assertEqual((r.exit_code, r.stderr), (3, "bye\n"))

    def test_binary_output(self) -> None:
        import base64

        m = self.client.machines.create()
        r = self.client.machines.exec(m.id, ["cat", "blob"], binary_output=True)
        self.assertEqual(self.api.execs[-1], {"command": ["cat", "blob"], "binary_output": True})
        self.assertEqual(base64.b64decode(r.stdout_base64 or ""), b"\x00\x01\xffbinary")
        self.assertEqual(r.stdout, "")

    def test_command_or_shell_one_of_the_two(self) -> None:
        m = self.client.machines.create()
        with self.assertRaises(ValueError):
            self.client.machines.exec(m.id)
        with self.assertRaises(ValueError):
            self.client.machines.exec(m.id, ["ls"], shell="ls")
        with self.assertRaises(TypeError):
            self.client.machines.exec(m.id, "ls -la")  # type: ignore[arg-type]
        self.assertEqual(self.api.execs, [])


class AccountAndCatalogue(APITest):
    def test_regions_gpus_and_usage(self) -> None:
        regions = self.client.regions()
        self.assertEqual([(r.id, r.city, r.country) for r in regions][0], ("mumbai", "Mumbai", "IN"))
        gpus = self.client.gpus()
        self.assertFalse(gpus.for_sale)
        offer = gpus.data[0]
        self.assertEqual((offer.id, offer.model, offer.count, offer.memory_gb, offer.price_per_hour_usd), ("h100x1", "H100 80GB", 1, 80, 2.53))
        assert offer.location is not None
        self.assertEqual(offer.location.city, "Helsinki")
        self.client.machines.create()
        usage = self.client.usage(month=date(2026, 10, 1), project=self.api.first_project)
        self.assertEqual(self.api.seen[-1].query, {"month": ["2026-10"], "project": [self.api.first_project]})
        self.assertEqual((usage.month, len(usage.machines), usage.total_usd), ("2026-10", 1, 0.01))
        self.assertEqual(usage.machines[0].running_seconds, 60.0)

    def test_limits(self) -> None:
        limits = self.client.limits()
        self.assertEqual((limits.account.limited, limits.account.month_cap_usd, limits.alert_percents), (False, None, [50, 75, 90, 100]))
        limits = self.client.set_month_limit(50)
        self.assertEqual(self.api.seen[-1].json(), {"month_cap_usd": 50})
        self.assertEqual((limits.account.month_cap_usd, limits.account.in_force, limits.account.percent), (50, "account", 25))
        self.assertEqual((limits.yours[0].scope, limits.yours[0].limit_usd), ("account", 50))
        self.client.set_month_limit(None)
        self.assertEqual(self.api.seen[-1].json(), {"month_cap_usd": None})

    def test_projects(self) -> None:
        p = self.client.projects.create("research")
        self.assertTrue(self.api.requests("POST", "^/v1/projects$")[-1].headers.get("idempotency-key"))
        self.assertEqual(self.client.projects.get(p.id).name, "research")
        self.assertEqual(self.client.projects.update(p.id, name="lab").name, "lab")
        self.client.projects.delete(p.id)
        with self.assertRaises(NotFoundError):
            self.client.projects.get(p.id)

    def test_keys_and_the_secret_shown_once(self) -> None:
        when = datetime(2026, 12, 31, 23, 59, 59, tzinfo=timezone(timedelta(hours=5, minutes=30)))
        made = self.client.keys.create(name="ci", scopes=["cloud:machines:read"], expires_at=when, rate_per_minute=60)
        body = self.api.requests("POST", "^/v1/keys$")[-1].json()
        self.assertEqual(body, {"name": "ci", "scopes": ["cloud:machines:read"], "expires_at": "2026-12-31T18:29:59Z", "rate_per_minute": 60})
        self.assertEqual(made.secret, "ak_live_" + "s" * 40)
        self.assertNotIn(made.secret, repr(made))
        self.assertEqual([k.id for k in self.client.keys.list()], [made.id])
        self.client.keys.revoke(made.id)
        self.assertEqual(self.client.keys.list(), [])
        with self.assertRaises(ValueError):
            self.client.keys.create(name="x", scopes=["cloud:*"], expires_at=datetime(2026, 12, 31))



class PauseReasons(unittest.TestCase):
    def test_a_machine_paused_at_a_limit_says_why_and_one_paused_by_its_holder_says_nothing(self) -> None:
        why = "Your limit for this month is reached. Your machines run again on 1 December, or once you raise it."
        limit = {"scope": "account", "measure": "month_usd", "limit": 20.0, "used": 20.0,
                 "resets_at": "2026-12-01T00:00:00Z", "raised_by": ["owner"]}
        m = wheelmachine.Machine.from_dict({"id": "m_1", "name": "a", "project": "p", "state": "paused",
                                            "pause_reason": {"why": why, "limit": limit}})
        self.assertIsInstance(m.pause_reason, wheelmachine.PauseReason)
        self.assertEqual((m.pause_reason.why, m.pause_reason.limit["scope"]), (why, "account"))
        self.assertIsNone(wheelmachine.Machine.from_dict({"id": "m_2", "name": "b", "project": "p", "state": "paused",
                                                          "pause_reason": None}).pause_reason)


if __name__ == "__main__":
    unittest.main()
