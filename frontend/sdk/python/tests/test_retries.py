"""Tried again when it is safe and worth it, never so that one call makes two; kept connections; proxies."""

from __future__ import annotations

import base64
import os
import re
import time
import unittest
from unittest import mock

from fake_api import KEY, ConnectProxy
from support import APITest, closed_port

from wheelmachine import (
    APIConnectionError,
    APITimeoutError,
    ConflictError,
    RateLimitError,
    UnavailableError,
    Wheelmachine,
)
from wheelmachine._http import Target, proxy_from_environment

UUID4 = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")


class Retries(APITest):
    def creates(self):  # type: ignore[no-untyped-def]
        return self.api.requests("POST", "^/v1/machines$")

    def test_a_503_is_tried_again_after_its_retry_after_with_the_same_idempotency_key(self) -> None:
        self.api.fault("POST", "^/v1/machines$", status=503, code="unavailable", retry_after=2)
        m = self.client.machines.create(name="once")
        self.assertEqual(m.state, "running")
        self.assertEqual(self.sleeps, [2.0])
        keys = [s.headers.get("idempotency-key") for s in self.creates()]
        self.assertEqual(len(keys), 2)
        self.assertEqual(keys[0], keys[1])
        self.assertRegex(keys[0] or "", UUID4)
        self.assertEqual(len(self.api.machines), 1)

    def test_each_create_has_its_own_key(self) -> None:
        self.client.machines.create()
        self.client.machines.create()
        keys = {s.headers.get("idempotency-key") for s in self.creates()}
        self.assertEqual(len(keys), 2)

    def test_a_create_whose_answer_was_lost_is_given_back_not_made_twice(self) -> None:
        self.api.fault("POST", "^/v1/machines$", drop="after")
        m = self.client.machines.create(name="once")
        self.assertEqual(len(self.api.machines), 1)
        self.assertEqual(list(self.api.machines), [m.id])
        keys = [s.headers.get("idempotency-key") for s in self.creates()]
        self.assertEqual(len(keys), 2)
        self.assertEqual(keys[0], keys[1])
        assert m.last_response is not None
        self.assertTrue(m.last_response.replayed)
        self.assertEqual(self.sleeps, [0.25])  # backoff: up to 0.5 s, the jitter at a half

    def test_a_key_given_is_the_key_sent(self) -> None:
        self.client.projects.create("p", idempotency_key="my-own-key-1")
        self.assertEqual(self.api.requests("POST", "^/v1/projects$")[-1].headers["idempotency-key"], "my-own-key-1")

    def test_a_create_still_running_is_waited_for(self) -> None:
        self.api.fault("POST", "^/v1/machines$", status=409, code="conflict", retry_after=1, times=4)
        m = self.client.machines.create()
        self.assertEqual(m.state, "running")
        self.assertEqual(self.sleeps, [1.0, 1.0, 1.0, 1.0])  # does not spend the three retries
        self.assertEqual(len({s.headers.get("idempotency-key") for s in self.creates()}), 1)

    def test_a_conflict_without_retry_after_is_the_answer(self) -> None:
        m = self.client.machines.create()
        self.api.fault("POST", "/pause$", status=409, code="conflict")
        with self.assertRaises(ConflictError):
            self.client.machines.pause(m.id)
        self.assertEqual(self.sleeps, [])

    def test_an_exec_whose_answer_was_lost_is_not_sent_again(self) -> None:
        m = self.client.machines.create()
        self.api.fault("POST", "/exec$", drop="after")
        with self.assertRaises(APIConnectionError) as caught:
            self.client.machines.exec(m.id, ["echo", "once"])
        self.assertNotIsInstance(caught.exception, APITimeoutError)
        self.assertEqual(len(self.api.execs), 1)
        self.assertEqual(self.sleeps, [])
        self.assertIn("lost", str(caught.exception))

    def test_an_exec_refused_with_504_is_not_sent_again(self) -> None:
        m = self.client.machines.create()
        self.api.fault("POST", "/exec$", status=504, code="timeout", times=5)
        with self.assertRaises(UnavailableError):
            self.client.machines.exec(m.id, ["sleep", "999"])
        self.assertEqual(len(self.api.requests("POST", "/exec$")), 1)

    def test_an_exec_refused_with_503_is_tried_again(self) -> None:
        m = self.client.machines.create()
        self.api.fault("POST", "/exec$", status=503, code="unavailable", retry_after=1)
        r = self.client.machines.exec(m.id, ["echo", "hi"])
        self.assertEqual(r.stdout, "hi\n")
        self.assertEqual(self.sleeps, [1.0])
        self.assertEqual(len(self.api.execs), 1)

    def test_a_read_that_lost_its_answer_is_tried_again(self) -> None:
        m = self.client.machines.create()
        self.api.fault("GET", "^/v1/machines/m_[0-9]+$", drop="before")
        self.api.fault("GET", "^/v1/machines/m_[0-9]+$", status=504, code="timeout")
        self.assertEqual(self.client.machines.get(m.id).id, m.id)
        self.assertEqual(self.sleeps, [0.25, 0.5])

    def test_429_is_tried_again_up_to_max_retries(self) -> None:
        self.api.fault("GET", "^/v1/me$", status=429, code="rate_limited", retry_after=1, times=10)
        with self.assertRaises(RateLimitError) as caught:
            self.client.me()
        e = caught.exception
        self.assertEqual((e.status, e.code, e.retry_after), (429, "rate_limited", 1.0))
        self.assertEqual(e.request_id, self.api.seen[-1].rid)
        self.assertEqual(self.sleeps, [1.0, 1.0, 1.0])
        self.assertEqual(len(self.api.requests("GET", "^/v1/me$")), 4)

    def test_backoff_grows_when_no_retry_after_is_given(self) -> None:
        self.api.fault("GET", "^/v1/me$", status=503, code="unavailable", times=10)
        with self.assertRaises(UnavailableError):
            self.client.me()
        self.assertEqual(self.sleeps, [0.25, 0.5, 1.0])

    def test_a_long_retry_after_is_not_waited_out(self) -> None:
        self.api.fault("POST", "^/v1/machines$", status=503, code="unavailable", retry_after=120)
        with self.assertRaises(UnavailableError) as caught:
            self.client.machines.create()
        self.assertEqual(caught.exception.retry_after, 120.0)
        self.assertEqual(self.sleeps, [])
        self.assertRegex(caught.exception.idempotency_key or "", UUID4)

    def test_a_limit_of_the_account_is_not_tried_again(self) -> None:
        self.api.fault("POST", "^/v1/machines$", status=429, code="limit_reached")
        with self.assertRaises(RateLimitError) as caught:
            self.client.machines.create()
        self.assertEqual(caught.exception.code, "limit_reached")
        self.assertEqual(self.sleeps, [])

    def test_a_503_not_open_is_not_tried_again_whatever_the_call(self) -> None:
        # something not served yet, not busy: waiting does not change it, whatever Retry-After says
        self.api.fault("GET", "^/v1/me$", status=503, code="not_open", retry_after=1)
        with self.assertRaises(UnavailableError) as caught:
            self.client.me()
        self.assertEqual((caught.exception.status, caught.exception.code), (503, "not_open"))
        self.assertEqual(self.sleeps, [])
        self.assertEqual(len(self.api.requests("GET", "^/v1/me$")), 1)

    def test_no_retries_when_asked_for_none(self) -> None:
        client = self.make_client(max_retries=0)
        self.api.fault("GET", "^/v1/me$", status=503, code="unavailable", retry_after=1)
        with self.assertRaises(UnavailableError):
            client.me()
        self.assertEqual(self.sleeps, [])

    def test_an_address_that_cannot_be_reached(self) -> None:
        client = self.make_client(base_url="http://127.0.0.1:{}".format(closed_port()), max_retries=2)
        with self.assertRaises(APIConnectionError) as caught:
            client.me()
        self.assertIn("could not reach", str(caught.exception))
        self.assertEqual(self.sleeps, [0.25, 0.5])
        # nothing was sent, so even a command is tried again
        self.sleeps.clear()
        with self.assertRaises(APIConnectionError):
            client.machines.exec("m_" + "0" * 26, ["true"])
        self.assertEqual(len(self.sleeps), 2)

    def test_no_answer_in_time(self) -> None:
        client = self.make_client(timeout=0.3, max_retries=0)
        self.api.fault("GET", "^/v1/me$", delay=1.5)
        with self.assertRaises(APITimeoutError) as caught:
            client.me()
        self.assertIn("within 0.3 seconds", str(caught.exception))


class Connections(APITest):
    def test_calls_share_one_kept_connection(self) -> None:
        m = self.client.machines.create()
        for _ in range(4):
            self.client.machines.get(m.id)
        self.client.machines.write_file(m.id, "work/a", b"x")
        self.client.machines.read_file(m.id, "work/a")
        self.assertEqual(self.api.connections, 1)

    def test_a_connection_the_server_closed_is_not_used_again(self) -> None:
        self.api.close_after_answer = True
        m = self.client.machines.create()
        time.sleep(0.05)  # the server closes the idle connection meanwhile
        self.client.machines.exec(m.id, ["echo", "1"])
        time.sleep(0.05)
        self.client.machines.exec(m.id, ["echo", "2"])
        self.assertEqual(len(self.api.execs), 2)
        self.assertEqual(self.sleeps, [])
        self.assertEqual(self.api.connections, 3)

    def test_a_closed_client_opens_new_connections(self) -> None:
        self.client.me()
        self.client.close()
        self.client.me()
        self.assertEqual(self.api.connections, 2)


class Proxies(APITest):
    def test_a_proxy_is_reached_with_connect_and_its_password(self) -> None:
        proxy = ConnectProxy()
        self.addCleanup(proxy.close)
        client = self.make_client(proxy="http://someone:pa%20ss@127.0.0.1:{}".format(proxy.port))
        self.assertEqual(client.me().account.id, "acct_test")
        method, target, headers = proxy.seen[0]
        self.assertEqual((method, target), ("CONNECT", "127.0.0.1:{}".format(self.api.server.server_address[1])))
        self.assertEqual(headers["proxy-authorization"], "Basic " + base64.b64encode(b"someone:pa ss").decode())
        self.assertNotIn("pa ss", repr(client))

    def test_the_environment_names_the_proxy_for_https(self) -> None:
        api = Target("https://api.wheelmachine.ai")
        with mock.patch.dict(os.environ, {"HTTPS_PROXY": "http://proxy.example:3128", "NO_PROXY": ""}):
            p = proxy_from_environment(api)
            assert p is not None
            self.assertEqual((p.host, p.port), ("proxy.example", 3128))
            self.assertIsNone(proxy_from_environment(Target("http://127.0.0.1:8080")))
        with mock.patch.dict(os.environ, {"HTTPS_PROXY": "http://proxy.example:3128", "NO_PROXY": "wheelmachine.ai"}):
            self.assertIsNone(proxy_from_environment(api))


class Construction(unittest.TestCase):
    def test_bad_settings(self) -> None:
        with self.assertRaises(ValueError):
            Wheelmachine(api_key=KEY, timeout=0)
        with self.assertRaises(ValueError):
            Wheelmachine(api_key=KEY, max_retries=-1)


if __name__ == "__main__":
    unittest.main()
