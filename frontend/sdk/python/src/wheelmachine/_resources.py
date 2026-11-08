"""The API's resources: machines, their ports, projects, keys, secrets, organisations, requests and the audit."""

from __future__ import annotations

import io
import json
import math
import os
import re
import stat
import tarfile
import tempfile
import time
import uuid
import warnings
from datetime import datetime, timedelta, timezone
from typing import TYPE_CHECKING, Any, BinaryIO, Dict, Iterator, List, Literal, Mapping, Optional, Sequence, Tuple, Union, overload
from urllib.parse import quote

from ._errors import WaitTimeoutError
from ._http import Answer, Body, BytesBody, PathBody, StreamBody
from ._models import (DeployBranches, DeployGuess, DeployLog, DeployRepo, DeployRun, Deployment, FolderFiles, ConfirmCodeSent, Agent, ScreenActionsDone, ScreenPicture,
    AgentList,
    AllowedModels,
    App,
    AuditEvent,
    Backup,
    CreditBalance,
    CreditEntry,
    CreditPurchase,
    ExecResult,
    Key,
    KeyCreated,
    Machine,
    MachineMetrics,
    MachineRequest,
    MachineState,
    ModelList,
    NetworkPolicy,
    Org,
    Port,
    PortLink,
    Project,
    Scope,
    Secret,
    SecretPreset,
    TeamRules,
    _answered,
)
from ._pagination import PageIterator

if TYPE_CHECKING:  # pragma: no cover
    from ._client import Wheelmachine

__all__ = [
    "Machines",
    "Ports",
    "Deployments",
    "Apps",
    "Projects",
    "Keys",
    "Secrets",
    "Models",
    "Agents",
    "Orgs",
    "Requests",
    "Audit",
    "FileData",
    "LONGEST_WAIT_S",
    "SECRET_VALUE_MAX",
]


#: What ``write_file`` sends: bytes, a file object opened in binary mode, or a path (``pathlib.Path``).
FileData = Union[bytes, bytearray, memoryview, BinaryIO, "os.PathLike[str]"]

#: The longest the API waits in one call for a machine to reach a state.
LONGEST_WAIT_S = 600.0
#: How long the API may hold each kind of call before it answers, past which the answer is not coming.
_HOLDS_READY = 30.0  # create, start, fork: wait_s and this
_HOLDS_EXEC = 600.0
_HOLDS_SCREEN = 150.0  # a desktop's picture or its actions: a first start of it, and half a minute with it
_HOLDS_ACT = 120.0  # pause, stop, delete
_HOLDS_KEEP = 1800.0  # archive, backup
_HOLDS_APP_ON = 60.0  # turning an app on: it starts, and is waited for
_HOLDS_APP_SHOW = 120.0  # showing one on the machine's screen: its desktop may start first, then its window
_HOLDS_EXPORT = 120.0  # an export of the audit: up to a year of it, which takes a while to begin
_HOLDS_GIVE = 60.0  # a fork for a teammate, or a hand-off: carried out in the call for a minute, then behind it


def _id(value: str, what: str) -> str:
    if not isinstance(value, str) or not value:
        raise ValueError("{} must be a non-empty string".format(what))
    return quote(value, safe="")


def _port(value: int) -> int:
    """A port number, checked before it goes in a path or a body: a whole number from 1 to 65535 (True is not one)."""
    if isinstance(value, bool) or not isinstance(value, int):
        raise TypeError("port is a whole number from 1 to 65535, such as 8080")
    if not 1 <= value <= 65535:
        raise ValueError("port is from 1 to 65535, not {}".format(value))
    return int(value)


_APP_NAME = re.compile(r"^[a-z0-9-]{1,32}$")


def _app(value: str) -> str:
    """An app's name, checked before it goes in a path: as the machine's list of apps gives it, such as ``desktop``."""
    if not isinstance(value, str) or not _APP_NAME.match(value):
        raise ValueError("app is an app's name, such as desktop: lower-case letters, digits and -, at most 32")
    return value


def _given(**fields: Any) -> Dict[str, Any]:
    """The fields that were given: None means left out."""
    return {k: v for k, v in fields.items() if v is not None}


def _key(given: Optional[str]) -> str:
    """The Idempotency-Key for one create call: made once, and sent again with every retry of it."""
    return given if given else str(uuid.uuid4())


def _seconds(value: float) -> str:
    return "{:g}".format(value)


def _items(data: Any) -> List[Dict[str, Any]]:
    got = data.get("data") if isinstance(data, dict) else None
    return [item for item in got if isinstance(item, dict)] if isinstance(got, list) else []


def _next(data: Any) -> Optional[str]:
    got = data.get("next") if isinstance(data, dict) else None
    return got if isinstance(got, str) and got else None


def _backup(given: Union[str, Backup, None]) -> Optional[str]:
    """A backup as a fork names it: its id, or ``latest``; a ``Backup`` by its id."""
    return given.id if isinstance(given, Backup) else given


def _file_body(data: Any, content_length: Optional[int]) -> Body:
    if isinstance(data, str):
        raise TypeError(
            "write_file takes bytes, a file object opened in binary mode, or a path such as pathlib.Path('notes.txt'); "
            "for text, give text.encode()"
        )
    if isinstance(data, (bytes, bytearray, memoryview)):
        raw = bytes(data)
        if content_length is not None and content_length != len(raw):
            raise ValueError("content_length says {} bytes, the data holds {}".format(content_length, len(raw)))
        return BytesBody(raw)
    if isinstance(data, os.PathLike):
        if content_length is not None:
            raise ValueError("content_length is for a file object; a path's length is the file's own")
        return PathBody(os.fspath(data))
    if isinstance(data, io.TextIOBase):
        raise TypeError("the file is open as text: open it in binary mode ('rb')")
    if hasattr(data, "read"):
        return StreamBody(data, content_length)
    raise TypeError("write_file takes bytes, a file object opened in binary mode, or a pathlib.Path")


#: The most a secret's value holds, in bytes of UTF-8.
SECRET_VALUE_MAX = 32 << 10

_SECRET_NAME = re.compile(r"[A-Z_][A-Z0-9_]{0,63}")


def _secret_name(name: str) -> str:
    """A secret's name: an environment variable's, which is what a machine's commands get it as."""
    if not isinstance(name, str) or not _SECRET_NAME.fullmatch(name):
        raise ValueError("a secret's name is capitals, digits and _, not beginning with a digit, such as DATABASE_URL")
    return name


def _names(names: Sequence[str]) -> List[str]:
    if isinstance(names, str):
        raise TypeError("secrets is a list of names, such as ['DATABASE_URL']")
    return [_secret_name(n) for n in names]


def _network(network: Union[Mapping[str, Any], NetworkPolicy, None]) -> Optional[Dict[str, Any]]:
    """A machine's internet policy as the API takes it: ``{"internet": ..., "allow": [...]}``. Accepts a
    ``NetworkPolicy`` or a mapping; the API checks it fully."""
    if network is None:
        return None
    if isinstance(network, NetworkPolicy):
        return {"internet": network.internet, "allow": list(network.allow)}
    if isinstance(network, Mapping):
        internet = network.get("internet")
        if internet not in ("open", "closed", "allowed"):
            raise ValueError("network['internet'] is 'open', 'closed' or 'allowed'")
        out: Dict[str, Any] = {"internet": internet}
        allow = network.get("allow")
        if allow is not None:
            if isinstance(allow, str):
                raise TypeError("network['allow'] is a list of names and ranges")
            out["allow"] = [str(a) for a in allow]
        return out
    raise TypeError("network is a NetworkPolicy or {'internet': ..., 'allow': [...]}")


def _secret_value(value: str) -> None:
    """Checks a value before it is sent, saying nothing of it: never a word of the value in an error."""
    if not isinstance(value, str):
        raise TypeError("a secret's value is a str")
    if not value:
        raise ValueError("a secret's value cannot be empty")
    if any("\ud800" <= c <= "\udfff" for c in value):
        raise ValueError("a secret's value is text that UTF-8 can carry: it holds a lone surrogate")
    if "\x00" in value:
        raise ValueError("a secret's value cannot hold a NUL: an environment variable cannot carry one")
    if len(value.encode("utf-8")) > SECRET_VALUE_MAX:
        raise ValueError("a secret's value is at most 32 KiB")


def _when(value: Union[str, datetime]) -> str:
    if isinstance(value, datetime):
        if value.tzinfo is None:
            raise ValueError("give a time with its zone, such as datetime(2026, 12, 31, tzinfo=timezone.utc)")
        return value.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    return value


class _Resource:
    def __init__(self, client: "Wheelmachine") -> None:
        self._client = client


class Machines(_Resource):
    """``client.machines``: make, follow, run commands in, fork and delete machines."""

    def create(
        self,
        *,
        name: Optional[str] = None,
        project: Optional[str] = None,
        region: Optional[str] = None,
        gpu: Optional[str] = None,
        cpus: Optional[int] = None,
        labels: Optional[Mapping[str, str]] = None,
        secrets: Optional[Sequence[str]] = None,
        network: Union[Mapping[str, Any], NetworkPolicy, None] = None,
        sleep_when_idle: Optional[bool] = None,
        idle_after_s: Optional[int] = None,
        desktop: Optional[bool] = None,
        wait_s: Optional[float] = None,
        idempotency_key: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> Machine:
        """Makes a machine. It comes back ``running`` (usually in about a second), or ``starting`` when it
        is not ready after ``wait_s`` (60 by default): then ``wait()`` follows it.

        ``region`` is an id from ``client.regions()``, ``gpu`` one from ``client.gpus()``; ``cpus`` is 2, 4 or 8 (2 by
        default; 16 is not offered yet), with 2 GB of memory each - not with ``gpu``, whose size comes with it. It
        pauses by itself once it is idle - nothing has reached it for ``idle_after_s`` seconds (300 by default) and its
        programs have been quiet as long, so a job left running keeps it going; with ``sleep_when_idle=False`` it never
        pauses by itself, and runs (and is charged) until it is paused or stopped. ``secrets`` names the account's
        secrets every command run in it gets as environment variables (``client.secrets``); giving them needs a key
        with the ``cloud:secrets:write`` scope, of the whole account. ``desktop=True`` turns its desktop on behind the
        answer, so that a first ``screenshot()`` finds it ready. A retry after a lost connection never makes two:
        the call carries an Idempotency-Key (``idempotency_key``, or one made for it).
        """
        payload = _given(
            name=name,
            project=project,
            region=region,
            gpu=gpu,
            cpus=cpus,
            labels=dict(labels) if labels is not None else None,
            secrets=_names(secrets) if secrets is not None else None,
            network=_network(network),
            sleep_when_idle=sleep_when_idle,
            idle_after_s=idle_after_s,
            desktop=desktop,
            wait_s=wait_s,
        )
        data, info = self._client._json(
            "POST",
            "/v1/machines",
            payload=payload or None,
            idempotency_key=_key(idempotency_key),
            timeout=self._ready_timeout(wait_s, timeout),
        )
        return _answered(Machine.from_dict(data or {}), info)

    def get(self, machine_id: str) -> Machine:
        data, info = self._client._json("GET", "/v1/machines/" + _id(machine_id, "machine_id"))
        return _answered(Machine.from_dict(data or {}), info)

    def list(
        self,
        *,
        project: Optional[str] = None,
        state: Optional[MachineState] = None,
        labels: Optional[Mapping[str, str]] = None,
        limit: Optional[int] = None,
    ) -> PageIterator[Machine]:
        """Every machine, newest first, page after page as it is iterated. ``labels`` asks for machines holding
        every one of them; ``limit`` is how many come in each page (1 to 200)."""
        base: List[Tuple[str, str]] = []
        if project is not None:
            base.append(("project", project))
        if state is not None:
            base.append(("state", state))
        for name, value in (labels or {}).items():
            base.append(("label", "{}={}".format(name, value)))
        if limit is not None:
            base.append(("limit", str(limit)))

        def page(after: Optional[str]) -> Tuple[List[Machine], Optional[str]]:
            query = base + ([("after", after)] if after else [])
            data, _ = self._client._json("GET", "/v1/machines", query=query)
            return [Machine.from_dict(m) for m in _items(data)], _next(data)

        return PageIterator(page)

    def update(
        self,
        machine_id: str,
        *,
        name: Optional[str] = None,
        labels: Optional[Mapping[str, Optional[str]]] = None,
        secrets: Optional[Sequence[str]] = None,
        network: Union[Mapping[str, Any], NetworkPolicy, None] = None,
    ) -> Machine:
        """Renames it, changes its labels, says which secrets it gets, or sets its internet policy. Labels merge: a
        label given None is taken off. ``secrets`` is every secret it is to get: the names take the place of the ones
        it had, and ``[]`` takes them all away. ``network`` sets what it may reach on the internet (a ``NetworkPolicy``
        or ``{"internet": "open"|"closed"|"allowed", "allow": [...]}``); a running machine's policy changes at once,
        without a restart."""
        payload = _given(
            name=name,
            labels=dict(labels) if labels is not None else None,
            secrets=_names(secrets) if secrets is not None else None,
            network=_network(network),
        )
        data, info = self._client._json("PATCH", "/v1/machines/" + _id(machine_id, "machine_id"), payload=payload)
        return _answered(Machine.from_dict(data or {}), info)

    def delete(self, machine_id: str) -> None:
        """Deletes it and its files. Deleting a machine already deleted is not an error."""
        self._client._json(
            "DELETE", "/v1/machines/" + _id(machine_id, "machine_id"), timeout=self._client._held(_HOLDS_ACT)
        )

    def start(self, machine_id: str, *, wait_s: Optional[float] = None, timeout: Optional[float] = None) -> Machine:
        """Runs it again, from paused, stopped or archived; ``starting`` when it is not ready after ``wait_s``."""
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/start".format(_id(machine_id, "machine_id")),
            payload=_given(wait_s=wait_s) or None,
            timeout=self._ready_timeout(wait_s, timeout),
        )
        return _answered(Machine.from_dict(data or {}), info)

    def pause(self, machine_id: str) -> Machine:
        """Pauses it with its memory kept: nothing runs and no running time is charged."""
        return self._act(machine_id, "pause", _HOLDS_ACT)

    def stop(self, machine_id: str) -> Machine:
        """Stops it; its files stay."""
        return self._act(machine_id, "stop", _HOLDS_ACT)

    def archive(self, machine_id: str, *, timeout: Optional[float] = None) -> Machine:
        """Archives its files; ``start`` brings it back."""
        return self._act(machine_id, "archive", _HOLDS_KEEP, timeout)

    def fork(
        self,
        machine_id: str,
        *,
        name: Optional[str] = None,
        labels: Optional[Mapping[str, str]] = None,
        backup: Union[str, Backup, None] = None,
        cpus: Optional[int] = None,
        wait_s: Optional[float] = None,
        idempotency_key: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> Union[Machine, MachineRequest]:
        """Forks it: a new machine of its own, made from it as it is now and at its size - ``running``, or
        ``starting`` as ``create`` says. With ``backup`` - a ``Backup``, its id, or ``"latest"`` for its newest - the
        fork is made from that backup's files instead, starting afresh, at the size ``cpus`` asks (2, 4 or 8, with
        2 GB of memory each; not for a GPU machine) or the machine's own. ``"latest"`` of a machine with no backup yet
        is refused (ConflictError).

        In an organisation, a member's fork is not made at once: it waits for the OK of a full manager of the machine's
        team, an owner or an admin, and the answer is the request - a ``MachineRequest`` in place of a ``Machine``, its
        ``holder`` and ``receiver`` both the key's person, its ``backup`` and ``cpus`` as asked. Made with a key, as
        every call of this library is, it waits first for the OK of the person who made the key, signed in to the dashboard
        (``waiting_on.who`` ``asker``). Asked again while it waits, it is the one there is. Once it is approved the
        fork is made, named, labelled and sized as asked, and its id is the request's ``result``: ``requests.get``
        follows it. A team whose Forks rule is Off refuses every member's fork (PermissionDeniedError); a full manager's
        own fork, an owner's and an admin's are made at once. Like ``create``, a retry never makes two."""
        payload = _given(
            name=name,
            labels=dict(labels) if labels is not None else None,
            backup=_backup(backup),
            cpus=cpus,
            wait_s=wait_s,
        )
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/fork".format(_id(machine_id, "machine_id")),
            payload=payload or None,
            idempotency_key=_key(idempotency_key),
            timeout=self._ready_timeout(wait_s, timeout),
        )
        if (data or {}).get("object") == "request":
            return _answered(MachineRequest.from_dict(data), info)
        return _answered(Machine.from_dict(data or {}), info)

    def copy(
        self,
        machine_id: str,
        *,
        name: Optional[str] = None,
        labels: Optional[Mapping[str, str]] = None,
        backup: Union[str, Backup, None] = None,
        cpus: Optional[int] = None,
        wait_s: Optional[float] = None,
        idempotency_key: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> Union[Machine, MachineRequest]:
        """Deprecated: ``fork``, by the name it had before. The same call, word for word."""
        warnings.warn("machines.copy is machines.fork now", DeprecationWarning, stacklevel=2)
        return self.fork(machine_id, name=name, labels=labels, backup=backup, cpus=cpus, wait_s=wait_s,
                         idempotency_key=idempotency_key, timeout=timeout)

    def fork_for(
        self,
        machine_id: str,
        to: str,
        *,
        team: Optional[str] = None,
        why: Optional[str] = None,
        idempotency_key: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> MachineRequest:
        """In an organisation, forks it for a teammate - ``to`` is their id, e-mail address or GitHub login - by its
        holder, or an owner or admin of the organisation. The fork is live - its memory, its programs and its screen -
        and cleared before it is theirs: the browser's tabs stay and open again signed out, while its cookies, saved
        passwords and sites' data, the apps' and agents' own sign-ins and what signs a person in from a terminal go;
        the machine itself is untouched. ``team`` is the team of theirs it goes into, when it is another
        than the machine's.

        Made with a key, as every call of this library is, it decides nothing by itself: the answer is the request,
        ``waiting`` on the OK of the person who made the key (``waiting_on.who`` ``asker``), which they give signed in to the dashboard.
        From then their right counts as it would have, had they given it there - one from an owner or an admin is
        carried out then, and so is a full manager's - and what else it needs follows: the OK of a full manager of the
        team, an owner or an admin, as every member's fork needs; an admin's for another team. Done, the fork is in
        ``result``; ``failed``, ``failure`` says why; ``requests.get`` follows it. A rule of the machine's team that is
        Off refuses it (PermissionDeniedError), into another team too, unless the key's person is an owner or an admin.
        Like ``create``, a retry never makes two. The fork is then handed to them as a machine is: the person who forked it takes it
        back (``take_back``), and they give it back (``give_back``) and never delete it."""
        return self._give(machine_id, "fork-for", to, team, why, idempotency_key, timeout)

    def hand_off(
        self,
        machine_id: str,
        to: str,
        *,
        team: Optional[str] = None,
        why: Optional[str] = None,
        idempotency_key: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> MachineRequest:
        """In an organisation, hands off a machine you made to a teammate (``to``) - or, as an owner or admin, one whose
        holder has left - decided as ``fork_for`` is. A machine handed to you is given back (``give_back``), never
        handed off again. It goes exactly as it is: its programs, terminals, files and every sign-in in it, its browsers'
        too - so whoever holds it can sign in to Wheelmachine as you through GitHub or Google there: sign out of them
        first if that is not what you want. Your Wheelmachine keys found in it are revoked once it is theirs
        (``keys_revoked``), and your sign-ins to the dashboard in it end. What is open into it ends, and every port of it
        is opened anew so no pass or link from before opens anything. It arrives paused, its memory kept, so nothing
        runs on the receiver's month until they start it; you watch it, and take it back whenever you choose
        (``take_back``). What it came to this month so far stays the giver's. While the machines cannot be read for it,
        it comes back ``failed``: ``Machines cannot sign out yet``, which starting the machine again does not change.
        One hand-off of a machine waits at a time (ConflictError for another)."""
        return self._give(machine_id, "hand-off", to, team, why, idempotency_key, timeout)

    def take_back(
        self,
        machine_id: str,
        *,
        team: Optional[str] = None,
        idempotency_key: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> MachineRequest:
        """In an organisation, takes back a machine you handed off (its ``given_by``), whenever you choose, with nobody's
        OK: it comes back exactly as it is - what its holder put in it, their sign-ins included - but for their
        Wheelmachine keys found in it, revoked, and their sign-ins to the dashboard in it, ended; paused, its memory
        kept. ``team`` is the team of yours it goes into, when it should be another than the one it is in or was handed
        off from. Made with a key, as every call of this library is, the request waits on the OK of the person who made the key
        (``waiting_on.who`` ``asker``), which they give signed in to the dashboard. Like ``create``, a retry never makes
        two."""
        return self._back(machine_id, "take-back", team, idempotency_key, timeout)

    def give_back(
        self,
        machine_id: str,
        *,
        team: Optional[str] = None,
        idempotency_key: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> MachineRequest:
        """In an organisation, gives a machine handed to you back to the person who handed it off, as ``take_back``
        takes one back - with what you put in it, but for your Wheelmachine keys found in it, revoked, and your sign-ins
        to the dashboard in it, ended. A machine handed to you is given back, never handed off again, and not deleted
        while they may still take it back."""
        return self._back(machine_id, "give-back", team, idempotency_key, timeout)

    def _back(self, machine_id: str, route: str, team: Optional[str], idempotency_key: Optional[str],
              timeout: Optional[float]) -> MachineRequest:
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/{}".format(_id(machine_id, "machine_id"), route),
            payload={"team": team} if team else {},
            idempotency_key=_key(idempotency_key),
            timeout=timeout if timeout is not None else self._client._held(_HOLDS_GIVE),
        )
        return _answered(MachineRequest.from_dict(data or {}), info)

    def _give(self, machine_id: str, route: str, to: str, team: Optional[str], why: Optional[str],
              idempotency_key: Optional[str], timeout: Optional[float]) -> MachineRequest:
        if not isinstance(to, str) or not to:
            raise ValueError("to is the teammate: their id, e-mail address or GitHub login")
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/{}".format(_id(machine_id, "machine_id"), route),
            payload=_given(to=to, team=team, why=why),
            idempotency_key=_key(idempotency_key),
            timeout=timeout if timeout is not None else self._client._held(_HOLDS_GIVE),
        )
        return _answered(MachineRequest.from_dict(data or {}), info)

    def backup(
        self,
        machine_id: str,
        *,
        note: Optional[str] = None,
        idempotency_key: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> Backup:
        """Backs it up: keeps its files as they are now, not what runs in it, with ``note`` (200 characters at most).
        ``fork(machine_id, backup=...)`` makes a new machine from the backup, at the machine's size or another. The
        person who holds the machine backs it up, and in an organisation its owners and admins. Like ``create``, a retry
        never makes two."""
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/backups".format(_id(machine_id, "machine_id")),
            payload=_given(note=note) or None,
            idempotency_key=_key(idempotency_key),
            timeout=timeout if timeout is not None else self._client._held(_HOLDS_KEEP),
        )
        return _answered(Backup.from_dict(data or {}), info)

    def backups(self, machine_id: Optional[str] = None, *, limit: Optional[int] = None) -> List[Backup]:
        """A machine's backups, the newest first - or, with no machine, every backup the key reaches, of the machines
        that are not deleted. In an organisation, those taken before a machine was last handed off are its owners' and
        admins' alone. ``limit`` is how many at most (1 to 200, 100 by default)."""
        path = "/v1/backups" if machine_id is None else "/v1/machines/{}/backups".format(_id(machine_id, "machine_id"))
        data, _ = self._client._json("GET", path, query=[("limit", str(limit))] if limit is not None else [])
        return [Backup.from_dict(b) for b in _items(data)]

    def wait(self, machine_id: str, state: MachineState = "running", *, timeout_s: float = 60.0) -> Machine:
        """Answers when the machine is in ``state`` (at once when it already is). Raises WaitTimeoutError when it
        is not there after ``timeout_s`` seconds. Waits longer than the API's 600 seconds are asked for again."""
        if timeout_s < 0:
            raise ValueError("timeout_s cannot be negative")
        path = "/v1/machines/{}/wait".format(_id(machine_id, "machine_id"))
        deadline = time.monotonic() + timeout_s
        while True:
            left = max(0.0, deadline - time.monotonic())
            this = min(left, LONGEST_WAIT_S)
            began = time.monotonic()
            try:
                data, info = self._client._json(
                    "GET",
                    path,
                    query=[("state", state), ("timeout_s", _seconds(this))],
                    timeout=self._client._held(this),
                )
            except WaitTimeoutError:
                # the API waited its longest and there is time left: ask again. An early answer (the machine was
                # deleted, say) is the answer.
                if left <= LONGEST_WAIT_S or time.monotonic() - began < this - 1:
                    raise
                continue
            return _answered(Machine.from_dict(data or {}), info)

    def exec(
        self,
        machine_id: str,
        command: Optional[Sequence[str]] = None,
        *,
        shell: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        cwd: Optional[str] = None,
        user: Optional[Literal["user", "root"]] = None,
        binary_output: bool = False,
        timeout: Optional[float] = None,
    ) -> ExecResult:
        """Runs a command and gives what it wrote. ``command`` is the words, run with no shell
        (``["python3", "-c", "print(2+2)"]``); ``shell`` is a line for ``sh -lc`` (``"ls -la | wc -l"``). It runs
        in the work folder as the machine's user unless ``cwd`` or ``user="root"`` says otherwise; a paused
        machine is woken for it. It runs at most 10 minutes; a call that loses its connection is not sent again,
        since the command may have run, and nor is one answered 503 unless the answer says it never reached its
        machine."""
        if isinstance(command, (str, bytes)):
            raise TypeError("command is a list of words, such as ['ls', '-la']; a line for the shell goes in shell=")
        if (command is None) == (shell is None):
            raise ValueError("give command (a list of words) or shell (a line for sh), one of the two")
        payload = _given(
            command=[str(word) for word in command] if command is not None else None,
            shell=shell,
            env=dict(env) if env is not None else None,
            cwd=cwd,
            user=user,
        )
        if binary_output:
            payload["binary_output"] = True
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/exec".format(_id(machine_id, "machine_id")),
            payload=payload,
            command=True,
            timeout=timeout if timeout is not None else self._client._held(_HOLDS_EXEC),
        )
        return _answered(ExecResult.from_dict(data or {}), info)

    def screenshot(
        self,
        machine_id: str,
        *,
        scale: Optional[float] = None,
        format: Optional[Literal["png", "jpeg"]] = None,
        region: Optional[Mapping[str, int]] = None,
        timeout: Optional[float] = None,
    ) -> ScreenPicture:
        """Sees the machine's desktop as a picture: ``.to_bytes()`` is its file. ``scale`` is 1, or one over a whole
        number up to 8 (0.5 when left out: 1440 x 900), and ``region`` a part of the screen, ``{"x", "y", "w", "h"}``,
        in pixels of a picture at this scale. A paused machine is woken for it, and its desktop turned on, which can take
        up to a minute the first time. It shows whatever is on the screen, and goes wherever your program sends it: an
        agent passes it on to whichever model provider it uses."""
        query: List[Tuple[str, str]] = []
        if scale is not None:
            query.append(("scale", repr(float(scale))))
        if format is not None:
            query.append(("format", format))
        for name in ("x", "y", "w", "h"):
            if region is not None and name in region:
                query.append((name, str(int(region[name]))))
        data, info = self._client._json(
            "GET",
            "/v1/machines/{}/screen/picture".format(_id(machine_id, "machine_id")),
            query=query,
            timeout=timeout if timeout is not None else self._client._held(_HOLDS_SCREEN),
        )
        return _answered(ScreenPicture.from_dict(data or {}), info)

    def use_screen(
        self,
        machine_id: str,
        actions: Sequence[Mapping[str, Any]],
        *,
        scale: Optional[float] = None,
        picture: bool = False,
        settle_ms: Optional[int] = None,
        timeout: Optional[float] = None,
    ) -> ScreenActionsDone:
        """Works the machine's desktop, in order: 1 to 64 actions, each ``{"type": ...}`` - ``click`` and ``move`` with ``x``
        and ``y``, ``drag`` with ``x``, ``y``, ``to_x`` and ``to_y``, ``scroll`` with ``x``, ``y`` and ``dx`` or ``dy``, ``keys``
        with ``keys``, ``type`` with ``text``, ``wait`` with ``ms``. ``x`` and ``y`` are pixels of a picture of the whole screen
        at this ``scale``. With ``picture=True``, a picture comes back after ``settle_ms``. What is typed is never kept or
        logged. A call that may have reached the API is never sent again, not even when refused for a moment, since a click
        or a word must not happen twice: look at the screen before you send it again."""
        if isinstance(actions, Mapping):
            raise TypeError("actions is a list of actions, such as [{'type': 'wait', 'ms': 100}]")
        payload = _given(actions=[dict(a) for a in actions], scale=scale, settle_ms=settle_ms)
        if picture:
            payload["picture"] = True
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/screen/actions".format(_id(machine_id, "machine_id")),
            payload=payload,
            once=True,
            timeout=timeout if timeout is not None else self._client._held(_HOLDS_SCREEN),
        )
        return _answered(ScreenActionsDone.from_dict(data or {}), info)

    def click(
        self,
        machine_id: str,
        x: int,
        y: int,
        *,
        button: Optional[Literal["left", "middle", "right"]] = None,
        count: Optional[int] = None,
        scale: Optional[float] = None,
        timeout: Optional[float] = None,
    ) -> ScreenActionsDone:
        """Clicks on the machine's desktop, at a place in pixels of a picture of the whole screen at this ``scale``."""
        return self.use_screen(machine_id, [_given(type="click", x=x, y=y, button=button, count=count)], scale=scale, timeout=timeout)

    def type(self, machine_id: str, text: str, *, timeout: Optional[float] = None) -> ScreenActionsDone:
        """Types on the machine's desktop, wherever it is taking keys: at most 4096 characters, never kept or logged. Letters
        of every language and emoji can be typed, but characters that aren't on an English keyboard, such as accents, Russian,
        Chinese or emoji, can come out wrong. Take a picture after typing them to check; a password box can't be checked this
        way."""
        return self.use_screen(machine_id, [{"type": "type", "text": text}], timeout=timeout)

    def keys(self, machine_id: str, *keys: str, timeout: Optional[float] = None) -> ScreenActionsDone:
        """Presses keys together on the machine's desktop, such as ``keys(id, "ctrl", "c")``. A key is a character, or one of
        enter, tab, escape, backspace, delete, insert, home, end, pageup, pagedown, up, down, left, right, space, ctrl,
        shift, alt, super, menu, capslock, printscreen, f1 to f12."""
        return self.use_screen(machine_id, [{"type": "keys", "keys": list(keys)}], timeout=timeout)

    def scroll(
        self,
        machine_id: str,
        x: int,
        y: int,
        *,
        dx: Optional[int] = None,
        dy: Optional[int] = None,
        scale: Optional[float] = None,
        timeout: Optional[float] = None,
    ) -> ScreenActionsDone:
        """Scrolls on the machine's desktop at a place: ``dy`` notches down, or up below 0, and ``dx`` to the right, or to the
        left below 0."""
        return self.use_screen(machine_id, [_given(type="scroll", x=x, y=y, dx=dx, dy=dy)], scale=scale, timeout=timeout)

    def metrics(
        self,
        machine_id: str,
        *,
        since: Union[str, datetime, timedelta, None] = None,
        step: Union[int, timedelta, None] = None,
    ) -> MachineMetrics:
        """What it used - its CPUs (a share of its own size), memory, disk and network - a point a ``step`` from
        ``since`` on. ``since`` is a time (an aware ``datetime`` or RFC 3339), a ``timedelta`` back, or how long ago as
        the API writes it (``"6h"``): the last hour when left out, a day back at most. ``step`` is seconds (or a
        ``timedelta``), whole minutes from 60 to 3600: a minute when left out. A machine is read while it runs."""
        query: List[Tuple[str, str]] = []
        if since is not None:
            if isinstance(since, timedelta):
                since = "{}s".format(int(since.total_seconds()))
            query.append(("since", _when(since)))
        if step is not None:
            seconds = int(step.total_seconds()) if isinstance(step, timedelta) else int(step)
            query.append(("step", str(seconds)))
        data, info = self._client._json(
            "GET", "/v1/machines/{}/metrics".format(_id(machine_id, "machine_id")), query=query
        )
        return _answered(MachineMetrics.from_dict(data or {}), info)

    @overload
    def read_file(self, machine_id: str, path: str, *, timeout: Optional[float] = None) -> bytes: ...

    @overload
    def read_file(
        self,
        machine_id: str,
        path: str,
        *,
        to: Union[str, "os.PathLike[str]", BinaryIO],
        timeout: Optional[float] = None,
    ) -> int: ...

    def read_file(
        self,
        machine_id: str,
        path: str,
        *,
        to: Union[str, "os.PathLike[str]", BinaryIO, None] = None,
        timeout: Optional[float] = None,
    ) -> Union[bytes, int]:
        """Reads a file in the machine: ``home/...`` (its home) or ``work/...`` (where commands start). Gives its
        bytes, or with ``to`` streams it into that local path or binary file object and gives how many bytes came.
        A path is written whole or not at all: the bytes go to a file beside it that takes its name at the end."""
        answer = self._client._send(
            "GET",
            "/v1/machines/{}/files".format(_id(machine_id, "machine_id")),
            query=[("path", path)],
            accept="application/octet-stream",
            timeout=timeout,
            buffer=to is None,
        )
        if to is None:
            return answer.content
        if hasattr(to, "write"):
            return self._client._drain(answer, to)  # type: ignore[arg-type]
        return _save(self._client, answer, os.fspath(to))

    def write_file(
        self,
        machine_id: str,
        path: str,
        data: FileData,
        *,
        content_length: Optional[int] = None,
        timeout: Optional[float] = None,
    ) -> None:
        """Writes a file in the machine (``home/...`` or ``work/...``), making the folders on the way, streamed
        with its length: ``data`` is bytes, a ``pathlib.Path`` to a file here, or a file object opened in binary
        mode (one that cannot seek needs ``content_length``). At most 5 GiB a call."""
        body = _file_body(data, content_length)
        self._client._send(
            "PUT",
            "/v1/machines/{}/files".format(_id(machine_id, "machine_id")),
            query=[("path", path)],
            body=body,
            timeout=timeout,
            buffer=True,
        )

    def _act(self, machine_id: str, action: str, holds: float, timeout: Optional[float] = None) -> Machine:
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/{}".format(_id(machine_id, "machine_id"), action),
            timeout=timeout if timeout is not None else self._client._held(holds),
        )
        return _answered(Machine.from_dict(data or {}), info)

    def _ready_timeout(self, wait_s: Optional[float], timeout: Optional[float]) -> float:
        if timeout is not None:
            return timeout
        return self._client._held((60.0 if wait_s is None else wait_s) + _HOLDS_READY)


def _save(client: "Wheelmachine", answer: Answer, dest: str, mode: int = 0o666) -> int:
    """Streams an answer into a file beside `dest`, which takes `dest`'s name once the whole file is in. A new file is
    made with `mode` (as the umask leaves it): readable by everyone by default, as a copy usually is."""
    folder = os.path.dirname(os.path.abspath(dest))
    part = os.path.join(folder, ".{}.{}.part".format(os.path.basename(dest), uuid.uuid4().hex[:12]))
    try:
        fd = os.open(part, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_BINARY", 0), mode)
    except BaseException:
        answer.close()
        raise
    try:
        with os.fdopen(fd, "wb") as f:
            n = client._drain(answer, f)
        os.replace(part, dest)
        return n
    except BaseException:
        try:
            os.unlink(part)
        except OSError:
            pass
        raise


class Ports(_Resource):
    """``client.ports``: a machine's ports, each at a web address of its own, private unless opened public."""

    def open(self, machine_id: str, port: int, *, public: bool = False) -> Port:
        """Opens a port at a web address of its own (``url``), for the program listening on it in the machine.
        Private unless ``public``: only a call carrying a key that can see the machine (``Authorization: Bearer
        ...``, which never reaches the program) and a browser that opened a ``link()`` get in; anyone else is
        answered 401. A port open already is made public or private as asked (``last_response.status`` 200, not
        201), so a call that lost its connection is sent again. The few ports the machine itself uses cannot be
        opened, and a machine has at most 20 open."""
        if not isinstance(public, bool):
            raise TypeError("public is True or False")
        payload: Dict[str, Any] = {"port": _port(port)}
        if public:
            payload["public"] = True
        data, info = self._client._json(
            "POST", "/v1/machines/{}/ports".format(_id(machine_id, "machine_id")), payload=payload, repeatable=True
        )
        return _answered(Port.from_dict(data or {}), info)

    def list(self, machine_id: str) -> List[Port]:
        """The machine's open ports, lowest first."""
        data, _ = self._client._json("GET", "/v1/machines/{}/ports".format(_id(machine_id, "machine_id")))
        return [Port.from_dict(p) for p in _items(data)]

    def close(self, machine_id: str, port: int) -> None:
        """Closes a port's web address: it answers 404 from then on, and links made for it no longer open it.
        Closing a port that is not open is not an error."""
        self._client._json("DELETE", "/v1/machines/{}/ports/{}".format(_id(machine_id, "machine_id"), _port(port)))

    def link(
        self,
        machine_id: str,
        port: int,
        *,
        access_s: Optional[int] = None,
        path: Optional[str] = None,
    ) -> PortLink:
        """A link that lets one browser into a private address. It opens once, within five minutes; the browser
        that opens it may reach the address for ``access_s`` seconds (60 to 3600, 1800 by default) and lands on
        ``path`` (``/`` by default; a query may follow it). Its ``url`` is in this answer only: give it to the one
        who should open it, at once. Each call makes a new link, so a call that may have reached the API is never
        sent again, not even one refused for a moment; only one that could not go out at all is."""
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/ports/{}/link".format(_id(machine_id, "machine_id"), _port(port)),
            payload=_given(access_s=access_s, path=path) or None,
            once=True,
        )
        return _answered(PortLink.from_dict(data or {}), info)


#: What stays on this computer, wherever it is in the folder and in any case: a file or a folder of one of these names, or
#: a name beginning or ending so. Another copy of the code's history and what its build makes again, and keys, tokens and
#: a person's settings: a deployment's secrets are given by name. ``wm`` and the dashboard's packer hold the same lists.
_DEPLOY_LEFT_OUT_NAMES = frozenset({
    ".aws", ".azure", ".bash_history", ".boto", ".cache", ".config", ".docker", ".dockercfg", ".gcloud", ".git",
    ".git-credentials", ".gitconfig", ".gnupg", ".hg", ".htpasswd", ".kube", ".local", ".my.cnf", ".mysql_history", ".netrc",
    ".node_repl_history", ".npmrc", ".pgpass", ".psql_history", ".pypirc", ".python_history", ".s3cfg", ".ssh", ".svn", ".terraform",
    ".vault-token", ".venv", ".wheelmachine", ".zsh_history", "__pycache__", "_netrc", "credentials.json", "credentials.toml",
    "node_modules", "secrets.json", "secrets.toml", "secrets.yaml", "secrets.yml", "service-account.json", "serviceaccountkey.json", "terraform.tfvars", "venv",
})
_DEPLOY_LEFT_OUT_PREFIXES = (".env", "id_dsa", "id_ecdsa", "id_ed25519", "id_rsa")
_DEPLOY_LEFT_OUT_SUFFIXES = (".env", ".jks", ".kdbx", ".key", ".keystore", ".p12", ".pem", ".pfx", ".ppk", ".tfstate", ".tfstate.backup", ".tfvars")
#: As much as the API takes of a deployment's files: a folder over them is told so before anything is sent.
_DEPLOY_FILES_MOST = 20_000
_DEPLOY_FILE_MOST = 100_000_000
_DEPLOY_ALL_MOST = 500_000_000
_DEPLOY_PATH_MOST = 255
_DEPLOY_DEPTH_MOST = 32
#: The most of a .gitignore read: one longer is no list of paths a person wrote.
_DEPLOY_IGNORE_MOST = 1 << 20
#: How often a run getting its machine ready is looked at, before its files are sent.
_DEPLOY_FILES_POLL_S = 2.0
#: The files at a folder's top that say how it builds and starts, as the API reads them by name; those whose contents it
#: reads, up to 64 KiB. The rest it needs only to know are there.
_DEPLOY_DETECT_NAMES = (
    "package.json",
    "package-lock.json",
    "pnpm-lock.yaml",
    "yarn.lock",
    "requirements.txt",
    "pyproject.toml",
    "uv.lock",
    "Pipfile",
    "manage.py",
    "main.py",
    "app.py",
    "Procfile",
    "go.mod",
    "Cargo.toml",
    "index.html",
    "Dockerfile",
)
_DEPLOY_DETECT_READ = frozenset({"package.json", "requirements.txt", "pyproject.toml", "Pipfile", "Procfile", "go.mod", "Cargo.toml"})
_DEPLOY_DETECT_MOST = 64 << 10


def _name_left_out(name: str) -> bool:
    """Whether a file or a folder of this name stays here, whatever else is said."""
    low = name.lower()
    return low in _DEPLOY_LEFT_OUT_NAMES or low.startswith(_DEPLOY_LEFT_OUT_PREFIXES) or low.endswith(_DEPLOY_LEFT_OUT_SUFFIXES)


class _IgnoreRule:
    """One line of a .gitignore."""

    __slots__ = ("base", "segments", "anchored", "negate", "dir_only")

    def __init__(self, base: str, segments: List[str], anchored: bool, negate: bool, dir_only: bool) -> None:
        self.base = base
        self.segments = segments
        self.anchored = anchored
        self.negate = negate
        self.dir_only = dir_only


def _parse_ignore(base: str, text: str) -> List[_IgnoreRule]:
    """The lines of a .gitignore read in the folder ``base`` ("", or ending in a slash), as git reads them: the last line
    that matches a path says whether it stays, so that a folder's own .gitignore, read after those above it, has the last
    word; a line beginning ! takes a path back. A path in a folder that stays is never looked at, so nothing in it can be
    taken back, as in git. ``wm`` and the dashboard read them the same way, and the three share their test's cases."""
    rules: List[_IgnoreRule] = []
    for line in text.split("\n"):
        if line.endswith("\r"):
            line = line[:-1]
        while line.endswith(" ") and not line.endswith("\\ "):
            line = line[:-1]
        if not line or line[0] == "#":
            continue
        negate = line[0] == "!"
        if negate:
            line = line[1:]
        elif line.startswith(("\\!", "\\#")):
            line = line[1:]
        dir_only = line.endswith("/")
        line = line.rstrip("/")
        if not line:
            continue
        segments: List[str] = []
        for part in line.lstrip("/").split("/"):
            # (a run of ** is one)
            if part != "**" or not segments or segments[-1] != "**":
                segments.append(part)
        rules.append(_IgnoreRule(base, segments, "/" in line, negate, dir_only))
    return rules


def _ignored_by(rules: Sequence[_IgnoreRule], path: str, is_dir: bool) -> bool:
    """Whether the rules leave out a path in the folder (with slashes, and no slash at its end)."""
    out = False
    for r in rules:
        if (r.dir_only and not is_dir) or not path.startswith(r.base):
            continue
        rest = path[len(r.base):]
        found = _ignore_segments(r.segments, rest.split("/")) if r.anchored else _ignore_name(r.segments[0], rest[rest.rfind("/") + 1:])
        if found:
            out = not r.negate
    return out


def _ignore_segments(pattern: Sequence[str], path: Sequence[str]) -> bool:
    """Whether a pattern's segments match a path's: ** stands for any number of folders, and at the end for all that is
    inside (one thing at least). Worked a segment at a time, so that no .gitignore can make it take long."""
    can = [False] * (len(path) + 1)
    can[0] = True
    for i, p in enumerate(pattern):
        following = [False] * (len(path) + 1)
        if p == "**":
            seen = False
            for j in range(len(following)):
                following[j] = seen or (can[j] and i < len(pattern) - 1)
                seen = seen or can[j]
        else:
            for j in range(1, len(following)):
                following[j] = can[j - 1] and _ignore_name(p, path[j - 1])
        can = following
    return can[len(path)]


def _ignore_name(pattern: str, name: str) -> bool:
    """Whether one name matches a pattern's segment: * for any characters, ? for one, [a-z] for one of those, \\ for the
    character after it as it is. Never goes back further than the last *, so that no pattern can make it take long."""
    pi = ni = mark = 0
    star = -1
    while ni < len(name):
        if pi < len(pattern) and pattern[pi] == "*":
            star, mark = pi, ni
            pi += 1
            continue
        if pi < len(pattern):
            ok, following = _ignore_char(pattern, pi, name[ni])
            if ok:
                pi, ni = following, ni + 1
                continue
        if star < 0:
            return False
        mark += 1
        pi, ni = star + 1, mark
    while pi < len(pattern) and pattern[pi] == "*":
        pi += 1
    return pi == len(pattern)


def _ignore_char(p: str, at: int, c: str) -> Tuple[bool, int]:
    """Whether a character matches what the pattern holds at this place, and the place after that."""
    if p[at] == "?":
        return True, at + 1
    if p[at] == "\\" and at + 1 < len(p):
        return p[at + 1] == c, at + 2
    if p[at] == "[":
        i = at + 1
        negate = i < len(p) and p[i] in "!^"
        if negate:
            i += 1
        found = False
        first = True
        while i < len(p) and (first or p[i] != "]"):
            first = False
            lo = p[i]
            if lo == "\\" and i + 1 < len(p):
                i += 1
                lo = p[i]
            hi = lo
            if i + 2 < len(p) and p[i + 1] == "-" and p[i + 2] != "]":
                i += 2
                hi = p[i]
                if hi == "\\" and i + 1 < len(p):
                    i += 1
                    hi = p[i]
            found = found or lo <= c <= hi
            i += 1
        if i < len(p):
            return found != negate, i + 1
        # (no end to it: the [ is itself)
    return p[at] == c, at + 1


class _FolderRules:
    """What of a folder stays here, as its rules are read on the way in."""

    def __init__(self, root: str, exclude: Optional[Sequence[str]], gitignore: bool) -> None:
        self.root = root
        self.gitignore = gitignore
        self.ignore: List[_IgnoreRule] = []
        self.exclude: List[_IgnoreRule] = []
        for pattern in exclude or ():
            self.exclude.extend(_parse_ignore("", pattern[2:] if pattern.startswith("./") else pattern))

    def enter(self, name: str) -> None:
        """Reads a folder's .gitignore, as it is gone into: ``name`` is its path in the folder sent, "" for its top. One
        that is a link is not read: it could be anything."""
        if not self.gitignore:
            return
        base = name + "/" if name else ""
        path = os.path.join(self.root, base.replace("/", os.sep), ".gitignore")
        try:
            found = os.lstat(path)
            if stat.S_ISREG(found.st_mode) and found.st_size <= _DEPLOY_IGNORE_MOST:
                self.ignore.extend(_parse_ignore(base, _read_regular(path, found, _DEPLOY_IGNORE_MOST)))
        except OSError:
            pass

    def left_out(self, name: str, is_dir: bool) -> bool:
        """Whether a file or a folder stays here, by its path in the folder sent."""
        return _name_left_out(name[name.rfind("/") + 1:]) or _ignored_by(self.exclude, name, is_dir) or _ignored_by(self.ignore, name, is_dir)


def _read_regular(path: str, seen: os.stat_result, most: int) -> str:
    """A file read whole, up to ``most`` bytes, if it is still the regular file that was looked at: not one put in its
    place, or a link to somewhere else."""
    with _open_regular(path, seen) as f:
        return f.read(most).decode("utf-8", "replace")


def _open_regular(path: str, seen: os.stat_result) -> BinaryIO:
    f = open(path, "rb")
    now = os.fstat(f.fileno())
    if not stat.S_ISREG(now.st_mode) or not os.path.samestat(seen, now):
        f.close()
        raise OSError("{} changed while it was being read".format(path))
    return f


def _is_inside(folder: str, path: str) -> bool:
    """Whether ``path`` is ``folder``, or inside it."""
    try:
        rel = os.path.relpath(path, folder)
    except ValueError:  # (another drive)
        return False
    return rel != ".." and not rel.startswith(".." + os.sep)


def _settings_folder() -> str:
    """The folder ``wm`` keeps its settings in, with its API key: $XDG_CONFIG_HOME/wheelmachine, else ~/.config/wheelmachine."""
    config = os.environ.get("XDG_CONFIG_HOME", "")
    if not config or not os.path.isabs(config):
        config = os.path.join(os.path.expanduser("~"), ".config")
    return os.path.join(config, "wheelmachine")


def _folder_to_send(folder: "os.PathLike[str] | str") -> str:
    """The folder, as it really is, or an error saying why it must not be sent whole: the home folder and anything above it,
    which hold everything a person has, and a folder that is, or is inside, one of the names that stay here, or that holds
    ``wm``'s settings."""
    root = os.path.realpath(os.fspath(folder))
    if not os.path.isdir(root):
        raise ValueError("{} is not a folder".format(os.fspath(folder)))
    home = os.path.realpath(os.path.expanduser("~"))
    if _is_inside(root, home):
        raise ValueError("{} holds your home folder, with all you keep there: say which folder of it to send".format(root))
    for step in root.replace(os.sep, "/").split("/"):
        if step and _name_left_out(step):
            raise ValueError("{} is in {}, which stays on this computer".format(root, step))
    settings = os.path.realpath(_settings_folder())
    if _is_inside(root, settings) or _is_inside(settings, root):
        raise ValueError("{} holds wm's settings, with its API key: say another folder".format(root))
    return root


def _walk_folder(root: str, rules: _FolderRules) -> Iterator[Tuple[str, str, bool, os.stat_result]]:
    """What goes of a folder and what stays, a path at a time, the way down sorted: (kind, path in the folder, whether it
    is a folder, what it is). ``kind`` is "goes" or "stays". What cannot be read is an error, not something left behind."""
    rules.enter("")
    stack = [""]
    while stack:
        at = stack.pop()
        with os.scandir(os.path.join(root, at.replace("/", os.sep))) as listing:
            entries = sorted(listing, key=lambda e: e.name)
        below: List[str] = []
        for entry in entries:
            name = at + entry.name
            found = entry.stat(follow_symlinks=False)
            is_dir = stat.S_ISDIR(found.st_mode)
            if rules.left_out(name, is_dir):
                yield "stays", name + "/" if is_dir else name, is_dir, found
                continue
            if not is_dir and not stat.S_ISREG(found.st_mode):
                yield "stays", name, False, found
                continue
            if is_dir:
                rules.enter(name)
                below.append(name + "/")
            yield "goes", name, is_dir, found
        stack.extend(reversed(below))


class _PackedFolder:
    """What goes of a folder and what stays."""

    __slots__ = ("files", "bytes", "paths", "left_out")

    def __init__(self) -> None:
        self.files = 0
        self.bytes = 0
        self.paths: List[str] = []
        self.left_out: List[str] = []


def _checked_path(name: str) -> None:
    """A path the API takes: told here, before anything is sent."""
    if any(ord(c) < 0x20 or ord(c) == 0x7f or c == "\\" for c in name):
        raise ValueError("{!r}: a path has no control characters and no backslashes".format(name))
    if len(os.fsencode(name)) > _DEPLOY_PATH_MOST:
        raise ValueError("{}: a path is at most {} bytes".format(name, _DEPLOY_PATH_MOST))
    if name.count("/") >= _DEPLOY_DEPTH_MOST:
        raise ValueError("{}: at most {} folders deep".format(name, _DEPLOY_DEPTH_MOST))


def _pack_folder(folder: "os.PathLike[str] | str", into: Optional[BinaryIO], exclude: Optional[Sequence[str]] = None, gitignore: bool = True) -> _PackedFolder:
    """A folder as a gzipped tar archive, written to ``into`` (None: only what would go is found out)."""
    root = _folder_to_send(folder)
    out = _PackedFolder()
    rules = _FolderRules(root, exclude, gitignore)
    archive = tarfile.open(fileobj=into, mode="w:gz", format=tarfile.PAX_FORMAT) if into is not None else None
    try:
        for kind, name, is_dir, found in _walk_folder(root, rules):
            if kind == "stays":
                out.left_out.append(name)
                continue
            _checked_path(name)
            if is_dir:
                if archive is not None:
                    archive.addfile(_tar_entry(name + "/", found, tarfile.DIRTYPE))
                continue
            out.files += 1
            if out.files > _DEPLOY_FILES_MOST:
                raise ValueError("at most {} files can be sent: use a repository, or send less".format(_DEPLOY_FILES_MOST))
            if found.st_size > _DEPLOY_FILE_MOST:
                raise ValueError("{}: a file is at most 100 MB".format(name))
            out.bytes += found.st_size
            if out.bytes > _DEPLOY_ALL_MOST:
                raise ValueError("at most 500 MB can be sent: use a repository, or send less")
            out.paths.append(name)
            if archive is not None:
                with _open_regular(os.path.join(root, name.replace("/", os.sep)), found) as f:
                    archive.addfile(_tar_entry(name, found, tarfile.REGTYPE), f)
    finally:
        if archive is not None:
            archive.close()
    if out.files == 0:
        raise ValueError("{} has no files to send".format(root))
    out.paths.sort()
    out.left_out.sort()
    return out


def _tar_entry(name: str, found: os.stat_result, kind: bytes) -> tarfile.TarInfo:
    entry = tarfile.TarInfo(name)
    entry.type = kind
    entry.mtime = int(found.st_mtime)
    entry.mode = 0o755 if kind == tarfile.DIRTYPE or found.st_mode & 0o111 else 0o644
    entry.size = found.st_size if kind == tarfile.REGTYPE else 0
    return entry


class Deployments(_Resource):
    """``client.deployments``: apps put on the internet from a repository on GitHub or GitLab, the https address of a
    public one, or files, each on a machine of its own at a public address.

    Not open yet: until Deploy opens, each call raises an error whose code is ``not_open``."""

    def create(
        self,
        *,
        start: str,
        port: int,
        repo: Optional[str] = None,
        connection: Optional[str] = None,
        url: Optional[str] = None,
        upload: bool = False,
        name: Optional[str] = None,
        project: Optional[str] = None,
        branch: Optional[str] = None,
        build: Optional[str] = None,
        cpus: Optional[int] = None,
        type: Optional[str] = None,
        region: Optional[str] = None,
        secrets: Optional[Sequence[str]] = None,
        keep_awake: Optional[bool] = None,
        idempotency_key: Optional[str] = None,
    ) -> Deployment:
        """Makes a deployment and begins its first run, which goes on after this answer: ``wait()`` follows it. Its code
        comes from a repository of one of your connections (``connection`` and ``repo``), a public repository's https
        address (``url``), or files (``upload=True``, then ``send_folder()`` or ``send_files()``). ``start`` is what
        starts it, with its port in ``$PORT`` and ``HOST`` as 0.0.0.0, and ``port`` the port it listens on; ``detect()``
        suggests them. It is charged as any machine, builds included. A retry after a lost connection never makes two."""
        source = _given(connection=connection, repo=repo, url=url, upload=True if upload else None)
        payload = _given(
            name=name,
            project=project,
            source=source,
            branch=branch,
            build=build,
            start=start,
            port=_port(port),
            cpus=cpus,
            type=type,
            region=region,
            secrets=_names(secrets) if secrets is not None else None,
            keep_awake=keep_awake,
        )
        data, info = self._client._json("POST", "/v1/deployments", payload=payload, idempotency_key=_key(idempotency_key))
        return _answered(Deployment.from_dict(data or {}), info)

    def list(self, *, project: Optional[str] = None) -> List[Deployment]:
        """The deployments you see, the newest first, each with its current or last run."""
        data, _ = self._client._json("GET", "/v1/deployments", query=[("project", project)] if project else None)
        return [Deployment.from_dict(d) for d in _items(data)]

    def get(self, deployment_id: str) -> Deployment:
        data, info = self._client._json("GET", "/v1/deployments/" + _id(deployment_id, "deployment_id"))
        return _answered(Deployment.from_dict(data or {}), info)

    def update(
        self,
        deployment_id: str,
        *,
        name: Optional[str] = None,
        branch: Optional[str] = None,
        build: Optional[str] = None,
        start: Optional[str] = None,
        port: Optional[int] = None,
        keep_awake: Optional[bool] = None,
    ) -> Deployment:
        """Changes its settings. Its name, branch, commands and port apply from its next run; ``keep_awake`` at once.
        Only the person who deployed it can."""
        payload = _given(name=name, branch=branch, build=build, start=start, port=_port(port) if port is not None else None, keep_awake=keep_awake)
        data, info = self._client._json("PATCH", "/v1/deployments/" + _id(deployment_id, "deployment_id"), payload=payload)
        return _answered(Deployment.from_dict(data or {}), info)

    def delete(self, deployment_id: str) -> None:
        """Deletes it, and its machine with all its files. Its address answers nothing from then on."""
        self._client._json("DELETE", "/v1/deployments/" + _id(deployment_id, "deployment_id"))

    def redeploy(self, deployment_id: str) -> Deployment:
        """Fetches its branch as it is now, or uses its files again, and builds it. A build that fails leaves the live
        version running. Once built, the live version stops and the new one starts, and one that does not answer within
        two minutes is replaced by the last one. ``wait()`` follows it. A call that may have reached the API is not sent
        again: a deployment has one run at a time."""
        data, info = self._client._json("POST", "/v1/deployments/{}/redeploy".format(_id(deployment_id, "deployment_id")), once=True)
        return _answered(Deployment.from_dict(data or {}), info)

    def send_files(
        self,
        deployment_id: str,
        data: FileData,
        *,
        content_length: Optional[int] = None,
        timeout: Optional[float] = None,
    ) -> Deployment:
        """Sends a tar archive, or a gzipped one, as its files: its first, or new ones in place of its last. It is then
        built and started. ``data`` is bytes, a ``pathlib.Path`` to the archive, or a file object opened in binary mode
        (one that cannot seek needs ``content_length``). At most 20,000 files, 100 MB a file and 500 MB in all; a folder
        goes through ``send_folder()``."""
        answer = self._client._send(
            "PUT",
            "/v1/deployments/{}/files".format(_id(deployment_id, "deployment_id")),
            body=_file_body(data, content_length),
            timeout=timeout,
            buffer=True,
        )
        return _answered(Deployment.from_dict(json.loads(answer.content.decode("utf-8"))), self._client._info(answer))

    def send_folder(
        self,
        deployment_id: str,
        folder: "os.PathLike[str] | str",
        *,
        exclude: Optional[Sequence[str]] = None,
        gitignore: bool = True,
        ready_timeout_s: float = 600.0,
        timeout: Optional[float] = None,
    ) -> Deployment:
        """Packs a folder here and sends it as ``send_files()`` does, once its machine is ready for it, waiting at most
        ``ready_timeout_s`` seconds. Left out, wherever they are and in any case, are keys, tokens and settings (``.env`` and
        the like, ``.git``, ``.npmrc``, ``.ssh``, ``*.pem``), what its ``.gitignore`` files name (unless ``gitignore=False``),
        what ``exclude`` names, written as in a ``.gitignore``, ``node_modules`` and the like, and links, which the API does
        not take: the answer's ``left_out`` names them, and ``folder_files()`` says it all without sending. A home folder
        and a folder of settings are refused. At most 20,000 files, 100 MB a file and 500 MB in all, told here before
        anything is sent. Give its secrets by name."""
        with tempfile.TemporaryFile() as packed:
            found = _pack_folder(folder, packed, exclude, gitignore)
            size = packed.tell()
            packed.seek(0)
            self._until_files_wanted(deployment_id, ready_timeout_s)
            sent = self.send_files(deployment_id, packed, content_length=size, timeout=timeout)
        sent.left_out = found.left_out
        return sent

    def folder_files(self, folder: "os.PathLike[str] | str", *, exclude: Optional[Sequence[str]] = None, gitignore: bool = True) -> FolderFiles:
        """What ``send_folder()`` would send of a folder, and what it would leave out: nothing is sent, and nothing asked of
        the API."""
        found = _pack_folder(folder, None, exclude, gitignore)
        return FolderFiles(files=found.paths, bytes=found.bytes, left_out=found.left_out)

    def _until_files_wanted(self, deployment_id: str, timeout_s: float) -> None:
        """Waits while its run gets its machine ready: the API holds a call only a minute for that, and a machine may take
        longer, and it is better not to send it all to be refused."""
        deadline = time.monotonic() + timeout_s
        while True:
            now = self.get(deployment_id)
            if now.run is None or now.run.ended_at is not None or now.run.step != "machine":
                return
            if time.monotonic() >= deadline:
                raise WaitTimeoutError("its machine is not ready yet, so its files were not sent: {} is made, and send_folder() can be called again".format(now.id),
                                       status=408, code="timeout")
            time.sleep(min(_DEPLOY_FILES_POLL_S, max(0.0, deadline - time.monotonic())))

    def wait(self, deployment_id: str, *, timeout_s: float = 300.0) -> Deployment:
        """Answers once its current run has ended (at once when it has none going): its ``state`` and its ``run`` say how
        it ended. Raises WaitTimeoutError when it is still deploying after ``timeout_s`` seconds. Waits longer than the
        API's 600 seconds are asked for again."""
        if timeout_s < 0:
            raise ValueError("timeout_s cannot be negative")
        path = "/v1/deployments/{}/wait".format(_id(deployment_id, "deployment_id"))
        deadline = time.monotonic() + timeout_s
        while True:
            left = max(0.0, deadline - time.monotonic())
            this = min(left, LONGEST_WAIT_S)
            began = time.monotonic()
            try:
                data, info = self._client._json("GET", path, query=[("timeout_s", str(math.ceil(this)))], timeout=self._client._held(this))
            except WaitTimeoutError:
                if left <= LONGEST_WAIT_S or time.monotonic() - began < this - 1:
                    raise
                continue
            return _answered(Deployment.from_dict(data or {}), info)

    def runs(self, deployment_id: str) -> List[DeployRun]:
        """Its last 20 runs, the newest first."""
        data, _ = self._client._json("GET", "/v1/deployments/{}/runs".format(_id(deployment_id, "deployment_id")))
        return [DeployRun.from_dict(r) for r in _items(data)]

    def logs(self, deployment_id: str, *, kind: Optional[Literal["build", "app"]] = None, run: Optional[str] = None) -> DeployLog:
        """Its log: ``build``, a run's own, its last 256 KB (``run``, or its last run's); or ``app``, the end of what the
        app itself writes, which wakes its machine. Only the person who deployed it can."""
        query = [(k, v) for k, v in (("kind", kind), ("run", run)) if v]
        data, info = self._client._json("GET", "/v1/deployments/{}/logs".format(_id(deployment_id, "deployment_id")), query=query or None)
        return _answered(DeployLog.from_dict(data or {}), info)

    def detect(
        self,
        *,
        connection: Optional[str] = None,
        repo: Optional[str] = None,
        url: Optional[str] = None,
        branch: Optional[str] = None,
        folder: "os.PathLike[str] | str | None" = None,
        exclude: Optional[Sequence[str]] = None,
        gitignore: bool = True,
    ) -> DeployGuess:
        """How code most likely builds and starts, a suggestion to change: a repository's, or that of a ``folder`` here, by
        the few files at its top that say so, of those ``send_folder()`` would send with the same ``exclude`` and
        ``gitignore``. A link is not followed: it could lead anywhere."""
        if folder is not None:
            payload: Dict[str, Any] = {"files": _detect_files(folder, exclude, gitignore)}
        else:
            payload = _given(source=_given(connection=connection, repo=repo, url=url), branch=branch)
        data, info = self._client._json("POST", "/v1/deployments/detect", payload=payload, repeatable=True)
        return _answered(DeployGuess.from_dict(data or {}), info)

    def repos(self, *, project: Optional[str] = None, connection: Optional[str] = None) -> List[DeployRepo]:
        """The repositories you may deploy into a team (your first when left out), the most recently pushed to first."""
        query = [(k, v) for k, v in (("project", project), ("connection", connection)) if v]
        data, _ = self._client._json("GET", "/v1/deployments/repos", query=query or None)
        return [DeployRepo.from_dict(r) for r in _items(data)]

    def branches(self, *, connection: Optional[str] = None, repo: Optional[str] = None, url: Optional[str] = None) -> DeployBranches:
        """A repository's branches, and which is its default: of one of your connections (``connection`` and ``repo``), or
        a public one (``url``)."""
        query = [(k, v) for k, v in (("connection", connection), ("repo", repo), ("url", url)) if v]
        data, info = self._client._json("GET", "/v1/deployments/branches", query=query or None)
        return _answered(DeployBranches.from_dict(data or {}), info)


def _detect_files(folder: "os.PathLike[str] | str", exclude: Optional[Sequence[str]] = None, gitignore: bool = True) -> Dict[str, Optional[str]]:
    """The files at a folder's top that say how it builds and starts."""
    root = _folder_to_send(folder)
    rules = _FolderRules(root, exclude, gitignore)
    rules.enter("")
    out: Dict[str, Optional[str]] = {}
    for name in _DEPLOY_DETECT_NAMES:
        path = os.path.join(root, name)
        try:
            found = os.lstat(path)
        except OSError:
            continue
        if not stat.S_ISREG(found.st_mode) or rules.left_out(name, False):
            continue
        out[name] = None
        if name in _DEPLOY_DETECT_READ and found.st_size <= _DEPLOY_DETECT_MOST:
            out[name] = _read_regular(path, found, _DEPLOY_DETECT_MOST)
    return out


class Apps(_Resource):
    """``client.apps``: the apps a machine carries, turned on and off - a desktop and a code editor in the browser, an
    agent's own page, a model server inside the machine, and agents run in its terminal. Each call asks the machine,
    so a paused one is woken for it. A machine with no apps answers ConflictError with the code ``no_apps``."""

    def list(self, machine_id: str) -> List[App]:
        """The machine's apps, and whether each is on."""
        data, _ = self._client._json("GET", "/v1/machines/{}/apps".format(_id(machine_id, "machine_id")))
        return [App.from_dict(a) for a in _items(data)]

    def turn_on(self, machine_id: str, app: str, *, access_s: Optional[int] = None) -> App:
        """Turns an app on; it may take up to a minute. A web app's port is opened private when it is not open, and
        the answer carries a one-time ``link``
        that lands on its page: the browser that opens it may reach the address for ``access_s`` seconds (60 to
        3600, 1800 by default). An app with a token of its own answers it once, in ``token``. The answer is shown
        once, so a call that may have reached the API is never sent again; only one that could not go out is."""
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/apps/{}/on".format(_id(machine_id, "machine_id"), _app(app)),
            payload=_given(access_s=access_s) or None,
            once=True,
            timeout=self._client._held(_HOLDS_APP_ON),
        )
        return _answered(App.from_dict(data or {}), info)

    def turn_off(self, machine_id: str, app: str) -> App:
        """Turns an app off. A web app's port is closed with it when turning it on opened it and it is still private;
        a port you opened yourself, or made public, stays open. Turning off an app that is off is not an error. The
        code editor is always on: turning it off is refused with ConflictError."""
        data, info = self._client._json(
            "POST", "/v1/machines/{}/apps/{}/off".format(_id(machine_id, "machine_id"), _app(app)), repeatable=True
        )
        return _answered(App.from_dict(data or {}), info)

    def show(self, machine_id: str, app: str) -> App:
        """Shows an app on the machine's own screen: its window opened on the desktop, signed in where it signs in,
        for a person who watches that screen in the dashboard. It may take up to two minutes. Showing an app that is
        shown already does no harm, so a call that lost its connection is sent again."""
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/apps/{}/show".format(_id(machine_id, "machine_id"), _app(app)),
            repeatable=True,
            timeout=self._client._held(_HOLDS_APP_SHOW),
        )
        return _answered(App.from_dict(data or {}), info)

    def link(self, machine_id: str, app: str, *, access_s: Optional[int] = None) -> App:
        """A fresh one-time ``link`` into a web app that is on, made as a port's link is: it opens once, within five
        minutes, and lands on the app's page. Each call makes a new link, so a call that may have reached the API is
        never sent again."""
        data, info = self._client._json(
            "POST",
            "/v1/machines/{}/apps/{}/link".format(_id(machine_id, "machine_id"), _app(app)),
            payload=_given(access_s=access_s) or None,
            once=True,
        )
        return _answered(App.from_dict(data or {}), info)


class Projects(_Resource):
    """``client.projects``: the account's projects, which hold its machines."""

    def create(self, name: str, *, idempotency_key: Optional[str] = None) -> Project:
        data, info = self._client._json(
            "POST", "/v1/projects", payload={"name": name}, idempotency_key=_key(idempotency_key)
        )
        return _answered(Project.from_dict(data or {}), info)

    def get(self, project_id: str) -> Project:
        data, info = self._client._json("GET", "/v1/projects/" + _id(project_id, "project_id"))
        return _answered(Project.from_dict(data or {}), info)

    def list(self, *, limit: Optional[int] = None) -> PageIterator[Project]:
        """Every project, oldest first, page after page as it is iterated."""

        def page(after: Optional[str]) -> Tuple[List[Project], Optional[str]]:
            query: List[Tuple[str, str]] = []
            if limit is not None:
                query.append(("limit", str(limit)))
            if after:
                query.append(("after", after))
            data, _ = self._client._json("GET", "/v1/projects", query=query)
            return [Project.from_dict(p) for p in _items(data)], _next(data)

        return PageIterator(page)

    def update(self, project_id: str, *, name: str) -> Project:
        """Renames it."""
        data, info = self._client._json("PATCH", "/v1/projects/" + _id(project_id, "project_id"), payload={"name": name})
        return _answered(Project.from_dict(data or {}), info)

    def delete(self, project_id: str) -> None:
        """Deletes an empty project; one that still holds machines is refused (ConflictError, project_not_empty)."""
        self._client._json("DELETE", "/v1/projects/" + _id(project_id, "project_id"))

    def rules(self, project_id: str) -> TeamRules:
        """In an organisation, a team's rules: whether its machines are handed off freely, with a manager's OK or not at
        all; whether its members fork them, every fork waiting for the OK of a full manager of the team, an owner or an
        admin, or not at all; whether GPUs and open internet are allowed in it; its budget for the month and what its
        machines came to so far. Those set, else the defaults. Setting them is its full managers', owners' and admins',
        signed in to the dashboard."""
        data, info = self._client._json("GET", "/v1/projects/{}/rules".format(_id(project_id, "project_id")))
        return _answered(TeamRules.from_dict(data or {}), info)


class Keys(_Resource):
    """``client.keys``: the account's API keys. A key makes only keys no stronger than itself."""

    def list(self) -> List[Key]:
        data, _ = self._client._json("GET", "/v1/keys")
        return [Key.from_dict(k) for k in _items(data)]

    def create(
        self,
        *,
        name: str,
        scopes: Sequence[Scope],
        projects: Optional[Sequence[str]] = None,
        expires_at: Union[str, datetime, None] = None,
        rate_per_minute: Optional[int] = None,
        idempotency_key: Optional[str] = None,
        confirm_code: Optional[str] = None,
    ) -> KeyCreated:
        """Makes a key. Its ``secret`` is in this answer only: keep it somewhere safe at once.

        A key with full access (``cloud:*``), or that makes keys or gives secrets (``cloud:keys:write``,
        ``cloud:secrets:write``), needs the code we e-mail the key's person: refused without it
        (PermissionDeniedError, ``code`` ``confirm_required``, its ``confirm`` saying what for), it is sent with
        ``client.confirm_codes.send(**error.confirm)`` and given here as ``confirm_code``."""
        payload = _given(
            name=name,
            scopes=list(scopes),
            projects=list(projects) if projects is not None else None,
            expires_at=_when(expires_at) if expires_at is not None else None,
            rate_per_minute=rate_per_minute,
        )
        data, info = self._client._json("POST", "/v1/keys", payload=payload, idempotency_key=_key(idempotency_key),
                                        confirm_code=confirm_code)
        return _answered(KeyCreated.from_dict(data or {}), info)

    def revoke(self, key_id: str) -> None:
        """Revokes a key: it stops working within seconds, everywhere. A key can revoke itself."""
        self._client._json("DELETE", "/v1/keys/" + _id(key_id, "key_id"))


class ConfirmCodes(_Resource):
    """``client.confirm_codes``: the codes we e-mail for a risky step - here, making a key with full access."""

    def send(self, step: str, *, org: Optional[str] = None, to: Optional[str] = None, account: Optional[str] = None) -> ConfirmCodeSent:
        """E-mails the key's person a 6-digit code for the step, as an error's ``confirm`` says it: it works once, for
        10 minutes, for that alone. At most 5 are sent in 15 minutes."""
        payload = _given(step=step, org=org, to=to, account=account)
        data, info = self._client._json("POST", "/v1/confirm-codes", payload=payload)
        return _answered(ConfirmCodeSent.from_dict(data or {}), info)


class Secrets(_Resource):
    """``client.secrets``: values every command run in a machine given them gets as environment variables - a token,
    a database's address - set once for the account and named by each machine that gets them. A value is never shown
    again: not by these calls, not by any other."""

    def set(self, name: str, value: str, *, outside: Union[bool, Mapping[str, Any], None] = None,
            every_machine: Optional[bool] = None) -> Secret:
        """Sets a secret, or sets it anew; the machines that get it get the new value from their next command. The
        value goes in the call's body alone, and the answer is the secret without it. Setting needs a key with the
        ``cloud:secrets:write`` scope, of the whole account; an account keeps at most 200.

        An AI key - a name ``presets()`` lists, such as ``ANTHROPIC_API_KEY`` - is kept outside the machines by its
        preset and is on every machine, unless ``outside=False`` (an ordinary secret, its value in the machines given
        it) or ``every_machine=False`` (only on the machines given it). ``outside`` may also be a policy of its own:
        ``{"hosts": [...], "header": "Authorization", "format": "Bearer {value}"}``. Said whole: what is left out is
        the API's default for the name, whatever the secret was before."""
        _secret_value(value)
        payload: Dict[str, Any] = {"value": value}
        if outside is not None:
            if not isinstance(outside, (bool, Mapping)):
                raise TypeError("outside is True, False, or a policy of hosts, header and format")
            payload["outside"] = outside if isinstance(outside, bool) else dict(outside)
        if every_machine is not None:
            if not isinstance(every_machine, bool):
                raise TypeError("every_machine is True or False")
            payload["every_machine"] = every_machine
        data, info = self._client._json("PUT", "/v1/secrets/" + _secret_name(name), payload=payload)
        return _answered(Secret.from_dict(data or {}), info)

    def presets(self) -> List[SecretPreset]:
        """The AI keys the API knows by their names, in the order agents choose between them: for each, the names it
        is set under and the hosts its key is sent to."""
        data, _ = self._client._json("GET", "/v1/secrets/presets")
        return [SecretPreset.from_dict(p) for p in _items(data)]

    def list(self) -> List[Secret]:
        """The account's secrets by name, each with the machines given it."""
        data, _ = self._client._json("GET", "/v1/secrets")
        return [Secret.from_dict(s) for s in _items(data)]

    def delete(self, name: str) -> None:
        """Deletes it, and takes it off every machine given it: set again later, it goes to none of them until it is
        given anew."""
        self._client._json("DELETE", "/v1/secrets/" + _secret_name(name))


def _team(project: Optional[str]) -> List[Tuple[str, str]]:
    """The team a choice is for, in an organisation: its project's id, as the call's query names it."""
    if project is None:
        return []
    return [("project", _id(project, "project"))]


def _model(value: str) -> str:
    """A model as a choice names it, checked before it is sent: its provider and the provider's own id for it."""
    if not isinstance(value, str) or "/" not in value:
        raise ValueError("model is its provider and the provider's own id for it, such as openai/gpt-6.1-sol")
    return value


def _allowed_path(organisation: bool, team: Optional[str], role: Optional[str], person: Optional[str]) -> str:
    """The route of a list of allowed models: the organisation's, a team's, a role's or a person's - one of the four."""
    named = [p for p in (organisation or None, team, role, person) if p is not None]
    if len(named) != 1:
        raise ValueError("name one list: organisation=True, team=, role= or person=")
    if organisation:
        return "/v1/models/allowed/organisation"
    if team is not None:
        return "/v1/models/allowed/teams/" + _id(team, "team")
    if role is not None:
        return "/v1/models/allowed/roles/" + _id(role, "role")
    return "/v1/models/allowed/people/" + _id(person or "", "person")


class Models(_Resource):
    """``client.models``: the models the account's AI keys can use, as each model's provider lists them for the key. The
    lists are read from each provider with your key, and kept for an hour; only the models an agent codes with are
    listed."""

    def list(self, *, agent: Optional[str] = None, refresh: bool = False, project: Optional[str] = None) -> ModelList:
        """The models, grouped by provider in the order agents choose between keys (``data``), and how each provider's list
        stands (``suppliers``). ``agent`` keeps the models of the providers that agent can use; ``refresh=True`` reads each
        list again when it was read more than a minute ago. Limited to a list of the models you may use in an organisation,
        its models alone, and the list (``allowed``): the one of the team ``project`` names (left out, your one team)."""
        query: List[Tuple[str, str]] = []
        if agent is not None:
            query.append(("agent", _app(agent)))
        if refresh:
            query.append(("refresh", "true"))
        query += _team(project)
        data, info = self._client._json("GET", "/v1/models", query=query)
        return _answered(ModelList.from_dict(data or {}), info)

    def allowed(self) -> List[AllowedModels]:
        """Every list of the models the organisation's people may use, the widest first: for its owners and admins."""
        data, info = self._client._json("GET", "/v1/models/allowed")
        return [_answered(AllowedModels.from_dict(x), info) for x in _items(data)]

    def allow(self, models: Sequence[str], *, organisation: bool = False, team: Optional[str] = None, role: Optional[str] = None,
              person: Optional[str] = None) -> AllowedModels:
        """Sets the models people may use - one to ten, as ``list()`` gives their ids, in order: the first is the one an
        agent starts on where its person chose none of them - for the whole organisation (``organisation=True``), a team
        (``team``, its project's id), a role (``role``: ``member``, ``approver``, ``manager`` or ``full``) or one person
        (``person``, their id); one of the four. A choice a person made that is not on the list is set aside: their
        agent starts on the list's first model it can use. For owners and admins, to whom no list applies."""
        if isinstance(models, str) or not 1 <= len(models) <= 10:
            raise ValueError("models is a list of one to ten models, such as ['openai/gpt-6.1-sol']")
        data, info = self._client._json("PUT", _allowed_path(organisation, team, role, person), payload={"models": [_model(m) for m in models]})
        return _answered(AllowedModels.from_dict(data or {}), info)

    def allow_any(self, *, organisation: bool = False, team: Optional[str] = None, role: Optional[str] = None,
                  person: Optional[str] = None) -> None:
        """Takes a list away: those it applied to may use every model again, unless another list applies to them."""
        self._client._json("DELETE", _allowed_path(organisation, team, role, person))


class Agents(_Resource):
    """``client.agents``: the agents of the account's machines, and the model each starts on - chosen for one agent, or
    one for every agent. A choice reaches running machines at once, and the others when they next start;
    an agent already running uses it from its next start. In an organisation the choices are the caller's own, in the
    team ``project`` names (left out, their one team)."""

    def list(self, *, project: Optional[str] = None) -> AgentList:
        """The agents, each with the providers it can use, the model chosen for it and what it starts on."""
        data, info = self._client._json("GET", "/v1/agents", query=_team(project))
        return _answered(AgentList.from_dict(data or {}), info)

    def choose(self, agent: str, model: str, *, project: Optional[str] = None) -> Agent:
        """Chooses the model an agent starts on, as ``client.models.list()`` gives its id (``openai/gpt-6.1-sol``). The
        model is checked with its provider as it is chosen."""
        data, info = self._client._json("PUT", "/v1/agents/{}/model".format(_app(agent)), query=_team(project),
                                        payload={"model": _model(model)})
        return _answered(Agent.from_dict(data or {}), info)

    def choose_for_every(self, model: str, *, project: Optional[str] = None) -> AgentList:
        """Chooses one model for every agent: each that can use its provider starts on it, and each agent's own choice is
        taken away."""
        data, info = self._client._json("PUT", "/v1/agents/model", query=_team(project), payload={"model": _model(model)})
        return _answered(AgentList.from_dict(data or {}), info)

    def reset(self, agent: str, *, project: Optional[str] = None) -> None:
        """Takes an agent's choice away: it starts on the model for every agent where it can, else on its default."""
        self._client._json("DELETE", "/v1/agents/{}/model".format(_app(agent)), query=_team(project))

    def reset_every(self, *, project: Optional[str] = None) -> None:
        """Takes the one model for every agent away."""
        self._client._json("DELETE", "/v1/agents/model", query=_team(project))


class Orgs(_Resource):
    """``client.orgs``: the organisations the key's person is in. Their people, invitations and teams are managed in
    the dashboard, signed in."""

    def list(self) -> List[Org]:
        """The organisations the person who made this key is in, the newest first, with their role in each. A key made
        in an organisation lists none - what else the person who made it is in is theirs - and is refused (PermissionDeniedError);
        a key of the person's own account lists them."""
        data, _ = self._client._json("GET", "/v1/orgs")
        return [Org.from_dict(o) for o in _items(data)]


class Requests(_Resource):
    """``client.requests``: in an organisation, the requests to fork a machine for someone or to hand it off to them -
    given with ``machines.fork_for`` and ``machines.hand_off``, or asked for here - and a member's own forks, which
    ``machines.fork`` answers while they wait. Approving and declining are a person's, signed in to the dashboard."""

    def list(
        self,
        *,
        open: Optional[bool] = None,
        machine: Optional[str] = None,
        limit: Optional[int] = None,
    ) -> PageIterator[MachineRequest]:
        """The newest first, page after page as it is iterated: those the key's person asked, those of machines they
        hold, those that would give them a machine - and to a manager of a team its machines' requests, to owners and
        admins every one. ``open=True`` gives those still waiting or being carried out alone; ``machine`` those of one
        machine, its own and the forks made of it; ``limit`` is how many come in each page (1 to 200)."""
        base: List[Tuple[str, str]] = []
        if open:
            base.append(("open", "true"))
        if machine is not None:
            base.append(("machine", machine))
        if limit is not None:
            base.append(("limit", str(limit)))

        def page(after: Optional[str]) -> Tuple[List[MachineRequest], Optional[str]]:
            query = base + ([("after", after)] if after else [])
            data, _ = self._client._json("GET", "/v1/requests", query=query)
            return [MachineRequest.from_dict(q) for q in _items(data)], _next(data)

        return PageIterator(page)

    def get(self, request_id: str) -> MachineRequest:
        """A request as it is now: how one answered ``approved``, and going on behind, is followed. One the key does not
        see is not found (NotFoundError)."""
        data, info = self._client._json("GET", "/v1/requests/" + _id(request_id, "request_id"))
        return _answered(MachineRequest.from_dict(data or {}), info)

    def ask(
        self,
        kind: Literal["fork", "hand_off"],
        of: str,
        *,
        team: Optional[str] = None,
        why: Optional[str] = None,
        idempotency_key: Optional[str] = None,
    ) -> MachineRequest:
        """Asks a teammate - ``of`` is their id, e-mail address or GitHub login - for a fork of one of their machines
        (``"fork"``), or for one of them (``"hand_off"``). Made with a key, it waits first for the OK of the person who made the key,
        signed in to the dashboard (``waiting_on.who`` ``asker``); then they pick which as they approve, and what the
        team's rules need of it follows. ``team`` is the team of the key's person it goes into, when it is another than
        the machine's. The same ask while it waits answers the one there is; it runs out after a day."""
        if kind not in ("fork", "hand_off"):
            raise ValueError("kind is 'fork' or 'hand_off'")
        if not isinstance(of, str) or not of:
            raise ValueError("of is the teammate: their id, e-mail address or GitHub login")
        data, info = self._client._json(
            "POST",
            "/v1/requests",
            payload=_given(kind=kind, of=of, team=team, why=why),
            idempotency_key=_key(idempotency_key),
        )
        return _answered(MachineRequest.from_dict(data or {}), info)

    def take_back(self, request_id: str) -> MachineRequest:
        """Withdraws a request the key's person asked, while it waits (ConflictError once it is decided or expired)."""
        data, info = self._client._json("DELETE", "/v1/requests/" + _id(request_id, "request_id"))
        return _answered(MachineRequest.from_dict(data or {}), info)


class Audit(_Resource):
    """``client.audit``: what was done in the account, by whom - the person, and the key or sign-in they did it with -
    to which thing, and from where. Read with a key that holds every scope (``cloud:*``), and in an organisation by its
    owners and admins alone (PermissionDeniedError for anyone else)."""

    def list(
        self,
        *,
        person: Optional[str] = None,
        action: Optional[str] = None,
        since: Union[str, datetime, None] = None,
        until: Union[str, datetime, None] = None,
        limit: Optional[int] = None,
    ) -> PageIterator[AuditEvent]:
        """Every event, the newest first, page after page as it is iterated. ``person`` is one person's id;
        ``action`` what the actions begin with (``"machine."``, ``"member."``); ``since`` and ``until`` are times (an
        aware ``datetime``, or RFC 3339), ``until`` itself left out; ``limit`` is how many come in each page (1 to
        200)."""
        base = _audit_query(person, action, since, until)
        if limit is not None:
            base.append(("limit", str(limit)))

        def page(after: Optional[str]) -> Tuple[List[AuditEvent], Optional[str]]:
            query = base + ([("after", after)] if after else [])
            data, _ = self._client._json("GET", "/v1/audit", query=query)
            return [AuditEvent.from_dict(e) for e in _items(data)], _next(data)

        return PageIterator(page)

    @overload
    def export(
        self,
        format: Literal["csv", "json"] = "csv",
        *,
        person: Optional[str] = None,
        action: Optional[str] = None,
        since: Union[str, datetime, None] = None,
        until: Union[str, datetime, None] = None,
        timeout: Optional[float] = None,
    ) -> bytes: ...

    @overload
    def export(
        self,
        format: Literal["csv", "json"] = "csv",
        *,
        to: Union[str, "os.PathLike[str]", BinaryIO],
        person: Optional[str] = None,
        action: Optional[str] = None,
        since: Union[str, datetime, None] = None,
        until: Union[str, datetime, None] = None,
        timeout: Optional[float] = None,
    ) -> int: ...

    def export(
        self,
        format: Literal["csv", "json"] = "csv",
        *,
        to: Union[str, "os.PathLike[str]", BinaryIO, None] = None,
        person: Optional[str] = None,
        action: Optional[str] = None,
        since: Union[str, datetime, None] = None,
        until: Union[str, datetime, None] = None,
        timeout: Optional[float] = None,
    ) -> Union[bytes, int]:
        """The audit as a file, whole: ``"csv"`` (every field quoted, and one a spreadsheet would run begun with
        ``'``, so that it is shown and never run) or ``"json"`` (an array of events, the newest first). It covers a
        year at most - the last one when ``since`` is left out - and 100,000 events at most; asking for more is refused
        (BadRequestError) in the API's own words. Gives the file's bytes, or with ``to`` streams it into that local path
        (written whole or not at all; a new one readable by you only, as it names people and where they were) or binary
        file object and gives how many bytes came."""
        if format not in ("csv", "json"):
            raise ValueError("format is 'csv' or 'json'")
        answer = self._client._send(
            "GET",
            "/v1/audit/export",
            query=[("format", format)] + _audit_query(person, action, since, until),
            accept="text/csv" if format == "csv" else "application/json",
            timeout=timeout if timeout is not None else self._client._held(_HOLDS_EXPORT),
            buffer=to is None,
        )
        if to is None:
            return answer.content
        if hasattr(to, "write"):
            return self._client._drain(answer, to)  # type: ignore[arg-type]
        return _save(self._client, answer, os.fspath(to), 0o600)


def _audit_query(
    person: Optional[str], action: Optional[str], since: Union[str, datetime, None], until: Union[str, datetime, None]
) -> List[Tuple[str, str]]:
    """What the audit is narrowed by, as the API takes it: times in RFC 3339."""
    query: List[Tuple[str, str]] = []
    if person is not None:
        query.append(("person", person))
    if action is not None:
        query.append(("action", action))
    if since is not None:
        query.append(("since", _when(since)))
    if until is not None:
        query.append(("until", _when(until)))
    return query


class Credit(_Resource):
    """``client.credit``: the account's credit - prepaid usage credit for Wheelmachine alone, counted in the account's
    currency (``currency``: US dollars, pounds sterling or euros). Your own account gets 5 of its currency in free credit
    once, and a new organisation none; machines' running time is taken from it once a minute, one entry a machine
    a day; at zero or below the account cannot spend, and its machines are paused until the balance is above zero
    (``buy``). The currency can be changed until your first purchase, or until we change it for you (``set_currency``). In an organisation
    the credit is the organisation's, read by its owners, admins and billing and bought by its owners and billing
    (PermissionDeniedError for anyone else, and for a key limited to some projects)."""

    def get(self, *, limit: Optional[int] = None) -> CreditBalance:
        """The credit: what is left, whether it has run out, whether credit can be bought here yet, and its latest
        ``limit`` entries (1 to 200; 50 when left out), the newest first."""
        query = [("limit", str(limit))] if limit is not None else []
        data, info = self._client._json("GET", "/v1/credit", query=query)
        return _answered(CreditBalance.from_dict(data or {}), info)

    def entries(self, *, limit: Optional[int] = None) -> PageIterator[CreditEntry]:
        """Every entry of the credit, the newest first, page after page as it is iterated; ``limit`` is how many come
        in each page (1 to 200)."""
        base = [("limit", str(limit))] if limit is not None else []

        def page(after: Optional[str]) -> Tuple[List[CreditEntry], Optional[str]]:
            query = base + ([("after", after)] if after else [])
            data, _ = self._client._json("GET", "/v1/credit", query=query)
            return [CreditEntry.from_dict(e) for e in _items(data)], _next(data)

        return PageIterator(page)

    def buy(self, amount: int, *, idempotency_key: Optional[str] = None) -> CreditPurchase:
        """A checkout for ``amount`` of the account's currency in credit - 10, 25, 50 or 100 - started. Nothing is
        charged until it is paid in Paddle's checkout: open ``checkout.url`` in a browser signed in to the dashboard, in
        the same account. Paddle adds any sales tax or VAT and e-mails the receipt and tax invoice; the credit is there
        once Paddle says the payment is complete. Where buying credit is not set up, the error's ``code`` is
        ``not_open``."""
        data, info = self._client._json(
            "POST", "/v1/credit/purchases", payload={"amount": amount}, idempotency_key=_key(idempotency_key)
        )
        return _answered(CreditPurchase.from_dict(data or {}), info)

    def set_currency(self, currency: str) -> CreditBalance:
        """The currency the credit is counted in, changed - ``"USD"``, ``"GBP"`` or ``"EUR"`` - until your first purchase, or until we change it for you
        (ConflictError then). The credit keeps its value at Wheelmachine's rates but the
        free credit, which becomes 5 of the new currency; limits, budgets and the spend alert are turned too. In your
        own account, you change it; in an organisation, its owners and billing. The credit, as it is then."""
        data, info = self._client._json("PUT", "/v1/credit/currency", payload={"currency": currency})
        return _answered(CreditBalance.from_dict(data or {}), info)

