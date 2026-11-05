"""A program signs in through the browser (as `wm login` does): no key sent, the device code out of every repr, asked
about at the API's pace, and the key given once - or the person's refusal, or the code running out, said as such."""

from __future__ import annotations

import unittest

from fake_api import KEY
from support import APITest

from wheelmachine import DeviceLogin, PermissionDeniedError, WheelmachineError


class DeviceLoginTest(APITest):
    def start(self, **kw):
        login = DeviceLogin.start(base_url=self.api.url, **kw)
        self.waits = []
        login._client._sleep = self.waits.append  # type: ignore[assignment]
        self.addCleanup(login.close)
        return login

    def test_a_program_is_given_its_key_once_the_person_approves(self) -> None:
        self.api.decide = lambda ask: "pending" if ask == 1 else "slow" if ask == 2 else "approved"
        login = self.start(name="tool on box", scopes=["cloud:machines:read"])
        self.assertEqual(login.user_code, "BCDF-GHJK")
        self.assertEqual(login.verification_uri, self.api.url + "/device")
        self.assertFalse(hasattr(login, "verification_uri_complete"), "the code is never carried in an address")
        self.assertNotIn("wmd_", repr(login))
        key = login.wait()
        self.assertEqual(key.secret, KEY)
        self.assertEqual(key.name, "tool on box")
        self.assertEqual(key.scopes, ["cloud:machines:read"])
        self.assertNotIn(KEY, repr(key))
        self.assertEqual(self.waits, [5.0, 5.0, 5.0], "every 5 seconds, and the API's own wait when it asked for one")
        started = self.api.requests("POST", "/v1/login/devices")
        asked = self.api.requests("POST", "/v1/login/devices/token")
        self.assertEqual(started[0].json(), {"name": "tool on box", "scopes": ["cloud:machines:read"]})
        self.assertEqual(len(asked), 3)
        for seen in started + asked:
            self.assertNotIn("authorization", seen.headers, "no key is sent to sign in")
        with self.assertRaises(WheelmachineError) as again:
            login.poll()
        self.assertEqual(again.exception.code, "login_expired", "the key is given once")

    def test_a_refusal_or_a_code_that_ran_out_is_said(self) -> None:
        self.api.decide = lambda ask: "denied"
        with self.assertRaises(PermissionDeniedError) as refused:
            self.start().wait()
        self.assertEqual(refused.exception.code, "login_denied")
        self.api.decide = lambda ask: "expired"
        with self.assertRaises(WheelmachineError) as late:
            self.start().wait()
        self.assertEqual(late.exception.code, "login_expired")
        # an account that holds as many keys as it may: said as such, not taken for a request to slow down
        self.api.decide = lambda ask: "full"
        with self.assertRaises(WheelmachineError) as full:
            self.start().wait()
        self.assertEqual(full.exception.code, "limit_reached")


if __name__ == "__main__":
    unittest.main()
