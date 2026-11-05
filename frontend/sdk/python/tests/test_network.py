"""A machine's internet policy through the library: it is set on create and changed on update, and the
machine shows it."""

from __future__ import annotations

from support import APITest

from wheelmachine import BadRequestError, NetworkPolicy


class Network(APITest):
    def test_a_machine_is_made_with_a_policy_and_shows_it(self) -> None:
        m = self.client.machines.create(network={"internet": "allowed", "allow": ["api.github.com", "10.0.0.0/8"]})
        self.assertIsInstance(m.network, NetworkPolicy)
        self.assertEqual(m.network.internet, "allowed")
        self.assertEqual(m.network.allow, ["api.github.com", "10.0.0.0/8"])
        post = self.api.requests("POST", "/v1/machines")[-1]
        self.assertEqual(post.json()["network"], {"internet": "allowed", "allow": ["api.github.com", "10.0.0.0/8"]})

    def test_an_open_machine_sends_no_policy_and_reads_back_open(self) -> None:
        m = self.client.machines.create()
        self.assertEqual(m.network.internet, "open")
        body = self.api.requests("POST", "/v1/machines")[-1].json()
        self.assertTrue(body is None or "network" not in body)

    def test_a_policy_is_changed_on_update(self) -> None:
        m = self.client.machines.create()
        changed = self.client.machines.update(m.id, network=NetworkPolicy(internet="closed", allow=[]))
        self.assertEqual(changed.network.internet, "closed")
        patch = self.api.requests("PATCH", "/v1/machines/" + m.id)[-1]
        self.assertEqual(patch.json()["network"], {"internet": "closed", "allow": []})

    def test_a_bad_policy_is_refused_before_the_call(self) -> None:
        with self.assertRaises(ValueError):
            self.client.machines.create(network={"internet": "maybe"})
        self.assertEqual(self.api.requests("POST", "/v1/machines"), [])
        # the API refuses a list with a closed policy
        with self.assertRaises(BadRequestError):
            self.client.machines.create(network={"internet": "closed", "allow": ["x.com"]})
