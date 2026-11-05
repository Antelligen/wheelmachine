"""A stand-in for the Wheelmachine API on 127.0.0.1, in a thread, for the tests: no network.

It answers the way the API does - problem+json errors with a code and the call's id, X-Request-Id and RateLimit
headers, pages of `{object, data, next}` followed with `?after=`, 201/202 on create, Idempotency-Key replays - and
refuses a field it does not know, so a test fails when the library sends something the API would refuse. Faults can be
set on a route: a status, an answer that never comes (before or after the call was acted on), or a slow answer.
"""

from __future__ import annotations

import base64
import hashlib
import io
import json
import math
import re
import secrets
import socket
import threading
import time
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Optional, Tuple
from urllib.parse import parse_qs, unquote, urlsplit

KEY = "ak_live_" + "a" * 40
REVOKED_KEY = "ak_live_" + "r" * 40

TITLES = {
    "invalid_request": "The request is not right",
    "unauthorized": "No key, or a key that does not work",
    "key_revoked": "This key was revoked",
    "forbidden": "This key may not do that",
    "not_found": "Not found",
    "conflict": "Not possible in the state it is in",
    "project_not_empty": "The project still holds machines",
    "payment_required": "Nothing left to spend",
    "rate_limited": "Too many calls just now",
    "limit_reached": "A limit is reached",
    "unavailable": "Not available just now",
    "timeout": "Not in time",
    "not_open": "Not available",
    "no_apps": "This machine has no apps",
    "internal": "Something went wrong on our side",
    "login_denied": "The sign-in was refused",
    "login_expired": "The sign-in's code is no longer good",
}

#: A part of the API's table of AI keys (GET /v1/secrets/presets), as much of it as the tests ask.
PRESETS = [
    {"object": "secret_preset", "id": "anthropic", "title": "Anthropic", "names": ["ANTHROPIC_API_KEY"], "hosts": ["api.anthropic.com"],
     "headers": [{"name": "x-api-key", "format": "{value}"}, {"name": "authorization", "format": "Bearer {value}"}], "query": []},
    {"object": "secret_preset", "id": "gemini", "title": "Gemini", "names": ["GEMINI_API_KEY", "GOOGLE_API_KEY"],
     "hosts": ["generativelanguage.googleapis.com"],
     "headers": [{"name": "x-goog-api-key", "format": "{value}"}, {"name": "authorization", "format": "Bearer {value}"}], "query": ["key"]},
]

CREATES = [
    ("POST", re.compile(r"^/v1/machines$")),
    ("POST", re.compile(r"^/v1/machines/[^/]+/(fork|copy)$")),
    ("POST", re.compile(r"^/v1/machines/[^/]+/(fork-for|hand-off|take-back|give-back)$")),
    ("POST", re.compile(r"^/v1/requests$")),
    ("POST", re.compile(r"^/v1/machines/[^/]+/backups$")),
    ("POST", re.compile(r"^/v1/projects$")),
    ("POST", re.compile(r"^/v1/keys$")),
]

#: the models of the account's AI keys, and the agents of its machines, as GET /v1/models and /v1/agents answer them
MODELS = [
    {"object": "model", "id": "openai/gpt-6.1-sol", "supplier": "openai", "model": "gpt-6.1-sol", "name": "gpt-6.1-sol",
     "created_at": None, "retiring_at": None, "agents": ["codex", "aider"],
     "price": {"input_per_million_usd": 2, "output_per_million_usd": 10, "source": "OpenAI",
               "url": "https://developers.openai.com/api/docs/pricing", "as_of": "2026-11-02"},
     "intelligence": {"score": 1481, "source": "LMArena text leaderboard", "url": "https://lmarena.ai/leaderboard/text",
                      "license": "CC BY 4.0", "as_of": "2026-10-28"}},
    {"object": "model", "id": "deepseek/deepseek-v4-pro", "supplier": "deepseek", "model": "deepseek-v4-pro", "name": "DeepSeek-V4-Pro",
     "created_at": None, "retiring_at": None, "agents": ["aider"],
     "price": {"input_per_million_usd": 1.32, "output_per_million_usd": 3.96, "source": "DeepSeek",
               "url": "https://api-docs.deepseek.com/quick_start/pricing", "as_of": "2026-11-02"},
     "intelligence": None},
]
SUPPLIERS = [
    {"object": "model_supplier", "id": "openai", "title": "OpenAI", "key": "OPENAI_API_KEY", "whose": "account", "state": "listed",
     "listed_at": "2026-10-23T07:00:00Z", "detail": None},
    {"object": "model_supplier", "id": "deepseek", "title": "DeepSeek", "key": "DEEPSEEK_API_KEY", "whose": "account", "state": "unreachable",
     "listed_at": "2026-10-23T06:20:00Z", "detail": "DeepSeek did not answer; this list is from 40 minutes ago."},
]
AGENTS = [
    {"name": "codex", "title": "Codex", "suppliers": [{"id": "openai", "title": "OpenAI", "default": None}]},
    {"name": "aider", "title": "Aider", "suppliers": [{"id": "openai", "title": "OpenAI", "default": "gpt-6.1-sol"},
                                                    {"id": "deepseek", "title": "DeepSeek", "default": None}]},
]

#: the ports a machine uses for itself, which are never opened, and how many one machine may have open
RESERVED_PORTS = frozenset({5900, 6080, 7070, 9222, 9223, 9224})
MOST_PORTS = 20

#: the apps a machine made since apps came carries, as each is when the machine is new
APPS = [
    {"name": "desktop", "title": "Desktop", "kind": "web", "port": 6901, "path": "/vnc.html?autoconnect=1&resize=remote&reconnect=1",
     "on": False, "running": False, "command": None},
    {"name": "editor", "title": "Code editor", "kind": "web", "port": 13337, "path": "/", "on": False, "running": False, "command": None},
    {"name": "openclaw", "title": "OpenClaw", "kind": "web", "port": 18789, "path": "/", "on": False, "running": False, "command": None},
    {"name": "ollama", "title": "Ollama", "kind": "service", "port": None, "path": None, "on": False, "running": False, "command": None},
    {"name": "codex", "title": "Codex", "kind": "agent", "port": None, "path": None, "on": True, "running": False, "command": "codex"},
    {"name": "gemini", "title": "Gemini CLI", "kind": "terminal", "port": None, "path": None, "on": None, "running": None, "command": "gemini"},
]
#: what turning openclaw on answers in `token`: made up, nobody's
APP_TOKEN = "oc" + "0123456789abcdef" * 2


class Seen:
    """A request as it came."""

    def __init__(self, method: str, path: str, query: Dict[str, List[str]], headers: Dict[str, str], body: bytes,
                 rid: str) -> None:
        self.rid = rid
        self.method = method
        self.path = path
        self.query = query
        self.headers = headers
        self.body = body

    def json(self) -> Any:
        return json.loads(self.body) if self.body else None

    def __repr__(self) -> str:
        return "Seen({} {} {})".format(self.method, self.path, self.query)


class Fault:
    def __init__(
        self,
        method: str,
        path: str,
        *,
        status: Optional[int] = None,
        code: Optional[str] = None,
        detail: str = "a fault set by the test",
        retry_after: Optional[float] = None,
        drop: Optional[str] = None,
        delay: float = 0.0,
        raw: Optional[Tuple[str, bytes]] = None,
        times: int = 1,
    ) -> None:
        self.method = method
        self.pattern = re.compile(path)
        self.status = status
        self.code = code
        self.detail = detail
        self.retry_after = retry_after
        self.drop = drop
        self.delay = delay
        self.raw = raw
        self.times = times


class Problem(Exception):
    def __init__(self, status: int, code: str, detail: str, retry_after: Optional[float] = None,
                 confirm: Optional[Dict[str, Any]] = None) -> None:
        super().__init__(detail)
        self.confirm = confirm
        self.status = status
        self.code = code
        self.detail = detail
        self.retry_after = retry_after


def _only(body: Any, allowed: Tuple[str, ...]) -> Dict[str, Any]:
    if body is None:
        return {}
    if not isinstance(body, dict):
        raise Problem(400, "invalid_request", "the body is not JSON")
    for k in body:
        if k not in allowed:
            raise Problem(400, "invalid_request", 'no field "{}" here'.format(k))
    return body


def _port_number(v: Any) -> int:
    if isinstance(v, bool) or not isinstance(v, int) or not 1 <= v <= 65535:
        raise Problem(400, "invalid_request", "a port is a number from 1 to 65535")
    return v


class FakeAPI:
    def __init__(self) -> None:
        self.lock = threading.RLock()
        self.seen: List[Seen] = []
        self.own_limit: Any = None
        self.faults: List[Fault] = []
        self.machines: Dict[str, Dict[str, Any]] = {}
        #: the credit as GET /v1/credit answers it: what is left, and its entries, the newest first; and whether credit
        #: can be bought (POST /v1/credit/purchases)
        self.buy_open = False
        self.credit_usd = 5.0
        self.credit_entries: List[Dict[str, Any]] = [
            {"id": "ce_" + "w" * 26, "object": "credit_entry", "kind": "welcome", "amount_usd": 5, "reason": "Free credit to start with",
             "machine": None, "machine_name": None, "at": "2026-10-23T12:00:00Z", "updated_at": "2026-10-23T12:00:00Z"}]
        self.projects: Dict[str, Dict[str, Any]] = {}
        self.keys: Dict[str, Dict[str, Any]] = {}
        # the code a key with full access needs, as POST /v1/confirm-codes "e-mails" it; None: none is asked
        self.confirm_code: Optional[str] = None
        self.files: Dict[Tuple[str, str], bytes] = {}
        #: My files' files, by path
        self.folder_files: Dict[str, bytes] = {}
        #: every backup taken, the oldest first
        self.backups: List[Dict[str, Any]] = []
        #: by name: the value as it reached the API (never in an answer), and when it was made and set
        self.secrets: Dict[str, Dict[str, str]] = {}
        # the model chosen, by agent ("*": every agent), as GET /v1/agents shows it
        self.chosen: Dict[str, str] = {}
        #: the organisation's lists of the models its people may use, as /v1/models/allowed keeps them; and the one that
        #: holds the key's own choices, which the models listed, the agents and a choice stand by
        self.allowed: List[Dict[str, Any]] = []
        self.held_to: Optional[Dict[str, Any]] = None
        #: each machine's points, by its id, as GET /v1/machines/{id}/metrics answers them
        self.metrics: Dict[str, List[Dict[str, Any]]] = {}
        #: each machine's open ports, by number
        self.ports: Dict[str, Dict[int, Dict[str, Any]]] = {}
        #: every link made, in order (a link's answer that was lost is still one made)
        self.links: List[Dict[str, Any]] = []
        #: each machine's apps, by name, made as APPS the first time they are asked for; and the machines made before
        #: apps came, which have none
        self.apps: Dict[str, Dict[str, Dict[str, Any]]] = {}
        self.old_machines: set = set()
        #: the ports an app opened as it was turned on, by machine and number: the app's name
        self.app_ports: Dict[str, Dict[int, str]] = {}
        self.idem: Dict[str, Tuple[str, int, bytes, Dict[str, str]]] = {}
        self.execs: List[Dict[str, Any]] = []
        self.connections = 0
        #: programs signing in through the browser, by their device code; `decide` says what the person did at each
        #: ask for the key (from 1): "pending", "approved", "denied" or "expired"
        self.logins: Dict[str, Dict[str, Any]] = {}
        self.decide = lambda ask: "approved"
        #: the organisation the one key that works was made in, with the person who made it, their role, teams and
        #: limits there ({"id", "name", "person", "role", "teams", "limits"}); None for a key of a person's own account
        self.org: Optional[Dict[str, Any]] = None
        #: what GET /v1/orgs answers a key of a person's own account
        self.orgs: List[Dict[str, Any]] = []
        #: the account's events, the newest first, as GET /v1/audit pages them and GET /v1/audit/export gives them
        self.audit: List[Dict[str, Any]] = []
        #: the organisation's other people a machine is given to, or asked of, by id ({"id", "name", "email",
        #: "github_login"}); the requests to fork a machine for one of them or hand it off, by id; each team's rules as
        #: set, by its project; and whether a fork for someone or a hand-off waits for a manager - as the rules' Ask has it - or is done at once
        self.people: Dict[str, Dict[str, Any]] = {}
        self.machine_requests: Dict[str, Dict[str, Any]] = {}
        self.rules: Dict[str, Dict[str, Any]] = {}
        self.gives_wait = False
        #: close every connection after its answer without saying so, as a server that times idle connections out does
        self.close_after_answer = False
        self._n = 0
        self.first_project = self.add_project("Default")["id"]
        self.server = _Server(("127.0.0.1", 0), _Handler)
        self.server.api = self  # type: ignore[attr-defined]
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True)

    # --- running it ---

    def start(self) -> "FakeAPI":
        self.thread.start()
        return self

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()

    @property
    def url(self) -> str:
        return "http://127.0.0.1:{}".format(self.server.server_address[1])

    def fault(self, method: str, path: str, **kw: Any) -> Fault:
        f = Fault(method, path, **kw)
        with self.lock:
            self.faults.append(f)
        return f

    def requests(self, method: str, path: str) -> List[Seen]:
        pattern = re.compile(path)
        return [s for s in self.seen if s.method == method and pattern.search(s.path)]

    # --- what it holds ---

    def _next(self) -> int:
        with self.lock:
            self._n += 1
            return self._n

    def add_project(self, name: str) -> Dict[str, Any]:
        n = self._next()
        p = {"id": "prj_%026d" % n, "object": "project", "name": name, "created_at": "2026-10-23T07:00:00Z"}
        self.projects[p["id"]] = p
        return p

    def add_machine(self, name: str = "", project: Optional[str] = None, labels: Optional[Dict[str, str]] = None,
                    state: str = "running") -> Dict[str, Any]:
        n = self._next()
        m: Dict[str, Any] = {
            "id": "m_%026d" % n,
            "object": "machine",
            "name": name,
            "project": project or self.first_project,
            "state": state,
            "gpu": None,
            "size": {"cpus": 2, "memory_gb": 4, "disk_gb": 32},
            "location": {"city": "Helsinki", "country": "FI"},
            "cpus": 2,
            "memory_gb": 4,
            "sleep_when_idle": True,
            "idle_after_s": 300,
            "labels": dict(labels or {}),
            "secrets": [],
            "network": {"internet": "open", "allow": []},
            "files_sealed": True,
            "running_seconds": 1.5,
            "created_at": "2026-10-23T07:00:00Z",
            "last_used_at": None,
            "made_by": self.made_by(),
            "held_by": self.held_by(),
        }
        self.machines[m["id"]] = m
        return m

    def made_by(self) -> Optional[str]:
        """Who what is made now is said to be made by: in an organisation, the person who made the key."""
        return self.org["person"]["id"] if self.org else None

    def held_by(self) -> Optional[Dict[str, Any]]:
        """Whose what is made now is said to be: in an organisation, the person who made the key, with their role."""
        if not self.org:
            return None
        return {"id": self.org["person"]["id"], "name": self.org["person"]["name"], "role": self.org["role"], "level": None}

    def key_view(self, n: int = 1) -> Dict[str, Any]:
        return {
            "id": "key_%016x" % n,
            "object": "key",
            "name": "test",
            "scopes": ["cloud:*"],
            "projects": [],
            "shown": "ak_live_aaaa…aaaa",
            "state": "active",
            "rate_per_minute": None,
            "created_at": "2026-10-23T07:00:00Z",
            "expires_at": None,
            "last_used_at": None,
            "made_by": self.made_by(),
        }

    # --- answering ---

    def handle(self, h: "_Handler") -> None:
        url = urlsplit(h.path)
        path, query = url.path, parse_qs(url.query, keep_blank_values=True)
        headers = {k.lower(): v for k, v in h.headers.items()}
        rid = "req_%06d" % self._next()
        method = h.command

        if "chunked" in headers.get("transfer-encoding", "").lower():
            self.send(h, rid, *self.problem(rid, Problem(411, "invalid_request", "say how long the body is")), close=True)
            return
        length = int(headers.get("content-length") or 0)
        m = re.match(r"^/v1/machines/([^/]+)/files$", path)
        if method == "PUT" and m and m.group(1) not in self.machines:
            # refused before the body is read, as the API does, and the connection closed
            with self.lock:
                self.seen.append(Seen(method, path, query, headers, b"", rid))
            self.send(h, rid, *self.problem(rid, Problem(404, "not_found", "no such machine")), close=True)
            return
        body = h.rfile.read(length) if length else b""
        with self.lock:
            self.seen.append(Seen(method, path, query, headers, body, rid))

        # a program signing in has no key yet
        signing_in = method == "POST" and path in ("/v1/login/devices", "/v1/login/devices/token")
        if not signing_in and headers.get("authorization") != "Bearer " + KEY:
            code = "key_revoked" if headers.get("authorization") == "Bearer " + REVOKED_KEY else "unauthorized"
            self.send(h, rid, *self.problem(rid, Problem(401, code, "this key is not known")))
            return

        fault = None
        with self.lock:
            for f in self.faults:
                if f.times > 0 and f.method == method and f.pattern.search(path):
                    f.times -= 1
                    fault = f
                    break
        if fault is not None:
            if fault.delay:
                time.sleep(fault.delay)
            if fault.drop == "before":
                h.close_connection = True
                return
            if fault.raw is not None:
                ctype, data = fault.raw
                self.send(h, rid, fault.status or 502, data, {"Content-Type": ctype})
                return
            if fault.status is not None:
                self.send(h, rid, *self.problem(rid, Problem(fault.status, fault.code or "conflict", fault.detail, fault.retry_after)))
                return

        status, payload, extra = self.idempotent(method, path, query, headers, body, rid)
        if fault is not None and fault.drop == "after":
            h.close_connection = True  # acted on, and the answer lost
            return
        self.send(h, rid, status, payload, extra)

    def idempotent(self, method: str, path: str, query: Dict[str, List[str]], headers: Dict[str, str], body: bytes,
                   rid: str) -> Tuple[int, Any, Dict[str, str]]:
        key = headers.get("idempotency-key")
        create = any(method == m and p.search(path) for m, p in CREATES)
        if not key or not create:
            return self.route(method, path, query, headers, body, rid)
        digest = hashlib.sha256((method + " " + path + "\n").encode() + body).hexdigest()
        with self.lock:
            kept = self.idem.get(key)
            if kept is not None:
                if kept[0] != digest:
                    return self.problem(rid, Problem(422, "invalid_request", "this Idempotency-Key was used with a different request"))
                extra = dict(kept[3])
                extra["Idempotent-Replayed"] = "true"
                return kept[1], kept[2], extra
            status, payload, extra = self.route(method, path, query, headers, body, rid)
            if status < 500 and status != 429:
                data = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
                self.idem[key] = (digest, status, data, {k: v for k, v in extra.items() if k in ("Location", "Content-Type")})
            return status, payload, extra

    def problem(self, rid: str, p: Problem) -> Tuple[int, Any, Dict[str, str]]:
        doc: Dict[str, Any] = {
            "type": "https://docs.wheelmachine.ai/errors/" + p.code,
            "title": TITLES.get(p.code, ""),
            "status": p.status,
            "detail": p.detail,
            "code": p.code,
            "request_id": rid,
        }
        extra = {"Content-Type": "application/problem+json"}
        if p.retry_after:
            doc["retry_after_s"] = p.retry_after
            extra["Retry-After"] = str(int(math.ceil(p.retry_after)))
        if p.confirm:
            doc["confirm"] = p.confirm
        return p.status, doc, extra

    def send(self, h: "_Handler", rid: str, status: int, payload: Any, extra: Optional[Dict[str, str]] = None,
             close: bool = False) -> None:
        extra = dict(extra or {})
        if isinstance(payload, (bytes, bytearray)):
            data = bytes(payload)
            extra.setdefault("Content-Type", "application/octet-stream")
        elif payload is None:
            data = b""
        else:
            data = json.dumps(payload).encode()
            extra.setdefault("Content-Type", "application/json")
        h.send_response(status)
        h.send_header("X-Request-Id", rid)
        h.send_header("RateLimit-Policy", '"key";q=600;w=60')
        h.send_header("RateLimit", '"key";r=599;t=1')
        for k, v in extra.items():
            h.send_header(k, v)
        if close:
            h.send_header("Connection", "close")
        if status != 204:
            h.send_header("Content-Length", str(len(data)))
        h.end_headers()
        if status != 204:
            h.wfile.write(data)
        h.wfile.flush()
        if close or self.close_after_answer:
            h.close_connection = True

    # --- the routes ---

    def limits_answer(self) -> Dict[str, Any]:
        """GET /v1/usage/limits as the API answers it, with the account's own limit as last set."""
        cap = self.own_limit
        return {"object": "usage_limits", "month": "2026-10", "resets_at": "2026-11-01T00:00:00Z", "alert_percents": [50, 75, 90, 100],
                "account": {"name": "Test", "kind": "person", "month_cap_usd": cap, "in_force_usd": cap, "in_force": "account" if cap is not None else "",
                            "spent_usd": 12.5, "percent": None if not cap else int(12.5 / cap * 100), "limited": cap is not None,
                            "set_by": None, "set_at": None, "wheelmachine": {"month_cap_usd": None, "max_machines": 100, "max_vcpus": None},
                            "changed_by": ["owner"], "you_change": True},
                "teams": [], "people": [], "yours": [] if cap is None else [{"scope": "account", "name": "Test", "limit_usd": cap,
                                                                           "spent_usd": 12.5, "percent": int(12.5 / cap * 100), "reached": False}],
                "passed": []}

    def route(self, method: str, path: str, query: Dict[str, List[str]], headers: Dict[str, str], body: bytes,
              rid: str) -> Tuple[int, Any, Dict[str, str]]:
        try:
            parsed = json.loads(body) if body and headers.get("content-type", "").startswith("application/json") else None
        except ValueError:
            return self.problem(rid, Problem(400, "invalid_request", "the body is not JSON"))
        try:
            with self.lock:
                # (the call's headers, for the routes that read one: a risky step's code)
                self._headers = headers
                status, payload, extra = self._route(method, path, query, parsed, body)
                return status, self._with_ai_keys(payload), extra
        except Problem as p:
            return self.problem(rid, p)

    def _with_ai_keys(self, payload: Any) -> Any:
        """A machine as answered, with the AI keys that reach it as the API says them: an open machine every key on
        every machine, one that reaches only its list those with a host on it, a closed one none."""
        if isinstance(payload, dict) and payload.get("object") == "list" and isinstance(payload.get("data"), list):
            return dict(payload, data=[self._with_ai_keys(x) for x in payload["data"]])
        if not (isinstance(payload, dict) and payload.get("object") == "machine"):
            return payload
        net = payload.get("network") or {"internet": "open", "allow": []}
        keys = []
        for name in sorted(self.secrets):
            kept = self.secrets[name]
            outside = kept.get("outside")
            if not kept.get("every_machine") or outside is None or payload.get("state") == "deleted":
                continue
            allow = net.get("allow") or []
            if net.get("internet") == "closed":
                continue
            if net.get("internet") == "allowed" and not any(h == a or a.startswith("*.") and (h == a[2:] or h.endswith(a[1:]))
                                                             for h in outside["hosts"] for a in allow):
                continue
            keys.append(name)
        return dict(payload, ai_keys=keys)

    def _agents(self) -> Dict[str, Any]:
        """The agents as the API shows them with the choices made: an agent's own, then the one for every agent where it
        can use its provider, else its default with its first provider's key."""
        out = []
        for a in AGENTS:
            first = a["suppliers"][0]
            v = dict(a, object="agent", model=None, chosen_by=None, note=None,
                     starts_on=first["id"] + "/" + first["default"] if first["default"] else None,
                     state="default" if first["default"] else "own")
            held = self.held_to["models"] if self.held_to else None
            for who, by in ((a["name"], "you"), ("*", "every_agent")):
                model = self.chosen.get(who)
                if model and any(x["id"] == model.split("/")[0] for x in a["suppliers"]) and (held is None or model in held):
                    v.update(model=model, chosen_by=by, starts_on=model, state="chosen")
                    break
            else:
                if held is not None:
                    # limited to a list, with no choice it holds: the list's first model it can use, else none
                    first = next((m for m in held if any(x["id"] == m.split("/")[0] for x in a["suppliers"])), None)
                    if first:
                        v.update(model=first, chosen_by="allowed", starts_on=first, state="chosen")
                    else:
                        v.update(state="not_allowed", note="None of the models you may use works with " + a["title"] + ".")
            out.append(v)
        every = self.chosen.get("*")
        if every and self.held_to and every not in self.held_to["models"]:
            every = None
        return {"object": "list", "data": out, "every_agent": {"model": every, "chosen_by": "you"} if every else None,
                "allowed": self.held_to}

    def _machine(self, mid: str) -> Dict[str, Any]:
        m = self.machines.get(mid)
        if m is None:
            raise Problem(404, "not_found", "no such machine")
        return m

    def _route(self, method: str, path: str, q: Dict[str, List[str]], body: Any, raw: bytes) -> Tuple[int, Any, Dict[str, str]]:

        def one(k: str) -> Optional[str]:
            got = q.get(k)
            return got[0] if got else None

        if (method, path) == ("POST", "/v1/login/devices"):
            asked = _only(body, ("name", "scopes", "account"))
            if "account" in asked and not re.fullmatch(r"acc_[0-9a-z]{26}", str(asked["account"])):
                raise Problem(400, "invalid_request", "account is an organisation's id, acc_ and 26 letters and digits")
            device = "wmd_" + "d" * 40 + str(len(self.logins))
            self.logins[device] = {"name": asked.get("name") or "A program", "scopes": asked.get("scopes") or ["cloud:*"], "asks": 0,
                                   "done": False}
            return 200, {"object": "device_login_start", "device_code": device, "user_code": "BCDF-GHJK",
                         "verification_uri": self.url + "/device", "expires_in": 600, "expires_at": "2026-10-23T12:10:00Z", "interval": 5}, {}
        if (method, path) == ("POST", "/v1/login/devices/token"):
            login = self.logins.get(_only(body, ("device_code",)).get("device_code") or "")
            if login is None:
                raise Problem(404, "not_found", "no such sign-in: start again")
            if login["done"]:
                raise Problem(410, "login_expired", "this sign-in was used already: start again")
            login["asks"] += 1
            said = self.decide(login["asks"])
            if said == "pending":
                return 202, {"object": "device_login", "state": "pending", "interval": 5}, {}
            if said == "denied":
                raise Problem(403, "login_denied", "the sign-in was refused at the dashboard")
            if said == "expired":
                raise Problem(410, "login_expired", "this sign-in's code expired: start again")
            if said == "slow":
                raise Problem(429, "rate_limited", "ask once every 5 seconds", 5)
            if said == "full":
                login["done"] = True
                raise Problem(429, "limit_reached", "an account holds at most 50 API keys; revoke one first")
            login["done"] = True
            made = dict(self.key_view(len(self.keys) + 2), name=login["name"], scopes=login["scopes"], secret=KEY)
            return 201, made, {}
        if (method, path) == ("GET", "/v1/me"):
            me = {"object": "me", "account": {"id": "acct_test", "kind": "person", "name": "Test Person"},
                  "person": {"id": "acct_test", "name": "Test Person", "email": None, "github_login": None}, "role": "owner",
                  "teams": None, "limits": None, "scopes": ["cloud:*"], "key": self.key_view(),
                  "standing": {"can_spend": True, "balance_usd": 10.0, "credit_usd": self.credit_usd}, "levels": {}}
            if self.org:
                me.update(account={"id": self.org["id"], "kind": "org", "name": self.org["name"]}, person=self.org["person"],
                          role=self.org["role"], teams=self.org["teams"], limits=self.org["limits"], levels=self.org.get("levels", {}))
            return 200, me, {}
        if (method, path) == ("GET", "/v1/orgs"):
            if self.org:
                raise Problem(403, "forbidden", "an organisation's key does not list the other organisations of the person who made it")
            return 200, {"object": "list", "data": self.orgs}, {}
        if (method, path) in (("GET", "/v1/audit"), ("GET", "/v1/audit/export")):
            return self._audit(path.endswith("/export"), q)
        if (method, path) == ("GET", "/v1/regions"):
            return 200, {"object": "list", "data": [{"id": "mumbai", "city": "Mumbai", "country": "IN"},
                                                    {"id": "helsinki", "city": "Helsinki", "country": "FI"}]}, {}
        if (method, path) == ("GET", "/v1/gpus"):
            return 200, {"object": "list", "for_sale": False, "data": [{
                "id": "h100x1", "model": "H100 80GB", "count": 1, "memory_gb": 80, "cpus": 16, "ram_gb": 128.0,
                "disk_gb": 500.0, "location": {"city": "Helsinki", "country": "FI"}, "price_per_hour_usd": 2.53,
                "interruptible": False, "available": True}]}, {}
        if (method, path) == ("GET", "/v1/credit"):
            limit, after = self._page(q)
            start = int(after) if after else 0
            page = self.credit_entries[start:start + limit]
            nxt = str(start + limit) if start + limit < len(self.credit_entries) else None
            buy = ({"open": True, "amounts_usd": [10, 25, 50, 100], "paddle": {"environment": "sandbox", "client_token": "test_madeup0123"}}
                   if self.buy_open else {"open": False, "amounts_usd": [], "paddle": None})
            return 200, {"object": "credit", "account": "acct_test", "balance_usd": self.credit_usd, "empty": self.credit_usd <= 0,
                         "buy": buy, "data": page, "next": nxt}, {}
        if (method, path) == ("POST", "/v1/credit/purchases"):
            b = _only(body, ("amount_usd",))
            if not self.buy_open:
                raise Problem(501, "not_open", "buying credit is not open yet")
            if b.get("amount_usd") not in (10, 25, 50, 100):
                raise Problem(400, "invalid_request", "amount_usd is 10, 25, 50 or 100")
            prices = [{"amount_usd": a, "price_id": "pri_" + str(a).rjust(26, "0")} for a in (10, 25, 50, 100)]
            return 201, {"object": "credit_purchase", "id": "cp_" + "t" * 26, "account": "acct_test", "amount_usd": b["amount_usd"],
                         "checkout": {"provider": "paddle", "environment": "sandbox", "client_token": "test_madeup0123",
                                      "transaction_id": None, "price_id": "pri_" + str(b["amount_usd"]).rjust(26, "0"), "prices": prices,
                                      "custom_data": {"wheelmachine_account": "acct_test", "wheelmachine_purchase": "cp_" + "t" * 26},
                                      "url": "https://app.wheelmachine.ai/billing?purchase=cp_" + "t" * 26},
                         "created_at": "2026-11-02T09:30:00Z"}, {}
        if (method, path) == ("GET", "/v1/folders"):
            return 200, {"object": "list", "on": True, "file_most_bytes": 5000000000, "data": [{"object": "shared_folder", "id": "fld_own", "kind": "own",
                "name": "My files", "person": None, "left_at": None, "goes_at": None, "access": "edit", "teams": [],
                "bytes": sum(len(b) for b in self.folder_files.values()), "changed_at": None, "made_at": "2026-11-04T10:00:00Z"}]}, {}
        fm = re.match(r"^/v1/folders/fld_own/(list|files|move)$", path)
        if fm:
            where = (q.get("path") or [""])[0]
            if fm.group(1) == "files" and method == "PUT":
                self.folder_files[where] = raw
                return 200, {"name": where.rsplit("/", 1)[-1], "type": "file", "size": len(raw), "changed_at": "2026-11-04T10:00:00Z",
                             "changed_by": {"id": "acc_1", "name": "Ada"}}, {}
            if fm.group(1) == "files" and method in ("GET", "DELETE"):
                if where not in self.folder_files:
                    raise Problem(404, "not_found", "no file is at " + where)
                if method == "GET":
                    return 200, self.folder_files[where], {"Content-Type": "application/octet-stream"}
                del self.folder_files[where]
                return 200, {"object": "shared_trash", "id": "ftr_1", "folder": "fld_own", "path": where, "type": "file", "bytes": 0,
                             "deleted_at": "2026-11-04T10:00:00Z", "deleted_by": None, "goes_at": "2026-12-04T10:00:00Z"}, {}
            if fm.group(1) == "list":
                # a page of `limit` entries at most, from the place in the listing `after` says
                entries = [{"name": k, "type": "file", "size": len(v), "changed_at": "2026-11-04T10:00:00Z", "changed_by": None}
                           for k, v in sorted(self.folder_files.items())]
                start = int((q.get("after") or ["0"])[0])
                end = start + int((q.get("limit") or [str(len(entries) or 1)])[0])
                return 200, {"object": "shared_listing", "folder": "fld_own", "path": where, "entries": entries[start:end],
                             "next": str(end) if end < len(entries) else None}, {}
            if fm.group(1) == "move":
                b = body or {}
                self.folder_files[b["to"]] = self.folder_files.pop(b["from"])
                return 200, {"object": "shared_moved", "folder": "fld_own", "path": b["to"]}, {}
        if (method, path) == ("GET", "/v1/code"):
            return 200, {"object": "code", "github": True, "connections": [{"object": "code_connection", "id": "code_1", "provider": "github",
                "host": "github.com", "name": "acme", "suspended": False, "added_at": "2026-11-03T10:00:00Z"}], "grants": []}, {}
        if (method, path) == ("GET", "/v1/code/connections/code_1/repos"):
            return 200, {"object": "list", "data": [{"object": "code_repo", "repo": "acme/api", "host": "github.com", "connection": "code_1"}]}, {}
        if method == "PUT" and path.startswith("/v1/projects/") and path.endswith("/code"):
            b = body or {}
            return 200, {"object": "code_grant", "id": "cg_1", "project": path.split("/")[3], "connection": b.get("connection"), "host": "github.com",
                         "repo": b.get("repo"), "access": b.get("access"), "granted_at": "2026-11-03T10:00:00Z"}, {}
        if (method, path) == ("GET", "/v1/usage/limits"):
            return 200, self.limits_answer(), {}
        if (method, path) == ("PUT", "/v1/usage/limits/account"):
            usd = (body or {}).get("month_cap_usd")
            self.own_limit = usd
            return 200, self.limits_answer(), {}
        if (method, path) == ("GET", "/v1/usage"):
            month = one("month") or "2026-10"
            lines = [{"machine": mid, "name": m["name"], "project": m["project"], "made_by": m.get("made_by"),
                      "held_by": m.get("held_by"), "running_seconds": 60.0, "usd": 0.01}
                     for mid, m in self.machines.items() if one("project") in (None, m["project"])]
            return 200, {"object": "usage", "month": month, "machines": lines,
                         "total_usd": round(sum(x["usd"] for x in lines), 4)}, {}

        # projects
        if (method, path) == ("GET", "/v1/projects"):
            limit, after = self._page(q)
            rows = sorted((p for p in self.projects.values() if after is None or p["id"] > after), key=lambda p: p["id"])
            page = rows[:limit]
            nxt = page[-1]["id"] if len(rows) > limit else None
            return 200, {"object": "list", "data": page, "next": nxt}, {}
        if (method, path) == ("POST", "/v1/projects"):
            b = _only(body, ("name",))
            if not b.get("name"):
                raise Problem(400, "invalid_request", "a project needs a name")
            p = self.add_project(b["name"])
            return 201, p, {"Location": "/v1/projects/" + p["id"]}
        found = re.match(r"^/v1/projects/([^/]+)$", path)
        if found:
            proj = self.projects.get(found.group(1))
            if proj is None:
                raise Problem(404, "not_found", "no such project")
            if method == "GET":
                return 200, proj, {}
            if method == "PATCH":
                b = _only(body, ("name",))
                proj["name"] = b.get("name") or proj["name"]
                return 200, proj, {}
            if method == "DELETE":
                if any(x["project"] == proj["id"] and x["state"] != "deleted" for x in self.machines.values()):
                    raise Problem(409, "project_not_empty", "delete its machines first")
                del self.projects[proj["id"]]
                return 204, None, {}

        found = re.match(r"^/v1/projects/([^/]+)/rules$", path)
        if found and method == "GET":
            self._in_org()
            proj = self.projects.get(found.group(1))
            if proj is None:
                raise Problem(404, "not_found", "no such project")
            return 200, self._rules(proj["id"]), {}

        # the requests to fork a machine for someone, or hand it off
        if (method, path) == ("GET", "/v1/requests"):
            self._in_org()
            limit, after = self._page(q)
            rows = sorted(self.machine_requests.values(), key=lambda r: r["id"], reverse=True)
            if one("open") == "true":
                rows = [r for r in rows if r["state"] in ("waiting", "approved")]
            if one("machine"):
                rows = [r for r in rows if (r["machine"] or {}).get("id") == one("machine") or r["result"] == one("machine")]
            if after is not None:
                rows = [r for r in rows if r["id"] < after]
            page = rows[:limit]
            nxt = page[-1]["id"] if len(rows) > limit else None
            return 200, {"object": "list", "data": page, "next": nxt}, {}
        if (method, path) == ("POST", "/v1/requests"):
            self._in_org()
            b = _only(body, ("kind", "of", "team", "why"))
            if b.get("kind") not in ("fork", "hand_off"):
                raise Problem(400, "invalid_request", "kind is fork or hand_off")
            holder = self._teammate(b.get("of"))
            me = self.org["person"]  # type: ignore[index]
            for r in self.machine_requests.values():
                if r["state"] == "waiting" and r["way"] == "ask" and r["kind"] == b["kind"] and r["holder"]["id"] == holder["id"]:
                    return 200, r, {}
            q_ = self._request(b["kind"], "ask", None, holder, me, b.get("why", ""))
            # (made with a key: the OK of the person who made the key first, in the dashboard - approve() here)
            q_["waiting_on"] = {"who": "asker", "name": me["name"]}
            return 201, q_, {"Location": "/v1/requests/" + q_["id"]}
        found = re.match(r"^/v1/requests/([^/]+)$", path)
        if found:
            self._in_org()
            r = self.machine_requests.get(found.group(1))
            if r is None:
                raise Problem(404, "not_found", "no such request")
            if method == "GET":
                return 200, r, {}
            if method == "DELETE":
                if r["asked_by"]["id"] != self.org["person"]["id"]:  # type: ignore[index]
                    raise Problem(403, "forbidden", "Only {} takes it back".format(r["asked_by"]["name"]))
                if r["state"] == "waiting":
                    r.update(state="taken_back", waiting_on=None, decided_by=dict(r["asked_by"]))
                elif r["state"] != "taken_back":
                    raise Problem(409, "conflict", "Decided already")
                return 200, r, {}

        # keys
        if (method, path) == ("GET", "/v1/keys"):
            return 200, {"object": "list", "data": list(self.keys.values())}, {}
        if (method, path) == ("POST", "/v1/confirm-codes"):
            b = _only(body, ("step", "org", "to", "account"))
            return 201, dict(b, object="confirm_code", expires_at="2026-11-04T15:30:00Z", sent_to="a…@example.com"), {}
        if (method, path) == ("POST", "/v1/keys"):
            b = _only(body, ("name", "scopes", "projects", "expires_at", "rate_per_minute"))
            if self.confirm_code and "cloud:*" in (b.get("scopes") or []):
                ask = {"step": "full_access_key", "account": "acc_01jb2xk3n4p5q6r7s8t9v0w1x2"}
                said = self._headers.get("x-confirm-code")
                if not said:
                    raise Problem(403, "confirm_required", "this needs a code we e-mail you", confirm=ask)
                if said != self.confirm_code:
                    raise Problem(403, "confirm_wrong", "that code is not right: 4 tries left", confirm=ask)
            n = self._next()
            k = self.key_view(n)
            k.update(name=b.get("name"), scopes=b.get("scopes") or [], projects=b.get("projects") or [],
                     expires_at=b.get("expires_at"), rate_per_minute=b.get("rate_per_minute"))
            self.keys[k["id"]] = k
            return 201, dict(k, secret="ak_live_" + "s" * 40), {"Location": "/v1/keys/" + k["id"]}
        found = re.match(r"^/v1/keys/([^/]+)$", path)
        if found and method == "DELETE":
            if self.keys.pop(found.group(1), None) is None:
                raise Problem(404, "not_found", "no such key")
            return 204, None, {}

        # the models of the keys, and the model each agent starts on
        if (method, path) == ("GET", "/v1/models"):
            agent = next((a for a in AGENTS if a["name"] == one("agent")), None)
            if one("agent") is not None and agent is None:
                raise Problem(404, "not_found", "no such agent")
            providers = [x["id"] for x in agent["suppliers"]] if agent else None
            held = self.held_to["models"] if self.held_to else None
            return 200, {"object": "list", "data": [m for m in MODELS if (providers is None or m["supplier"] in providers) and (held is None or m["id"] in held)],
                         "suppliers": [x for x in SUPPLIERS if providers is None or x["id"] in providers], "allowed": self.held_to}, {}
        # the organisation's lists of the models its people may use: its owners' and admins'
        if path == "/v1/models/allowed" or path.startswith("/v1/models/allowed/"):
            if not self.org:
                raise Problem(409, "conflict", "lists of the models people may use are an organisation's: a person's own account keeps none")
            if self.org["role"] not in ("owner", "admin"):
                raise Problem(403, "forbidden", "the models people may use are set by the organisation's owners and admins")
            if (method, path) == ("GET", "/v1/models/allowed"):
                return 200, {"object": "list", "data": self.allowed}, {}
            found = re.match(r"^/v1/models/allowed/(?:(organisation)|(teams|roles|people)/([^/]+))$", path)
            if found:
                level = {"teams": "team", "roles": "role", "people": "person"}.get(found.group(2) or "", "organisation")
                lid = self.org["id"] if level == "organisation" else unquote(found.group(3))
                if level == "role" and lid not in ("member", "approver", "manager", "full"):
                    raise Problem(404, "not_found", "no such role")
                self.allowed = [x for x in self.allowed if (x["level"], x["id"]) != (level, lid)]
                if method == "DELETE":
                    return 204, None, {}
                if method == "PUT":
                    models = _only(body, ("models",)).get("models")
                    if not isinstance(models, list) or not 1 <= len(models) <= 10:
                        raise Problem(400, "invalid_request", "name the models people may use in `models`, one to 10")
                    if any(not any(m["id"] == x for m in MODELS) for x in models):
                        raise Problem(400, "invalid_request", "the provider does not list that model for the organisation's key")
                    name = {"organisation": self.org["name"], "role": {"member": "Members", "approver": "Approvers", "manager": "Team managers",
                            "full": "Full managers"}.get(lid, "")}.get(level, "")
                    row = {"object": "allowed_models", "level": level, "id": lid, "name": name, "models": list(models),
                           "set_by": {"id": self.org["person"]["id"], "name": self.org["person"]["name"]}, "set_at": "2026-11-02T09:00:00Z"}
                    self.allowed.append(row)
                    return 200, row, {}
        if (method, path) == ("GET", "/v1/agents"):
            return 200, self._agents(), {}
        found = re.match(r"^/v1/agents/(?:([^/]+)/)?model$", path)
        if found:
            who = found.group(1) or "*"
            agent = next((a for a in AGENTS if a["name"] == who), None)
            if who != "*" and agent is None:
                raise Problem(404, "not_found", "no such agent")
            if method == "PUT":
                model = _only(body, ("model",)).get("model")
                if not any(m["id"] == model for m in MODELS):
                    raise Problem(400, "invalid_request", "the provider does not list that model for your key")
                if self.held_to and model not in self.held_to["models"]:
                    raise Problem(403, "forbidden", str(model) + " is not one of the models you may use: " + ", ".join(self.held_to["models"]))
                if agent is not None and not any(x["id"] == str(model).split("/")[0] for x in agent["suppliers"]):
                    raise Problem(400, "invalid_request", agent["title"] + " cannot use that provider's models")
                if who == "*":
                    self.chosen = {}
                self.chosen[who] = str(model)
                listed = self._agents()
                return 200, (listed if who == "*" else next(a for a in listed["data"] if a["name"] == who)), {}
            if method == "DELETE":
                self.chosen.pop(who, None)
                return 204, None, {}

        # secrets: never a value in an answer
        if (method, path) == ("GET", "/v1/secrets/presets"):
            return 200, {"object": "list", "data": PRESETS}, {}
        if (method, path) == ("GET", "/v1/secrets"):
            return 200, {"object": "list", "data": [self._secret_view(n) for n in sorted(self.secrets)]}, {}
        found = re.match(r"^/v1/secrets/([^/]+)$", path)
        if found:
            name = found.group(1)
            if not re.fullmatch(r"[A-Z_][A-Z0-9_]{0,63}", name):
                raise Problem(400, "invalid_request", "a secret's name is capitals, digits and _")
            if method == "PUT":
                b = _only(body, ("value", "outside", "every_machine"))
                value = b.get("value")
                if not isinstance(value, str) or not value or len(value.encode()) > 32768:
                    raise Problem(400, "invalid_request", "give the secret's value in `value`, at most 32 KiB")
                outside, every = self._kept_as(name, b)
                made = name not in self.secrets
                kept = self.secrets.setdefault(name, {"created_at": "2026-10-23T07:00:00Z"})
                kept.update(value=value, updated_at="2026-10-23T07:0%d:00Z" % min(self._next() % 10, 9), outside=outside,
                            every_machine=every)
                return (201 if made else 200), self._secret_view(name), {}
            if method == "DELETE":
                if self.secrets.pop(name, None) is None:
                    raise Problem(404, "not_found", "no such secret")
                for m in self.machines.values():
                    m["secrets"] = [n for n in m["secrets"] if n != name]
                return 204, None, {}

        # machines
        if (method, path) == ("GET", "/v1/machines"):
            limit, after = self._page(q)
            rows = sorted(self.machines.values(), key=lambda x: x["id"], reverse=True)
            rows = [x for x in rows if x["state"] != "deleted"]
            if one("project"):
                rows = [x for x in rows if x["project"] == one("project")]
            if one("state"):
                rows = [x for x in rows if x["state"] == one("state")]
            for label in q.get("label", []):
                name, _, value = label.partition("=")
                rows = [x for x in rows if x["labels"].get(name) == value]
            if after is not None:
                rows = [x for x in rows if x["id"] < after]
            page = rows[:limit]
            nxt = page[-1]["id"] if len(rows) > limit else None
            return 200, {"object": "list", "data": page, "next": nxt}, {}
        if (method, path) == ("POST", "/v1/machines"):
            b = _only(body, ("name", "project", "region", "gpu", "cpus", "labels", "secrets", "network", "sleep_when_idle", "idle_after_s", "wait_s"))
            secrets = self._secret_names(b.get("secrets"))
            policy = self._policy(b.get("network"))
            if b.get("cpus") == 16:
                raise Problem(400, "invalid_request", "16 CPUs are not offered yet: choose 2, 4 or 8")
            if b.get("cpus") is not None and b["cpus"] not in (2, 4, 8):
                raise Problem(400, "invalid_request", "cpus is 2, 4 or 8")
            starting = b.get("wait_s") == 0
            mach = self.add_machine(b.get("name") or "", b.get("project"), b.get("labels"), "starting" if starting else "running")
            mach["secrets"] = secrets or []
            if policy is not None:
                mach["network"] = policy
            if b.get("cpus") is not None:
                mach["cpus"], mach["memory_gb"] = b["cpus"], 2 * b["cpus"]
                mach["size"] = {"cpus": b["cpus"], "memory_gb": 2 * b["cpus"], "disk_gb": 32}
            if b.get("sleep_when_idle") is False:
                mach["sleep_when_idle"], mach["idle_after_s"] = False, None
            elif b.get("idle_after_s") is not None:
                mach["idle_after_s"] = b["idle_after_s"]
            made = dict(mach)
            if not starting:
                made["ready_ms"] = 12.5
            return (202 if starting else 201), made, {"Location": "/v1/machines/" + mach["id"]}
        if (method, path) == ("GET", "/v1/backups"):
            return 200, {"object": "list", "data": self._backups(q)}, {}

        found = re.match(r"^/v1/machines/([^/]+)/(fork-for|hand-off)$", path)
        if found and method == "POST":
            return self._give(self._machine(found.group(1)), "fork" if found.group(2) == "fork-for" else "hand_off", body)

        found = re.match(r"^/v1/machines/([^/]+)/(take-back|give-back)$", path)
        if found and method == "POST":
            return self._back(self._machine(found.group(1)), found.group(2) == "take-back", body)

        found = re.match(r"^/v1/machines/([^/]+)/apps(?:/([^/]+)/(on|off|link|show))?$", path)
        if found:
            return self._apps(method, self._machine(found.group(1)), found.group(2), found.group(3), body)

        found = re.match(r"^/v1/machines/([^/]+)/ports(?:/([^/]+)(/link)?)?$", path)
        if found:
            return self._ports(method, self._machine(found.group(1)), found.group(2), found.group(3) is not None, body)

        found = re.match(r"^/v1/machines/([^/]+)(?:/([a-z]+))?$", path)
        if not found:
            raise Problem(404, "not_found", "no such route")
        mid, action = found.group(1), found.group(2)
        mach = self._machine(mid)
        if action is None:
            if method == "GET":
                return 200, mach, {}
            if method == "PATCH":
                b = _only(body, ("name", "labels", "secrets", "network"))
                if "secrets" in b:
                    mach["secrets"] = self._secret_names(b["secrets"])
                if b.get("network") is not None:
                    mach["network"] = self._policy(b["network"])
                if "name" in b:
                    mach["name"] = b["name"]
                for k, v in (b.get("labels") or {}).items():
                    if v is None:
                        mach["labels"].pop(k, None)
                    else:
                        mach["labels"][k] = v
                return 200, mach, {}
            if method == "DELETE":
                mach["state"] = "deleted"
                return 204, None, {}
        if method == "GET" and action == "backups":
            # (a deleted machine has none)
            return 200, {"object": "list", "data": self._backups(q, mid)}, {}
        if mach["state"] == "deleted":
            raise Problem(404, "not_found", "no such machine")
        if method == "POST" and action in ("start", "pause", "stop", "archive"):
            _only(body, ("wait_s",) if action == "start" else ())
            mach["state"] = {"start": "running", "pause": "paused", "stop": "stopped", "archive": "archived"}[action]
            return 200, mach, {}
        if method == "POST" and action in ("fork", "copy"):
            b = _only(body, ("name", "labels", "backup", "cpus", "wait_s"))
            backup = self._fork_from(mach, b)
            if self.org and self.org["role"] == "member" and self.org.get("levels", {}).get(mach["project"]) != "full":
                return self._own_fork(mach, backup, b.get("cpus"))
            cp = self.add_machine(b.get("name", mach["name"]), mach["project"], b.get("labels", mach["labels"]))
            cp["secrets"] = list(mach["secrets"])
            # the original's size, or the one asked of a fork from a backup
            cpus = b.get("cpus") or mach["cpus"]
            cp.update(cpus=cpus, memory_gb=2 * cpus, size={"cpus": cpus, "memory_gb": 2 * cpus, "disk_gb": 32})
            return 201, dict(cp, ready_ms=3.0), {"Location": "/v1/machines/" + cp["id"]}
        if method == "POST" and action == "backups":
            b = _only(body, ("note",))
            made = {"id": "20261023t070000z-%d" % self._next(), "object": "backup", "machine": mid, "note": b.get("note", ""),
                    "created_at": "2026-10-23T07:00:00Z"}
            self.backups.append(made)
            return 201, made, {}
        if method == "GET" and action == "metrics":
            step = one("step") or "60"
            if step not in ("60", "300", "3600"):
                raise Problem(400, "invalid_request", "step is whole minutes from 1 to 60")
            points = self.metrics.get(mid, [])
            return 200, {"object": "metrics", "machine": mid, "step_s": int(step), "cpus": mach["cpus"],
                         "kept_since": points[0]["at"] if points else None, "points": points}, {}
        if method == "GET" and action == "wait":
            want = one("state")
            if want == "running" and mach["state"] == "starting":
                mach["state"] = "running"
            if mach["state"] == want:
                return 200, mach, {}
            raise Problem(408, "timeout", "the machine is {}, not {}".format(mach["state"], want))
        if method == "POST" and action == "exec":
            b = _only(body, ("command", "shell", "env", "cwd", "user", "binary_output"))
            if bool(b.get("command")) == bool(b.get("shell")):
                raise Problem(400, "invalid_request", "give `command` or `shell`, not both")
            self.execs.append(b)
            argv = b.get("command") or ["/bin/sh", "-lc", b["shell"]]
            mach["state"] = "running"
            out: Dict[str, Any] = {"exit_code": 0, "stderr": "", "duration_ms": 3.2, "truncated": False}
            if argv[0] == "exit":
                out.update(exit_code=int(argv[1]), stderr="bye\n")
            text = " ".join(argv[1:]) + "\n" if argv[0] == "echo" else "ran {}\n".format(argv)
            if b.get("binary_output"):
                out["stdout_base64"] = base64.b64encode(b"\x00\x01\xffbinary").decode()
            else:
                out["stdout"] = text
            return 200, out, {}
        if action == "files":
            where = one("path") or ""
            if not re.match(r"^(home|work)/.+", where):
                raise Problem(400, "invalid_request", "a path begins with home/ or work/")
            if method == "PUT":
                self.files[(mid, where)] = raw
                return 204, None, {}
            if method == "GET":
                if (mid, where) not in self.files:
                    raise Problem(404, "not_found", "no such file in the machine")
                return 200, self.files[(mid, where)], {"Content-Type": "application/octet-stream"}
        raise Problem(404, "not_found", "no such route")

    def _in_org(self) -> None:
        if not self.org:
            raise Problem(409, "conflict", "Only an organisation's machines are handed off")

    def _teammate(self, who: Any) -> Dict[str, Any]:
        """A person of the organisation by their id, e-mail address or GitHub login, as the API finds them."""
        for p in list(self.people.values()) + ([self.org["person"]] if self.org else []):
            if who and who in (p["id"], p.get("email"), p.get("github_login")):
                return {"id": p["id"], "name": p["name"]}
        raise Problem(404, "not_found", "no such teammate")

    def _rules(self, project: str) -> Dict[str, Any]:
        rules = {"object": "team_rules", "project": project, "hand_offs": "ask", "forks": "ask", "gpus": True, "open_internet": True,
                 "month_budget_usd": None, "spent_usd": 0.0, "open_machines": 0, "models": "any", "set_by": None, "set_at": None}
        rules.update(self.rules.get(project, {}))
        return rules

    def _request(self, kind: str, way: str, mach: Optional[Dict[str, Any]], holder: Dict[str, Any], receiver: Dict[str, Any],
                 why: str) -> Dict[str, Any]:
        me = self.org["person"]  # type: ignore[index]
        q = {"id": "rq_%026d" % self._next(), "object": "request", "kind": kind, "way": way,
             "machine": {"id": mach["id"], "name": mach["name"]} if mach else None,
             "holder": {"id": holder["id"], "name": holder["name"]}, "receiver": {"id": receiver["id"], "name": receiver["name"]},
             "asked_by": {"id": me["id"], "name": me["name"]}, "from_team": mach["project"] if mach else None, "to_team": None,
             "gpu": False, "why": why, "state": "waiting", "waiting_on": None, "decided_by": None, "decision_why": "", "result": None,
             "backup": None, "cpus": None, "failure": None, "restarted": False, "keys_revoked": 0, "created_at": "2026-10-27T07:00:00Z",
             "expires_at": "2026-10-28T07:00:00Z", "done_at": None}
        self.machine_requests[q["id"]] = q
        return q

    def _give(self, mach: Dict[str, Any], kind: str, body: Any) -> Tuple[int, Any, Dict[str, str]]:
        """A machine forked for a teammate, or handed off to them, with a key: waiting on the OK of the person who made the key (approve()),
        then done, or waiting on a manager when gives wait; refused where the team's rules say Off, and for the person
        who holds it already."""
        self._in_org()
        b = _only(body, ("to", "team", "why"))
        if not b.get("to"):
            raise Problem(400, "invalid_request", "to is the teammate: their id, e-mail address or GitHub login")
        receiver = self._teammate(b["to"])
        holder = mach.get("held_by") or {}
        if receiver["id"] == holder.get("id"):
            raise Problem(409, "conflict", "it is theirs already")
        rule = self._rules(mach["project"])["forks" if kind == "fork" else "hand_offs"]
        team = self.projects[mach["project"]]["name"]
        if rule == "off":
            raise Problem(403, "forbidden", "{}: Off in {}".format("Forks" if kind == "fork" else "Hand-offs", team))
        for r in self.machine_requests.values():
            if r["state"] == "waiting" and r["kind"] == kind and (r["machine"] or {}).get("id") == mach["id"] and r["receiver"]["id"] == receiver["id"]:
                return 200, r, {}
        q = self._request(kind, "give", mach, {"id": holder.get("id", ""), "name": holder.get("name", "")}, receiver, b.get("why", ""))
        if b.get("team"):
            if b["team"] not in self.projects:
                raise Problem(404, "not_found", "no such project of theirs")
            q["to_team"] = b["team"]
        q["waiting_on"] = {"who": "asker", "name": q["asked_by"]["name"]}
        return 201, q, {"Location": "/v1/requests/" + q["id"]}

    def _back(self, mach: Dict[str, Any], taking: bool, body: Any) -> Tuple[int, Any, Dict[str, str]]:
        """A machine handed off, back with the one who handed it off - taken back by them, or given back by its holder -
        with a key: waiting on the OK of the person who made the key (approve()); refused for one handed off by nobody, and for its holder
        taking it back."""
        self._in_org()
        b = _only(body or {}, ("team",))
        giver, holder = mach.get("given_by"), mach.get("held_by") or {}
        me = {"id": self.org["person"]["id"], "name": self.org["person"]["name"]}
        if taking and holder.get("id") == me["id"]:
            raise Problem(409, "conflict", "it is yours already")
        if taking and (giver or {}).get("id") != me["id"]:
            raise Problem(403, "forbidden", "only the person who handed this machine off takes it back")
        if not taking and not giver:
            raise Problem(409, "conflict", "it was not handed to you: there is nobody to give it back to")
        q = self._request("hand_off", "back", mach, {"id": holder.get("id", ""), "name": holder.get("name", "")}, giver or me, "")
        if b.get("team"):
            q["to_team"] = b["team"]
        q["waiting_on"] = {"who": "asker", "name": q["asked_by"]["name"]}
        return 201, q, {"Location": "/v1/requests/" + q["id"]}

    def _fork_from(self, mach: Dict[str, Any], b: Dict[str, Any]) -> Optional[str]:
        """The backup a fork is asked from, by its id - the machine's newest for `latest` - checked with the size asked
        of it as the API checks them; None for a fork of the machine as it is, which keeps its size."""
        asked, cpus = b.get("backup"), b.get("cpus")
        if cpus == 16:
            raise Problem(400, "invalid_request", "16 CPUs are not offered yet: choose 2, 4 or 8")
        if cpus is not None and cpus not in (2, 4, 8):
            raise Problem(400, "invalid_request", "cpus is 2, 4 or 8")
        if asked is None:
            if cpus is not None:
                raise Problem(400, "invalid_request", "cpus is only for a fork from a backup: a fork of the machine as it is now keeps its size")
            return None
        its = [x["id"] for x in reversed(self.backups) if x["machine"] == mach["id"] and asked in ("latest", x["id"])]
        if its:
            return its[0]
        if asked != "latest":
            raise Problem(404, "not_found", "no such backup")
        raise Problem(409, "conflict", "no backup of it yet: back it up first")

    def _own_fork(self, mach: Dict[str, Any], backup: Optional[str],
                  cpus: Optional[int]) -> Tuple[int, Any, Dict[str, str]]:
        """A member's fork of a machine they hold - of it as it is, or from one of its backups - but a full manager's of
        its team, with a key: nothing made, but a request of theirs to them, its backup and size as asked, answered 202
        and waiting on the OK of the person who made the key (approve()), then on a full manager's; the one there is when asked again while
        it waits; refused where the team's rules say Off."""
        me = {"id": self.org["person"]["id"], "name": self.org["person"]["name"]}  # type: ignore[index]
        if self._rules(mach["project"])["forks"] == "off":
            raise Problem(403, "forbidden", "Forks: Off in {}".format(self.projects[mach["project"]]["name"]))
        for r in self.machine_requests.values():
            if r["state"] == "waiting" and r["kind"] == "fork" and (r["machine"] or {}).get("id") == mach["id"] and r["receiver"]["id"] == me["id"]:
                return 200, r, {}
        q = self._request("fork", "give", mach, me, me, "")
        q.update(backup=backup, cpus=cpus, waiting_on={"who": "asker", "name": me["name"]})
        return 202, q, {"Location": "/v1/requests/" + q["id"]}

    def approve(self, request_id: str) -> Dict[str, Any]:
        """The OK of the person who made the key, of a request that waits for it, as they give it signed in to the dashboard: an ask then
        waits for its holder's pick; a member's own fork for a full manager; a hand-off for a manager when gives wait and
        the team's rules ask, else it is carried out - a fork made for its receiver, or the machine theirs, paused."""
        q = self.machine_requests[request_id]
        assert q["state"] == "waiting" and (q["waiting_on"] or {}).get("who") == "asker", q
        if q["way"] == "ask":
            q["waiting_on"] = {"who": "holder", "name": q["holder"]["name"]}
            return q
        if q["kind"] == "fork" and q["holder"]["id"] == q["receiver"]["id"]:
            # (every member's fork waits for a full manager, whatever gives_wait says)
            q["waiting_on"] = {"who": "manager", "name": None}
            return q
        mach = self.machines[q["machine"]["id"]]
        kind, receiver = q["kind"], q["receiver"]
        rule = self._rules(mach["project"])["forks" if kind == "fork" else "hand_offs"]
        if self.gives_wait and rule == "ask":
            q["waiting_on"] = {"who": "manager", "name": None}
            return q
        given = {"id": receiver["id"], "name": receiver["name"], "role": "member", "level": None}
        if kind == "fork":
            made = self.add_machine(mach["name"] + "-fork", q["to_team"] or mach["project"], mach["labels"])
            made.update(held_by=given, origin={"kind": "fork", "from": q["holder"], "machine_name": mach["name"], "at": "2026-10-27T07:00:00Z"})
            q["result"] = made["id"]
        elif q["way"] == "back":
            mach.update(held_by=given, origin=None, given_by=None, state="paused")
        else:
            mach.update(held_by=given, origin={"kind": "hand_off", "from": q["holder"], "machine_name": mach["name"], "at": "2026-10-27T07:00:00Z"},
                        given_by=q["holder"], state="paused")
            if q["to_team"]:
                mach["project"] = q["to_team"]
        q.update(state="done", waiting_on=None, decided_by=q["asked_by"], done_at="2026-10-27T07:00:01Z")
        return q

    def _secret_view(self, name: str) -> Dict[str, Any]:
        kept = self.secrets[name]
        used = sorted(m["id"] for m in self.machines.values() if name in m["secrets"] and m["state"] != "deleted")
        return {"object": "secret", "name": name, "machines": used, "outside": kept.get("outside"),
                "every_machine": bool(kept.get("every_machine")), "created_at": kept["created_at"], "updated_at": kept["updated_at"]}

    @staticmethod
    def _kept_as(name: str, b: Dict[str, Any]) -> Tuple[Optional[Dict[str, Any]], bool]:
        """How a secret is kept, as the API says it: `outside` left out (or null) the name's preset when it has one,
        true the preset (refused for a name with none), false an ordinary secret, an object a policy of its own;
        `every_machine` left out true for a preset's, and only for a secret kept outside."""
        preset = next((p for p in PRESETS if name in p["names"]), None)
        as_preset = None if preset is None else {"preset": preset["id"], "hosts": preset["hosts"], "header": preset["headers"][0]["name"],
                                                  "format": preset["headers"][0]["format"], "headers": preset["headers"], "query": preset["query"]}
        given = b.get("outside")
        if given is None:
            outside = as_preset
        elif given is True:
            if as_preset is None:
                raise Problem(400, "invalid_request", "{} is not a name the API knows as an AI key".format(name))
            outside = as_preset
        elif given is False:
            outside = None
        elif isinstance(given, dict) and given.get("hosts") and given.get("header"):
            fmt = given.get("format") or "{value}"
            outside = {"preset": None, "hosts": list(given["hosts"]), "header": given["header"], "format": fmt,
                       "headers": [{"name": given["header"], "format": fmt}], "query": []}
        else:
            raise Problem(400, "invalid_request", "`outside` is true, false, or an object of hosts, header and format")
        every = b.get("every_machine")
        if every is None:
            every = outside is not None and outside["preset"] is not None
        if not isinstance(every, bool):
            raise Problem(400, "invalid_request", "every_machine must be bool")
        if every and outside is None:
            raise Problem(400, "invalid_request", "only a secret kept outside the machines can be on every machine")
        return outside, every

    def _secret_names(self, names: Any) -> Optional[List[str]]:
        if names is None:
            return None
        if not isinstance(names, list):
            raise Problem(400, "invalid_request", "secrets is a list of names")
        for n in names:
            if n not in self.secrets:
                raise Problem(400, "invalid_request", "no secret {}: set it first (PUT /v1/secrets/{})".format(n, n))
        return sorted(set(names))

    def _ports(self, method: str, mach: Dict[str, Any], given: Optional[str], link: bool,
               body: Any) -> Tuple[int, Any, Dict[str, str]]:
        """A machine's ports: opened (201; 200 when open already, made public or private as asked), listed lowest
        first, closed (204, open or not), and a new one-time link made for one on every call. A deleted machine has
        none to list or close, and none can be opened or linked."""
        mid = mach["id"]
        gone = mach["state"] == "deleted"
        held: Dict[int, Dict[str, Any]] = {} if gone else self.ports.setdefault(mid, {})
        if given is None:
            if method == "GET":
                return 200, {"object": "list", "data": [held[n] for n in sorted(held)]}, {}
            if method == "POST":
                if gone:
                    raise Problem(404, "not_found", "no such machine")
                b = _only(body, ("port", "public"))
                n = _port_number(b.get("port"))
                if n in RESERVED_PORTS:
                    raise Problem(400, "invalid_request", "port {} is the machine's own; open another".format(n))
                public = b.get("public", False)
                if not isinstance(public, bool):
                    raise Problem(400, "invalid_request", "public is true or false")
                self.app_ports.get(mid, {}).pop(n, None)
                if n in held:
                    held[n]["public"] = public
                    return 200, held[n], {}
                if len(held) >= MOST_PORTS:
                    raise Problem(429, "limit_reached", "at most {} ports are open at once".format(MOST_PORTS))
                held[n] = {"object": "port", "machine": mid, "port": n, "public": public,
                           "url": "https://{}-{}.run.wheelmachine.ai".format(n, mid[2:]),
                           "created_at": "2026-10-23T07:00:00Z"}
                return 201, held[n], {}
            raise Problem(404, "not_found", "no such route")
        # a plain number, as the API reads it: no sign, no leading zero
        n = _port_number(int(given) if re.fullmatch(r"[1-9][0-9]{0,4}", given) else None)
        if method == "DELETE" and not link:
            held.pop(n, None)
            self.app_ports.get(mid, {}).pop(n, None)
            return 204, None, {}
        if method == "POST" and link:
            if gone:
                raise Problem(404, "not_found", "no such machine")
            b = _only(body, ("access_s", "path"))
            access_s = b.get("access_s", 1800)
            if isinstance(access_s, bool) or not isinstance(access_s, int) or not 60 <= access_s <= 3600:
                raise Problem(400, "invalid_request", "access_s is from 60 to 3600 seconds")
            where = b.get("path", "/")
            if not isinstance(where, str) or not where.startswith("/") or len(where) > 2048:
                raise Problem(400, "invalid_request", "path begins with /")
            if n not in held:
                raise Problem(404, "not_found", "the port is not open")
            made = {"object": "port_link", "machine": mid, "port": n,
                    "url": held[n]["url"] + "/.wheelmachine/link/" + secrets.token_urlsafe(32),
                    "expires_at": "2026-10-23T07:05:00Z", "access_s": access_s}
            self.links.append(made)
            return 201, made, {}
        raise Problem(404, "not_found", "no such route")

    def _apps(self, method: str, mach: Dict[str, Any], name: Optional[str], verb: Optional[str],
              body: Any) -> Tuple[int, Any, Dict[str, str]]:
        """A machine's apps: listed, turned on (a web app's port opened private when it is not, and a new link into
        it; openclaw's token, once) and off (the port closed when it was opened for the app), and a new link into a web
        app that is on (its port opened private first when it is not). A machine with no apps answers (409
        no_apps)."""
        mid = mach["id"]
        if mach["state"] == "deleted":
            raise Problem(404, "not_found", "no such machine")
        if mid in self.old_machines:
            raise Problem(409, "no_apps", "this machine has no apps; make a new machine to have them")
        held = self.apps.setdefault(mid, {a["name"]: dict(a, object="app", machine=mid) for a in APPS})
        for a in held.values():
            a["url"] = "https://{}-{}.run.wheelmachine.ai".format(a["port"], mid[2:]) if a["kind"] == "web" else None
        if name is None:
            if method != "GET":
                raise Problem(404, "not_found", "no such route")
            return 200, {"object": "list", "data": list(held.values())}, {}
        if method != "POST":
            raise Problem(404, "not_found", "no such route")
        app = held.get(name)
        if app is None:
            raise Problem(404, "not_found", "no such app in this machine")
        b = _only(body, ("access_s",) if verb not in ("off", "show") else ())
        access_s = b.get("access_s", 1800)
        if isinstance(access_s, bool) or not isinstance(access_s, int) or not 60 <= access_s <= 3600:
            raise Problem(400, "invalid_request", "access_s is between 60 and 3600")
        answer = dict(app)
        if verb == "show":
            # shown on the machine's screen when it is on: nothing opened, linked or given
            if app["on"] is False:
                raise Problem(409, "conflict", "{} could not be shown on the machine's screen: {} is off: turn it on first".format(name, app["title"]))
            return 200, answer, {}
        ports = self.ports.setdefault(mid, {})
        if verb == "link":
            if app["kind"] != "web":
                raise Problem(400, "invalid_request", "{} has no web address".format(app["title"]))
            if not app["on"]:
                raise Problem(409, "conflict", "{} is off: turn it on first".format(app["title"]))
        if verb in ("on", "off") and app["kind"] != "terminal":
            app["on"] = app["running"] = verb == "on"
            answer = dict(app)
        if app["kind"] == "web" and verb in ("on", "link"):
            n = app["port"]
            if n not in ports:
                ports[n] = {"object": "port", "machine": mid, "port": n, "public": False, "url": app["url"],
                            "created_at": "2026-10-23T07:00:00Z"}
                self.app_ports.setdefault(mid, {})[n] = name
            made = {"link": app["url"] + "/.wheelmachine/link/" + secrets.token_urlsafe(32), "expires_at": "2026-10-23T07:05:00Z",
                    "access_s": access_s}
            self.links.append(made)
            answer.update(made)
        if verb == "off" and app["kind"] == "web":
            n = app["port"]
            if n in ports and self.app_ports.get(mid, {}).get(n) == name and not ports[n]["public"]:
                del ports[n]
                del self.app_ports[mid][n]
        if verb == "on" and name == "openclaw":
            answer["token"] = APP_TOKEN
        return (201 if verb == "link" else 200), answer, {}

    def _policy(self, network: Any) -> Optional[Dict[str, Any]]:
        if network is None:
            return None
        _only(network, ("internet", "allow"))
        internet = network.get("internet")
        if internet not in ("open", "closed", "allowed"):
            raise Problem(400, "invalid_request", 'network.internet is "open", "closed" or "allowed"')
        allow = network.get("allow") or []
        if internet != "allowed" and allow:
            raise Problem(400, "invalid_request", 'network.allow is only for "allowed"')
        return {"internet": internet, "allow": list(allow)}

    def _audit(self, export: bool, q: Dict[str, List[str]]) -> Tuple[int, Any, Dict[str, str]]:
        """The audit, as the API reads it: with a key of every scope, and in an organisation by its owners and admins
        alone; a page of it, the newest first, or the whole of a year at most as a file - CSV quoted as the API quotes
        it, or JSON."""
        if self.org and self.org["role"] not in ("owner", "admin"):
            raise Problem(403, "forbidden", "your role in this organisation does not allow this")
        fmt = (q.get("format") or [""])[0]
        if export and fmt not in ("csv", "json"):
            raise Problem(400, "invalid_request", "say format=csv or format=json")
        when: Dict[str, Optional[datetime]] = {}
        for name in ("since", "until"):
            given = (q.get(name) or [""])[0]
            when[name] = None
            if given:
                try:
                    when[name] = datetime.fromisoformat(given.replace("Z", "+00:00"))
                except ValueError:
                    raise Problem(400, "invalid_request", "{} is a time like 2026-10-01T00:00:00Z".format(name)) from None
                if when[name].tzinfo is None:  # type: ignore[union-attr]
                    raise Problem(400, "invalid_request", "{} is a time like 2026-10-01T00:00:00Z".format(name))
        if export:
            span = timedelta(days=366)
            until = when["until"] or datetime(2026, 10, 23, 12, 0, tzinfo=timezone.utc)
            since = when["since"] or until - span
            if not since < until or until - since > span:
                raise Problem(400, "invalid_request", "an export covers a year at most, from since to until")
            when = {"since": since, "until": until}
        person, action = (q.get("person") or [""])[0], (q.get("action") or [""])[0]

        def at(e: Dict[str, Any]) -> datetime:
            return datetime.fromisoformat(e["at"].replace("Z", "+00:00"))

        rows = [e for e in self.audit
                if (not person or (e.get("person") or {}).get("id") == person) and e["action"].startswith(action)
                and (when["since"] is None or at(e) >= when["since"]) and (when["until"] is None or at(e) < when["until"])]
        if not export:
            limit, after = self._page(q)
            if after is not None:
                ids = [e["id"] for e in rows]
                if after not in ids:
                    raise Problem(400, "invalid_request", "after is the `next` of the page before")
                rows = rows[ids.index(after) + 1:]
            nxt = rows[limit - 1]["id"] if len(rows) > limit else None
            return 200, {"object": "list", "data": rows[:limit], "next": nxt}, {}
        named = {"Content-Disposition": 'attachment; filename="audit-acct_test.{}"'.format(fmt)}
        if fmt == "json":
            return 200, rows, named
        out = io.StringIO()
        _csv(out, "id", "at", "action", "target", "person", "person_name", "by", "address", "request_id", "detail")
        for e in rows:
            who = e.get("person") or {}
            detail = "; ".join("{}={}".format(k, v) for k, v in sorted(e["detail"].items()))
            _csv(out, e["id"], e["at"], e["action"], e["target"], who.get("id", ""), who.get("name", ""), e["by"], e["address"],
                 e["request_id"], detail)
        return 200, out.getvalue().encode(), dict(named, **{"Content-Type": "text/csv; charset=utf-8"})

    def _page(self, q: Dict[str, List[str]]) -> Tuple[int, Optional[str]]:
        limit = int((q.get("limit") or ["50"])[0])
        if not 1 <= limit <= 200:
            raise Problem(400, "invalid_request", "limit is between 1 and 200")
        after = q.get("after")
        return limit, after[0] if after else None

    def _backups(self, q: Dict[str, List[str]], machine: Optional[str] = None) -> List[Dict[str, Any]]:
        """Backups as the API lists them, the newest first and as many as `limit` (100 when left out): one machine's, or
        every machine's; a deleted machine's go with it."""
        limit = int((q.get("limit") or ["100"])[0])
        if not 1 <= limit <= 200:
            raise Problem(400, "invalid_request", "limit is between 1 and 200")
        rows = [b for b in reversed(self.backups) if machine in (None, b["machine"]) and self.machines[b["machine"]]["state"] != "deleted"]
        return rows[:limit]


def _csv(out: "io.StringIO", *fields: str) -> None:
    """A line of CSV as the API writes it: every field quoted, and one a spreadsheet would run as a formula - beginning
    with =, +, -, @, a tab or a carriage return - begun with ', so that it is shown as it is, never run."""
    cells = []
    for f in fields:
        if f and f[0] in "=+-@\t\r":
            f = "'" + f
        cells.append('"' + f.replace('"', '""') + '"')
    out.write(",".join(cells) + "\r\n")


class _Server(ThreadingHTTPServer):
    daemon_threads = True

    def handle_error(self, request: Any, client_address: Any) -> None:
        pass  # a client that went away mid-answer is what some tests are about


class _Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def setup(self) -> None:
        super().setup()
        # as the API's own server does: an answer's headers and body are not held back waiting for an ACK
        self.connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        api: FakeAPI = self.server.api  # type: ignore[attr-defined]
        with api.lock:
            api.connections += 1

    def log_message(self, format: str, *args: Any) -> None:  # quiet
        pass

    def _any(self) -> None:
        self.server.api.handle(self)  # type: ignore[attr-defined]

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = _any


class ConnectProxy:
    """A proxy that takes CONNECT, says who asked, and pipes the bytes through."""

    def __init__(self) -> None:
        self.seen: List[Tuple[str, str, Dict[str, str]]] = []
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.bind(("127.0.0.1", 0))
        self.sock.listen(8)
        self.port = self.sock.getsockname()[1]
        threading.Thread(target=self._accept, daemon=True).start()

    def close(self) -> None:
        self.sock.close()

    def _accept(self) -> None:
        while True:
            try:
                client, _ = self.sock.accept()
            except OSError:
                return
            threading.Thread(target=self._serve, args=(client,), daemon=True).start()

    def _serve(self, client: socket.socket) -> None:
        head = b""
        while b"\r\n\r\n" not in head:
            piece = client.recv(1)
            if not piece:
                client.close()
                return
            head += piece
        lines = head.decode("latin-1").split("\r\n")
        method, target, _ = lines[0].split(" ", 2)
        headers = {}
        for line in lines[1:]:
            if ":" in line:
                k, v = line.split(":", 1)
                headers[k.strip().lower()] = v.strip()
        self.seen.append((method, target, headers))
        host, port = target.rsplit(":", 1)
        upstream = socket.create_connection((host, int(port)))
        client.sendall(b"HTTP/1.1 200 Connection established\r\n\r\n")
        there = threading.Thread(target=_pump, args=(client, upstream), daemon=True)
        there.start()
        _pump(upstream, client)
        there.join(5)
        upstream.close()
        client.close()


def _pump(src: socket.socket, dst: socket.socket) -> None:
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except OSError:
        pass
    finally:
        try:
            dst.shutdown(socket.SHUT_WR)
        except OSError:
            pass
