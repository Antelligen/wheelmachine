"""Machines given to a teammate in an organisation: forked for them or handed off to them - made with a key, each waits for the
OK in the dashboard of the person who made the key, then is done or waits for whoever decides it, followed and taken back - a member's own
fork, which waits for a full manager, asks of a teammate for one of their machines, and a team's rules. Each is a
create: a retry after a lost answer never makes two."""

from __future__ import annotations

import unittest

from support import APITest

from wheelmachine import ConflictError, Machine, MachineRequest, NotFoundError, PermissionDeniedError, TeamRules

ADA = {"id": "acc_0000000000000000000000ada1", "name": "Ada Lovelace", "email": None, "github_login": "ada"}
RAVI = {"id": "acc_00000000000000000000000ra1", "name": "Ravi", "email": "ravi@example.com", "github_login": "ravi"}
ACME = {"id": "acc_00000000000000000000000077", "name": "Acme", "person": ADA, "role": "member",
        "teams": ["prj_00000000000000000000000001"], "limits": None}


class Giving(APITest):
    def setUp(self) -> None:
        super().setUp()
        self.api.org = dict(ACME)
        self.api.people[RAVI["id"]] = dict(RAVI)
        self.box = self.api.add_machine("box", labels={"env": "dev"})

    def test_a_machine_forked_for_a_teammate_and_handed_on_to_them(self) -> None:
        q = self.client.machines.fork_for(self.box["id"], "ravi@example.com", why="for the demo")
        self.assertIsInstance(q, MachineRequest)
        assert q.waiting_on is not None
        # made with a key: the OK of the person who made the key first, given signed in to the dashboard
        self.assertEqual((q.kind, q.way, q.state, q.open, q.receiver.name, q.holder.name, q.why, q.waiting_on.who, q.waiting_on.name), (
            "fork", "give", "waiting", True, "Ravi", "Ada Lovelace", "for the demo", "asker", "Ada Lovelace"))
        assert q.machine is not None and q.last_response is not None
        self.assertEqual((q.machine.id, q.machine.name, q.last_response.status, q.result), (self.box["id"], "box", 201, None))
        sent = self.api.requests("POST", "/fork-for$")[-1]
        self.assertEqual(sent.json(), {"to": "ravi@example.com", "why": "for the demo"})
        self.assertTrue(sent.headers.get("idempotency-key"), "a request is a create: it carries an Idempotency-Key")
        self.api.approve(q.id)
        done = self.client.requests.get(q.id)
        self.assertEqual((done.state, done.open, done.waiting_on, done.keys_revoked), ("done", False, None, 0))
        assert done.result is not None
        fork = self.client.machines.get(done.result)
        assert fork.held_by is not None and fork.origin is not None
        self.assertEqual((fork.name, fork.held_by.name, fork.origin.kind, fork.origin.from_.name, fork.origin.machine_name, fork.clearing),
                         ("box-fork", "Ravi", "fork", "Ada Lovelace", "box", False))

        handed = self.client.machines.hand_off(self.box["id"], "ravi", team=self.api.first_project)
        assert handed.waiting_on is not None
        self.assertEqual((handed.kind, handed.state, handed.to_team, handed.waiting_on.who), ("hand_off", "waiting", self.api.first_project, "asker"))
        self.assertEqual(self.api.requests("POST", "/hand-off$")[-1].json(), {"to": "ravi", "team": self.api.first_project})
        self.api.approve(handed.id)
        self.assertEqual(self.client.requests.get(handed.id).state, "done")
        box = self.client.machines.get(self.box["id"])
        assert box.held_by is not None and box.origin is not None
        # a hand-off arrives paused, its memory kept
        self.assertEqual((box.held_by.id, box.origin.kind, box.origin.from_.id, box.state), (RAVI["id"], "hand_off", ADA["id"], "paused"))

    def test_a_machine_handed_on_is_taken_back_and_given_back(self) -> None:
        handed = self.client.machines.hand_off(self.box["id"], "ravi")
        self.api.approve(handed.id)
        box = self.client.machines.get(self.box["id"])
        assert box.given_by is not None
        self.assertEqual((box.given_by.id, box.given_by.name), (ADA["id"], "Ada Lovelace"))
        # taken back by the one who handed it off, with a key: the OK of the person who made the key first, then back as it is
        back = self.client.machines.take_back(self.box["id"])
        assert back.waiting_on is not None and back.last_response is not None
        self.assertEqual((back.kind, back.way, back.state, back.waiting_on.who, back.receiver.id, back.last_response.status),
                         ("hand_off", "back", "waiting", "asker", ADA["id"], 201))
        self.assertEqual(self.api.requests("POST", "/take-back$")[-1].json(), {})
        self.assertTrue(self.api.requests("POST", "/take-back$")[-1].headers.get("idempotency-key"))
        self.api.approve(back.id)
        box = self.client.machines.get(self.box["id"])
        assert box.held_by is not None
        self.assertEqual((box.held_by.id, box.given_by, box.origin, box.state), (ADA["id"], None, None, "paused"))
        with self.assertRaises(ConflictError) as nobody:
            self.client.machines.give_back(self.box["id"], team=self.api.first_project)
        self.assertIn("there is nobody to give it back to", str(nobody.exception))
        self.assertEqual(self.api.requests("POST", "/give-back$")[-1].json(), {"team": self.api.first_project})

    def test_what_the_api_refuses_is_said_in_its_words(self) -> None:
        with self.assertRaises(ConflictError) as theirs:
            self.client.machines.hand_off(self.box["id"], ADA["id"])
        self.assertIn("it is theirs already", str(theirs.exception))
        with self.assertRaises(NotFoundError):
            self.client.machines.fork_for(self.box["id"], "nobody@example.com")
        self.api.rules[self.api.first_project] = {"forks": "off"}
        with self.assertRaises(PermissionDeniedError) as off:
            self.client.machines.fork_for(self.box["id"], RAVI["id"])
        self.assertIn("Forks: Off in Default", str(off.exception))
        before = len(self.api.seen)
        for bad in ("", None):
            with self.assertRaises(ValueError):
                self.client.machines.fork_for(self.box["id"], bad)  # type: ignore[arg-type]
        self.assertEqual(len(self.api.seen), before, "a request to nobody is refused before any call")
        self.api.org = None
        with self.assertRaises(ConflictError) as own:
            self.client.machines.hand_off(self.box["id"], RAVI["id"])
        self.assertIn("Only an organisation's machines are handed off", str(own.exception))

    def test_a_give_that_waits_is_followed_and_taken_back(self) -> None:
        self.api.gives_wait = True
        q = self.client.machines.fork_for(self.box["id"], RAVI["id"])
        self.api.approve(q.id)
        q = self.client.requests.get(q.id)
        assert q.waiting_on is not None
        self.assertEqual((q.state, q.open, q.waiting_on.who, q.result), ("waiting", True, "manager", None))
        self.assertEqual([r.id for r in self.client.requests.list(open=True, machine=self.box["id"])], [q.id])
        self.assertEqual(self.api.requests("GET", "/v1/requests$")[-1].query, {"open": ["true"], "machine": [self.box["id"]]})
        # the same give again, while it waits, is the one there is
        self.assertEqual(self.client.machines.fork_for(self.box["id"], RAVI["id"]).id, q.id)
        back = self.client.requests.take_back(q.id)
        self.assertEqual((back.state, back.open), ("taken_back", False))
        # sent again - after its answer was lost, say - it is answered as it is
        self.assertEqual(self.client.requests.take_back(q.id).state, "taken_back")
        self.assertEqual(list(self.client.requests.list(open=True)), [])
        self.assertEqual([r.id for r in self.client.requests.list()], [q.id])
        with self.assertRaises(NotFoundError):
            self.client.requests.get("rq_00000000000000000000000000")

    def test_a_give_whose_answer_was_lost_is_sent_again_and_makes_one(self) -> None:
        self.api.fault("POST", "/fork-for$", drop="after")
        q = self.client.machines.fork_for(self.box["id"], RAVI["id"])
        self.assertEqual(len(self.api.requests("POST", "/fork-for$")), 2)
        assert q.last_response is not None
        self.assertTrue(q.last_response.replayed, "the second send is the first one's answer, kept")
        self.assertEqual(len(self.api.machine_requests), 1)
        self.api.approve(q.id)
        self.assertEqual(sum(1 for m in self.api.machines.values() if m["name"] == "box-fork"), 1)

    def test_a_members_fork_waits_for_a_full_manager(self) -> None:
        q = self.client.machines.fork(self.box["id"], name="box-2", labels={"stage": "try"})
        # nothing made: the request, theirs to them, waiting first on the OK of the person who made the key, as one asked with a key does
        assert isinstance(q, MachineRequest) and q.waiting_on is not None and q.machine is not None and q.last_response is not None
        self.assertEqual((q.kind, q.way, q.state, q.holder.id, q.receiver.id, q.asked_by.id, q.waiting_on.who, q.result),
                         ("fork", "give", "waiting", ADA["id"], ADA["id"], ADA["id"], "asker", None))
        self.assertEqual((q.machine.id, q.raw["object"], q.last_response.status), (self.box["id"], "request", 202))
        # of the machine as it is: no backup, and its own size
        self.assertEqual((q.backup, q.cpus), (None, None))
        self.assertEqual(self.api.requests("POST", "/fork$")[-1].json(), {"name": "box-2", "labels": {"stage": "try"}})
        self.assertEqual([m.id for m in self.client.machines.list()], [self.box["id"]])
        # asked again while it waits: the one there is
        again = self.client.machines.fork(self.box["id"])
        assert isinstance(again, MachineRequest) and again.last_response is not None
        self.assertEqual((again.id, again.last_response.status), (q.id, 200))
        # the OK of the person who made the key given in the dashboard, it waits on a full manager
        self.api.approve(q.id)
        waiting = self.client.requests.get(q.id)
        assert waiting.waiting_on is not None
        self.assertEqual((waiting.state, waiting.waiting_on.who, waiting.result), ("waiting", "manager", None))
        # where forks are Off, a member's fork is refused
        self.api.rules[self.api.first_project] = {"forks": "off"}
        with self.assertRaises(PermissionDeniedError) as off:
            self.client.machines.fork(self.box["id"])
        self.assertIn("Forks: Off in Default", str(off.exception))

    def test_a_members_fork_from_a_backup_waits_too_and_says_which_and_how_big(self) -> None:
        self.client.machines.backup(self.box["id"], note="older")
        newest = self.client.machines.backup(self.box["id"], note="newest")
        self.assertEqual([b.note for b in self.client.machines.backups(self.box["id"])], ["newest", "older"])
        q = self.client.machines.fork(self.box["id"], name="box-big", backup="latest", cpus=8)
        # nothing made: the request, saying the backup it is made from - the newest, by its id - and the size asked
        assert isinstance(q, MachineRequest) and q.waiting_on is not None and q.last_response is not None
        self.assertEqual((q.kind, q.way, q.state, q.backup, q.cpus, q.waiting_on.who, q.result, q.last_response.status),
                         ("fork", "give", "waiting", newest.id, 8, "asker", None, 202))
        self.assertEqual(self.api.requests("POST", "/fork$")[-1].json(), {"name": "box-big", "backup": "latest", "cpus": 8})
        self.assertEqual([m.id for m in self.client.machines.list()], [self.box["id"]])
        # the OK of the person who made the key given in the dashboard, it waits on a full manager, as it was asked
        self.api.approve(q.id)
        waiting = self.client.requests.get(q.id)
        assert waiting.waiting_on is not None
        self.assertEqual((waiting.state, waiting.waiting_on.who, waiting.backup, waiting.cpus), ("waiting", "manager", newest.id, 8))

    def test_a_full_managers_own_fork_and_an_owners_are_made_at_once(self) -> None:
        self.api.org = dict(ACME, levels={self.api.first_project: "full"})
        fork = self.client.machines.fork(self.box["id"], name="box-2")
        assert isinstance(fork, Machine) and fork.last_response is not None
        self.assertEqual((fork.name, fork.raw["object"], fork.last_response.status), ("box-2", "machine", 201))
        self.api.org = dict(ACME, role="owner", teams=None)
        self.assertIsInstance(self.client.machines.fork(self.box["id"]), Machine)
        self.assertEqual(self.api.machine_requests, {})

    def test_a_teammate_asked_for_one_of_their_machines(self) -> None:
        q = self.client.requests.ask("fork", RAVI["id"], why="the demo box")
        assert q.waiting_on is not None
        self.assertEqual((q.way, q.kind, q.state, q.machine, q.holder.name, q.waiting_on.who, q.waiting_on.name),
                         ("ask", "fork", "waiting", None, "Ravi", "asker", "Ada Lovelace"))
        self.api.approve(q.id)
        picked = self.client.requests.get(q.id)
        assert picked.waiting_on is not None
        self.assertEqual((picked.waiting_on.who, picked.waiting_on.name), ("holder", "Ravi"))
        self.assertEqual(self.api.requests("POST", "/v1/requests$")[-1].json(), {"kind": "fork", "of": RAVI["id"], "why": "the demo box"})
        # asked again while it waits: the same request
        again = self.client.requests.ask("fork", "ravi@example.com")
        assert again.last_response is not None
        self.assertEqual((again.id, again.last_response.status), (q.id, 200))
        self.assertEqual(self.client.requests.ask("hand_off", "ravi").kind, "hand_off")
        before = len(self.api.seen)
        with self.assertRaises(ValueError):
            self.client.requests.ask("copy", RAVI["id"])  # type: ignore[arg-type]
        with self.assertRaises(ValueError):
            self.client.requests.ask("fork", "")
        self.assertEqual(len(self.api.seen), before)
        with self.assertRaises(NotFoundError):
            self.client.requests.ask("fork", "nobody")

    def test_a_teams_rules_and_the_managers_levels(self) -> None:
        rules = self.client.projects.rules(self.api.first_project)
        self.assertIsInstance(rules, TeamRules)
        self.assertEqual((rules.hand_offs, rules.forks, rules.gpus, rules.open_internet, rules.month_budget_usd, rules.models, rules.set_by),
                         ("ask", "ask", True, True, None, "any", None))
        # (forks are asked for or off, never free; hand-offs may be free)
        self.api.rules[self.api.first_project] = {"hand_offs": "free", "forks": "off", "gpus": False, "month_budget_usd": 50.0,
                                                  "spent_usd": 12.5, "open_machines": 2, "set_by": {"id": ADA["id"], "name": "Ada Lovelace"},
                                                  "set_at": "2026-10-27T07:00:00Z"}
        rules = self.client.projects.rules(self.api.first_project)
        assert rules.set_by is not None
        self.assertEqual((rules.hand_offs, rules.forks, rules.gpus, rules.month_budget_usd, rules.spent_usd, rules.open_machines,
                          rules.set_by.name), ("free", "off", False, 50.0, 12.5, 2, "Ada Lovelace"))
        # to one of its members, what its machines came to is not said
        self.api.rules[self.api.first_project] = {"spent_usd": None, "open_machines": None}
        rules = self.client.projects.rules(self.api.first_project)
        self.assertEqual((rules.spent_usd, rules.open_machines), (None, None))
        with self.assertRaises(NotFoundError):
            self.client.projects.rules("prj_00000000000000000000000999")
        self.assertEqual(self.client.me().levels, {})
        self.api.org = dict(ACME, levels={self.api.first_project: "full"})
        self.assertEqual(self.client.me().levels, {self.api.first_project: "full"})
        self.api.org = None
        with self.assertRaises(ConflictError):
            self.client.projects.rules(self.api.first_project)


if __name__ == "__main__":
    unittest.main()
