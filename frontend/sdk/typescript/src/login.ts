/**
 * Signing a program in through the browser, the way `wm login` does: no key to start from. The program asks for a
 * sign-in and shows the person a short code; they open the address, sign in with GitHub, check the code is
 * the same and approve; the program is then given a new key of their account, once.
 *
 * ```ts
 * import { DeviceLogin, Wheelmachine } from "wheelmachine";
 * const login = await DeviceLogin.start({ name: "my-tool on laptop" });
 * console.log(`Open ${login.verificationUri} and enter ${login.userCode}`);
 * const key = await login.wait(); // key.secret is the new key: keep it now
 * const client = new Wheelmachine({ apiKey: key.secret });
 * ```
 */

import { checkedBaseURL, DEFAULT_BASE_URL } from "./client.js";
import { Core, defaultSleep, withResponse } from "./core.js";
import { RateLimitError, WheelmachineError } from "./errors.js";
import { env, inBrowser } from "./runtime.js";
import type { KeyCreated, Scope, WithResponse } from "./types.js";

export interface DeviceLoginOptions {
  /** What the key will be called, such as "my-tool on laptop". */
  name?: string;
  /** What the key may do; everything when left out. The person may give it less. */
  scopes?: Scope[];
  /**
   * The organisation to make the key in, by its id (`acc_...`); the person's own account when left out. The page they
   * approve on shows it first, and they still choose: the key's `me()` says where it was made.
   */
  account?: string;
  /** Where the API is; `WHEELMACHINE_BASE_URL`, else https://api.wheelmachine.ai. */
  baseURL?: string;
  /** Milliseconds each call may take (30 000). */
  timeout?: number;
  maxRetries?: number;
  fetch?: typeof fetch;
  /** As the client's: a key made in a web page reaches everyone who can read the page. */
  dangerouslyAllowBrowser?: boolean;
}

interface Started {
  device_code?: string;
  user_code?: string;
  verification_uri?: string;
  expires_in?: number;
  interval?: number;
}

/**
 * A sign-in through the browser, on its way. Show the person `userCode` and open `verificationUri`, where they type
 * it - the code is never carried in the address, so a link someone sends cannot approve their own code. The code that
 * fetches the key stays in a private field, out of JSON and inspect.
 */
export class DeviceLogin {
  readonly userCode: string;
  readonly verificationUri: string;
  readonly expiresAt: Date;
  /** Milliseconds between two asks for the key. */
  readonly interval: number;
  readonly #deviceCode: string;
  readonly #core: Core;

  /** @internal How a pause between asks is taken (ms), a seam for tests. */
  _sleep: (ms: number, signal?: AbortSignal) => Promise<void> = defaultSleep;

  private constructor(core: Core, deviceCode: string, started: Started) {
    this.#core = core;
    this.#deviceCode = deviceCode;
    this.userCode = String(started.user_code ?? "");
    this.verificationUri = String(started.verification_uri ?? "");
    this.expiresAt = new Date(Date.now() + (started.expires_in ?? 600) * 1000);
    this.interval = Math.max(started.interval ?? 5, 1) * 1000;
    Object.defineProperty(this, "_sleep", { enumerable: false });
  }

  /** Asks the API for a sign-in. No key is needed, and none is sent. */
  static async start(options: DeviceLoginOptions = {}): Promise<DeviceLogin> {
    if (inBrowser() && !options.dangerouslyAllowBrowser) {
      throw new WheelmachineError(
        "this library does not run in a web page: the key a sign-in gives would reach everyone who opens it. Pass " +
          "dangerouslyAllowBrowser: true only for a page that only you open",
      );
    }
    if (options.account !== undefined && !/^acc_[0-9a-z]{26}$/.test(options.account)) {
      throw new TypeError("account is an organisation's id: acc_ and 26 lower-case letters and digits");
    }
    const timeout = options.timeout ?? 30_000;
    if (!(timeout > 0)) throw new TypeError("timeout is a number of milliseconds above 0");
    const maxRetries = options.maxRetries ?? 3;
    if (!Number.isInteger(maxRetries) || maxRetries < 0) throw new TypeError("maxRetries is a whole number, 0 or more");
    const fetcher = options.fetch ?? (globalThis as { fetch?: typeof fetch }).fetch;
    if (typeof fetcher !== "function") {
      throw new WheelmachineError("this runtime has no fetch: use Node 18 or later, or pass one as fetch");
    }
    let login: DeviceLogin | undefined;
    const core = new Core({
      apiKey: "",
      baseURL: checkedBaseURL(options.baseURL ?? env("WHEELMACHINE_BASE_URL") ?? DEFAULT_BASE_URL),
      timeout,
      maxRetries,
      fetch: (input, init) => fetcher(input, init),
      hooks: { sleep: (ms, signal) => (login ? login._sleep(ms, signal) : defaultSleep(ms, signal)), jitter: Math.random },
    });
    const asked: Record<string, unknown> = {};
    if (options.name) asked.name = options.name;
    if (options.scopes?.length) asked.scopes = options.scopes;
    if (options.account !== undefined) asked.account = options.account;
    const { data } = await core.json<Started>({ method: "POST", path: "/v1/login/devices", json: asked });
    if (!data?.device_code || !data.user_code) throw new WheelmachineError("the API's answer to a sign-in has no code in it");
    login = new DeviceLogin(core, data.device_code, data);
    return login;
  }

  /** Asks once: the new key when the person approved (its secret in this answer only), null while they have not decided. */
  async poll(options: { signal?: AbortSignal } = {}): Promise<WithResponse<KeyCreated> | null> {
    const { data, info } = await this.#core.json<KeyCreated>({
      method: "POST",
      path: "/v1/login/devices/token",
      json: { device_code: this.#deviceCode },
      options: { signal: options.signal },
    });
    if (info.status === 202) return null;
    if (!data?.secret) {
      throw new WheelmachineError("the API's answer with the key could not be read", { status: info.status, requestId: info.requestId });
    }
    return withResponse(data, info);
  }

  /**
   * Asks every `interval` - less often when asked to slow down - until the person approves (the new key), refuses
   * (a PermissionDeniedError with code `login_denied`) or the code runs out (code `login_expired`).
   */
  async wait(options: { signal?: AbortSignal } = {}): Promise<WithResponse<KeyCreated>> {
    let every = this.interval;
    for (;;) {
      await this._sleep(every, options.signal);
      try {
        const got = await this.poll(options);
        if (got) return got;
      } catch (err) {
        // a limit of the account's (as many keys as it may hold) is not a pace: asking again changes nothing
        if (!(err instanceof RateLimitError) || err.code !== "rate_limited") throw err;
        every += 5000;
      }
    }
  }

  toJSON(): Record<string, unknown> {
    return { userCode: this.userCode, verificationUri: this.verificationUri, expiresAt: this.expiresAt.toISOString() };
  }
}
