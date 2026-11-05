"""What every test shares: a fake API of its own, and a client of it whose pauses are counted, never slept."""

from __future__ import annotations

import socket
import unittest
from typing import Any, List

from fake_api import KEY, FakeAPI

from wheelmachine import Wheelmachine


class APITest(unittest.TestCase):
    max_retries = 3

    def setUp(self) -> None:
        self.api = FakeAPI().start()
        self.client = self.make_client()

    def make_client(self, **kw: Any) -> Wheelmachine:
        kw.setdefault("api_key", KEY)
        kw.setdefault("base_url", self.api.url)
        kw.setdefault("max_retries", self.max_retries)
        client = Wheelmachine(**kw)
        self.sleeps: List[float] = []
        client._sleep = self.sleeps.append  # type: ignore[assignment]
        client._jitter = lambda: 0.5  # type: ignore[assignment]
        self.addCleanup(client.close)
        return client

    def tearDown(self) -> None:
        self.client.close()
        self.api.stop()


def closed_port() -> int:
    """A port on this computer that nothing listens on."""
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return int(port)
