/**
 * Errors. Everything a call throws is a WheelmachineError, so one `instanceof` check catches all of it - but for an
 * argument the library cannot use (a TypeError, thrown before anything is sent) and a call stopped by its AbortSignal
 * (the signal's own reason, as fetch does). No error ever carries the API key.
 */

import type { ConfirmAsk, LimitHit, Problem } from "./types.js";

export interface ErrorFields {
  status?: number | null;
  code?: string | null;
  detail?: string | null;
  title?: string | null;
  requestId?: string | null;
  retryAfter?: number | null;
  body?: Problem | null;
  cause?: unknown;
}

/**
 * Anything the library throws. `status` is the HTTP status (null when no answer came), `code` the API's stable code,
 * `detail` its words, `requestId` the call's id, `retryAfter` the seconds the API asked to wait before trying
 * again, and `body` the problem as it came. `idempotencyKey` is the key a create call was sent with: calling it
 * again with that key gets the first call's answer instead of making a second one.
 */
export class WheelmachineError extends Error {
  override name = "WheelmachineError";
  readonly status: number | null;
  readonly code: string | null;
  readonly detail: string | null;
  readonly title: string | null;
  readonly requestId: string | null;
  readonly retryAfter: number | null;
  readonly body: Problem | null;
  idempotencyKey: string | null = null;

  /** The limit that stopped the call, where one did: which, who can raise it and when it lifts. */
  get limit(): LimitHit | null {
    return this.body?.limit ?? null;
  }

  /** What the code a risky step needs must be for, where it needs one: to give `client.confirmCodes.send`. */
  get confirm(): ConfirmAsk | null {
    return this.body?.confirm ?? null;
  }

  constructor(message: string, fields: ErrorFields = {}) {
    super(message, fields.cause === undefined ? undefined : { cause: fields.cause });
    this.status = fields.status ?? null;
    this.code = fields.code ?? null;
    this.detail = fields.detail ?? null;
    this.title = fields.title ?? null;
    this.requestId = fields.requestId ?? null;
    this.retryAfter = fields.retryAfter ?? null;
    this.body = fields.body ?? null;
  }
}

/** No answer came: the API could not be reached, or the connection was lost before the answer. */
export class APIConnectionError extends WheelmachineError {
  override name = "APIConnectionError";
}

/** No answer came in the time allowed. */
export class APITimeoutError extends APIConnectionError {
  override name = "APITimeoutError";
}

/** 400, 411, 413: the request is not right (`invalid_request`, `too_large`). */
export class BadRequestError extends WheelmachineError {
  override name = "BadRequestError";
}

/** 401: no key, or a key that does not work (`unauthorized`, `key_revoked`, `key_expired`). */
export class AuthenticationError extends WheelmachineError {
  override name = "AuthenticationError";
}

/** 402: the account cannot spend now - its balance, or this month's allowance, is used up, or it is on hold. */
export class PaymentRequiredError extends WheelmachineError {
  override name = "PaymentRequiredError";
}

/** 403: the key does not hold the scope or project this needs (`forbidden`, `not_open`). */
export class PermissionDeniedError extends WheelmachineError {
  override name = "PermissionDeniedError";
}

/** 404: nothing of that id that this key can see. */
export class NotFoundError extends WheelmachineError {
  override name = "NotFoundError";
}

/** 408: the machine did not reach the state waited for in time. */
export class WaitTimeoutError extends WheelmachineError {
  override name = "WaitTimeoutError";
}

/** 409, 422: not possible in the state it is in, or an Idempotency-Key used with another request. */
export class ConflictError extends WheelmachineError {
  override name = "ConflictError";
}

/** 429: a limit is reached (`rate_limited`, `limit_reached`); `retryAfter` says when to try again. */
export class RateLimitError extends WheelmachineError {
  override name = "RateLimitError";
}

/** 500: something went wrong on the API's side; `requestId` names it. */
export class InternalServerError extends WheelmachineError {
  override name = "InternalServerError";
}

/**
 * 502, 503, 504: not available just now; `retryAfter` says when to try again. A 503 `not_open` is something that is
 * not open yet: it is not tried again.
 */
export class UnavailableError extends WheelmachineError {
  override name = "UnavailableError";
}

type ErrorClass = new (message: string, fields?: ErrorFields) => WheelmachineError;

const BY_STATUS: Record<number, ErrorClass> = {
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
};

// The code an answer without problem details (one from a proxy on the way, say) is given, so `code` is always set
// on an error and a program can branch on it alone.
const CODE_BY_STATUS: Record<number, string> = {
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
};

function errorClass(status: number): ErrorClass {
  const known = BY_STATUS[status];
  if (known) return known;
  if (status >= 500) return status === 501 ? InternalServerError : UnavailableError;
  return WheelmachineError;
}

function text(v: unknown): string | null {
  return typeof v === "string" && v !== "" ? v : null;
}

/** The error for an answer of `status`, from its problem details when it has them. */
export function errorFromAnswer(
  status: number,
  reason: string,
  problem: Record<string, unknown> | null,
  requestId: string | null,
  retryAfter: number | null,
): WheelmachineError {
  const p = problem ?? {};
  const code = text(p["code"]) ?? CODE_BY_STATUS[status] ?? (status >= 500 ? "unavailable" : null);
  const detail = text(p["detail"]);
  const title = text(p["title"]);
  const id = requestId ?? text(p["request_id"]);
  let after = retryAfter;
  const given = p["retry_after_s"];
  if (after === null && typeof given === "number" && given > 0) after = given;
  const words = detail ?? title ?? (reason || "no details were given");
  let message = code ? `${status} ${code}: ${words}` : `${status}: ${words}`;
  if (id) message += ` (request ${id})`;
  const Cls = errorClass(status);
  return new Cls(message, {
    status,
    code,
    detail,
    title,
    requestId: id,
    retryAfter: after,
    body: problem ? (problem as unknown as Problem) : null,
  });
}
