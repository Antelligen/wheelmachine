"""What the API answers with, as typed objects. Field names are the API's own, so its reference reads one to one.

Each object keeps the JSON it was made from in ``raw`` (a field the API adds later is there before this library knows
it), and an object a call answered with directly says how that answer came in ``last_response``.
Times are the API's ISO 8601 strings in UTC, such as ``2026-10-23T07:00:00Z``.
"""

from __future__ import annotations

import base64
from dataclasses import dataclass, field
from typing import Any, Dict, List, Literal, Mapping, Optional, TypeVar

__all__ = [
    "MachineState",
    "Scope",
    "KeyState",
    "Role",
    "Level",
    "RuleWay",
    "RequestState",
    "RateLimit",
    "ResponseInfo",
    "APIObject",
    "Account",
    "Person",
    "PersonName",
    "PersonLimits",
    "Standing",
    "Key",
    "KeyCreated",
    "Me",
    "Org",
    "AuditPerson",
    "AuditEvent",
    "Project",
    "GpuRef",
    "Size",
    "Location",
    "Machine",
    "Origin",
    "MachineRequest",
    "RequestMachine",
    "WaitingOn",
    "TeamRules",
    "Backup",
    "ExecResult",
    "MetricsPoint",
    "MachineMetrics",
    "Secret",
    "Outside",
    "HeaderRule",
    "SecretPreset",
    "DeployBranch",
    "DeployBranches",
    "DeployGuess",
    "DeployLog",
    "DeployRepo",
    "DeployRun",
    "DeploySource",
    "Deployment",
    "Port",
    "PortLink",
    "AppKind",
    "App",
    "Region",
    "GpuOffer",
    "GpuList",
    "UsageLine",
    "Usage",
]

#: ``starting`` being made ready; ``running``; ``paused`` memory kept, nothing running; ``stopped`` files kept, memory
#: not; ``archived`` files in storage; ``recovering`` a fault on our side stopped it, and it is being brought back; ``deleted``;
#: ``failed`` it never came up, and ``failure`` says why - nothing is charged for it, and it is gone as a deleted
#: machine is.
MachineState = Literal["starting", "running", "paused", "stopped", "archived", "recovering", "deleted", "failed"]

#: What a key may do. ``cloud:*`` holds all of the others.
Scope = Literal[
    "cloud:*",
    "cloud:machines:read",
    "cloud:machines:write",
    "cloud:machines:exec",
    "cloud:gpus",
    "cloud:projects:write",
    "cloud:keys:write",
    "cloud:secrets:write",
    "cloud:folders:read",
    "cloud:folders:write",
]

KeyState = Literal["active", "revoked", "expired"]

#: A person's role in an organisation: ``owner`` may do everything; ``admin`` everything but change owners; ``billing``
#: sees what machines cost and the people, no machine, and changes nothing but a person's limit for the month;
#: ``member`` makes and uses machines in their teams - those they hold, and in a team they lead those its members hold.
#: In a person's own account they are its ``owner``.
Role = Literal["owner", "admin", "billing", "member"]

#: A team manager's level: ``approver`` sees the team's members' machines, watches their screens and decides the team's
#: hand-offs; ``manager`` also starts, stops, pauses and deletes those machines and puts people in the team; ``full``
#: also decides its forks, sets its rules and makes approvers. None uses or forks a member's machine.
Level = Literal["approver", "manager", "full"]

#: How a team's machines are handed off: ``free``; ``ask``, with a manager's OK; ``off``, not at all. Forks are ``ask``
#: or ``off`` alone (``TeamRules.forks``).
RuleWay = Literal["free", "ask", "off"]

#: A request's state: ``waiting`` on someone's OK (``waiting_on`` says whose); ``approved`` and being carried out;
#: ``done``; ``failed`` (``failure`` says why); ``declined``; ``expired`` after a day; ``taken_back`` by the one who asked.
RequestState = Literal["waiting", "approved", "done", "failed", "declined", "expired", "taken_back"]


@dataclass(frozen=True)
class RateLimit:
    """The key's rate as the answer gave it (the ``RateLimit`` and ``RateLimit-Policy`` headers)."""

    limit: Optional[int]
    """Calls a minute the key may make."""
    window_seconds: Optional[int]
    remaining: Optional[int]
    """Calls that could be made at once now."""
    reset_seconds: Optional[int]
    """Seconds until the whole allowance is back."""


@dataclass(frozen=True)
class ResponseInfo:
    """How an answer came: its status, the call's id, and whether it was a kept answer given again."""

    status: int
    request_id: Optional[str]
    replayed: bool
    """True when the API gave a create call's first answer again (the same ``Idempotency-Key``)."""
    rate_limit: Optional[RateLimit]


class APIObject:
    """What every answer object shares."""

    @property
    def raw(self) -> Dict[str, Any]:
        """The JSON this object was made from."""
        got = self.__dict__.get("_raw")
        return got if isinstance(got, dict) else {}

    @property
    def last_response(self) -> Optional[ResponseInfo]:
        """How the answer that carried this object came; None for an item of a list."""
        got = self.__dict__.get("_last_response")
        return got if isinstance(got, ResponseInfo) else None


_O = TypeVar("_O", bound=APIObject)


def _stamp(obj: _O, raw: Mapping[str, Any]) -> _O:
    obj.__dict__["_raw"] = dict(raw)
    return obj


def _answered(obj: _O, response: ResponseInfo) -> _O:
    """`obj`, marked with the answer that carried it."""
    obj.__dict__["_last_response"] = response
    return obj


# --- reading the JSON, tolerant of a field left out -------------------------------------------------------------


def _dict(d: Mapping[str, Any], k: str) -> Dict[str, Any]:
    v = d.get(k)
    return v if isinstance(v, dict) else {}


def _opt_dict(d: Mapping[str, Any], k: str) -> Optional[Dict[str, Any]]:
    v = d.get(k)
    return v if isinstance(v, dict) else None


def _str(d: Mapping[str, Any], k: str, default: str = "") -> str:
    v = d.get(k)
    return v if isinstance(v, str) else default


def _opt_str(d: Mapping[str, Any], k: str) -> Optional[str]:
    v = d.get(k)
    return v if isinstance(v, str) else None


def _num(v: Any) -> Optional[float]:
    if isinstance(v, bool) or not isinstance(v, (int, float)):
        return None
    return float(v)


def _float(d: Mapping[str, Any], k: str, default: float = 0.0) -> float:
    v = _num(d.get(k))
    return default if v is None else v


def _opt_float(d: Mapping[str, Any], k: str) -> Optional[float]:
    return _num(d.get(k))


def _amount(d: Mapping[str, Any], k: str) -> Optional[float]:
    """An amount in the account's currency: ``k`` - or, said by an API from before accounts had currencies, which says
    its amounts in dollars alone, ``k``_usd."""
    return _num(d.get(k)) if k in d else _num(d.get(k + "_usd"))


def _currency(d: Mapping[str, Any]) -> str:
    """The currency an answer's amounts are in: ``USD``, ``GBP`` or ``EUR`` - US dollars for an answer that says none."""
    return _opt_str(d, "currency") or "USD"


def _int(d: Mapping[str, Any], k: str, default: int = 0) -> int:
    v = _num(d.get(k))
    return default if v is None else int(v)


def _opt_int(d: Mapping[str, Any], k: str) -> Optional[int]:
    v = _num(d.get(k))
    return None if v is None else int(v)


def _bool(d: Mapping[str, Any], k: str, default: bool = False) -> bool:
    v = d.get(k)
    return v if isinstance(v, bool) else default


def _opt_bool(d: Mapping[str, Any], k: str) -> Optional[bool]:
    v = d.get(k)
    return v if isinstance(v, bool) else None


def _strs(d: Mapping[str, Any], k: str) -> List[str]:
    v = d.get(k)
    return [s for s in v if isinstance(s, str)] if isinstance(v, list) else []


def _labels(d: Mapping[str, Any], k: str) -> Dict[str, str]:
    v = d.get(k)
    if not isinstance(v, dict):
        return {}
    return {str(name): value for name, value in v.items() if isinstance(value, str)}


def _list(d: Mapping[str, Any], k: str) -> List[Dict[str, Any]]:
    v = d.get(k)
    return [item for item in v if isinstance(item, dict)] if isinstance(v, list) else []


# --- the objects --------------------------------------------------------------------------------------------------


@dataclass
class Account(APIObject):
    """The account a call acts in: a person's own, or an organisation's."""

    id: str
    kind: Optional[str] = None
    """``person`` for a person's own account; ``org`` for an organisation's."""
    name: Optional[str] = None
    """An organisation's name, or the person's in their own account."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Account":
        return _stamp(cls(id=_str(d, "id"), kind=_opt_str(d, "kind"), name=_opt_str(d, "name")), d)


@dataclass
class Person(APIObject):
    """A person, by their own account's id. Their e-mail address is said with the dashboard's sign-in only, never to
    a key."""

    id: str
    name: str
    email: Optional[str]
    github_login: Optional[str]
    handle: str = ""
    """Their @handle, without its @: theirs alone; empty for a person not yet given one."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Person":
        return _stamp(
            cls(id=_str(d, "id"), name=_str(d, "name"), email=_opt_str(d, "email"), github_login=_opt_str(d, "github_login"),
                handle=_opt_str(d, "handle") or ""),
            d,
        )


@dataclass
class HeldBy(APIObject):
    """Whose a machine of an organisation is: the person who holds it, by their own account's id and their name."""

    id: str
    name: str
    role: Optional[str]
    """Their role there (Role); None once they are no longer in it."""
    level: Optional[str]
    """Their level (Level) when they manage the machine's team; None otherwise."""
    handle: str = ""
    """Their @handle, without its @: theirs alone; empty for a person not yet given one."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "HeldBy":
        return _stamp(cls(id=_str(d, "id"), name=_str(d, "name"), role=_opt_str(d, "role"), level=_opt_str(d, "level"),
                          handle=_opt_str(d, "handle") or ""), d)


@dataclass
class PersonName(APIObject):
    """A person, by their own account's id and their name alone."""

    id: str
    name: str
    """As they signed in with it; empty when they gave none."""
    handle: str = ""
    """Their @handle, without its @: theirs alone; empty for a person not yet given one."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "PersonName":
        return _stamp(cls(id=_str(d, "id"), name=_str(d, "name"), handle=_opt_str(d, "handle") or ""), d)


@dataclass
class PersonLimits(APIObject):
    """A person's own limits in an organisation, beside the organisation's own: None is no limit of their own."""

    max_machines: Optional[int]
    """The machines they may hold."""
    max_vcpus: Optional[int]
    """The CPUs their machines may run at once."""
    gpus: bool
    """Whether they may make GPU machines."""
    month_cap_usd: Optional[float]
    """Deprecated: ``month_cap`` in US dollars at Wheelmachine's rate."""
    currency: str = "USD"
    """The organisation's currency, which ``month_cap`` is in."""
    month_cap: Optional[float] = None
    """What their machines may come to in a month, in the organisation's currency."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "PersonLimits":
        return _stamp(
            cls(max_machines=_opt_int(d, "max_machines"), max_vcpus=_opt_int(d, "max_vcpus"), gpus=_bool(d, "gpus"),
                month_cap_usd=_opt_float(d, "month_cap_usd"), currency=_currency(d), month_cap=_amount(d, "month_cap")),
            d,
        )


@dataclass
class Standing(APIObject):
    """Whether the account can spend now: nothing runs once its credit has run out."""

    can_spend: bool
    balance_usd: Optional[float]
    """Deprecated: ``balance`` in US dollars at Wheelmachine's rate."""
    credit_usd: Optional[float]
    """Deprecated: ``credit`` in US dollars at Wheelmachine's rate."""
    reason: Optional[str]
    """Why it cannot spend, in words a person reads."""
    currency: str = "USD"
    """The account's currency, which ``balance`` and ``credit`` are in."""
    balance: Optional[float] = None
    """What is left to spend this month under the account's limit; None for an account with no monthly limit."""
    credit: Optional[float] = None
    """The account's credit left; None for an organisation's member, who does not read it."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Standing":
        return _stamp(
            cls(can_spend=_bool(d, "can_spend"), balance_usd=_opt_float(d, "balance_usd"), credit_usd=_opt_float(d, "credit_usd"),
                reason=_opt_str(d, "reason"), currency=_currency(d), balance=_amount(d, "balance"), credit=_amount(d, "credit")),
            d,
        )


@dataclass
class Key(APIObject):
    """An API key, never its secret."""

    id: str
    name: str
    scopes: List[str]
    projects: List[str]
    """The projects it is limited to; empty for every project of the account."""
    shown: str
    """Its first and last characters, to recognise it by."""
    state: str
    rate_per_minute: Optional[int]
    created_at: str
    expires_at: Optional[str]
    last_used_at: Optional[str]
    made_by: Optional[str]
    """In an organisation, the person who made it, by their own account's id; None in a person's own account."""

    @classmethod
    def _fields(cls, d: Mapping[str, Any]) -> Dict[str, Any]:
        return dict(
            id=_str(d, "id"),
            name=_str(d, "name"),
            scopes=_strs(d, "scopes"),
            projects=_strs(d, "projects"),
            shown=_str(d, "shown"),
            state=_str(d, "state", "active"),
            rate_per_minute=_opt_int(d, "rate_per_minute"),
            created_at=_str(d, "created_at"),
            expires_at=_opt_str(d, "expires_at"),
            last_used_at=_opt_str(d, "last_used_at"),
            made_by=_opt_str(d, "made_by"),
        )

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Key":
        return _stamp(cls(**cls._fields(d)), d)


@dataclass
class KeyCreated(Key):
    """A key just made, with its secret: shown in this answer only, and never in this object's repr."""

    secret: str = field(repr=False)

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "KeyCreated":
        return _stamp(cls(secret=_str(d, "secret"), **cls._fields(d)), d)


@dataclass
class Me(APIObject):
    """The account and key a call is made with: the account it acts in, the person behind it - the one who made the
    key - and their role there; in an organisation, their teams and their own limits there too."""

    account: Account
    key: Key
    standing: Standing
    person: Optional[Person] = None
    role: Optional[str] = None
    """Their role in the account (Role); ``owner`` in their own."""
    teams: Optional[List[str]] = None
    """In an organisation, for a member, the projects of their teams - empty for one in no team yet; None for any other
    role, and in a person's own account."""
    limits: Optional[PersonLimits] = None
    """In an organisation, the person's own limits there; None in their own account."""
    scopes: List[str] = field(default_factory=list)
    """What the call holds."""
    levels: Dict[str, str] = field(default_factory=dict)
    """In an organisation, their level (Level) in each team they manage, by its project; empty otherwise."""

    @property
    def in_org(self) -> bool:
        """Whether the call acts in an organisation."""
        return self.account.kind == "org"

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Me":
        teams = d.get("teams")
        return _stamp(
            cls(
                account=Account.from_dict(_dict(d, "account")),
                key=Key.from_dict(_dict(d, "key")),
                standing=Standing.from_dict(_dict(d, "standing")),
                person=_opt(d, "person", Person.from_dict),
                role=_opt_str(d, "role"),
                teams=_strs(d, "teams") if isinstance(teams, list) else None,
                limits=_opt(d, "limits", PersonLimits.from_dict),
                scopes=_strs(d, "scopes"),
                levels=_labels(d, "levels"),
            ),
            d,
        )


@dataclass
class ConfirmCodeSent(APIObject):
    """A confirm code on its way: what it is for, when it stops working, and the address it went to, in part."""

    step: str
    org: Optional[str]
    to: Optional[str]
    account: Optional[str]
    expires_at: str
    sent_to: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "ConfirmCodeSent":
        return _stamp(
            cls(step=_str(d, "step"), org=_opt_str(d, "org"), to=_opt_str(d, "to"), account=_opt_str(d, "account"),
                expires_at=_str(d, "expires_at"), sent_to=_str(d, "sent_to")),
            d,
        )


@dataclass
class Org(APIObject):
    """An organisation as one of its people sees it: their role in it, and whether they are its primary owner, the
    owner who transfers it."""

    id: str
    name: str
    role: str
    """Your role in it (Role)."""
    primary_owner: bool
    created_at: str
    people: int
    """How many people are in it."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Org":
        return _stamp(
            cls(id=_str(d, "id"), name=_str(d, "name"), role=_str(d, "role"), primary_owner=_bool(d, "primary_owner"),
                created_at=_str(d, "created_at"), people=_int(d, "people")),
            d,
        )


@dataclass
class AuditPerson(APIObject):
    """The person who did what an event says, by their id and name."""

    id: str
    name: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "AuditPerson":
        return _stamp(cls(id=_str(d, "id"), name=_str(d, "name")), d)


@dataclass
class AuditEvent(APIObject):
    """A line of the account's audit: what was done, to which thing, by whom, from where."""

    id: str
    at: str
    action: str
    """Such as ``machine.create``, ``member.role`` or ``machine.terminal.open``."""
    target: str
    """What it was done to: a machine, a key, a person."""
    person: Optional[AuditPerson]
    """Who did it; None for what the API did by itself, and for Wheelmachine's staff."""
    by: str
    """The key (``key_...``) or the sign-in (``ses_...``) it was done with; ``system`` for what Wheelmachine did by itself,
    ``wheelmachine`` for Wheelmachine's staff."""
    address: str
    """Where it came from."""
    request_id: str
    detail: Dict[str, str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "AuditEvent":
        return _stamp(
            cls(id=_str(d, "id"), at=_str(d, "at"), action=_str(d, "action"), target=_str(d, "target"),
                person=_opt(d, "person", AuditPerson.from_dict), by=_str(d, "by"), address=_str(d, "address"),
                request_id=_str(d, "request_id"), detail=_labels(d, "detail")),
            d,
        )


@dataclass
class Project(APIObject):
    id: str
    name: str
    created_at: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Project":
        return _stamp(cls(id=_str(d, "id"), name=_str(d, "name"), created_at=_str(d, "created_at")), d)


@dataclass
class GpuRef(APIObject):
    id: str
    """An ``id`` from ``client.gpus()``."""
    model: Optional[str]
    count: Optional[int]
    memory_gb: Optional[int]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "GpuRef":
        return _stamp(
            cls(id=_str(d, "id"), model=_opt_str(d, "model"), count=_opt_int(d, "count"), memory_gb=_opt_int(d, "memory_gb")),
            d,
        )


@dataclass
class Size(APIObject):
    cpus: float
    memory_gb: float
    disk_gb: Optional[float]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Size":
        return _stamp(cls(cpus=_float(d, "cpus"), memory_gb=_float(d, "memory_gb"), disk_gb=_opt_float(d, "disk_gb")), d)


@dataclass
class Location(APIObject):
    city: Optional[str]
    country: Optional[str]
    """ISO 3166-1 alpha-2."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Location":
        return _stamp(cls(city=_opt_str(d, "city"), country=_opt_str(d, "country")), d)


@dataclass
class NetworkPolicy(APIObject):
    """What a machine may reach on the internet."""

    internet: str
    """``"open"`` (the whole internet), ``"closed"`` (no internet in or out and no name lookups: a public port
    answers only as a private one), or ``"allowed"`` (only ``allow``). Whatever it is, your team's repositories and
    folders still work where given."""
    allow: List[str]
    """For ``"allowed"``: names (exact, or a leading ``*.``) for HTTP and HTTPS, IPv4/IPv6 ranges (CIDRs) for other ports."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "NetworkPolicy":
        return _stamp(cls(internet=_str(d, "internet", "open"), allow=_strs(d, "allow")), d)


def _opt(d: Mapping[str, Any], k: str, make: Any) -> Any:
    v = _opt_dict(d, k)
    return None if v is None else make(v)


@dataclass
class Machine(APIObject):
    id: str
    name: str
    project: str
    made_by: Optional[str]
    """In an organisation, the person who made it, by their own account's id; None in a person's own account."""
    held_by: Optional[HeldBy]
    """In an organisation, whose it is: the person who holds it, with their role there; None in a person's own
    account."""
    state: MachineState
    labels: Dict[str, str]
    secrets: List[str]
    """The names of the account's secrets its commands get as environment variables; never their values."""
    ai_keys: List[str]
    """The names of the account's AI keys - its secrets on every machine - that reach it: its commands get each as a
    placeholder, and the key is added on the way out, only to the key's hosts."""
    network: "NetworkPolicy"
    """What it may reach on the internet."""
    files_sealed: bool
    """Its files in storage are encrypted."""
    created_at: str
    gpu: Optional[GpuRef]
    size: Optional[Size]
    location: Optional[Location]
    """Where it runs now."""
    cpus: Optional[int]
    """Its CPUs, 2, 4, 8 or 16; None for a GPU machine, whose size comes with its GPU."""
    memory_gb: Optional[float]
    """Its memory, 2 GB for each CPU; None for a GPU machine."""
    sleep_when_idle: bool
    """True: it pauses by itself once it is idle (nothing reached it for ``idle_after_s``, its programs quiet as long)."""
    idle_after_s: Optional[int]
    """How long it must be idle before it pauses by itself; None when it never does."""
    running_seconds: float
    """All the time it has run, which is what is charged."""
    disk_bytes: Optional[int]
    cpu_cores: Optional[float]
    """CPUs it used, about a minute's average, when running."""
    memory_bytes: Optional[int]
    last_used_at: Optional[str]
    ready_ms: Optional[float]
    """How long making it ready took, when this answer made it ready (create, start, fork)."""
    failure: Optional[str]
    """Why it never came up, in plain words, when its ``state`` is ``failed``."""
    clearing: bool = False
    """It is being handed off, or cleared for the person it was forked for: every way into it is refused meanwhile."""
    origin: Optional["Origin"] = None
    """Where it came from, when another person's machine was its start: a fork of theirs, or handed off by them."""
    access: str = "full"
    """What you may do with it: "full" - you hold it and use it all - or "watch": in an organisation, its owners and
    admins and its team's managers see a member's machine and watch its screen, and never use it."""
    given_by: Optional[PersonName] = None
    """In an organisation, who handed it off to the person who holds it: they watch it, and take it back whenever they
    choose (``machines.take_back``); its holder gives it back to them (``machines.give_back``). None when nobody did."""
    pause_reason: Optional["PauseReason"] = None
    """Why it is paused, while a limit holds it so: the limit's words as you are told them, and the limit; None
    otherwise - a machine paused by its holder, or by itself, has none."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Machine":
        return _stamp(
            cls(
                id=_str(d, "id"),
                name=_str(d, "name"),
                project=_str(d, "project"),
                made_by=_opt_str(d, "made_by"),
                held_by=_opt(d, "held_by", HeldBy.from_dict),
                state=_str(d, "state", "starting"),  # type: ignore[arg-type]
                labels=_labels(d, "labels"),
                secrets=_strs(d, "secrets"),
                ai_keys=_strs(d, "ai_keys"),
                network=NetworkPolicy.from_dict(_opt_dict(d, "network") or {}),
                files_sealed=_bool(d, "files_sealed"),
                created_at=_str(d, "created_at"),
                gpu=_opt(d, "gpu", GpuRef.from_dict),
                size=_opt(d, "size", Size.from_dict),
                location=_opt(d, "location", Location.from_dict),
                cpus=_opt_int(d, "cpus"),
                memory_gb=_opt_float(d, "memory_gb"),
                sleep_when_idle=_bool(d, "sleep_when_idle", True),
                idle_after_s=_opt_int(d, "idle_after_s"),
                running_seconds=_float(d, "running_seconds"),
                disk_bytes=_opt_int(d, "disk_bytes"),
                cpu_cores=_opt_float(d, "cpu_cores"),
                memory_bytes=_opt_int(d, "memory_bytes"),
                last_used_at=_opt_str(d, "last_used_at"),
                ready_ms=_opt_float(d, "ready_ms"),
                failure=_opt_str(d, "failure"),
                clearing=_bool(d, "clearing"),
                origin=_opt(d, "origin", Origin.from_dict),
                access=_str(d, "access", "full"),
                given_by=_opt(d, "given_by", PersonName.from_dict),
                pause_reason=_opt(d, "pause_reason", PauseReason.from_dict),
            ),
            d,
        )


@dataclass
class PauseReason(APIObject):
    """Why a machine is paused, while a limit holds it so."""

    why: str
    """The limit, who can raise it and when it lifts by itself, in plain words."""
    limit: Dict[str, Any]
    """The limit itself: its ``scope``, ``measure``, ``limit``, ``used`` (None when you are not shown them),
    ``resets_at`` and ``raised_by``, as an error's ``limit`` says them."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "PauseReason":
        return _stamp(cls(why=_str(d, "why"), limit=dict(_opt_dict(d, "limit") or {})), d)


@dataclass
class Origin(APIObject):
    """Where a machine came from, when another person's machine was its start."""

    kind: str
    """``fork``: a fork of another person's machine; ``hand_off``: handed off by them."""
    from_: PersonName
    """The API's ``from``: the person whose machine it was."""
    machine_name: str
    """The name the machine it came from had then."""
    at: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Origin":
        return _stamp(
            cls(kind=_str(d, "kind"), from_=PersonName.from_dict(_dict(d, "from")), machine_name=_str(d, "machine_name"),
                at=_str(d, "at")),
            d,
        )


@dataclass
class RequestMachine(APIObject):
    """The machine a request is of: by its name when it was picked, and by its id to those who reach it."""

    id: Optional[str]
    name: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "RequestMachine":
        return _stamp(cls(id=_opt_str(d, "id"), name=_str(d, "name")), d)


@dataclass
class WaitingOn(APIObject):
    """Whose OK a request waits on."""

    who: str
    """``asker``, the person who made the key, for one given or asked with a key - a library's, wm's or an agent's: they approve it signed
    in to the dashboard, as a key decides nothing by itself; ``holder``, the machine's holder; ``manager``, a manager
    of its team - for a fork, a full manager, or an owner or admin; ``admin``, an owner or admin."""
    name: Optional[str]
    """The name of the person who made the key, or the holder's, when it waits on them."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "WaitingOn":
        return _stamp(cls(who=_str(d, "who"), name=_opt_str(d, "name")), d)


@dataclass
class MachineRequest(APIObject):
    """A request, in an organisation, to fork a machine for someone or to hand it off to them: given by its holder (or
    one above them), or asked for by the person who would get it - or a machine handed off, back with its giver, or a
    member's fork of a machine they hold (``machines.fork``), made once it is approved."""

    id: str
    kind: str
    """``fork`` or ``hand_off``."""
    way: str
    """``give``: by the machine's holder or one above them - and a member's fork of their own machine, its ``holder``
    and ``receiver`` both them; ``ask``: by the one who would get it; ``back``: a machine handed off, back with the one
    who handed it off - taken back by them (``asked_by`` is the ``receiver``), or given back by its holder."""
    machine: Optional[RequestMachine]
    """None while an ask waits for its holder to pick which of their machines."""
    holder: PersonName
    receiver: PersonName
    """Who gets the machine, or the fork."""
    asked_by: PersonName
    from_team: Optional[str]
    """The machine's team, by its project."""
    to_team: Optional[str]
    """The team it goes into, when it is another."""
    gpu: bool
    why: str
    state: RequestState
    waiting_on: Optional[WaitingOn]
    """Whose OK it waits on; None once it waits no more."""
    decided_by: Optional[PersonName]
    decision_why: str
    result: Optional[str]
    """The fork, by its id, once it is made."""
    backup: Optional[str]
    """A fork from one of the machine's backups: that backup's id; None for a fork of the machine as it is, and for a
    hand-off."""
    cpus: Optional[int]
    """A fork from a backup asked at another size: its CPUs; None for the machine's own size."""
    failure: Optional[str]
    """Why it could not be carried out, such as ``Machines cannot sign out yet``."""
    restarted: bool
    """The machine started again on the way, its programs ended."""
    keys_revoked: int
    """Wheelmachine keys found in it as it was cleared, and revoked."""
    created_at: str
    expires_at: str
    done_at: Optional[str]

    @property
    def open(self) -> bool:
        """Whether it still waits, or is being carried out."""
        return self.state in ("waiting", "approved")

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "MachineRequest":
        return _stamp(
            cls(
                id=_str(d, "id"),
                kind=_str(d, "kind"),
                way=_str(d, "way"),
                machine=_opt(d, "machine", RequestMachine.from_dict),
                holder=PersonName.from_dict(_dict(d, "holder")),
                receiver=PersonName.from_dict(_dict(d, "receiver")),
                asked_by=PersonName.from_dict(_dict(d, "asked_by")),
                from_team=_opt_str(d, "from_team"),
                to_team=_opt_str(d, "to_team"),
                gpu=_bool(d, "gpu"),
                why=_str(d, "why"),
                state=_str(d, "state", "waiting"),  # type: ignore[arg-type]
                waiting_on=_opt(d, "waiting_on", WaitingOn.from_dict),
                decided_by=_opt(d, "decided_by", PersonName.from_dict),
                decision_why=_str(d, "decision_why"),
                result=_opt_str(d, "result"),
                backup=_opt_str(d, "backup"),
                cpus=_opt_int(d, "cpus"),
                failure=_opt_str(d, "failure"),
                restarted=_bool(d, "restarted"),
                keys_revoked=_int(d, "keys_revoked"),
                created_at=_str(d, "created_at"),
                expires_at=_str(d, "expires_at"),
                done_at=_opt_str(d, "done_at"),
            ),
            d,
        )


@dataclass
class TeamRules(APIObject):
    """A team's rules, in an organisation: those set, else the defaults - hand-offs and forks ``ask``, GPUs and open
    internet on, no budget."""

    project: str
    hand_offs: RuleWay
    """Whether its machines are handed off freely, with a manager's OK, or not at all."""
    forks: Literal["ask", "off"]
    """Whether its members fork its machines - every fork, of their own machine or for another person, waiting for the
    OK of a full manager of the team, an owner or an admin (``ask``) - or not at all (``off``). Never ``free``."""
    gpus: bool
    """Whether GPU machines are made, forked or started in it."""
    open_internet: bool
    """Whether its machines may have open internet; one open already stays open until changed."""
    month_budget_usd: Optional[float]
    """Deprecated: ``month_budget`` in US dollars at Wheelmachine's rate."""
    spent_usd: Optional[float]
    """Deprecated: ``spent`` in US dollars at Wheelmachine's rate."""
    open_machines: Optional[int]
    """Its machines whose internet is open; None as ``spent`` is."""
    models: str
    """``any``, until a team chooses its models."""
    set_by: Optional[PersonName]
    set_at: Optional[str]
    max_machine_vcpus: Optional[int] = None
    """The largest machine it holds, in CPUs: a bigger one is not made, forked, moved or handed into it, nor started in
    it. None for no rule."""
    idle_max_seconds: Optional[int] = None
    """The longest its machines stay up idle before they sleep, in seconds. None for no rule."""
    public_ports: Literal["on", "links_only", "off"] = "on"
    """Whether its machines' ports may be opened to the whole internet (``on``), only shared by a five-minute link
    (``links_only``), or neither (``off``)."""
    currency: str = "USD"
    """The organisation's currency, which ``month_budget`` and ``spent`` are in."""
    month_budget: Optional[float] = None
    """What its machines may come to in a month, in the organisation's currency; None for no budget."""
    spent: Optional[float] = None
    """What its machines came to this month: shown to its managers, owners, admins and billing; None to its members."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "TeamRules":
        return _stamp(
            cls(
                project=_str(d, "project"),
                hand_offs=_str(d, "hand_offs", "ask"),  # type: ignore[arg-type]
                forks=_str(d, "forks", "ask"),  # type: ignore[arg-type]
                gpus=_bool(d, "gpus", True),
                open_internet=_bool(d, "open_internet", True),
                month_budget_usd=_opt_float(d, "month_budget_usd"),
                spent_usd=_opt_float(d, "spent_usd"),
                open_machines=_opt_int(d, "open_machines"),
                models=_str(d, "models", "any"),
                set_by=_opt(d, "set_by", PersonName.from_dict),
                set_at=_opt_str(d, "set_at"),
                max_machine_vcpus=_opt_int(d, "max_machine_vcpus"),
                idle_max_seconds=_opt_int(d, "idle_max_seconds"),
                public_ports=_str(d, "public_ports", "on"),  # type: ignore[arg-type]
                currency=_currency(d),
                month_budget=_amount(d, "month_budget"),
                spent=_amount(d, "spent"),
            ),
            d,
        )


@dataclass
class Backup(APIObject):
    """A backup: a machine's files as they were when it was taken. ``client.machines.fork(backup.machine,
    backup=backup)`` makes a new machine from it, at the machine's size or another."""

    id: str
    machine: str
    note: Optional[str]
    created_at: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Backup":
        return _stamp(
            cls(id=_str(d, "id"), machine=_str(d, "machine"), note=_opt_str(d, "note"), created_at=_str(d, "created_at")),
            d,
        )


@dataclass
class ExecResult(APIObject):
    """What a command wrote and how it ended."""

    exit_code: int
    stdout: str
    """Empty when ``binary_output`` was asked for: then the bytes are in ``stdout_base64``."""
    stdout_base64: Optional[str]
    stderr: str
    duration_ms: float
    truncated: bool
    """True when the output was too long to come back whole: cut, past 8 MiB of a stream or as little as 5 MiB of one that
    is mostly <, >, & or control characters, or left out when it could not be brought back. ``exit_code`` is the
    command's either way."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "ExecResult":
        return _stamp(
            cls(
                exit_code=_int(d, "exit_code"),
                stdout=_str(d, "stdout"),
                stdout_base64=_opt_str(d, "stdout_base64"),
                stderr=_str(d, "stderr"),
                duration_ms=_float(d, "duration_ms"),
                truncated=_bool(d, "truncated"),
            ),
            d,
        )


@dataclass
class ScreenPicture(APIObject):
    """A picture of a machine's desktop. It shows whatever was on the screen, so take care where it goes."""

    width: int
    height: int
    scale: float
    """How far it was drawn down: 0.5 is half the desktop's size each way."""
    format: str
    """``png`` or ``jpeg``."""
    data_base64: str = field(repr=False)
    """The picture's file, in base64: what is on the screen, and many megabytes of it, so never in a repr."""

    def to_bytes(self) -> bytes:
        """The picture's file."""
        return base64.b64decode(self.data_base64)

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "ScreenPicture":
        return _stamp(
            cls(
                width=_int(d, "width"),
                height=_int(d, "height"),
                scale=_float(d, "scale"),
                format=_str(d, "format"),
                data_base64=_str(d, "data_base64"),
            ),
            d,
        )


@dataclass
class ScreenActionsDone(APIObject):
    """How many actions on a machine's desktop were done, all of them, and the picture when one was asked for."""

    done: int
    picture: Optional[ScreenPicture]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "ScreenActionsDone":
        picture = _opt_dict(d, "picture")
        return _stamp(cls(done=_int(d, "done"), picture=ScreenPicture.from_dict(picture) if picture is not None else None), d)


@dataclass
class MetricsPoint(APIObject):
    """One step of a machine. A number that could not be read is None, and so are the rates of a step its readings
    cover none of (the first after it woke)."""

    at: str
    """Where the step begins."""
    cpu_percent: Optional[float]
    """Its CPUs' use, a share of its own size, 0 to 100."""
    memory_bytes: Optional[float]
    """The most memory it held in the step."""
    memory_limit_bytes: Optional[float]
    disk_read_bps: Optional[float]
    disk_write_bps: Optional[float]
    net_rx_bps: Optional[float]
    net_tx_bps: Optional[float]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "MetricsPoint":
        return _stamp(
            cls(
                at=_str(d, "at"),
                cpu_percent=_opt_float(d, "cpu_percent"),
                memory_bytes=_opt_float(d, "memory_bytes"),
                memory_limit_bytes=_opt_float(d, "memory_limit_bytes"),
                disk_read_bps=_opt_float(d, "disk_read_bps"),
                disk_write_bps=_opt_float(d, "disk_write_bps"),
                net_rx_bps=_opt_float(d, "net_rx_bps"),
                net_tx_bps=_opt_float(d, "net_tx_bps"),
            ),
            d,
        )


@dataclass
class MachineMetrics(APIObject):
    """What a machine used, a point a step. A point a minute is kept, for up to the last day: ``kept_since`` is how far back that goes (None: nothing kept)."""

    machine: str
    step_s: int
    cpus: Optional[float]
    """What ``cpu_percent`` is a share of."""
    kept_since: Optional[str]
    points: List[MetricsPoint]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "MachineMetrics":
        return _stamp(
            cls(
                machine=_str(d, "machine"),
                step_s=_int(d, "step_s", 60),
                cpus=_opt_float(d, "cpus"),
                kept_since=_opt_str(d, "kept_since"),
                points=[MetricsPoint.from_dict(p) for p in _list(d, "points")],
            ),
            d,
        )


@dataclass
class HeaderRule(APIObject):
    """A request header a secret kept outside the machines goes in, and how it is written there."""

    name: str
    format: str
    """How the value is written, with ``{value}`` once: ``Bearer {value}``, or ``{value}`` for the value alone."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "HeaderRule":
        return _stamp(cls(name=_str(d, "name"), format=_str(d, "format", "{value}")), d)


@dataclass
class Outside(APIObject):
    """How a secret kept outside the machines is sent - never its value, which never enters a machine: the machine
    holds a placeholder, and the value is added on the way out, only to requests going to ``hosts``."""

    preset: Optional[str]
    """The preset it follows (``client.secrets.presets()``), an AI key's; None for a policy of its own."""
    hosts: List[str]
    header: str
    """The first of ``headers``."""
    format: str
    headers: List[HeaderRule]
    """Every request header its value goes in."""
    query: List[str]
    """Query parameters whose value may carry it."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Outside":
        return _stamp(
            cls(preset=_opt_str(d, "preset"), hosts=_strs(d, "hosts"), header=_str(d, "header"), format=_str(d, "format", "{value}"),
                headers=[HeaderRule.from_dict(h) for h in _list(d, "headers")], query=_strs(d, "query")),
            d,
        )


@dataclass
class Secret(APIObject):
    """A secret as the API shows it: its name, the machines given it and how it is kept - never its value, which the
    API does not show again once it is set."""

    name: str
    machines: List[str]
    """The machines that name it, that the key in use can see."""
    outside: Optional[Outside]
    """Set when it is kept outside the machines; None for an ordinary secret, whose value reaches them."""
    every_machine: bool
    """On every machine of the account that may send it somewhere, not only on those that name it: an AI key is,
    unless it was set with ``every_machine=False``."""
    created_at: str
    updated_at: str
    """When its value was last set."""

    @property
    def ai_key(self) -> bool:
        """Whether it is an AI key: kept outside the machines by its name's preset."""
        return self.outside is not None and self.outside.preset is not None

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Secret":
        return _stamp(
            cls(name=_str(d, "name"), machines=_strs(d, "machines"), outside=_opt(d, "outside", Outside.from_dict),
                every_machine=_bool(d, "every_machine"), created_at=_str(d, "created_at"), updated_at=_str(d, "updated_at")),
            d,
        )


@dataclass
class SecretPreset(APIObject):
    """A model provider's key as the API knows it by its names: set under one of ``names``, a secret is kept outside the
    machines, on every one of them, and sent only to ``hosts``."""

    id: str
    title: str
    """The provider, as people know it."""
    names: List[str]
    hosts: List[str]
    headers: List[HeaderRule]
    query: List[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "SecretPreset":
        return _stamp(
            cls(id=_str(d, "id"), title=_str(d, "title"), names=_strs(d, "names"), hosts=_strs(d, "hosts"),
                headers=[HeaderRule.from_dict(h) for h in _list(d, "headers")], query=_strs(d, "query")),
            d,
        )


@dataclass
class ModelPrice(APIObject):
    """A model's list price: a million tokens in and a million out, in US dollars, as the provider that bills for it
    (``source``) lists it at ``url``, checked on ``as_of`` (a day). It is paid to that provider with the account's own
    key: Wheelmachine charges nothing for a model."""

    input_per_million_usd: float
    output_per_million_usd: float
    source: str
    url: str
    as_of: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "ModelPrice":
        return _stamp(
            cls(input_per_million_usd=_float(d, "input_per_million_usd"), output_per_million_usd=_float(d, "output_per_million_usd"),
                source=_str(d, "source"), url=_str(d, "url"), as_of=_str(d, "as_of")),
            d,
        )


@dataclass
class ModelIntelligence(APIObject):
    """A model's score on LMArena's text leaderboard: the best it has there at any effort, as a whole number, higher
    better - the board (``source``) at ``url``, its ``license``, and the day it was published (``as_of``)."""

    score: float
    source: str
    url: str
    license: str
    as_of: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "ModelIntelligence":
        return _stamp(
            cls(score=_float(d, "score"), source=_str(d, "source"), url=_str(d, "url"), license=_str(d, "license"), as_of=_str(d, "as_of")),
            d,
        )


@dataclass
class Model(APIObject):
    """A model one of the account's AI keys can use, as its provider lists it for the key. ``id`` is its provider and the
    provider's own id for it (``openai/gpt-6.1-sol``) - what a choice names."""

    id: str
    supplier: str
    """Its provider, as the presets name it."""
    model: str
    """The provider's own id for it."""
    name: str
    created_at: Optional[str]
    retiring_at: Optional[str]
    """When its provider says it is to be retired."""
    agents: List[str]
    """The agents of the account's machines that can use its provider's models."""
    price: Optional[ModelPrice] = None
    """What it costs, as the provider that bills for it lists it; None where that is not known."""
    intelligence: Optional[ModelIntelligence] = None
    """Its score on LMArena's text leaderboard; None where the board has none for it."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Model":
        return _stamp(
            cls(id=_str(d, "id"), supplier=_str(d, "supplier"), model=_str(d, "model"), name=_str(d, "name"),
                created_at=_opt_str(d, "created_at"), retiring_at=_opt_str(d, "retiring_at"), agents=_strs(d, "agents"),
                price=_opt(d, "price", ModelPrice.from_dict), intelligence=_opt(d, "intelligence", ModelIntelligence.from_dict)),
            d,
        )


@dataclass
class ModelSupplier(APIObject):
    """A provider of the account's AI keys, and how its list stands: ``listed``, ``refused`` (the key), ``busy``,
    ``unreachable``, or ``unlisted`` - no agent works with its keys yet. ``detail`` says it in words when it is not
    listed; while a provider does not answer, the list it last gave in the last day is shown, read at ``listed_at``."""

    id: str
    title: str
    key: str
    """The name of the AI key its list is read with."""
    whose: str
    state: str
    listed_at: Optional[str]
    detail: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "ModelSupplier":
        return _stamp(
            cls(id=_str(d, "id"), title=_str(d, "title"), key=_str(d, "key"), whose=_str(d, "whose"), state=_str(d, "state"),
                listed_at=_opt_str(d, "listed_at"), detail=_opt_str(d, "detail")),
            d,
        )


@dataclass
class AllowedModels(APIObject):
    """A list of the models an organisation's people may use, as its owners and admins keep it: for the whole
    organisation (``level`` ``organisation``), a team (``team``), a role - a person's level in the team: ``member``,
    ``approver``, ``manager`` or ``full`` (``role``) - or one person (``person``); by ``id`` and by ``name``; its
    ``models`` in order, the first the one an agent starts on where its person chose none of them; and who set it, when.
    A person's choices are limited to the most specific list they have: theirs, their role's, their team's, the
    organisation's."""

    level: str
    id: str
    name: str
    models: List[str]
    set_by: Optional[PersonName]
    set_at: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "AllowedModels":
        return _stamp(
            cls(level=_str(d, "level"), id=_str(d, "id"), name=_str(d, "name"), models=_strs(d, "models"),
                set_by=_opt(d, "set_by", PersonName.from_dict), set_at=_str(d, "set_at")),
            d,
        )


@dataclass
class ModelList(APIObject):
    """The models of the account's AI keys (``data``), their providers (``suppliers``), and the list of the models the
    caller may use that applies to them (``allowed``; None when none does) - its models alone are in ``data`` then."""

    data: List[Model]
    suppliers: List[ModelSupplier]
    allowed: Optional[AllowedModels] = None
    currency: str = "USD"
    """The account's currency; with ``rate``, what a US dollar is in it at Wheelmachine's rate (1 in an account counted
    in dollars), a provider's dollar price is shown beside it as a guide. The price itself is the provider's, in dollars."""
    rate: Optional[float] = None

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "ModelList":
        currency = _currency(d)
        rate = _opt_float(d, "rate")
        return _stamp(
            cls(data=[Model.from_dict(m) for m in _list(d, "data")], suppliers=[ModelSupplier.from_dict(s) for s in _list(d, "suppliers")],
                allowed=_opt(d, "allowed", AllowedModels.from_dict), currency=currency,
                rate=rate if rate is not None else (1.0 if currency == "USD" else None)),
            d,
        )


@dataclass
class AgentSupplier(APIObject):
    """A provider an agent can use, and the model it starts on with its key when none is chosen (None: its own)."""

    id: str
    title: str
    default: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "AgentSupplier":
        return _stamp(cls(id=_str(d, "id"), title=_str(d, "title"), default=_opt_str(d, "default")), d)


@dataclass
class Agent(APIObject):
    """An agent of the account's machines, and the model it starts on. ``model`` is the one chosen for it - for it
    (``chosen_by`` ``you``), for every agent (``every_agent``), or by the list of the models you may use (``allowed``:
    its first the agent can use, where you chose none it holds); ``starts_on`` what it starts on with your keys; and
    ``state`` how that stands in a word: ``chosen``, ``default``, ``own``, ``no_key``, or - for a choice it cannot
    take - ``needs_key``, ``cannot_use`` or ``retired``, or ``not_allowed`` where it can use none of the models you may
    use, said in a sentence in ``note``."""

    name: str
    title: str
    suppliers: List[AgentSupplier]
    model: Optional[str]
    chosen_by: Optional[str]
    starts_on: Optional[str]
    state: str
    note: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Agent":
        return _stamp(
            cls(name=_str(d, "name"), title=_str(d, "title"), suppliers=[AgentSupplier.from_dict(s) for s in _list(d, "suppliers")],
                model=_opt_str(d, "model"), chosen_by=_opt_str(d, "chosen_by"), starts_on=_opt_str(d, "starts_on"),
                state=_str(d, "state"), note=_opt_str(d, "note")),
            d,
        )


@dataclass
class AgentList(APIObject):
    """The agents of the account's machines (``data``), the one model chosen for every agent (``every_agent``, its
    ``model`` and ``chosen_by``; None for none), and the list of the models you may use that applies to your choices in the
    team (``allowed``; None when none does)."""

    data: List[Agent]
    every_agent: Optional[Dict[str, Any]]
    allowed: Optional[AllowedModels] = None

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "AgentList":
        return _stamp(cls(data=[Agent.from_dict(a) for a in _list(d, "data")], every_agent=_opt_dict(d, "every_agent"),
                          allowed=_opt(d, "allowed", AllowedModels.from_dict)), d)


@dataclass
class DeploySource(APIObject):
    """Where a deployment's code comes from."""

    kind: str
    """``repo``, a repository of a connection; ``url``, a public repository's address; ``upload``, files sent."""
    provider: Optional[str]
    connection: Optional[str]
    host: Optional[str]
    repo: Optional[str]
    url: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "DeploySource":
        return _stamp(
            cls(
                kind=_str(d, "kind"),
                provider=_opt_str(d, "provider"),
                connection=_opt_str(d, "connection"),
                host=_opt_str(d, "host"),
                repo=_opt_str(d, "repo"),
                url=_opt_str(d, "url"),
            ),
            d,
        )


@dataclass
class DeployRun(APIObject):
    """One go at putting a version on a deployment's machine."""

    id: str
    why: str
    state: str
    """``queued``, ``working``, ``live``, ``failed`` or ``cancelled``."""
    step: str
    progress: int
    note: Optional[str]
    failure: Optional[str]
    commit: Optional[str]
    branch: Optional[str]
    files: Optional[int]
    created_at: str
    started_at: Optional[str]
    ended_at: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "DeployRun":
        return _stamp(
            cls(
                id=_str(d, "id"),
                why=_str(d, "why"),
                state=_str(d, "state"),
                step=_str(d, "step"),
                progress=_int(d, "progress"),
                note=_opt_str(d, "note"),
                failure=_opt_str(d, "failure"),
                commit=_opt_str(d, "commit"),
                branch=_opt_str(d, "branch"),
                files=_opt_int(d, "files"),
                created_at=_str(d, "created_at"),
                started_at=_opt_str(d, "started_at"),
                ended_at=_opt_str(d, "ended_at"),
            ),
            d,
        )


@dataclass
class Deployment(APIObject):
    """An app on a machine of its own, at a public address, with its current or last run."""

    id: str
    name: str
    project: str
    held_by: Optional[HeldBy]
    kind: str
    source: DeploySource
    branch: Optional[str]
    commit: Optional[str]
    build: Optional[str]
    """What builds it; None unless you use its machine, with a key that holds ``cloud:machines:exec``."""
    start: Optional[str]
    """What starts it; None unless you use its machine, with a key that holds ``cloud:machines:exec``."""
    port: int
    cpus: Optional[int]
    type: Optional[str]
    region: Optional[str]
    keep_awake: bool
    secrets: List[str]
    machine: Optional[str]
    url: Optional[str]
    """Its public address, once it has been live."""
    state: str
    """``building``, ``waiting_for_files``, ``live``, ``not_answering`` or ``failed``; ``run.failure`` says why."""
    last_status: Optional[int]
    run: Optional[DeployRun]
    created_at: str
    updated_at: str
    live_at: Optional[str]
    left_out: List[str] = field(default_factory=list)
    """What ``send_folder`` did not send, by its path in the folder."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Deployment":
        return _stamp(
            cls(
                id=_str(d, "id"),
                name=_str(d, "name"),
                project=_str(d, "project"),
                held_by=_opt(d, "held_by", HeldBy.from_dict),
                kind=_str(d, "kind"),
                source=DeploySource.from_dict(_opt_dict(d, "source") or {}),
                branch=_opt_str(d, "branch"),
                commit=_opt_str(d, "commit"),
                build=_opt_str(d, "build"),
                start=_opt_str(d, "start"),
                port=_int(d, "port"),
                cpus=_opt_int(d, "cpus"),
                type=_opt_str(d, "type"),
                region=_opt_str(d, "region"),
                keep_awake=_bool(d, "keep_awake"),
                secrets=_strs(d, "secrets"),
                machine=_opt_str(d, "machine"),
                url=_opt_str(d, "url"),
                state=_str(d, "state"),
                last_status=_opt_int(d, "last_status"),
                run=_opt(d, "run", DeployRun.from_dict),
                created_at=_str(d, "created_at"),
                updated_at=_str(d, "updated_at"),
                live_at=_opt_str(d, "live_at"),
            ),
            d,
        )


@dataclass
class FolderFiles:
    """What ``send_folder()`` would send of a folder: its files by their paths in it, how many bytes they come to, and what
    it would leave out. Asked of nothing but the folder."""

    files: List[str]
    bytes: int
    left_out: List[str]


@dataclass
class DeployGuess(APIObject):
    """How code most likely builds and starts, to change as needed."""

    runtime: str
    build: str
    start: str
    port: int
    note: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "DeployGuess":
        return _stamp(
            cls(runtime=_str(d, "runtime"), build=_str(d, "build"), start=_str(d, "start"), port=_int(d, "port"), note=_opt_str(d, "note")),
            d,
        )


@dataclass
class DeployRepo(APIObject):
    """A repository that can be deployed into a team."""

    connection: str
    provider: str
    host: str
    repo: str
    shared: bool
    """Given to the team already; False: deploying it gives it to the team to read first."""
    updated_at: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "DeployRepo":
        return _stamp(
            cls(
                connection=_str(d, "connection"),
                provider=_str(d, "provider"),
                host=_str(d, "host"),
                repo=_str(d, "repo"),
                shared=_bool(d, "shared"),
                updated_at=_opt_str(d, "updated_at"),
            ),
            d,
        )


@dataclass
class DeployBranch(APIObject):
    name: str
    commit: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "DeployBranch":
        return _stamp(cls(name=_str(d, "name"), commit=_str(d, "commit")), d)


@dataclass
class DeployBranches(APIObject):
    """A repository's branches, and which is its default."""

    data: List[DeployBranch]
    default: Optional[str]
    known: bool
    """False for a public repository on a host none are read of: type the branch."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "DeployBranches":
        return _stamp(cls(data=[DeployBranch.from_dict(b) for b in _list(d, "data")], default=_opt_str(d, "default"), known=_bool(d, "known")), d)


@dataclass
class DeployLog(APIObject):
    """A deployment's log: a run's own (``build``), or the end of what the app itself writes (``app``)."""

    kind: str
    run: Optional[str]
    text: str
    done: bool
    """With ``build``, the run has ended; with ``app``, the app is not running."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "DeployLog":
        return _stamp(cls(kind=_str(d, "kind"), run=_opt_str(d, "run"), text=_str(d, "text"), done=_bool(d, "done")), d)


@dataclass
class Port(APIObject):
    """A port of a machine, open at a web address of its own for the program listening on it."""

    machine: str
    port: int
    public: bool
    """True: anyone may reach the address. False: only a call with a key that can see the machine, and a browser
    that opened a link made for it."""
    url: str
    """``https://<port>-<the machine's id without m_>.run.wheelmachine.ai``."""
    created_at: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Port":
        return _stamp(
            cls(
                machine=_str(d, "machine"),
                port=_int(d, "port"),
                public=_bool(d, "public"),
                url=_str(d, "url"),
                created_at=_str(d, "created_at"),
            ),
            d,
        )


@dataclass
class PortLink(APIObject):
    """A link that lets one browser into a private address: its ``url`` opens once, within five minutes. It is shown
    in this answer only, and never in this object's repr."""

    machine: str
    port: int
    url: str = field(repr=False)
    expires_at: str
    """When the link stops opening, if it was not opened."""
    access_s: int
    """How long the browser that opens it may reach the address."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "PortLink":
        return _stamp(
            cls(
                machine=_str(d, "machine"),
                port=_int(d, "port"),
                url=_str(d, "url"),
                expires_at=_str(d, "expires_at"),
                access_s=_int(d, "access_s"),
            ),
            d,
        )


#: ``web``: a page at the machine's private web address for its port; ``service``: reached from inside the machine
#: alone; ``agent``: an agent with a session of its own in the machine's terminal, running its ``command``;
#: ``terminal``: an agent run in its terminal by typing its ``command``.
AppKind = Literal["web", "service", "agent", "terminal"]


@dataclass
class App(APIObject):
    """An app in a machine, as the machine says it is now. ``link`` (with ``expires_at`` and ``access_s``) comes in the
    answer to turning a web app on and to ``link()``; ``token`` in the answer to turning on an app that has one of its
    own. Each is shown in that answer only, and neither ever in this object's repr."""

    machine: str
    name: str
    title: str
    kind: str
    """``web``, ``service``, ``agent`` or ``terminal`` (AppKind)."""
    on: Optional[bool]
    """Whether it is turned on; None for a terminal agent, which runs while it is typed."""
    running: Optional[bool]
    """Whether it runs now - for an agent, whether its session does; None for a terminal agent."""
    port: Optional[int]
    """Where it listens inside the machine, when that is said."""
    path: Optional[str]
    """A web app's own page on its address."""
    url: Optional[str]
    """A web app's private address: ``https://<port>-<the machine's id without m_>.run.wheelmachine.ai``."""
    command: Optional[str]
    """What an agent's session runs, or what to type in the machine's terminal to run a terminal agent."""
    keys_changed: Optional[bool] = None
    """For an agent: whether its session, which runs, began with other AI keys than the machine has now. Turned off and
    on again, its next session begins with them. None when the machine does not say it."""
    suppliers: Optional[List[str]] = None
    """For an agent, and a web app with an agent of its own: the providers whose AI keys it can use, in the order it takes
    them. None when the machine does not say them (one made before models were chosen)."""
    model: Optional[str] = None
    """With ``suppliers``: what it last started on, as its provider and the provider's id for the model; None when it has not
    started yet, or picks a model of its own."""
    model_by: Optional[str] = None
    """With ``suppliers``: who chose what it last started on - ``chosen``, ``team``, ``default`` or ``agent``."""
    model_changed: Optional[bool] = None
    """With ``suppliers``: whether it runs on another model than the one chosen for it now; turned off and on again, it
    starts on the one chosen."""
    link: Optional[str] = field(default=None, repr=False)
    """A link that lets one browser into the app's address once, within five minutes, landing on its page."""
    expires_at: Optional[str] = None
    access_s: Optional[int] = None
    """How long the browser that opens ``link`` may reach the address."""
    token: Optional[str] = field(default=None, repr=False)
    """The app's own token, which its page asks for: shown in the answer to turning it on alone."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "App":
        return _stamp(
            cls(
                machine=_str(d, "machine"),
                name=_str(d, "name"),
                title=_str(d, "title"),
                kind=_str(d, "kind"),
                on=_opt_bool(d, "on"),
                running=_opt_bool(d, "running"),
                port=_opt_int(d, "port"),
                path=_opt_str(d, "path"),
                url=_opt_str(d, "url"),
                command=_opt_str(d, "command"),
                keys_changed=_opt_bool(d, "keys_changed"),
                suppliers=_strs(d, "suppliers") if isinstance(d.get("suppliers"), list) else None,
                model=_opt_str(d, "model"),
                model_by=_opt_str(d, "model_by"),
                model_changed=_opt_bool(d, "model_changed"),
                link=_opt_str(d, "link"),
                expires_at=_opt_str(d, "expires_at"),
                access_s=_opt_int(d, "access_s"),
                token=_opt_str(d, "token"),
            ),
            d,
        )


@dataclass
class CodeConnection(APIObject):
    """A code host connected to the account: a GitHub App installation, or a GitLab group."""

    id: str
    provider: str
    """``github`` or ``gitlab``."""
    host: str
    name: str
    suspended: bool
    added_at: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "CodeConnection":
        return _stamp(cls(id=_str(d, "id"), provider=_str(d, "provider"), host=_str(d, "host"), name=_str(d, "name"),
                          suspended=_bool(d, "suspended"), added_at=_str(d, "added_at")), d)


@dataclass
class CodeGrant(APIObject):
    """A repository a team is given: to ``read``, or to ``write`` (push) too."""

    id: str
    project: str
    connection: str
    host: str
    repo: str
    access: str
    granted_at: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "CodeGrant":
        return _stamp(cls(id=_str(d, "id"), project=_str(d, "project"), connection=_str(d, "connection"), host=_str(d, "host"),
                          repo=_str(d, "repo"), access=_str(d, "access"), granted_at=_str(d, "granted_at")), d)


@dataclass
class Code(APIObject):
    """The account's code hosts, and the repositories given to the teams the caller is in."""

    github: bool
    """Whether GitHub can be connected here."""
    connections: List[CodeConnection]
    grants: List[CodeGrant]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Code":
        return _stamp(cls(github=_bool(d, "github"), connections=[CodeConnection.from_dict(c) for c in _list(d, "connections")],
                          grants=[CodeGrant.from_dict(g) for g in _list(d, "grants")]), d)


@dataclass
class CodePush(APIObject):
    """A team's own rule for pushes to a repository: the branches its machines push to, none for any the account allows,
    and whether its tags too."""

    branches: List[str]
    tags: bool

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "CodePush":
        return _stamp(cls(branches=_strs(d, "branches"), tags=_bool(d, "tags")), d)


@dataclass
class MachineRepository(APIObject):
    """A repository a machine reaches: to ``read``, or to ``write`` (push) too."""

    provider: str
    """``github`` or ``gitlab``."""
    host: str
    repository: str
    """Its path on its host, such as ``acme/api``."""
    access: str
    linked: bool
    """Whether the person who holds the machine has their own account linked there, and it still works."""
    can_push: bool
    """Whether a push will go: to ``write``, with their account linked - or none needed, on a company's own GitLab."""
    push: Optional[CodePush]
    """The team's own rule for pushes to it; None for any branch the account does not protect, and its tags."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "MachineRepository":
        push = d.get("push")
        return _stamp(cls(provider=_str(d, "provider"), host=_str(d, "host"), repository=_str(d, "repository"), access=_str(d, "access"),
                          linked=_bool(d, "linked"), can_push=_bool(d, "can_push"), push=CodePush.from_dict(push) if isinstance(push, Mapping) else None), d)


@dataclass
class MachineRepositories(APIObject):
    """The repositories a machine reaches now, and the branches no machine of the account pushes to."""

    data: List[MachineRepository]
    protected_branches: List[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "MachineRepositories":
        return _stamp(cls(data=[MachineRepository.from_dict(r) for r in _list(d, "data")], protected_branches=_strs(d, "protected_branches")), d)


@dataclass
class PullRequest(APIObject):
    """A pull request opened from a machine (``opened``, on GitHub: its ``number`` and ``url``) - or, on GitLab, ``run``:
    the line of git to run in the clone in the machine, which opens a merge request as it pushes, and a ``note`` saying
    so."""

    opened: bool
    provider: str
    host: str
    repository: str
    head: str
    base: str
    draft: bool
    number: Optional[int]
    url: Optional[str]
    run: Optional[str]
    note: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "PullRequest":
        return _stamp(cls(opened=_bool(d, "opened"), provider=_str(d, "provider"), host=_str(d, "host"), repository=_str(d, "repository"),
                          head=_str(d, "head"), base=_str(d, "base"), draft=_bool(d, "draft"), number=_opt_int(d, "number"), url=_opt_str(d, "url"),
                          run=_opt_str(d, "run"), note=_opt_str(d, "note")), d)


@dataclass
class FolderGrant(APIObject):
    """A folder given to a team (a project, in a person's own account): to ``view``, or to ``edit``."""

    folder: str
    folder_name: str
    project: str
    access: str
    granted_at: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "FolderGrant":
        return _stamp(cls(folder=_str(d, "folder"), folder_name=_str(d, "folder_name"), project=_str(d, "project"), access=_str(d, "access"),
                          granted_at=_str(d, "granted_at")), d)


@dataclass
class SharedFolder(APIObject):
    """A team folder, or a person's own (My files), as you may use it: ``edit``, ``view``, or ``size`` - how much it keeps,
    alone (another person's own, to an organisation's owners and admins)."""

    id: str
    kind: str
    """``team`` or ``own``."""
    name: str
    access: str
    teams: List[FolderGrant]
    bytes: int
    """What it keeps, its Trash included."""
    changed_at: Optional[str]
    left_at: Optional[str]
    """A person's own: when they left the organisation; it goes 30 days after (``goes_at``)."""
    goes_at: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "SharedFolder":
        return _stamp(cls(id=_str(d, "id"), kind=_str(d, "kind"), name=_str(d, "name"), access=_str(d, "access"),
                          teams=[FolderGrant.from_dict(g) for g in _list(d, "teams")], bytes=int(d.get("bytes") or 0),
                          changed_at=d.get("changed_at"), left_at=d.get("left_at"), goes_at=d.get("goes_at")), d)


@dataclass
class FolderList(APIObject):
    """The folders you see - your own first - and, to those who are told, what the account keeps in them and may."""

    on: bool
    """Whether folders are set up here."""
    data: List[SharedFolder]
    used_bytes: Optional[int]
    cap_bytes: Optional[int]
    file_most_bytes: int

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "FolderList":
        return _stamp(cls(on=_bool(d, "on"), data=[SharedFolder.from_dict(f) for f in _list(d, "data")], used_bytes=d.get("used_bytes"),
                          cap_bytes=d.get("cap_bytes"), file_most_bytes=int(d.get("file_most_bytes") or 0)), d)


@dataclass
class FolderEntry(APIObject):
    """A file or a folder in a folder."""

    name: str
    type: str
    """``file`` or ``folder``."""
    size: int
    changed_at: str
    changed_by: Optional[str]
    """The name of who changed it last - from one of their machines too."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "FolderEntry":
        by = d.get("changed_by") or {}
        return _stamp(cls(name=_str(d, "name"), type=_str(d, "type"), size=int(d.get("size") or 0), changed_at=_str(d, "changed_at"),
                          changed_by=by.get("name") if isinstance(by, Mapping) else None), d)


@dataclass
class FolderListing(APIObject):
    """One page of what is in one folder of a folder: folders first, then files, each by name. ``next`` is where the
    next page starts, None after the last."""

    folder: str
    path: str
    entries: List[FolderEntry]
    next: Optional[str]

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "FolderListing":
        return _stamp(cls(folder=_str(d, "folder"), path=_str(d, "path"), entries=[FolderEntry.from_dict(e) for e in _list(d, "entries")],
                          next=_opt_str(d, "next")), d)


@dataclass
class FolderTrash(APIObject):
    """What one deletion put in a folder's Trash: put back from there until ``goes_at``."""

    id: str
    folder: str
    path: str
    type: str
    bytes: int
    deleted_at: str
    goes_at: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "FolderTrash":
        return _stamp(cls(id=_str(d, "id"), folder=_str(d, "folder"), path=_str(d, "path"), type=_str(d, "type"), bytes=int(d.get("bytes") or 0),
                          deleted_at=_str(d, "deleted_at"), goes_at=_str(d, "goes_at")), d)


@dataclass
class Region(APIObject):
    """A place a machine can be asked to run near."""

    id: str
    city: str
    country: str

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Region":
        return _stamp(cls(id=_str(d, "id"), city=_str(d, "city"), country=_str(d, "country")), d)


@dataclass
class GpuOffer(APIObject):
    id: str
    model: str
    count: int
    memory_gb: int
    price_per_hour_usd: float
    available: bool
    interruptible: bool
    """Can be interrupted at any time; cheaper for it."""
    cpus: Optional[int]
    ram_gb: Optional[float]
    disk_gb: Optional[float]
    location: Optional[Location]
    currency: str = "USD"
    """The account's currency, which ``price_per_hour`` is in."""
    price_per_hour: float = 0.0
    """What an hour of it comes to, in the account's currency (``price_per_hour_usd``: in US dollars, the price list's
    own)."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "GpuOffer":
        return _stamp(
            cls(
                id=_str(d, "id"),
                model=_str(d, "model"),
                count=_int(d, "count", 1),
                memory_gb=_int(d, "memory_gb"),
                price_per_hour_usd=_float(d, "price_per_hour_usd"),
                available=_bool(d, "available"),
                interruptible=_bool(d, "interruptible"),
                cpus=_opt_int(d, "cpus"),
                ram_gb=_opt_float(d, "ram_gb"),
                disk_gb=_opt_float(d, "disk_gb"),
                location=_opt(d, "location", Location.from_dict),
                currency=_currency(d),
                price_per_hour=_amount(d, "price_per_hour") or 0.0,
            ),
            d,
        )


@dataclass
class GpuList(APIObject):
    for_sale: bool
    """False while GPU machines are not yet open to every account; the list shows what is coming."""
    data: List[GpuOffer]
    currency: str = "USD"
    """The account's currency, which each offer's ``price_per_hour`` is in."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "GpuList":
        return _stamp(cls(for_sale=_bool(d, "for_sale"), data=[GpuOffer.from_dict(o) for o in _list(d, "data")], currency=_currency(d)), d)


@dataclass
class UsageLine(APIObject):
    machine: str
    name: Optional[str]
    """The machine's name."""
    project: Optional[str]
    made_by: Optional[str]
    """In an organisation, the person who made the machine; None in a person's own account."""
    held_by: Optional[HeldBy]
    """In an organisation, whose the machine is; None in a person's own account."""
    running_seconds: float
    usd: float
    """What it came to in US dollars, as it is counted."""
    amount: float = 0.0
    """What it came to, in the account's currency."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "UsageLine":
        return _stamp(
            cls(
                machine=_str(d, "machine"),
                name=_opt_str(d, "name"),
                project=_opt_str(d, "project"),
                made_by=_opt_str(d, "made_by"),
                held_by=_opt(d, "held_by", HeldBy.from_dict),
                running_seconds=_float(d, "running_seconds"),
                usd=_float(d, "usd"),
                amount=_num(d.get("amount")) if "amount" in d else _float(d, "usd"),
            ),
            d,
        )


@dataclass
class AccountLimit(APIObject):
    """The account's own limit for the month and Wheelmachine's under it: the lower holds (``in_force_amount``), in the
    account's currency (``currency``). Amounts are None to those not shown them; ``limited`` says to anyone whether a
    limit holds the account. Each ``*_usd`` amount is the same in US dollars at Wheelmachine's rate, deprecated."""

    name: str
    kind: str
    month_cap_usd: Optional[float]
    in_force_usd: Optional[float]
    in_force: str
    """Whose limit holds: ``"account"``, ``"wheelmachine"``, or ``""`` for none."""
    spent_usd: Optional[float]
    percent: Optional[int]
    limited: bool
    wheelmachine_month_cap_usd: Optional[float]
    changed_by: List[str]
    you_change: bool
    currency: str = "USD"
    month_cap: Optional[float] = None
    in_force_amount: Optional[float] = None
    spent: Optional[float] = None
    wheelmachine_month_cap: Optional[float] = None

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "AccountLimit":
        wm = _dict(d, "wheelmachine")
        return _stamp(
            cls(
                name=_str(d, "name"),
                kind=_str(d, "kind"),
                month_cap_usd=_opt_float(d, "month_cap_usd"),
                in_force_usd=_opt_float(d, "in_force_usd"),
                in_force=_str(d, "in_force"),
                spent_usd=_opt_float(d, "spent_usd"),
                percent=_opt_int(d, "percent"),
                limited=_bool(d, "limited"),
                wheelmachine_month_cap_usd=_opt_float(wm, "month_cap_usd"),
                changed_by=_strs(d, "changed_by"),
                you_change=_bool(d, "you_change"),
                currency=_currency(d),
                month_cap=_amount(d, "month_cap"),
                in_force_amount=_num(d.get("in_force_amount")) if "in_force_amount" in d else _opt_float(d, "in_force_usd"),
                spent=_amount(d, "spent"),
                wheelmachine_month_cap=_amount(wm, "month_cap"),
            ),
            d,
        )


@dataclass
class TeamLimit(APIObject):
    """A team's budget for the month, and what it spent to those who see it."""

    id: str
    name: str
    month_budget_usd: Optional[float]
    spent_usd: Optional[float]
    percent: Optional[int]
    yours: bool
    you_change: bool
    month_budget: Optional[float] = None
    spent: Optional[float] = None

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "TeamLimit":
        return _stamp(
            cls(
                id=_str(d, "id"),
                name=_str(d, "name"),
                month_budget_usd=_opt_float(d, "month_budget_usd"),
                spent_usd=_opt_float(d, "spent_usd"),
                percent=_opt_int(d, "percent"),
                yours=_bool(d, "yours"),
                you_change=_bool(d, "you_change"),
                month_budget=_amount(d, "month_budget"),
                spent=_amount(d, "spent"),
            ),
            d,
        )


@dataclass
class PersonLimit(APIObject):
    """A person's own limits in an organisation."""

    person_id: str
    name: str
    role: str
    month_cap_usd: Optional[float]
    spent_usd: Optional[float]
    percent: Optional[int]
    max_machines: Optional[int]
    max_vcpus: Optional[int]
    gpus: bool
    you_change: bool
    month_cap: Optional[float] = None
    spent: Optional[float] = None

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "PersonLimit":
        who = _dict(d, "person")
        return _stamp(
            cls(
                person_id=_str(who, "id"),
                name=_str(who, "name"),
                role=_str(d, "role"),
                month_cap_usd=_opt_float(d, "month_cap_usd"),
                spent_usd=_opt_float(d, "spent_usd"),
                percent=_opt_int(d, "percent"),
                max_machines=_opt_int(d, "max_machines"),
                max_vcpus=_opt_int(d, "max_vcpus"),
                gpus=_bool(d, "gpus"),
                you_change=_bool(d, "you_change"),
                month_cap=_amount(d, "month_cap"),
                spent=_amount(d, "spent"),
            ),
            d,
        )


@dataclass
class LimitNear(APIObject):
    """A limit for the month that applies to you, and how near it is."""

    scope: str
    name: str
    limit_usd: Optional[float]
    spent_usd: Optional[float]
    percent: Optional[int]
    reached: bool
    limit: Optional[float] = None
    spent: Optional[float] = None

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "LimitNear":
        return _stamp(
            cls(
                scope=_str(d, "scope"),
                name=_str(d, "name"),
                limit_usd=_opt_float(d, "limit_usd"),
                spent_usd=_opt_float(d, "spent_usd"),
                percent=_opt_int(d, "percent"),
                reached=_bool(d, "reached"),
                limit=_amount(d, "limit"),
                spent=_amount(d, "spent"),
            ),
            d,
        )


@dataclass
class UsageLimits(APIObject):
    """Every limit for the month that applies to the account, as you may see it: the account's own and Wheelmachine's, each
    team's budget, each person's own, those that apply to you, and the thresholds told this month."""

    month: str
    resets_at: str
    """When limits for the month start again: the first of the next month, 00:00 UTC."""
    alert_percents: List[int]
    account: AccountLimit
    teams: List[TeamLimit]
    people: List[PersonLimit]
    yours: List[LimitNear]
    currency: str = "USD"
    """The account's currency: every amount is in it."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "UsageLimits":
        percents = d.get("alert_percents")
        return _stamp(
            cls(
                month=_str(d, "month"),
                resets_at=_str(d, "resets_at"),
                alert_percents=[int(p) for p in percents] if isinstance(percents, list) else [],
                account=AccountLimit.from_dict(_dict(d, "account")),
                teams=[TeamLimit.from_dict(t) for t in _list(d, "teams")],
                people=[PersonLimit.from_dict(p) for p in _list(d, "people")],
                yours=[LimitNear.from_dict(y) for y in _list(d, "yours")],
                currency=_currency(d),
            ),
            d,
        )


@dataclass
class Usage(APIObject):
    """Each machine's running time and cost in a month - and what the account's folders came to, to those who watch its
    money (``folders``; None to anyone else) - in the account's currency at Wheelmachine's rate now (``currency``), and
    in US dollars as it is counted (``*_usd``)."""

    month: str
    total_usd: float
    machines: List[UsageLine]
    folders_usd: Optional[float] = None
    currency: str = "USD"
    total: float = 0.0
    folders: Optional[float] = None

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Usage":
        folders = d.get("folders_usd")
        return _stamp(
            cls(
                month=_str(d, "month"),
                total_usd=_float(d, "total_usd"),
                machines=[UsageLine.from_dict(m) for m in _list(d, "machines")],
                folders_usd=float(folders) if isinstance(folders, (int, float)) and not isinstance(folders, bool) else None,
                currency=_currency(d),
                total=_amount(d, "total") or 0.0,
                folders=_amount(d, "folders"),
            ),
            d,
        )


CreditEntryKind = Literal["welcome", "machine_time", "folders", "purchase", "refund", "staff_credit", "staff_debit"]


@dataclass
class CreditEntry(APIObject):
    """Credit put in (``amount`` above zero) or taken (below), in the account's currency (``amount_usd``, deprecated: in
    US dollars at Wheelmachine's rate)."""

    id: str
    kind: CreditEntryKind
    """``welcome`` the free credit your own account gets - 5 of its currency; ``machine_time`` a machine's running time of a
    day (UTC); ``purchase`` credit bought, paid through Paddle (and a disputed payment that came back); ``refund``
    credit refunded through Paddle, or disputed with a bank; ``staff_credit`` and ``staff_debit`` credit
    Wheelmachine's staff put in or took back."""
    amount_usd: float
    reason: str
    """What it is, in words a person reads."""
    machine: Optional[str]
    """The machine whose running time it is; None for any other entry."""
    machine_name: Optional[str]
    """That machine's name, a deleted one's too."""
    at: str
    updated_at: str
    """When it last changed: a day's running time grows until the day ends; no other entry changes."""
    amount: float = 0.0

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "CreditEntry":
        return _stamp(
            cls(id=_str(d, "id"), kind=_str(d, "kind"), amount_usd=_float(d, "amount_usd"),  # type: ignore[arg-type]
                reason=_str(d, "reason"), machine=_opt_str(d, "machine"), machine_name=_opt_str(d, "machine_name"),
                at=_str(d, "at"), updated_at=_str(d, "updated_at"), amount=_amount(d, "amount") or 0.0),
            d,
        )


@dataclass
class CreditBalance(APIObject):
    """The account's credit - prepaid usage credit for Wheelmachine alone, counted in the account's currency
    (``currency``) - and a page of its entries, the newest first. Zero or below, the account cannot spend: its machines
    are paused, and none starts. Each ``*_usd`` amount is the same in US dollars at Wheelmachine's rate, deprecated."""

    account: str
    balance_usd: float
    empty: bool
    buy_open: bool
    """Whether credit can be bought here (``client.credit.buy``): False where it is not set up."""
    buy_amounts_usd: List[int]
    """The amounts credit is bought in, in dollars, one at a time: 10, 25, 50 and 100; empty while it cannot be."""
    entries: List[CreditEntry]
    next: Optional[str]
    """Where the page after this one begins; None for the last."""
    low_usd: float = 0.0
    """Below this the credit is running low, and those who buy it are e-mailed once."""
    charging: str = "on"
    """Whether its use is taken now: ``on``; ``from`` an account made before billing began, not charged before
    ``charging_from`` (None until its notice has gone out); ``off`` not charged at all."""
    charging_from: Optional[str] = None
    free_credit_usd: Optional[float] = None
    """Deprecated: ``free_credit`` in US dollars at Wheelmachine's rate."""
    currency: str = "USD"
    """The currency it is counted in: ``USD``, ``GBP`` or ``EUR``."""
    currency_fixed: bool = False
    """Whether the currency is fixed, by the account's first purchase or by Wheelmachine's support; until then
    ``client.credit.set_currency`` changes it."""
    balance: float = 0.0
    """The credit left, in the account's currency, to the millionth."""
    low: float = 0.0
    """Below this the credit is running low, and those who buy it are e-mailed once."""
    buy_amounts: List[int] = field(default_factory=list)
    """The amounts credit is bought in, in the account's currency, one at a time: 10, 25, 50 and 100; empty while it
    cannot be."""
    free_credit: Optional[float] = None
    """The free credit your own account is still to get - 5 of its currency - once you link a GitHub account on the
    dashboard's Code page; None when there is none to get."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "CreditBalance":
        buy = d.get("buy") if isinstance(d.get("buy"), Mapping) else {}
        amounts = buy.get("amounts_usd") if isinstance(buy.get("amounts_usd"), list) else []
        local = buy.get("amounts") if isinstance(buy.get("amounts"), list) else amounts
        free = d.get("free_credit") if isinstance(d.get("free_credit"), Mapping) else None
        charging = d.get("charging") if isinstance(d.get("charging"), Mapping) else {}
        return _stamp(
            cls(account=_str(d, "account"), balance_usd=_float(d, "balance_usd"), empty=_bool(d, "empty"),
                buy_open=_bool(buy, "open"),
                buy_amounts_usd=[int(a) for a in amounts if isinstance(a, (int, float)) and not isinstance(a, bool)],
                entries=[CreditEntry.from_dict(e) for e in _list(d, "data")], next=_opt_str(d, "next"),
                low_usd=_float(d, "low_usd") if "low_usd" in d else 0.0,
                free_credit_usd=_float(free, "usd") if free is not None else None,
                charging=_str(charging, "state") or "on", charging_from=_opt_str(charging, "from"),
                currency=_currency(d), currency_fixed=_bool(d, "currency_fixed"), balance=_amount(d, "balance") or 0.0,
                low=_amount(d, "low") or 0.0,
                buy_amounts=[int(a) for a in local if isinstance(a, (int, float)) and not isinstance(a, bool)],
                free_credit=(_num(free.get("amount")) if "amount" in free else _float(free, "usd")) if free is not None else None),
            d,
        )


@dataclass
class CreditCheckout(APIObject):
    """Where a purchase is paid: ``url``, the dashboard's Billing page, which opens Paddle's checkout on it for a person
    signed in there (with ``client_token``, Paddle's token made to be given to browsers, in Paddle's
    ``environment``), on ``price_id`` - or another of ``prices``, the four amounts' - with ``custom_data``;
    ``transaction_id`` is Paddle's id for the purchase's transaction, or ``None`` while it has none: a purchase can have
    one before it is paid."""

    provider: str
    environment: str
    client_token: str
    transaction_id: Optional[str]
    price_id: str
    prices: List[Dict[str, Any]]
    custom_data: Dict[str, str]
    url: str
    currency: str = "USD"
    """The account's currency: each price is in it."""

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "CreditCheckout":
        return _stamp(
            cls(provider=_str(d, "provider"), environment=_str(d, "environment"), client_token=_str(d, "client_token"),
                transaction_id=_opt_str(d, "transaction_id"), price_id=_str(d, "price_id"), prices=_list(d, "prices"),
                custom_data=_labels(d, "custom_data"), url=_str(d, "url"), currency=_currency(d)),
            d,
        )


@dataclass
class CreditPurchase(APIObject):
    """A checkout started to buy credit. Nothing is charged until it is paid in Paddle's checkout (``checkout.url``);
    ``amount`` of credit, in the account's currency, is there once Paddle says the payment is complete."""

    id: str
    account: str
    amount_usd: float
    """Deprecated: ``amount`` in US dollars at Wheelmachine's rate."""
    checkout: CreditCheckout
    created_at: str
    currency: str = "USD"
    amount: float = 0.0

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "CreditPurchase":
        co = d.get("checkout") if isinstance(d.get("checkout"), Mapping) else {}
        return _stamp(
            cls(id=_str(d, "id"), account=_str(d, "account"), amount_usd=_float(d, "amount_usd"), checkout=CreditCheckout.from_dict(co),
                created_at=_str(d, "created_at"), currency=_currency(d), amount=_amount(d, "amount") or 0.0),
            d,
        )

