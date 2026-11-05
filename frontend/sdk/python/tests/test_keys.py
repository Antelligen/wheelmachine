"""Keys: one with full access needs the code we e-mail its person - asked for as its error says, and carried with
the same call."""

from __future__ import annotations

import unittest

from support import APITest

from wheelmachine import PermissionDeniedError


class KeysTest(APITest):
    def test_a_key_with_full_access_is_made_with_the_code_we_email(self) -> None:
        self.api.confirm_code = "482913"
        with self.assertRaises(PermissionDeniedError) as refused:
            self.client.keys.create(name="deploy", scopes=["cloud:*"])
        self.assertEqual(refused.exception.code, "confirm_required")
        ask = refused.exception.confirm
        self.assertEqual(ask, {"step": "full_access_key", "account": "acc_01jb2xk3n4p5q6r7s8t9v0w1x2"})
        sent = self.client.confirm_codes.send(**ask)
        self.assertEqual((sent.step, sent.sent_to), ("full_access_key", "a…@example.com"))
        self.assertEqual(self.api.seen[-1].json(), ask)
        with self.assertRaises(PermissionDeniedError) as wrong:
            self.client.keys.create(name="deploy", scopes=["cloud:*"], confirm_code="111111")
        self.assertEqual(wrong.exception.code, "confirm_wrong")
        made = self.client.keys.create(name="deploy", scopes=["cloud:*"], confirm_code="482913")
        self.assertTrue(made.secret.startswith("ak_live_"))
        self.assertEqual(self.api.seen[-1].headers.get("x-confirm-code"), "482913")
        # a key that does less needs none
        self.client.keys.create(name="reader", scopes=["cloud:machines:read"])


if __name__ == "__main__":
    unittest.main()
