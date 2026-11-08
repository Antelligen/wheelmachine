"""The client: the key, the address, and how a call is made, retried and answered."""

from __future__ import annotations

import json
import os
import random
import re
import time
from datetime import date, datetime, timezone
from email.utils import parsedate_to_datetime
from typing import Any, BinaryIO, Callable, Dict, List, Optional, Sequence, Tuple, Union
from urllib.parse import urlencode, quote

from ._errors import APIConnectionError, APITimeoutError, WheelmachineError, error_from_answer
from ._http import Answer, Body, BytesBody, Failure, OpenBody, Proxy, Target, Transport, is_timeout, proxy_from_environment
from ._models import (Code, CodeConnection, CodeGrant, FolderEntry, FolderGrant, FolderList, FolderListing, FolderTrash, GpuList, Me, RateLimit,
                      Region, ResponseInfo, Usage, UsageLimits, _answered)
from ._pagination import PageIterator
from ._resources import (Agents, Apps, Audit, ConfirmCodes, Credit, Deployments, FileData, Keys, Machines, Models, Orgs, Ports, Projects, Requests, Secrets,
                         _file_body)
from ._version import __version__

__all__ = ["Wheelmachine", "DEFAULT_BASE_URL"]

DEFAULT_BASE_URL = "https://api.wheelmachine.ai"
USER_AGENT = "wheelmachine-python/" + __version__

#: Methods that do the same however many times they are sent, so a call that lost its connection is sent again.
#: A POST is sent again only when it carries an Idempotency-Key (the calls that create something), or when sending it
#: twice does what sending it once does (opening a port: `repeatable=True`).
_REPEATABLE = frozenset({"GET", "HEAD", "PUT", "PATCH", "DELETE"})
#: A Retry-After longer than this is not waited out: the error is raised at once, with `retry_after` on it.
LONGEST_RETRY_AFTER_S = 60.0
_BACKOFF_FIRST_S = 0.5
_BACKOFF_MOST_S = 8.0


class _Secret:
    """The API key: kept out of every repr, so it cannot reach a log through this object."""

    __slots__ = ("_value",)

    def __init__(self, value: str) -> None:
        self._value = value

    def value(self) -> str:
        return self._value

    def __repr__(self) -> str:
        return "<hidden>"

    __str__ = __repr__


def _retry_after(value: Optional[str]) -> Optional[float]:
    """Retry-After in seconds: a number of seconds, or an HTTP date."""
    if not value:
        return None
    value = value.strip()
    try:
        seconds = float(value)
    except ValueError:
        try:
            when = parsedate_to_datetime(value)
        except (TypeError, ValueError, IndexError):
            return None
        if when is None:
            return None
        if when.tzinfo is None:
            when = when.replace(tzinfo=timezone.utc)
        seconds = (when - datetime.now(timezone.utc)).total_seconds()
    return max(0.0, seconds)


_FIELD = re.compile(r"(?:^|;)\s*([a-z]+)=(\d+)")


def _rate_limit(state: Optional[str], policy: Optional[str]) -> Optional[RateLimit]:
    """The RateLimit and RateLimit-Policy headers: `"key";r=599;t=1` and `"key";q=600;w=60`."""
    if not state and not policy:
        return None
    s = dict(_FIELD.findall(state or ""))
    p = dict(_FIELD.findall(policy or ""))

    def num(d: Dict[str, str], k: str) -> Optional[int]:
        return int(d[k]) if k in d else None

    return RateLimit(limit=num(p, "q"), window_seconds=num(p, "w"), remaining=num(s, "r"), reset_seconds=num(s, "t"))


class Wheelmachine:
    """A client of the Wheelmachine API.

    ``api_key`` is read from ``WHEELMACHINE_API_KEY`` and ``base_url`` from ``WHEELMACHINE_BASE_URL`` when they are
    not given. ``timeout`` (seconds) is how long a call may go without a byte coming; calls the API holds longer by
    design (making a machine ready, waiting for a state, running a command) are given the time they need on top.
    Calls refused for a moment (429, 503), and calls that lost their connection, are tried again up to
    ``max_retries`` times, with a growing wait or the one the API asked for. ``proxy`` (``http://host:port``) is
    reached with CONNECT; without it, HTTPS_PROXY and NO_PROXY are followed.

    Safe to share between threads. ``close()``, or a ``with`` block, closes its kept connections.
    """

    machines: Machines
    ports: Ports
    deployments: Deployments
    apps: Apps
    projects: Projects
    keys: Keys
    secrets: Secrets
    orgs: Orgs
    requests: Requests
    audit: Audit

    def __init__(
        self,
        api_key: Optional[str] = None,
        base_url: Optional[str] = None,
        timeout: float = 60.0,
        max_retries: int = 3,
        *,
        proxy: Optional[str] = None,
    ) -> None:
        key = (api_key if api_key is not None else os.environ.get("WHEELMACHINE_API_KEY", "")).strip()
        if not key:
            raise WheelmachineError("no API key: pass api_key=... or set WHEELMACHINE_API_KEY")
        if any(c < "!" or c > "~" for c in key):
            # never echoed: it would put the key in a log
            raise WheelmachineError("the API key holds characters an API key cannot hold")
        self._open(key, base_url, timeout, max_retries, proxy)

    def _open(self, key: str, base_url: Optional[str], timeout: float, max_retries: int, proxy: Optional[str]) -> None:
        """Everything but the key's own checks: a client signing a program in through the browser has none yet."""
        if not timeout or timeout <= 0:
            raise ValueError("timeout is a number of seconds above 0")
        if max_retries < 0:
            raise ValueError("max_retries cannot be negative")
        self._key = _Secret(key)
        self._target = Target(base_url or os.environ.get("WHEELMACHINE_BASE_URL") or DEFAULT_BASE_URL)
        self.timeout = float(timeout)
        self.max_retries = int(max_retries)
        self._transport = Transport(self._target, Proxy.parse(proxy) if proxy else proxy_from_environment(self._target))
        # seams for tests: how a pause is taken, and the jitter of a backoff
        self._sleep: Callable[[float], None] = time.sleep
        self._jitter: Callable[[], float] = random.random
        self.machines = Machines(self)
        self.ports = Ports(self)
        self.deployments = Deployments(self)
        self.apps = Apps(self)
        self.projects = Projects(self)
        self.keys = Keys(self)
        self.confirm_codes = ConfirmCodes(self)
        self.secrets = Secrets(self)
        self.models = Models(self)
        self.agents = Agents(self)
        self.orgs = Orgs(self)
        self.requests = Requests(self)
        self.audit = Audit(self)
        self.credit = Credit(self)

    @property
    def base_url(self) -> str:
        return self._target.url

    def __repr__(self) -> str:
        return "Wheelmachine(base_url={!r})".format(self.base_url)

    def close(self) -> None:
        """Closes the kept connections. The client can still be used: it opens new ones."""
        self._transport.close()

    def __enter__(self) -> "Wheelmachine":
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    # --- the account and the catalogue ----------------------------------------------------------------------------

    def me(self) -> Me:
        """The account and key this client calls with, and whether the account can spend now. In an organisation, also
        the person who made the key, their role and teams there, and their own limits there."""
        data, info = self._json("GET", "/v1/me")
        return _answered(Me.from_dict(data or {}), info)

    def regions(self) -> List[Region]:
        """Places a machine can be asked to run near (``create(region=...)``)."""
        data, _ = self._json("GET", "/v1/regions")
        return [Region.from_dict(r) for r in _list_of(data)]

    def gpus(self) -> GpuList:
        """GPUs on offer now, where they are and what an hour costs."""
        data, info = self._json("GET", "/v1/gpus")
        return _answered(GpuList.from_dict(data or {}), info)

    def usage(self, *, month: Union[str, date, None] = None, project: Optional[str] = None) -> Usage:
        """Each machine's running time and cost in a month (``"2026-10"``; this month when left out)."""
        query: List[Tuple[str, str]] = []
        if month is not None:
            query.append(("month", month.strftime("%Y-%m") if isinstance(month, date) else month))
        if project is not None:
            query.append(("project", project))
        data, info = self._json("GET", "/v1/usage", query=query)
        return _answered(Usage.from_dict(data or {}), info)

    def limits(self) -> UsageLimits:
        """Every limit for the month that applies to the account, as you may see it, and how near each is."""
        data, info = self._json("GET", "/v1/usage/limits")
        return _answered(UsageLimits.from_dict(data or {}), info)

    def set_month_limit(self, amount: Optional[float]) -> UsageLimits:
        """Set the account's own limit for the month, in the account's currency, or take it away with None: in your own
        account, with a key that has ``cloud:*``; in an organisation, an owner's."""
        data, info = self._json("PUT", "/v1/usage/limits/account", payload={"month_cap": amount})
        return _answered(UsageLimits.from_dict(data or {}), info)

    # --- team folders --------------------------------------------------------------------------------------------

    def folders(self) -> FolderList:
        """The folders you see: your own (My files) first, then your teams'. ``on`` is False where folders are not set
        up here yet."""
        data, info = self._json("GET", "/v1/folders")
        return _answered(FolderList.from_dict(data or {}), info)

    def list_folder(self, folder: str, path: str = "", *, after: Optional[str] = None, limit: Optional[int] = None) -> FolderListing:
        """One page of what is in a folder of a folder (its top for ``""``): folders first, then files, each by name -
        ``limit`` of them at most (1 to 1000), from the ``next`` of the page before (``after``). ``folder_entries``
        walks them all."""
        query = [("path", path)]
        if after:
            query.append(("after", after))
        if limit is not None:
            query.append(("limit", str(limit)))
        data, info = self._json("GET", "/v1/folders/" + quote(folder, safe="") + "/list", query=query)
        return _answered(FolderListing.from_dict(data or {}), info)

    def folder_entries(self, folder: str, path: str = "", *, limit: Optional[int] = None) -> PageIterator[FolderEntry]:
        """Everything in a folder of a folder, folders first, page after page as it is iterated."""

        def page(after: Optional[str]) -> Tuple[List[FolderEntry], Optional[str]]:
            listing = self.list_folder(folder, path, after=after, limit=limit)
            return listing.entries, listing.next

        return PageIterator(page)

    def read_folder_file(self, folder: str, path: str, *, timeout: Optional[float] = None) -> bytes:
        """A file of a folder, its bytes."""
        answer = self._send("GET", "/v1/folders/" + quote(folder, safe="") + "/files", query=[("path", path)],
                            accept="application/octet-stream", timeout=timeout, buffer=True)
        return answer.content

    def write_folder_file(self, folder: str, path: str, data: FileData, *, content_length: Optional[int] = None,
                          timeout: Optional[float] = None) -> FolderEntry:
        """Writes a file of a folder, in the place of the one there, making the folders on the way: ``data`` is bytes, a
        ``pathlib.Path`` to a file here, or a binary file object (one that cannot seek needs ``content_length``). At most
        5 GB a file; the account keeps 25 GB in its folders for now, the Trash included."""
        answer = self._send("PUT", "/v1/folders/" + quote(folder, safe="") + "/files", query=[("path", path)],
                            body=_file_body(data, content_length), timeout=timeout, buffer=True)
        return FolderEntry.from_dict(json.loads(answer.content or b"{}"))

    def delete_folder_file(self, folder: str, path: str) -> FolderTrash:
        """Puts a file or folder of a folder in its Trash, from which it can be put back for 30 days."""
        data, info = self._json("DELETE", "/v1/folders/" + quote(folder, safe="") + "/files", query=[("path", path)])
        return _answered(FolderTrash.from_dict(data or {}), info)

    def move_folder_item(self, folder: str, src: str, dst: str, *, to_folder: Optional[str] = None) -> None:
        """Moves or renames a file or folder of a folder - into ``to_folder``, another folder you edit, too."""
        payload: Dict[str, Any] = {"from": src, "to": dst}
        if to_folder:
            payload["to_folder"] = to_folder
        self._json("POST", "/v1/folders/" + quote(folder, safe="") + "/move", payload=payload)

    def give_team_folder(self, project: str, folder: str, access: str = "view") -> FolderGrant:
        """Gives a team a folder, to ``view`` or to ``edit``: its full managers, the organisation's owners and admins, or a
        person in their own account."""
        data, info = self._json("PUT", "/v1/projects/" + quote(project, safe="") + "/folders/" + quote(folder, safe=""),
                                payload={"access": access})
        return _answered(FolderGrant.from_dict(data or {}), info)

    def take_team_folder(self, project: str, folder: str) -> None:
        """Takes a folder from a team: its machines lose it at once."""
        self._json("DELETE", "/v1/projects/" + quote(project, safe="") + "/folders/" + quote(folder, safe=""))

    # --- code access ---------------------------------------------------------------------------------------------

    def code(self) -> Code:
        """The GitHub and GitLab the account connects, and the repositories its teams are given."""
        data, info = self._json("GET", "/v1/code")
        return _answered(Code.from_dict(data or {}), info)

    def github_install_url(self) -> str:
        """Where to install Wheelmachine's GitHub App for the account: an owner's or an admin's (good for half an hour)."""
        data, _ = self._json("POST", "/v1/code/github")
        return str((data or {}).get("url", ""))

    def connect_gitlab(self, token: str, address: Optional[str] = None) -> CodeConnection:
        """Connect a GitLab group by its access token (``read_repository``, ``write_repository``); ``address`` is the
        GitLab's https address, gitlab.com when left out. The token is never shown again."""
        payload: Dict[str, Any] = {"token": token}
        if address:
            payload["address"] = address
        data, info = self._json("POST", "/v1/code/gitlab", payload=payload)
        return _answered(CodeConnection.from_dict(data or {}), info)

    def disconnect_code(self, connection: str) -> None:
        """Take a connection away, with every repository a team was given of it."""
        self._json("DELETE", "/v1/code/connections/" + quote(connection, safe=""))

    def code_repos(self, connection: str) -> List[str]:
        """The repositories a connection reaches, by their paths."""
        data, _ = self._json("GET", "/v1/code/connections/" + quote(connection, safe="") + "/repos")
        return [str(r.get("repo", "")) for r in _list_of(data)]

    def team_code(self, project: str) -> List[CodeGrant]:
        """The repositories a team is given."""
        data, _ = self._json("GET", "/v1/projects/" + quote(project, safe="") + "/code")
        return [CodeGrant.from_dict(g) for g in _list_of(data)]

    def grant_code(self, project: str, connection: str, repo: str, access: str = "read") -> CodeGrant:
        """Give a team a repository of a connection, to ``read`` or to ``write`` (push) too: its full managers', owners'
        and admins'."""
        data, info = self._json("PUT", "/v1/projects/" + quote(project, safe="") + "/code",
                                payload={"connection": connection, "repo": repo, "access": access})
        return _answered(CodeGrant.from_dict(data or {}), info)

    def revoke_code(self, project: str, grant: str) -> None:
        """Take a repository from a team."""
        self._json("DELETE", "/v1/projects/" + quote(project, safe="") + "/code/" + quote(grant, safe=""))

    # --- how a call is made ---------------------------------------------------------------------------------------

    def _held(self, seconds: float) -> float:
        """The time a call the API may hold for `seconds` is given: that, and a margin, and never less than usual."""
        return max(self.timeout, seconds + 15.0)

    def _json(
        self,
        method: str,
        path: str,
        *,
        query: Optional[Sequence[Tuple[str, str]]] = None,
        payload: Any = None,
        idempotency_key: Optional[str] = None,
        repeatable: bool = False,
        once: bool = False,
        command: bool = False,
        timeout: Optional[float] = None,
        confirm_code: Optional[str] = None,
    ) -> Tuple[Any, ResponseInfo]:
        """A call with a JSON answer: the answer's JSON (None when it has no body) and how it came."""
        body = None
        if payload is not None:
            raw = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
            body = BytesBody(raw, "application/json")
        answer = self._send(
            method,
            path,
            query=query,
            body=body,
            idempotency_key=idempotency_key,
            repeatable=repeatable,
            once=once,
            command=command,
            timeout=timeout,
            confirm_code=confirm_code,
        )
        info = self._info(answer)
        if not answer.content.strip():
            return None, info
        try:
            return json.loads(answer.content.decode("utf-8")), info
        except ValueError:
            raise WheelmachineError(
                "the answer could not be read as JSON", status=answer.status, request_id=info.request_id
            ) from None

    def _send(
        self,
        method: str,
        path: str,
        *,
        query: Optional[Sequence[Tuple[str, str]]] = None,
        body: Optional[Body] = None,
        idempotency_key: Optional[str] = None,
        repeatable: bool = False,
        once: bool = False,
        command: bool = False,
        timeout: Optional[float] = None,
        accept: str = "application/json",
        buffer: bool = True,
        confirm_code: Optional[str] = None,
    ) -> Answer:
        """One call, tried again when it is safe and worth it. Returns a 2xx or 3xx answer, its body read into
        `content` when `buffer`; raises the error for any other. `repeatable`: a POST that does the same however
        many times it is sent, so it is sent again as a PUT would be. `once`: a call that makes something new each
        time, shown once, so it is never sent again once it may have reached the API, not even when refused for a
        moment; only when it could not go out at all. `command`: a command run in a machine, which may have run
        whatever its answer, unless the answer says it never reached its machine (`not_run`): a 503 is tried again
        only then."""
        target = self._target.prefix + path
        if query:
            target += "?" + urlencode(list(query), quote_via=quote)
        limit = timeout if timeout is not None else self.timeout
        repeatable = not once and (repeatable or method in _REPEATABLE or idempotency_key is not None)
        deadline = time.monotonic() + limit
        attempt = 0
        while True:
            headers = [
                ("User-Agent", USER_AGENT),
                ("Accept", accept),
            ]
            if self._key.value():
                headers.insert(0, ("Authorization", "Bearer " + self._key.value()))
            if idempotency_key is not None:
                headers.append(("Idempotency-Key", idempotency_key))
            if confirm_code is not None:
                headers.append(("X-Confirm-Code", confirm_code))
            opened: Optional[OpenBody] = None
            if body is not None:
                opened = body.open()
                headers.append(("Content-Type", body.content_type))
                headers.append(("Content-Length", str(opened.length)))
            elif method in ("POST", "PUT", "PATCH"):
                headers.append(("Content-Length", "0"))
            try:
                answer = self._transport.send(method, target, headers, opened, limit)
                if buffer and answer.status < 400:
                    answer.content = answer.read()
            except Failure as failure:
                if (
                    attempt < self.max_retries
                    and (repeatable or not failure.sent)
                    and (body is None or body.can_repeat())
                ):
                    self._sleep(self._backoff(attempt))
                    attempt += 1
                    continue
                raise self._no_answer(failure, limit, idempotency_key) from failure.cause
            finally:
                if opened is not None:
                    opened.close()
            if answer.status < 400:
                return answer
            error = self._refusal(answer)
            error.idempotency_key = idempotency_key
            # an answer came, so the call reached the API: one that makes something new each time is not sent again
            pause = None
            if not once:
                pause = self._pause_before_retry(error, attempt, repeatable, idempotency_key, body, deadline, command)
            if pause is None:
                raise error
            if error.status != 409:
                attempt += 1
            self._sleep(pause)

    def _pause_before_retry(
        self,
        error: WheelmachineError,
        attempt: int,
        repeatable: bool,
        idempotency_key: Optional[str],
        body: Optional[Body],
        deadline: float,
        command: bool = False,
    ) -> Optional[float]:
        """How long to wait before trying a refused call again, or None when it is not tried again."""
        if body is not None and not body.can_repeat():
            return None
        after = error.retry_after
        if after is not None and after > LONGEST_RETRY_AFTER_S:
            return None
        status = error.status
        if status == 409:
            # the first call with this Idempotency-Key is still running: its answer is waited for, within the call's
            # own time, without spending a retry
            if idempotency_key is None or after is None or time.monotonic() + after > deadline:
                return None
            return after
        if attempt >= self.max_retries:
            return None
        if status == 429:
            if error.code == "limit_reached" and after is None:
                return None  # a limit of the account's, not of the moment: trying again soon changes nothing
        elif status in (502, 504):
            # the call may have reached the API and run: sent again only when that is safe
            if not repeatable:
                return None
        elif status != 503:
            return None
        elif error.code == "not_open":
            # not open, not busy: waiting does not change it, whatever Retry-After says
            return None
        elif command and not (isinstance(error.body, dict) and error.body.get("not_run") is True):
            # a command may have run: sent again only when the answer says it never reached its machine
            return None
        return after if after is not None else self._backoff(attempt)

    def _backoff(self, attempt: int) -> float:
        """Exponential backoff with full jitter: anywhere from 0 to 0.5 s, 1 s, 2 s... at most 8 s."""
        return min(_BACKOFF_MOST_S, _BACKOFF_FIRST_S * 2.0 ** attempt) * self._jitter()

    def _refusal(self, answer: Answer) -> WheelmachineError:
        problem: Optional[Dict[str, Any]] = None
        try:
            raw = answer.read(1 << 16)
        except Failure:
            raw = b""
        finally:
            answer.close()
        if raw:
            try:
                parsed = json.loads(raw.decode("utf-8"))
                if isinstance(parsed, dict):
                    problem = parsed
            except ValueError:
                problem = None
        return error_from_answer(
            answer.status,
            answer.reason,
            problem,
            answer.header("X-Request-Id"),
            _retry_after(answer.header("Retry-After")),
        )

    def _no_answer(self, failure: Failure, limit: float, idempotency_key: Optional[str]) -> APIConnectionError:
        cause = failure.cause
        error: APIConnectionError
        if is_timeout(cause):
            error = APITimeoutError("no answer from {} within {:g} seconds".format(self.base_url, limit))
        else:
            words = "the connection to {} was lost before its answer".format(self.base_url) if failure.sent else (
                "could not reach {}".format(self.base_url)
            )
            error = APIConnectionError("{}: {}".format(words, str(cause) or type(cause).__name__))
        error.idempotency_key = idempotency_key
        return error

    def _info(self, answer: Answer) -> ResponseInfo:
        return ResponseInfo(
            status=answer.status,
            request_id=answer.header("X-Request-Id"),
            replayed=(answer.header("Idempotent-Replayed") or "").strip().lower() == "true",
            rate_limit=_rate_limit(answer.header("RateLimit"), answer.header("RateLimit-Policy")),
        )

    def _drain(self, answer: Answer, out: BinaryIO) -> int:
        """Writes an answer's body into `out`, a piece at a time; gives how many bytes came."""
        n = 0
        try:
            for piece in answer.chunks():
                out.write(piece)
                n += len(piece)
        except Failure as failure:
            raise self._no_answer(failure, self.timeout, None) from failure.cause
        return n


def _list_of(data: Any) -> List[Dict[str, Any]]:
    got = data.get("data") if isinstance(data, dict) else None
    return [item for item in got if isinstance(item, dict)] if isinstance(got, list) else []
