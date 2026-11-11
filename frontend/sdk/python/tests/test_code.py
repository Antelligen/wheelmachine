"""Code access: the code hosts an account connects, what a connection reaches, and a team given a repository."""

from __future__ import annotations

import unittest

from support import APITest

from wheelmachine import NotFoundError


class CodeAccess(APITest):
    def test_the_code_hosts_what_they_reach_and_a_team_given_a_repository(self) -> None:
        code = self.client.code()
        self.assertTrue(code.github)
        self.assertEqual([(c.id, c.provider, c.name) for c in code.connections], [("code_1", "github", "acme")])
        self.assertEqual(self.client.code_repos("code_1"), ["acme/api"])
        g = self.client.grant_code("prj_00000000000000000000000001", "code_1", "acme/api", "write")
        self.assertEqual((g.project, g.repo, g.access), ("prj_00000000000000000000000001", "acme/api", "write"))
        self.assertEqual(self.api.requests("PUT", "/v1/projects/prj_00000000000000000000000001/code")[-1].json(),
                         {"connection": "code_1", "repo": "acme/api", "access": "write"})

    def test_what_a_machine_reaches_and_a_pull_request_from_it(self) -> None:
        m = self.client.machines.create(name="box")
        self.api.repositories = [
            {"object": "machine_repository", "provider": "github", "host": "github.com", "repository": "acme/api", "access": "write", "linked": True,
             "can_push": True, "push": None},
            {"object": "machine_repository", "provider": "gitlab", "host": "gitlab.com", "repository": "team/site", "access": "write", "linked": False,
             "can_push": False, "push": None},
            {"object": "machine_repository", "provider": "github", "host": "github.com", "repository": "acme/docs", "access": "read", "linked": True,
             "can_push": False, "push": {"branches": ["agent/**"], "tags": False}},
        ]
        seen = self.client.machines.repositories(m.id)
        self.assertEqual([(r.repository, r.access, r.linked, r.can_push) for r in seen.data],
                         [("acme/api", "write", True, True), ("team/site", "write", False, False), ("acme/docs", "read", True, False)])
        self.assertEqual(seen.protected_branches, ["main", "master", "release/**"])
        self.assertIsNone(seen.data[0].push)
        self.assertEqual((seen.data[2].push.branches, seen.data[2].push.tags), (["agent/**"], False))

        pull = self.client.machines.open_pull_request(m.id, "acme/api", head="agent/a", base="main", title="A change", body="Why.")
        self.assertTrue(pull.opened)
        self.assertEqual((pull.head, pull.base, pull.draft, pull.run), ("agent/a", "main", False, None))
        self.assertTrue(pull.url.endswith("/acme/api/pull/" + str(pull.number)))
        sent = self.api.requests("POST", "/v1/machines/" + m.id + "/pull-requests")[-1]
        self.assertEqual(sent.json(), {"repository": "acme/api", "head": "agent/a", "base": "main", "title": "A change", "body": "Why."})
        self.assertTrue(sent.headers.get("idempotency-key"), "a retry never makes two")

        # on GitLab, the push that opens it
        lab = self.client.machines.open_pull_request(m.id, "team/site", head="agent/a", base="main", title="A change", draft=True, host="gitlab.com")
        self.assertFalse(lab.opened)
        self.assertEqual((lab.number, lab.url, lab.draft), (None, None, True))
        self.assertIn("merge_request.create", lab.run)
        self.assertEqual(self.api.requests("POST", "/v1/machines/" + m.id + "/pull-requests")[-1].json(),
                         {"repository": "team/site", "host": "gitlab.com", "head": "agent/a", "base": "main", "title": "A change", "draft": True})

        with self.assertRaises(NotFoundError):
            self.client.machines.open_pull_request(m.id, "acme/docs", head="agent/a", base="main", title="A change")


if __name__ == "__main__":
    unittest.main()
