"""Signing a program in through the browser, the way `wm login` does: no key to start from. The program asks for a
sign-in and shows the person a short code; they open the address, sign in with GitHub, check the code is the
same, and approve; the program is then given a new key of their account, once.

    from wheelmachine import DeviceLogin, Wheelmachine

    login = DeviceLogin.start(name="my-tool on laptop")
    print("Open", login.verification_uri, "and enter", login.user_code)
    key = login.wait()                     # the key's secret is in key.secret, given this once
    client = Wheelmachine(api_key=key.secret)
"""

from __future__ import annotations

import re
import time
from typing import Any, Optional, Sequence

from ._client import Wheelmachine
from ._errors import APITimeoutError, RateLimitError, WheelmachineError
from ._models import KeyCreated, _answered

__all__ = ["DeviceLogin"]

#: An account's id, an organisation's among them.
_ACCOUNT_ID = re.compile(r"acc_[0-9a-z]{26}")


class _Keyless(Wheelmachine):
    """A client with no key, which makes only the two calls of signing in."""

    def __init__(self, base_url: Optional[str], timeout: float, max_retries: int, proxy: Optional[str]) -> None:
        self._open("", base_url, timeout, max_retries, proxy)


class DeviceLogin:
    """A sign-in through the browser, on its way. Show the person ``user_code`` and open ``verification_uri``, where
    they type it - the code is never carried in the address, so a link someone sends cannot approve their own code.
    The code that fetches the key stays inside this object and out of its repr.

    ``wait()`` asks every ``interval`` seconds until the person approves (the new key), refuses (a
    ``PermissionDeniedError`` with code ``login_denied``) or the code runs out (a ``WheelmachineError`` with code
    ``login_expired``)."""

    user_code: str
    verification_uri: str
    expires_at: float
    """When the codes run out, in seconds since the epoch."""
    interval: float
    """Seconds between two asks for the key."""

    def __init__(self, client: Wheelmachine, device_code: str, answer: Any) -> None:
        self._client = client
        self._device_code = device_code
        self.user_code = str(answer.get("user_code") or "")
        self.verification_uri = str(answer.get("verification_uri") or "")
        self.expires_at = time.time() + float(answer.get("expires_in") or 600)
        self.interval = float(max(int(answer.get("interval") or 5), 1))

    @classmethod
    def start(
        cls,
        name: Optional[str] = None,
        scopes: Optional[Sequence[str]] = None,
        *,
        account: Optional[str] = None,
        base_url: Optional[str] = None,
        timeout: float = 30.0,
        max_retries: int = 3,
        proxy: Optional[str] = None,
    ) -> "DeviceLogin":
        """Asks for a sign-in: ``name`` is what the key will be called, ``scopes`` what it may do (everything when
        left out; the person may give it less). ``account`` asks for the key to be made in an organisation the person is
        in, by its id (``acc_...``): the page they approve on shows it first, and they still choose - the key's
        ``me()`` says where it was made."""
        if account is not None and not _ACCOUNT_ID.fullmatch(account):
            raise ValueError("account is an organisation's id: acc_ and 26 lower-case letters and digits")
        client = _Keyless(base_url, timeout, max_retries, proxy)
        payload: dict = {}
        if name:
            payload["name"] = name
        if scopes:
            payload["scopes"] = list(scopes)
        if account is not None:
            payload["account"] = account
        data, _ = client._json("POST", "/v1/login/devices", payload=payload)
        if not isinstance(data, dict) or not data.get("device_code") or not data.get("user_code"):
            client.close()
            raise WheelmachineError("the API's answer to a sign-in has no code in it")
        return cls(client, str(data["device_code"]), data)

    def poll(self) -> Optional[KeyCreated]:
        """Asks once: the new key when the person approved, None while they have not decided."""
        data, info = self._client._json("POST", "/v1/login/devices/token", payload={"device_code": self._device_code})
        if info.status == 202:
            return None
        if not isinstance(data, dict) or not data.get("secret"):
            raise WheelmachineError("the API's answer with the key could not be read", status=info.status, request_id=info.request_id)
        self.close()
        return _answered(KeyCreated.from_dict(data), info)

    def wait(self, *, timeout: Optional[float] = None) -> KeyCreated:
        """Asks every ``interval`` seconds - less often when asked to slow down - until the person decides, the code
        runs out, or ``timeout`` seconds pass (``APITimeoutError``)."""
        deadline = time.monotonic() + (timeout if timeout is not None else max(0.0, self.expires_at - time.time()) + 30.0)
        every = self.interval
        while True:
            self._client._sleep(every)
            try:
                got = self.poll()
            except RateLimitError as slow:
                if slow.code != "rate_limited":
                    raise  # a limit of the account's (as many keys as it may hold), not a pace: asking again changes nothing
                every += 5.0
                continue
            if got is not None:
                return got
            if time.monotonic() >= deadline:
                self.close()
                raise APITimeoutError("the sign-in was not approved in time")

    def close(self) -> None:
        self._client.close()

    def __repr__(self) -> str:
        return "DeviceLogin(user_code={!r}, verification_uri={!r})".format(self.user_code, self.verification_uri)
