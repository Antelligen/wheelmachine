"""Organisations and the audit: who a key's person is and what they may do in an organisation, the organisations they
are in, who made each key and machine there, and the audit - read page after page, narrowed as the API takes it, and
exported as a file, whole."""

from __future__ import annotations

import io
import json
import os
import tempfile
import unittest
from datetime import datetime, timedelta, timezone

from support import APITest

from wheelmachine import AuditEvent, BadRequestError, DeviceLogin, HeldBy, Org, PermissionDeniedError, Person, PersonName

ADA = {"id": "acc_0000000000000000000000ada1", "name": "Ada Lovelace", "email": None, "github_login": "ada"}
ACME = {"id": "acc_00000000000000000000000077", "name": "Acme", "person": ADA, "role": "member",
        "teams": ["prj_00000000000000000000000001"],
        "limits": {"max_machines": 5, "max_vcpus": None, "gpus": False, "month_cap_usd": 200.0}}


def events(n: int):
    """n events of the audit, the newest first, a minute apart."""
    base = datetime(2026, 10, 23, 11, 0, tzinfo=timezone.utc)
    return [{"id": str(n - i), "object": "audit_event", "at": (base - timedelta(minutes=i)).strftime("%Y-%m-%dT%H:%M:%SZ"),
             "action": "machine.create", "target": "m_%026d" % 1, "person": {"id": ADA["id"], "name": ADA["name"]},
             "by": "key_00000000000000a1", "address": "203.0.113.7", "request_id": "req_%d" % i, "detail": {}} for i in range(n)]


class Organisations(APITest):
    def test_me_in_a_persons_own_account_and_in_an_organisation(self) -> None:
        me = self.client.me()
        self.assertFalse(me.in_org)
        self.assertEqual((me.account.kind, me.role, me.teams, me.limits, me.scopes), ("person", "owner", None, None, ["cloud:*"]))
        self.api.org = dict(ACME)
        me = self.client.me()
        self.assertTrue(me.in_org)
        self.assertEqual((me.account.id, me.account.name), (ACME["id"], "Acme"))
        assert me.person is not None and me.limits is not None
        self.assertEqual((me.person.name, me.person.email, me.person.github_login), ("Ada Lovelace", None, "ada"))
        self.assertEqual((me.role, me.teams), ("member", ["prj_00000000000000000000000001"]))
        self.assertEqual((me.limits.max_machines, me.limits.max_vcpus, me.limits.gpus, me.limits.month_cap_usd), (5, None, False, 200.0))
        # in no team yet: an empty list, not None
        self.api.org = dict(ACME, teams=[])
        self.assertEqual(self.client.me().teams, [])

    def test_a_person_given_no_handle_yet_has_an_empty_one(self) -> None:
        # made in code, or read from an answer of before handles: empty, never missing
        self.assertEqual(Person(id=ADA["id"], name="Ada Lovelace", email=None, github_login="ada").handle, "")
        self.assertEqual(PersonName(id=ADA["id"], name="Ada Lovelace").handle, "")
        self.assertEqual(HeldBy(id=ADA["id"], name="Ada Lovelace", role="member", level=None).handle, "")
        self.assertEqual(PersonName.from_dict({"id": ADA["id"], "name": "Ada Lovelace"}).handle, "")
        self.assertEqual(PersonName.from_dict({"id": ADA["id"], "name": "Ada Lovelace", "handle": "ada"}).handle, "ada")

    def test_the_organisations_of_a_keys_person(self) -> None:
        self.assertEqual(self.client.orgs.list(), [])
        self.api.orgs = [{"id": ACME["id"], "object": "org", "name": "Acme", "role": "owner", "primary_owner": True,
                          "created_at": "2026-10-01T00:00:00Z", "people": 3}]
        orgs = self.client.orgs.list()
        self.assertEqual(len(orgs), 1)
        self.assertIsInstance(orgs[0], Org)
        self.assertEqual((orgs[0].id, orgs[0].role, orgs[0].primary_owner, orgs[0].people), (ACME["id"], "owner", True, 3))
        # a key made in an organisation lists none, in the API's words
        self.api.org = dict(ACME)
        with self.assertRaises(PermissionDeniedError) as refused:
            self.client.orgs.list()
        self.assertIn("does not list the other organisations of the person who made it", str(refused.exception))

    def test_keys_machines_and_usage_say_who_made_them_and_whose_they_are_in_an_organisation(self) -> None:
        own = self.api.add_machine("web")
        self.assertIsNone(self.client.machines.get(own["id"]).made_by)
        self.assertIsNone(self.client.machines.get(own["id"]).held_by)
        self.api.org = dict(ACME)
        box = self.api.add_machine("box")
        self.assertEqual(self.client.machines.get(box["id"]).made_by, ADA["id"])
        held = self.client.machines.get(box["id"]).held_by
        self.assertIsInstance(held, HeldBy)
        assert held is not None
        self.assertEqual((held.id, held.name, held.role, held.level), (ADA["id"], "Ada Lovelace", "member", None))
        # a team manager's, and one whose holder has left
        self.api.machines[own["id"]]["held_by"] = {"id": "acc_0000000000000000000000bee1", "name": "Meera", "role": "member",
                                                   "level": "manager"}
        theirs = self.client.machines.get(own["id"]).held_by
        assert theirs is not None
        self.assertEqual((theirs.name, theirs.role, theirs.level), ("Meera", "member", "manager"))
        self.api.machines[own["id"]]["held_by"] = {"id": "acc_0000000000000000000000bee2", "name": "Ravi", "role": None, "level": None}
        gone = self.client.machines.get(own["id"]).held_by
        assert gone is not None
        self.assertEqual((gone.name, gone.role), ("Ravi", None))
        self.api.machines[own["id"]]["held_by"] = None
        key = self.client.keys.create(name="ci", scopes=["cloud:machines:read"])
        self.assertEqual(key.made_by, ADA["id"])
        self.assertEqual([k.made_by for k in self.client.keys.list()], [ADA["id"]])
        lines = self.client.usage().machines
        self.assertEqual({line.machine: line.made_by for line in lines}, {own["id"]: None, box["id"]: ADA["id"]})
        self.assertEqual({line.machine: (line.name, line.held_by.name if line.held_by else None) for line in lines},
                         {own["id"]: ("web", None), box["id"]: ("box", "Ada Lovelace")})

    def test_a_program_asks_for_its_key_in_an_organisation(self) -> None:
        login = DeviceLogin.start(name="tool", account=ACME["id"], base_url=self.api.url)
        self.addCleanup(login.close)
        self.assertEqual(self.api.requests("POST", "/v1/login/devices")[-1].json(), {"name": "tool", "account": ACME["id"]})
        before = len(self.api.seen)
        for bad in ("Acme", "acc_123", "ACC_00000000000000000000000077"):
            with self.assertRaises(ValueError):
                DeviceLogin.start(account=bad, base_url=self.api.url)
        self.assertEqual(len(self.api.seen), before, "an account that is not one is refused before any call")


class TheAudit(APITest):
    def test_events_come_newest_first_page_after_page(self) -> None:
        self.api.audit = events(5)
        self.api.audit[1].update(person=None, by="system", address="")
        got = list(self.client.audit.list(limit=2))
        self.assertEqual([e.id for e in got], ["5", "4", "3", "2", "1"])
        self.assertIsInstance(got[0], AuditEvent)
        self.assertEqual((got[0].person.name if got[0].person else None, got[0].by, got[0].address), ("Ada Lovelace", "key_00000000000000a1", "203.0.113.7"))
        self.assertEqual((got[1].person, got[1].by), (None, "system"))
        pages = self.api.requests("GET", "/v1/audit$")
        self.assertEqual([p.query.get("after") for p in pages], [None, ["4"], ["2"]])

    def test_narrowed_by_person_action_and_time(self) -> None:
        self.api.audit = events(3)
        asked = lambda: self.api.requests("GET", "/v1/audit$")[-1].query  # noqa: E731
        list(self.client.audit.list(person=ADA["id"], action="machine.", since="2026-10-23T10:59:00Z",
                                    until=datetime(2026, 10, 23, 14, 0, tzinfo=timezone(timedelta(hours=3)))))
        self.assertEqual(asked(), {"person": [ADA["id"]], "action": ["machine."], "since": ["2026-10-23T10:59:00Z"],
                                   "until": ["2026-10-23T11:00:00Z"]})
        self.assertEqual([e.id for e in self.client.audit.list(since="2026-10-23T10:59:00Z")], ["3", "2"])
        with self.assertRaises(ValueError):
            list(self.client.audit.list(since=datetime(2026, 10, 23, 9, 0)))
        with self.assertRaises(BadRequestError):
            list(self.client.audit.list(since="yesterday"))

    def test_owners_and_admins_alone_read_it(self) -> None:
        self.api.org = dict(ACME, role="admin")
        self.assertEqual(list(self.client.audit.list()), [])
        self.api.org = dict(ACME, role="member")
        with self.assertRaises(PermissionDeniedError):
            list(self.client.audit.list())
        with self.assertRaises(PermissionDeniedError):
            self.client.audit.export()

    def test_exported_as_a_file_whole(self) -> None:
        self.api.audit = events(2)
        self.api.audit[0]["person"]["name"] = "=2+3"
        self.api.audit[0]["detail"] = {"user": "root", "app": "desktop"}
        csv = self.client.audit.export()
        self.assertEqual(csv.decode().split("\r\n")[:2], [
            '"id","at","action","target","person","person_name","by","address","request_id","detail"',
            '"2","2026-10-23T11:00:00Z","machine.create","m_00000000000000000000000001","acc_0000000000000000000000ada1","\'=2+3",'
            '"key_00000000000000a1","203.0.113.7","req_0","app=desktop; user=root"',
        ])
        seen = self.api.requests("GET", "/v1/audit/export$")[-1]
        self.assertEqual((seen.query, seen.headers["accept"]), ({"format": ["csv"]}, "text/csv"))

        as_json = json.loads(self.client.audit.export("json", action="machine.", since=datetime(2026, 10, 23, 10, 59, 30, tzinfo=timezone.utc)))
        self.assertEqual([e["id"] for e in as_json], ["2"])
        self.assertEqual(self.api.requests("GET", "/v1/audit/export$")[-1].query,
                         {"format": ["json"], "action": ["machine."], "since": ["2026-10-23T10:59:30Z"]})

        with tempfile.TemporaryDirectory() as folder:
            path = os.path.join(folder, "audit.csv")
            n = self.client.audit.export(to=path)
            with open(path, "rb") as f:
                self.assertEqual((n, f.read()), (len(csv), csv))
            if os.name == "posix":
                self.assertEqual(os.stat(path).st_mode & 0o777, 0o600, "a new file of the audit is its owner's alone")
        sink = io.BytesIO()
        self.assertEqual(self.client.audit.export(to=sink), len(csv))
        self.assertEqual(sink.getvalue(), csv)

        with self.assertRaises(BadRequestError) as too_long:
            self.client.audit.export(since="2025-01-01T00:00:00Z")
        self.assertIn("a year at most", str(too_long.exception))
        before = len(self.api.seen)
        with self.assertRaises(ValueError):
            self.client.audit.export("xml")  # type: ignore[arg-type]
        self.assertEqual(len(self.api.seen), before)


if __name__ == "__main__":
    unittest.main()
