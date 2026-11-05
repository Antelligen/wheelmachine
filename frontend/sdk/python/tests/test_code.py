"""Code access: the code hosts an account connects, what a connection reaches, and a team given a repository."""

from __future__ import annotations

import unittest

from support import APITest


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


if __name__ == "__main__":
    unittest.main()
