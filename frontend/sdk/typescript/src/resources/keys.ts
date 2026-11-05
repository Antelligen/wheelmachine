/** `client.keys`: the account's API keys. A key makes only keys no stronger than itself. */

import { type Core, withResponse } from "../core.js";
import type { ConfirmAsk, ConfirmCodeSent, Key, KeyCreateOptions, KeyCreateParams, KeyCreated, Page, RequestOptions, WithResponse } from "../types.js";
import { given, id, keyFor } from "./shared.js";

function when(value: string | Date): string {
  if (value instanceof Date) {
    if (Number.isNaN(value.getTime())) throw new TypeError("expires_at is not a valid Date");
    return value.toISOString().replace(/\.\d{3}Z$/, "Z");
  }
  return value;
}

export class Keys {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /** The account's keys that hold a machine scope. Secrets are never shown again after a key is made. */
  async list(options: RequestOptions = {}): Promise<Key[]> {
    const { data } = await this.#core.json<Page<Key>>({ method: "GET", path: "/v1/keys", options });
    return data?.data ?? [];
  }

  /**
   * Makes a key. Its `secret` is in this answer only: keep it somewhere safe at once. A key with full access
   * (`cloud:*`) needs the code we e-mail the key's person: refused without it (a PermissionDeniedError with code
   * `confirm_required`, its `confirm` saying what for), it is sent with `client.confirmCodes.send(error.confirm)` and
   * given here as `confirmCode`.
   */
  async create(params: KeyCreateParams, options: KeyCreateOptions = {}): Promise<WithResponse<KeyCreated>> {
    const { data, info } = await this.#core.json<KeyCreated>({
      method: "POST",
      path: "/v1/keys",
      json: given({ ...params, expires_at: params.expires_at === undefined ? undefined : when(params.expires_at) }),
      idempotencyKey: await keyFor(options),
      confirmCode: options.confirmCode,
      options,
    });
    return withResponse(data ?? ({} as KeyCreated), info);
  }

  /** Revokes a key: it stops working within seconds, everywhere. A key can revoke itself. */
  async revoke(keyId: string, options: RequestOptions = {}): Promise<void> {
    await this.#core.send({ method: "DELETE", path: `/v1/keys/${id(keyId, "keyId")}`, options });
  }
}

/** `client.confirmCodes`: the codes we e-mail for a risky step - here, making a key with full access. */
export class ConfirmCodes {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * E-mails the key's person a 6-digit code for the step, as an error's `confirm` says it: it works once, for 10
   * minutes, for that alone. At most 5 are sent in 15 minutes.
   */
  async send(ask: ConfirmAsk, options: RequestOptions = {}): Promise<WithResponse<ConfirmCodeSent>> {
    const { data, info } = await this.#core.json<ConfirmCodeSent>({ method: "POST", path: "/v1/confirm-codes", json: given({ ...ask }), options });
    return withResponse(data ?? ({} as ConfirmCodeSent), info);
  }
}
