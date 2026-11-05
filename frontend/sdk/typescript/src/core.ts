/** How a call is made: the key, the address, retries, timeouts, and the answer read or turned into an error. */

import { APIConnectionError, APITimeoutError, WheelmachineError, errorFromAnswer } from "./errors.js";
import type { BodySource, Prepared } from "./files.js";
import type { RateLimit, RequestOptions, ResponseInfo, WithResponse } from "./types.js";
import { VERSION } from "./version.js";

export const USER_AGENT = `wheelmachine-typescript/${VERSION}`;
/** A Retry-After longer than this (seconds) is not waited out: the error is thrown at once, with `retryAfter` on it. */
export const LONGEST_RETRY_AFTER_S = 60;
const BACKOFF_FIRST_S = 0.5;
const BACKOFF_MOST_S = 8;

/** Methods that do the same however many times they are sent, so a call that lost its connection is sent again. A
 * POST is sent again only when it carries an Idempotency-Key (the calls that create something) or is `repeatable`. */
const REPEATABLE = new Set(["GET", "HEAD", "PUT", "PATCH", "DELETE"]);

/** Failures before a request could go out: whatever the call, it is safe to send it again. */
const NOT_SENT = new Set([
  "ECONNREFUSED",
  "ENOTFOUND",
  "EAI_AGAIN",
  "EHOSTUNREACH",
  "ENETUNREACH",
  "EHOSTDOWN",
  "ENETDOWN",
  "UND_ERR_CONNECT_TIMEOUT",
]);

export type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

export interface Call {
  method: Method;
  path: string;
  query?: Array<[string, string]>;
  json?: unknown;
  body?: BodySource;
  idempotencyKey?: string;
  /** The code a risky step carries (`X-Confirm-Code`), where it needs one. */
  confirmCode?: string;
  /** A POST that is safe to send again even when it may have reached the API: it moves something to a state, and
   * the same call again gives the same. */
  repeatable?: boolean;
  /** Never sent again once it may have reached the API, not even when refused for a moment: each call makes
   * something new that is shown once. Only a call that could not go out at all is tried again. */
  once?: boolean;
  /** Milliseconds this call is given when its options give none. */
  timeout?: number;
  accept?: string;
  options?: RequestOptions;
  /** Leave a 2xx answer's body unread, for the caller to stream; it calls `finish()` when done. */
  stream?: boolean;
}

export interface Answer {
  response: Response;
  info: ResponseInfo;
  bytes: Uint8Array;
  finish(): void;
}

export interface Hooks {
  sleep(ms: number, signal?: AbortSignal): Promise<void>;
  jitter(): number;
}

interface Failure {
  sent: boolean;
  timeout: boolean;
  headersTimeout: boolean;
  words: string;
  cause: unknown;
}

/** One attempt's time: a timer, and the caller's signal followed. */
class Attempt {
  readonly controller = new AbortController();
  timedOut = false;
  readonly #timer: ReturnType<typeof setTimeout>;
  readonly #user: AbortSignal | undefined;
  readonly #onUser = (): void => this.controller.abort(this.#user?.reason);

  constructor(ms: number, user: AbortSignal | undefined) {
    this.#user = user;
    user?.addEventListener("abort", this.#onUser, { once: true });
    this.#timer = setTimeout(() => {
      this.timedOut = true;
      this.controller.abort();
    }, ms);
  }

  stopTimer(): void {
    clearTimeout(this.#timer);
  }

  done(): void {
    clearTimeout(this.#timer);
    this.#user?.removeEventListener("abort", this.#onUser);
  }
}

/** What a stopped call throws: the signal's own reason, as fetch does. */
export function stopped(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException("This call was stopped.", "AbortError");
}

export function defaultSleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(stopped(signal));
      return;
    }
    const onAbort = (): void => {
      clearTimeout(timer);
      reject(stopped(signal as AbortSignal));
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

function isOk(status: number): boolean {
  return status >= 200 && status < 300;
}

function causeOf(err: unknown): unknown {
  const cause = (err as { cause?: unknown } | null)?.cause;
  return cause === undefined ? err : cause;
}

function codeOf(err: unknown): string {
  const e = err as { code?: unknown; errors?: Array<{ code?: unknown }> } | null;
  if (typeof e?.code === "string") return e.code;
  const first = Array.isArray(e?.errors) ? e?.errors[0]?.code : undefined;
  return typeof first === "string" ? first : "";
}

function describe(err: unknown): string {
  if (err instanceof Error) return err.message || err.name;
  return String(err);
}

function classify(err: unknown, timedOut: boolean): Failure {
  const cause = causeOf(err);
  const code = codeOf(cause);
  const base = { cause: err, words: describe(cause), headersTimeout: false };
  if (timedOut) return { ...base, sent: true, timeout: true };
  if (code === "UND_ERR_HEADERS_TIMEOUT") return { ...base, sent: true, timeout: true, headersTimeout: true };
  if (code === "UND_ERR_CONNECT_TIMEOUT") return { ...base, sent: false, timeout: true };
  return { ...base, sent: !NOT_SENT.has(code), timeout: false };
}

/** Retry-After in seconds: a number of seconds, or an HTTP date. */
export function retryAfter(value: string | null): number | null {
  if (value === null || value.trim() === "") return null;
  const seconds = Number(value.trim());
  if (Number.isFinite(seconds)) return Math.max(0, seconds);
  const when = Date.parse(value);
  return Number.isNaN(when) ? null : Math.max(0, (when - Date.now()) / 1000);
}

/** The RateLimit and RateLimit-Policy headers: `"key";r=599;t=1` and `"key";q=600;w=60`. */
export function rateLimit(state: string | null, policy: string | null): RateLimit | null {
  if (!state && !policy) return null;
  const read = (header: string | null): Record<string, number> => {
    const out: Record<string, number> = {};
    for (const m of (header ?? "").matchAll(/(?:^|;)\s*([a-z]+)=(\d+)/g)) out[m[1] as string] = Number(m[2]);
    return out;
  };
  const s = read(state);
  const p = read(policy);
  return { limit: p["q"] ?? null, windowSeconds: p["w"] ?? null, remaining: s["r"] ?? null, resetSeconds: s["t"] ?? null };
}

export function responseInfo(response: Response): ResponseInfo {
  return {
    status: response.status,
    requestId: response.headers.get("x-request-id"),
    replayed: (response.headers.get("idempotent-replayed") ?? "").trim().toLowerCase() === "true",
    rateLimit: rateLimit(response.headers.get("ratelimit"), response.headers.get("ratelimit-policy")),
  };
}

/** `obj`, marked with the answer that carried it (not enumerable: JSON and spreads leave it out). */
export function withResponse<T extends object>(obj: T, info: ResponseInfo): WithResponse<T> {
  Object.defineProperty(obj, "lastResponse", { value: info, enumerable: false, configurable: true });
  return obj as WithResponse<T>;
}

async function refusal(response: Response): Promise<WheelmachineError> {
  let problem: Record<string, unknown> | null = null;
  const raw = await response.text().catch(() => "");
  if (raw.trim() !== "") {
    try {
      const parsed: unknown = JSON.parse(raw);
      if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
        problem = parsed as Record<string, unknown>;
      }
    } catch {
      problem = null;
    }
  }
  return errorFromAnswer(
    response.status,
    response.statusText,
    problem,
    response.headers.get("x-request-id"),
    retryAfter(response.headers.get("retry-after")),
  );
}

export interface CoreSettings {
  apiKey: string;
  baseURL: string;
  timeout: number;
  maxRetries: number;
  fetch: typeof fetch;
  hooks: Hooks;
}

export class Core {
  readonly baseURL: string;
  readonly timeout: number;
  readonly maxRetries: number;
  readonly #apiKey: string;
  readonly #fetch: typeof fetch;
  readonly #hooks: Hooks;

  constructor(settings: CoreSettings) {
    this.baseURL = settings.baseURL;
    this.timeout = settings.timeout;
    this.maxRetries = settings.maxRetries;
    this.#apiKey = settings.apiKey;
    this.#fetch = settings.fetch;
    this.#hooks = settings.hooks;
  }

  /** The time a call the API may hold for `seconds` is given (ms): that, a margin, and never less than usual. */
  held(seconds: number): number {
    return Math.max(this.timeout, (seconds + 15) * 1000);
  }

  /** A call with a JSON answer: the answer's JSON (undefined when it has no body) and how it came. */
  async json<T>(call: Call): Promise<{ data: T | undefined; info: ResponseInfo }> {
    const { bytes, info } = await this.send(call);
    const body = new TextDecoder().decode(bytes);
    if (body.trim() === "") return { data: undefined, info };
    try {
      return { data: JSON.parse(body) as T, info };
    } catch {
      throw new WheelmachineError("the answer could not be read as JSON", { status: info.status, requestId: info.requestId });
    }
  }

  /** One call, tried again when it is safe and worth it. Gives a 2xx answer; throws the error for any other. */
  async send(call: Call): Promise<Answer> {
    const opts = call.options ?? {};
    const limit = opts.timeout ?? call.timeout ?? this.timeout;
    const maxRetries = opts.maxRetries ?? this.maxRetries;
    const repeatable =
      !call.once && (call.repeatable === true || REPEATABLE.has(call.method) || call.idempotencyKey !== undefined);
    const deadline = Date.now() + limit;
    const url = this.baseURL + call.path + (call.query?.length ? "?" + new URLSearchParams(call.query).toString() : "");
    const json = call.json === undefined ? undefined : new TextEncoder().encode(JSON.stringify(call.json));
    for (let attempt = 0; ; ) {
      if (opts.signal?.aborted) throw stopped(opts.signal);
      const headers: Record<string, string> = {
        "user-agent": USER_AGENT,
        accept: call.accept ?? "application/json",
      };
      // a program signing in through the browser has no key yet (login.ts)
      if (this.#apiKey !== "") headers.authorization = `Bearer ${this.#apiKey}`;
      if (call.idempotencyKey !== undefined) headers["idempotency-key"] = call.idempotencyKey;
      if (call.confirmCode !== undefined) headers["x-confirm-code"] = call.confirmCode;
      let body: Uint8Array | Blob | ReadableStream<Uint8Array> | undefined;
      let prepared: Prepared | undefined;
      if (json !== undefined) {
        headers["content-type"] = "application/json";
        body = json;
      } else if (call.body !== undefined) {
        prepared = await call.body.prepare();
        headers["content-type"] = "application/octet-stream";
        headers["content-length"] = String(prepared.length);
        body = prepared.body;
      }
      const run = new Attempt(limit, opts.signal);
      let response: Response;
      let bytes = new Uint8Array(0);
      try {
        const init: RequestInit & { duplex?: "half" } = {
          method: call.method,
          headers,
          signal: run.controller.signal,
          redirect: "manual",
        };
        if (body !== undefined) init.body = body as BodyInit;
        if (prepared?.streaming) init.duplex = "half";
        response = await this.#fetch(url, init);
        if (!call.stream && isOk(response.status)) bytes = new Uint8Array(await response.arrayBuffer());
      } catch (err) {
        run.done();
        await prepared?.close();
        if (opts.signal?.aborted) throw stopped(opts.signal);
        const own = causeOf(err);
        if (own instanceof WheelmachineError) throw own; // the body's own failure (a file that ended early)
        if (codeOf(own) === "UND_ERR_REQ_CONTENT_LENGTH_MISMATCH") {
          throw new WheelmachineError("the data's length is not the contentLength given", { cause: err });
        }
        const failure = classify(err, run.timedOut);
        if (attempt < maxRetries && (repeatable || !failure.sent) && (call.body === undefined || call.body.canRepeat())) {
          await this.#hooks.sleep(this.#backoff(attempt), opts.signal);
          attempt += 1;
          continue;
        }
        throw this.#noAnswer(failure, limit, call.idempotencyKey);
      }
      await prepared?.close();
      const info = responseInfo(response);
      if (isOk(response.status)) {
        if (call.stream) {
          run.stopTimer(); // the body is timed piece by piece as it streams
          return { response, info, bytes, finish: () => run.done() };
        }
        run.done();
        return { response, info, bytes, finish: () => undefined };
      }
      const error = await refusal(response);
      run.done();
      error.idempotencyKey = call.idempotencyKey ?? null;
      const pause = this.#pauseFor(error, attempt, maxRetries, repeatable, call, deadline);
      if (pause === null) throw error;
      if (error.status !== 409) attempt += 1;
      await this.#hooks.sleep(pause * 1000, opts.signal);
    }
  }

  /** How long (seconds) to wait before trying a refused call again, or null when it is not tried again. */
  #pauseFor(
    error: WheelmachineError,
    attempt: number,
    maxRetries: number,
    repeatable: boolean,
    call: Call,
    deadline: number,
  ): number | null {
    if (call.body !== undefined && !call.body.canRepeat()) return null;
    // an answer came, so the call reached the API: one that makes something new each time is not sent again
    if (call.once) return null;
    const after = error.retryAfter;
    if (after !== null && after > LONGEST_RETRY_AFTER_S) return null;
    const status = error.status;
    if (status === 409) {
      // the first call with this Idempotency-Key is still running: its answer is waited for, within the call's own
      // time, without spending a retry
      if (call.idempotencyKey === undefined || after === null || Date.now() + after * 1000 > deadline) return null;
      return after;
    }
    if (attempt >= maxRetries) return null;
    if (status === 429) {
      // a limit of the account's, not of the moment: trying again soon changes nothing
      if (error.code === "limit_reached" && after === null) return null;
    } else if (status === 502 || status === 504) {
      // the call may have reached the API and run: sent again only when that is safe
      if (!repeatable) return null;
    } else if (status === 503) {
      // not open yet: trying again soon changes nothing
      if (error.code === "not_open") return null;
    } else {
      return null;
    }
    return after ?? this.#backoff(attempt) / 1000;
  }

  /** Exponential backoff with full jitter (ms): anywhere from 0 to 0.5 s, 1 s, 2 s... at most 8 s. */
  #backoff(attempt: number): number {
    return Math.min(BACKOFF_MOST_S, BACKOFF_FIRST_S * 2 ** attempt) * this.#hooks.jitter() * 1000;
  }

  #noAnswer(failure: Failure, limit: number, idempotencyKey: string | undefined): APIConnectionError {
    let error: APIConnectionError;
    if (failure.headersTimeout) {
      error = new APITimeoutError(
        `no answer from ${this.baseURL} in the time Node's fetch waits for one (300 seconds unless set otherwise); the call may still be running`,
        { cause: failure.cause },
      );
    } else if (failure.timeout) {
      error = new APITimeoutError(`no answer from ${this.baseURL} within ${limit / 1000} seconds`, { cause: failure.cause });
    } else if (failure.sent) {
      error = new APIConnectionError(`the connection to ${this.baseURL} was lost before its answer: ${failure.words}`, {
        cause: failure.cause,
      });
    } else {
      error = new APIConnectionError(`could not reach ${this.baseURL}: ${failure.words}`, { cause: failure.cause });
    }
    error.idempotencyKey = idempotencyKey ?? null;
    return error;
  }
}
