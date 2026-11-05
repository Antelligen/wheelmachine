"""A machine's ports at web addresses of their own: opened, listed and closed, and one-time links into a private one."""

from __future__ import annotations

import unittest

from support import APITest, closed_port

from wheelmachine import (
    APIConnectionError,
    APITimeoutError,
    BadRequestError,
    NotFoundError,
    Port,
    PortLink,
    RateLimitError,
    UnavailableError,
)


class OnAMachine(APITest):
    """A machine made in the stand-in itself, so that no call is seen before a test's own."""

    def setUp(self) -> None:
        super().setUp()
        self.machine = self.api.add_machine()["id"]


class Ports(OnAMachine):
    def opens(self):  # type: ignore[no-untyped-def]
        return self.api.requests("POST", "/ports$")

    def test_a_port_opens_private_and_opened_again_is_made_as_asked(self) -> None:
        port = self.client.ports.open(self.machine, 8080)
        seen = self.opens()[-1]
        self.assertEqual(seen.path, "/v1/machines/{}/ports".format(self.machine))
        self.assertEqual(seen.json(), {"port": 8080})
        self.assertNotIn("idempotency-key", seen.headers)
        self.assertIsInstance(port, Port)
        self.assertEqual((port.machine, port.port, port.public), (self.machine, 8080, False))
        self.assertEqual(port.url, "https://8080-{}.run.wheelmachine.ai".format(self.machine[2:]))
        self.assertEqual((port.created_at, port.raw["object"]), ("2026-10-23T07:00:00Z", "port"))
        self.assertEqual(port.last_response.status if port.last_response else None, 201)
        made_public = self.client.ports.open(self.machine, 8080, public=True)
        self.assertEqual(self.opens()[-1].json(), {"port": 8080, "public": True})
        self.assertEqual((made_public.public, made_public.url), (True, port.url))
        self.assertEqual(made_public.last_response.status if made_public.last_response else None, 200)
        made_private = self.client.ports.open(self.machine, 8080)
        self.assertEqual(self.opens()[-1].json(), {"port": 8080})
        self.assertFalse(made_private.public)
        self.assertEqual(made_private.last_response.status if made_private.last_response else None, 200)
        self.assertEqual(list(self.api.ports[self.machine]), [8080])

    def test_the_open_ports_come_lowest_first(self) -> None:
        self.assertEqual(self.client.ports.list(self.machine), [])
        for n, public in ((9000, False), (3000, True), (8080, False)):
            self.client.ports.open(self.machine, n, public=public)
        got = self.client.ports.list(self.machine)
        self.assertEqual(self.api.seen[-1].path, "/v1/machines/{}/ports".format(self.machine))
        self.assertEqual([(p.port, p.public) for p in got], [(3000, True), (8080, False), (9000, False)])
        self.assertTrue(all(isinstance(p, Port) and p.machine == self.machine for p in got))

    def test_a_port_closes_and_closing_one_not_open_is_not_an_error(self) -> None:
        self.client.ports.open(self.machine, 8080)
        self.client.ports.open(self.machine, 9000)
        self.assertIsNone(self.client.ports.close(self.machine, 8080))
        seen = self.api.seen[-1]
        self.assertEqual((seen.method, seen.path), ("DELETE", "/v1/machines/{}/ports/8080".format(self.machine)))
        self.assertEqual([p.port for p in self.client.ports.list(self.machine)], [9000])
        self.client.ports.close(self.machine, 8080)
        with self.assertRaises(NotFoundError):
            self.client.ports.list("m_" + "9" * 26)

    def test_an_open_whose_answer_was_lost_is_sent_again(self) -> None:
        # a port open already is left as asked, so sending the call twice does what sending it once does
        self.api.fault("POST", "/ports$", drop="after")
        self.api.fault("POST", "/ports$", status=504, code="timeout")
        port = self.client.ports.open(self.machine, 8080, public=True)
        self.assertEqual((port.port, port.public), (8080, True))
        self.assertEqual(port.last_response.status if port.last_response else None, 200)
        self.assertEqual(len(self.opens()), 3)
        self.assertEqual(list(self.api.ports[self.machine]), [8080])
        self.assertEqual(self.sleeps, [0.25, 0.5])

    def test_the_ports_the_machine_uses_and_one_past_twenty_are_the_apis_to_refuse(self) -> None:
        with self.assertRaises(BadRequestError) as caught:
            self.client.ports.open(self.machine, 5900)
        self.assertEqual(caught.exception.code, "invalid_request")
        for n in range(3000, 3020):
            self.client.ports.open(self.machine, n)
        with self.assertRaises(RateLimitError) as caught:
            self.client.ports.open(self.machine, 3020)
        self.assertEqual(caught.exception.code, "limit_reached")
        self.assertEqual(self.sleeps, [])  # a limit of the machine's, not of the moment: not tried again
        again = self.client.ports.open(self.machine, 3000, public=True)  # one open already is still answered
        self.assertEqual((again.public, again.last_response.status if again.last_response else None), (True, 200))

    def test_until_web_addresses_are_served_the_answer_is_not_open_and_it_is_not_tried_again(self) -> None:
        self.api.fault("POST", "/ports$", status=503, code="not_open", detail="not served yet", retry_after=1)
        with self.assertRaises(UnavailableError) as caught:
            self.client.ports.open(self.machine, 8080)
        e = caught.exception
        self.assertEqual((e.status, e.code, e.detail), (503, "not_open", "not served yet"))
        self.assertEqual(len(self.opens()), 1)
        self.assertEqual(self.sleeps, [])
        # a 503 that is a moment's is tried again all the same
        self.api.fault("POST", "/ports$", status=503, code="unavailable", retry_after=1)
        self.assertEqual(self.client.ports.open(self.machine, 8080).port, 8080)
        self.assertEqual(self.sleeps, [1.0])
        self.api.fault("POST", "/link$", status=503, code="not_open", retry_after=1)
        with self.assertRaises(UnavailableError) as caught:
            self.client.ports.link(self.machine, 8080)
        self.assertEqual(caught.exception.code, "not_open")
        self.assertEqual(len(self.api.requests("POST", "/link$")), 1)
        self.assertEqual(self.sleeps, [1.0])

    def test_a_port_that_is_not_one_is_refused_before_any_call(self) -> None:
        calls = {
            "open": lambda port: self.client.ports.open(self.machine, port),
            "close": lambda port: self.client.ports.close(self.machine, port),
            "link": lambda port: self.client.ports.link(self.machine, port),
        }
        bad = ((0, ValueError), (65536, ValueError), (-80, ValueError),
               ("8080", TypeError), (8080.0, TypeError), (True, TypeError), (None, TypeError))
        for name, call in calls.items():
            for port, error in bad:
                with self.subTest(call=name, port=port), self.assertRaises(error):
                    call(port)
        for no_machine in (
            lambda: self.client.ports.open("", 8080),
            lambda: self.client.ports.list(""),
            lambda: self.client.ports.close("", 8080),
            lambda: self.client.ports.link("", 8080),
        ):
            with self.assertRaises(ValueError):
                no_machine()
        with self.assertRaises(TypeError):
            self.client.ports.open(self.machine, 8080, public="false")  # type: ignore[arg-type]
        self.assertEqual(self.api.seen, [])
        self.assertEqual([self.client.ports.open(self.machine, n).port for n in (1, 65535)], [1, 65535])


class Links(OnAMachine):
    def calls(self):  # type: ignore[no-untyped-def]
        return self.api.requests("POST", "/link$")

    def test_a_link_lets_one_browser_in_and_is_never_in_a_repr(self) -> None:
        port = self.client.ports.open(self.machine, 8080)
        link = self.client.ports.link(self.machine, 8080)
        seen = self.calls()[-1]
        self.assertEqual(seen.path, "/v1/machines/{}/ports/8080/link".format(self.machine))
        self.assertEqual((seen.body, seen.headers["content-length"]), (b"", "0"))
        self.assertNotIn("idempotency-key", seen.headers)
        self.assertIsInstance(link, PortLink)
        self.assertEqual((link.machine, link.port, link.access_s), (self.machine, 8080, 1800))
        self.assertEqual(link.expires_at, "2026-10-23T07:05:00Z")
        self.assertEqual(link.last_response.status if link.last_response else None, 201)
        # checked without being shown: a failure here names no link
        self.assertTrue(link.url.startswith(port.url + "/.wheelmachine/link/"), "the link is not to the port's address")
        token = link.url.rsplit("/", 1)[-1]
        for text in (repr(link), str(link)):
            self.assertFalse(link.url in text or token in text, "the link is in its repr")
        other = self.client.ports.link(self.machine, 8080, access_s=600, path="/app?tab=logs")
        self.assertEqual(self.calls()[-1].json(), {"access_s": 600, "path": "/app?tab=logs"})
        self.assertEqual(other.access_s, 600)
        self.assertTrue(other.url != link.url, "two calls gave the same link")
        self.assertEqual(len(self.api.links), 2)

    def test_a_link_that_may_have_reached_the_api_is_not_sent_again(self) -> None:
        self.client.ports.open(self.machine, 8080)
        self.api.fault("POST", "/link$", drop="after")
        with self.assertRaises(APIConnectionError) as caught:
            self.client.ports.link(self.machine, 8080)
        self.assertNotIsInstance(caught.exception, APITimeoutError)
        self.assertIn("lost", str(caught.exception))
        self.assertEqual(len(self.calls()), 1)
        self.assertEqual(len(self.api.links), 1)  # made, and its answer lost: not made a second time
        # an answer came, so it reached the API: not sent again even when refused for a moment (a second try here
        # would be answered with a link, and the test would see it)
        for status, code, error in ((504, "timeout", UnavailableError), (503, "unavailable", UnavailableError),
                                    (429, "rate_limited", RateLimitError)):
            self.api.fault("POST", "/link$", status=status, code=code, retry_after=1)
            with self.subTest(status=status), self.assertRaises(error):
                self.client.ports.link(self.machine, 8080)
        self.assertEqual(len(self.calls()), 4)
        self.assertEqual(len(self.api.links), 1)
        self.assertEqual(self.sleeps, [])

    def test_a_link_that_never_left_is_tried_again(self) -> None:
        client = self.make_client(base_url="http://127.0.0.1:{}".format(closed_port()), max_retries=2)
        with self.assertRaises(APIConnectionError) as caught:
            client.ports.link(self.machine, 8080)
        self.assertIn("could not reach", str(caught.exception))
        self.assertEqual(self.sleeps, [0.25, 0.5])


if __name__ == "__main__":
    unittest.main()
