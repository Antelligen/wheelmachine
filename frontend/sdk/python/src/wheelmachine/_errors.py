"""Errors. Everything the library raises is a WheelmachineError, so one ``except`` catches all of it.

An error from the API is ``application/problem+json`` with a stable ``code``; the error carries it with the HTTP
status, the words (``detail``), the call's ``request_id`` (give it when you ask us about a call) and, when the API
said when to try again, ``retry_after`` in seconds. No error ever carries the API key.
"""

from __future__ import annotations

from typing import Any, Dict, Optional, Type

__all__ = [
    "WheelmachineError",
    "APIConnectionError",
    "APITimeoutError",
    "BadRequestError",
    "AuthenticationError",
    "PaymentRequiredError",
    "PermissionDeniedError",
    "NotFoundError",
    "WaitTimeoutError",
    "ConflictError",
    "RateLimitError",
    "InternalServerError",
    "UnavailableError",
]


class WheelmachineError(Exception):
    """Anything the library raises.

    ``status`` is the HTTP status (None when no answer came), ``code`` the API's stable code (``not_found``,
    ``rate_limited``...), ``detail`` the API's words, ``request_id`` the call's id, ``retry_after`` the seconds the
    API asked to wait before trying again, and ``body`` the whole problem as it came. ``idempotency_key`` is the key
    a create call was sent with: calling it again with that key gets the first call's answer instead of making a
    second one.
    """

    def __init__(
        self,
        message: str,
        *,
        status: Optional[int] = None,
        code: Optional[str] = None,
        detail: Optional[str] = None,
        title: Optional[str] = None,
        request_id: Optional[str] = None,
        retry_after: Optional[float] = None,
        body: Optional[Dict[str, Any]] = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.status = status
        self.code = code
        self.detail = detail
        self.title = title
        self.request_id = request_id
        self.retry_after = retry_after
        self.body = body
        self.idempotency_key: Optional[str] = None

    def __str__(self) -> str:
        return self.message

    @property
    def limit(self) -> Optional[Dict[str, Any]]:
        """The limit that stopped the call, where one did: its ``scope``, ``measure``, ``limit``, ``used``,
        ``resets_at`` and ``raised_by``, as the problem carried it; None otherwise."""
        got = (self.body or {}).get("limit")
        return got if isinstance(got, dict) else None

    @property
    def confirm(self) -> Optional[Dict[str, Any]]:
        """What the code a risky step needs must be for, where it needs one (``confirm_required``,
        ``confirm_wrong``): its ``step``, and its ``org``, ``to`` or ``account`` - to give ``client.confirm_codes.send``."""
        got = (self.body or {}).get("confirm")
        return got if isinstance(got, dict) else None

    def __repr__(self) -> str:
        return "{}(status={!r}, code={!r}, request_id={!r})".format(
            type(self).__name__, self.status, self.code, self.request_id
        )


class APIConnectionError(WheelmachineError):
    """No answer came: the API could not be reached, or the connection was lost before the answer."""


class APITimeoutError(APIConnectionError):
    """No answer came in the time allowed."""


class BadRequestError(WheelmachineError):
    """400, 411, 413: the request is not right (``invalid_request``, ``too_large``)."""


class AuthenticationError(WheelmachineError):
    """401: no key, or a key that does not work (``unauthorized``, ``key_revoked``, ``key_expired``)."""


class PaymentRequiredError(WheelmachineError):
    """402: the account cannot spend now - its balance, or this month's allowance, is used up, or it is on hold."""


class PermissionDeniedError(WheelmachineError):
    """403: the key does not hold the scope or project this needs (``forbidden``, ``not_open``) - or the step needs the
    code we e-mail its person (``confirm_required``; ``confirm_wrong`` for one not right)."""


class NotFoundError(WheelmachineError):
    """404: nothing of that id that this key can see."""


class WaitTimeoutError(WheelmachineError):
    """408: the machine did not reach the state waited for in time."""


class ConflictError(WheelmachineError):
    """409, 422: not possible in the state it is in, or an Idempotency-Key used with another request."""


class RateLimitError(WheelmachineError):
    """429: a limit is reached (``rate_limited``, ``limit_reached``); ``retry_after`` says when to try again."""


class InternalServerError(WheelmachineError):
    """500: something went wrong on the API's side; ``request_id`` names it."""


class UnavailableError(WheelmachineError):
    """502, 503, 504: nothing available or no answer just now; ``retry_after`` says when to try again. A 503
    ``not_open`` is something not open: waiting does not change it, so it is not tried again."""


_BY_STATUS: Dict[int, Type[WheelmachineError]] = {
    400: BadRequestError,
    401: AuthenticationError,
    402: PaymentRequiredError,
    403: PermissionDeniedError,
    404: NotFoundError,
    408: WaitTimeoutError,
    409: ConflictError,
    411: BadRequestError,
    413: BadRequestError,
    422: ConflictError,
    429: RateLimitError,
    500: InternalServerError,
    502: UnavailableError,
    503: UnavailableError,
    504: UnavailableError,
}

# The code an answer without problem details (one from a proxy on the way, say) is given, so `code` is always set
# on an error and a program can branch on it alone.
_CODE_BY_STATUS: Dict[int, str] = {
    400: "invalid_request",
    401: "unauthorized",
    402: "payment_required",
    403: "forbidden",
    404: "not_found",
    408: "timeout",
    409: "conflict",
    411: "invalid_request",
    413: "too_large",
    422: "invalid_request",
    429: "rate_limited",
    500: "internal",
    502: "unavailable",
    503: "unavailable",
    504: "timeout",
}


def error_class(status: int) -> Type[WheelmachineError]:
    """The error class for an HTTP status."""
    if status in _BY_STATUS:
        return _BY_STATUS[status]
    if status >= 500:
        return UnavailableError if status != 501 else InternalServerError
    return WheelmachineError


def error_from_answer(
    status: int,
    reason: str,
    problem: Optional[Dict[str, Any]],
    request_id: Optional[str],
    retry_after: Optional[float],
) -> WheelmachineError:
    """The error for an answer of `status`, from its problem details when it has them."""
    problem = problem or {}
    code = problem.get("code") if isinstance(problem.get("code"), str) else None
    code = code or _CODE_BY_STATUS.get(status) or ("unavailable" if status >= 500 else None)
    detail = problem.get("detail") if isinstance(problem.get("detail"), str) else None
    title = problem.get("title") if isinstance(problem.get("title"), str) else None
    if request_id is None and isinstance(problem.get("request_id"), str):
        request_id = problem["request_id"]
    if retry_after is None:
        given = problem.get("retry_after_s")
        if isinstance(given, (int, float)) and not isinstance(given, bool) and given > 0:
            retry_after = float(given)
    words = detail or title or reason or "no details were given"
    message = "{} {}: {}".format(status, code, words) if code else "{}: {}".format(status, words)
    if request_id:
        message += " (request {})".format(request_id)
    return error_class(status)(
        message,
        status=status,
        code=code,
        detail=detail,
        title=title,
        request_id=request_id,
        retry_after=retry_after,
        body=problem or None,
    )
