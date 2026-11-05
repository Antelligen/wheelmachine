"""The wire: HTTP/1.1 from the standard library, on connections kept open between calls.

A kept connection saves a new TCP and TLS handshake on every call. One that has sat idle for a while, or that the
far side has closed, is never used again: it is checked before each use, so a call is not lost to a connection that
was already gone.
"""

from __future__ import annotations

import base64
import http.client
import io
import os
import select
import socket
import ssl
import threading
import time
import urllib.parse
import urllib.request
from typing import IO, Any, Dict, List, Optional, Tuple

from ._errors import WheelmachineError

#: A connection idle longer than this is closed rather than used again: servers close idle connections after a
#: minute or more, and one closing just as a call goes out would lose that call.
IDLE_LIMIT_S = 45.0
MAX_IDLE = 16
CHUNK = 64 * 1024
#: Bodies up to this size go out in the same write as the headers.
SMALL_BODY = 1 << 20

_LOOPBACK = {"localhost", "127.0.0.1", "::1"}


class Target:
    """Where the API is: its scheme, host and port, and a path prefix (empty for the API's own address)."""

    def __init__(self, base_url: str) -> None:
        parts = urllib.parse.urlsplit(base_url.strip())
        try:
            port = parts.port
        except ValueError:
            port = -1
        if (
            parts.scheme not in ("http", "https")
            or not parts.hostname
            or port == -1
            or parts.query
            or parts.fragment
            or parts.username is not None
            or parts.password is not None
        ):
            raise WheelmachineError(
                "base_url must be an address such as https://api.wheelmachine.ai, with no user name, query or fragment"
            )
        self.scheme: str = parts.scheme
        self.host: str = parts.hostname
        self.port: int = port or (443 if parts.scheme == "https" else 80)
        self.prefix: str = parts.path.rstrip("/")
        self.url: str = "{}://{}{}".format(parts.scheme, parts.netloc, self.prefix)


class Proxy:
    """An http:// proxy, reached with CONNECT, so the API's TLS runs end to end through it."""

    def __init__(self, host: str, port: int, authorization: Optional[str]) -> None:
        self.host = host
        self.port = port
        self._authorization = authorization

    @classmethod
    def parse(cls, url: str) -> "Proxy":
        if "://" not in url:
            url = "http://" + url
        parts = urllib.parse.urlsplit(url)
        try:
            port = parts.port
        except ValueError:
            port = -1
        if parts.scheme != "http" or not parts.hostname or port == -1:
            raise WheelmachineError("a proxy is given as http://host:port (or http://user:password@host:port)")
        authorization = None
        if parts.username is not None:
            pair = "{}:{}".format(urllib.parse.unquote(parts.username), urllib.parse.unquote(parts.password or ""))
            authorization = "Basic " + base64.b64encode(pair.encode("utf-8")).decode("ascii")
        return cls(parts.hostname, port or 80, authorization)

    def headers(self) -> Dict[str, str]:
        return {"Proxy-Authorization": self._authorization} if self._authorization else {}

    def __repr__(self) -> str:
        return "Proxy({}:{})".format(self.host, self.port)


def proxy_from_environment(target: Target) -> Optional[Proxy]:
    """The proxy the environment names for the API's address (HTTPS_PROXY, minding NO_PROXY), if any.

    Only https addresses go through one: a plain http address is a test server on this computer or its network.
    """
    if target.scheme != "https" or target.host in _LOOPBACK:
        return None
    try:
        if urllib.request.proxy_bypass(target.host):
            return None
        proxies = urllib.request.getproxies()
    except Exception:  # a broken system setting never stops a call
        return None
    url = proxies.get("https") or proxies.get("all")
    return Proxy.parse(url) if url else None


class Failure(Exception):
    """A call that got no answer. `sent`: the whole request went out, so the API may have acted on it."""

    def __init__(self, cause: BaseException, sent: bool) -> None:
        super().__init__(str(cause))
        self.cause = cause
        self.sent = sent


# --- request bodies -----------------------------------------------------------------------------------------------


class OpenBody:
    """A body ready for one attempt: its length, and its bytes either at hand (`data`) or to be read (`reader`)."""

    def __init__(self, length: int, data: Optional[bytes] = None, reader: Optional[IO[bytes]] = None, owned: bool = False) -> None:
        self.length = length
        self.data = data
        self.reader = reader
        self._owned = owned

    def close(self) -> None:
        if self._owned and self.reader is not None:
            self.reader.close()


class Body:
    """A request's body, which can be opened again for another attempt when `can_repeat()` says so."""

    content_type = "application/octet-stream"

    def open(self) -> OpenBody:
        raise NotImplementedError

    def can_repeat(self) -> bool:
        return True


class BytesBody(Body):
    def __init__(self, data: bytes, content_type: str = "application/octet-stream") -> None:
        self._data = data
        self.content_type = content_type

    def open(self) -> OpenBody:
        if len(self._data) <= SMALL_BODY:
            return OpenBody(len(self._data), data=self._data)
        return OpenBody(len(self._data), reader=io.BytesIO(self._data), owned=True)


class PathBody(Body):
    """A file on this computer, streamed from disk; opened afresh for each attempt."""

    def __init__(self, path: str) -> None:
        self._path = path
        if not os.path.isfile(path):
            raise FileNotFoundError("no such file: {}".format(path))

    def open(self) -> OpenBody:
        f = open(self._path, "rb")
        try:
            return OpenBody(os.fstat(f.fileno()).st_size, reader=f, owned=True)
        except BaseException:
            f.close()
            raise


class _Touched:
    """A reader that remembers whether anything was read from it."""

    def __init__(self, reader: Any) -> None:
        self._reader = reader
        self.touched = False

    def read(self, n: int) -> Any:
        self.touched = True
        return self._reader.read(n)


class StreamBody(Body):
    """A file object the caller opened. A seekable one is sent again from where it was; any other, only if nothing
    was read from it yet. Its length is `length`, or found by seeking when it can seek."""

    def __init__(self, fileobj: Any, length: Optional[int]) -> None:
        self._file = fileobj
        self._start: Optional[int] = None
        try:
            if fileobj.seekable():
                self._start = fileobj.tell()
        except (AttributeError, OSError, ValueError):
            self._start = None
        if length is None:
            if self._start is None:
                raise ValueError(
                    "this file object cannot seek, so its length is not known: give it as content_length=..."
                )
            end = fileobj.seek(0, io.SEEK_END)
            fileobj.seek(self._start)
            length = end - self._start
        if length < 0:
            raise ValueError("content_length cannot be negative")
        self._length = length
        self._reader = _Touched(fileobj)

    def open(self) -> OpenBody:
        if self._start is not None:
            self._file.seek(self._start)
        return OpenBody(self._length, reader=self._reader)  # type: ignore[arg-type]

    def can_repeat(self) -> bool:
        return self._start is not None or not self._reader.touched


# --- answers ------------------------------------------------------------------------------------------------------


class Answer:
    """An answer whose body is still to be read. Reading it to the end gives its connection back for the next call."""

    def __init__(self, transport: "Transport", conn: http.client.HTTPConnection, response: http.client.HTTPResponse, reusable: bool) -> None:
        self._transport = transport
        self._conn = conn
        self._response = response
        self._reusable = reusable
        self._done = False
        self.status: int = response.status
        self.reason: str = response.reason or ""
        self.content: bytes = b""

    def header(self, name: str) -> Optional[str]:
        return self._response.getheader(name)

    def read(self, limit: Optional[int] = None) -> bytes:
        try:
            data = self._response.read() if limit is None else self._response.read(limit)
        except (OSError, http.client.HTTPException) as exc:
            self.close()
            raise Failure(exc, sent=True) from exc
        self._finish()
        return data

    def chunks(self, size: int = CHUNK) -> Any:
        """The body, a piece at a time (a generator of bytes)."""
        try:
            while True:
                try:
                    piece = self._response.read(size)
                except (OSError, http.client.HTTPException) as exc:
                    raise Failure(exc, sent=True) from exc
                if not piece:
                    break
                yield piece
        except BaseException:
            self.close()
            raise
        self._finish()

    def _finish(self) -> None:
        if self._done:
            return
        self._done = True
        if self._reusable and self._response.isclosed() and not self._response.will_close:
            self._transport.give_back(self._conn)
        else:
            self._conn.close()

    def close(self) -> None:
        if not self._done:
            self._done = True
            self._conn.close()


# --- the connections ----------------------------------------------------------------------------------------------


def _gone(conn: http.client.HTTPConnection) -> bool:
    """Whether a kept connection was closed by the far side (or holds bytes nobody asked for): it is readable."""
    sock = conn.sock
    if sock is None:
        return True
    try:
        if hasattr(select, "poll"):
            poller = select.poll()
            poller.register(sock, select.POLLIN)
            return bool(poller.poll(0))
        readable, _, _ = select.select([sock], [], [], 0)
        return bool(readable)
    except (OSError, ValueError):
        return True


class Transport:
    """Sends requests to one address, keeping connections for the calls after."""

    def __init__(self, target: Target, proxy: Optional[Proxy]) -> None:
        self._target = target
        self._proxy = proxy
        self._ssl = ssl.create_default_context() if target.scheme == "https" else None
        self._lock = threading.Lock()
        self._idle: List[Tuple[http.client.HTTPConnection, float]] = []
        self._closed = False

    def _take(self, timeout: float) -> http.client.HTTPConnection:
        now = time.monotonic()
        with self._lock:
            while self._idle:
                conn, since = self._idle.pop()
                if now - since <= IDLE_LIMIT_S and not _gone(conn):
                    conn.timeout = timeout
                    if conn.sock is not None:
                        conn.sock.settimeout(timeout)
                    return conn
                conn.close()
        return self._open(timeout)

    def _open(self, timeout: float) -> http.client.HTTPConnection:
        t = self._target
        host, port = (self._proxy.host, self._proxy.port) if self._proxy else (t.host, t.port)
        conn: http.client.HTTPConnection
        if t.scheme == "https":
            conn = http.client.HTTPSConnection(host, port, timeout=timeout, context=self._ssl)
        else:
            conn = http.client.HTTPConnection(host, port, timeout=timeout)
        if self._proxy is not None:
            conn.set_tunnel(t.host, t.port, headers=self._proxy.headers())
        return conn

    def give_back(self, conn: http.client.HTTPConnection) -> None:
        with self._lock:
            if not self._closed and len(self._idle) < MAX_IDLE:
                self._idle.append((conn, time.monotonic()))
                return
        conn.close()

    def close(self) -> None:
        with self._lock:
            self._closed = True
            idle, self._idle = self._idle, []
        for conn, _ in idle:
            conn.close()

    def send(self, method: str, path: str, headers: List[Tuple[str, str]], body: Optional[OpenBody], timeout: float) -> Answer:
        """One attempt. Raises Failure when no answer came."""
        conn = self._take(timeout)
        streaming = False
        try:
            conn.putrequest(method, path, skip_accept_encoding=True)
            for name, value in headers:
                conn.putheader(name, value)
            if body is not None and body.data is not None:
                conn.endheaders(body.data)
            else:
                conn.endheaders()
                if body is not None and body.reader is not None:
                    streaming = True
                    _stream(conn, body)
        except (OSError, http.client.HTTPException) as exc:
            # The API may have answered before taking the whole body (an error, say) and closed: that answer is
            # the one to give, not the broken pipe.
            early = self._early_answer(conn) if streaming else None
            if early is not None:
                return early
            conn.close()
            raise Failure(exc, sent=False) from exc
        except BaseException:
            conn.close()
            raise
        try:
            response = conn.getresponse()
        except (OSError, http.client.HTTPException) as exc:
            conn.close()
            raise Failure(exc, sent=True) from exc
        except BaseException:
            conn.close()
            raise
        return Answer(self, conn, response, reusable=True)

    def _early_answer(self, conn: http.client.HTTPConnection) -> Optional[Answer]:
        try:
            if conn.sock is not None:
                conn.sock.settimeout(2.0)
            response = conn.getresponse()
        except Exception:
            return None
        return Answer(self, conn, response, reusable=False)


def _stream(conn: http.client.HTTPConnection, body: OpenBody) -> None:
    left = body.length
    reader = body.reader
    assert reader is not None
    while left > 0:
        piece = reader.read(min(CHUNK, left))
        if not isinstance(piece, (bytes, bytearray, memoryview)):
            conn.close()
            raise TypeError("the file object gave text, not bytes: open it in binary mode ('rb')")
        if not piece:
            conn.close()
            raise WheelmachineError(
                "the data ended after {} of the {} bytes its length said".format(body.length - left, body.length)
            )
        conn.send(piece)
        left -= len(piece)


def is_timeout(exc: BaseException) -> bool:
    return isinstance(exc, (socket.timeout, TimeoutError))
