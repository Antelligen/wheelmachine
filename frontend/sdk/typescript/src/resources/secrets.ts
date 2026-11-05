/**
 * `client.secrets`: values every command run in a machine given them gets as environment variables - a token, a
 * database's address - set once for the account and named by each machine that gets them. A value is never shown
 * again: not by these calls, not by any other.
 */

import { type Core, withResponse } from "../core.js";
import type { Page, RequestOptions, Secret, SecretPreset, SecretSetOptions, WithResponse } from "../types.js";

/** The most a secret's value holds, in bytes of UTF-8. */
export const SECRET_VALUE_MAX = 32 << 10;

const SECRET_NAME = /^[A-Z_][A-Z0-9_]{0,63}$/;
/** Half of a surrogate pair without the other half: UTF-8 cannot carry it. */
const LONE_SURROGATE = /[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/;

/** A secret's name: an environment variable's, which is what a machine's commands get it as. */
export function secretName(name: string): string {
  if (typeof name !== "string" || !SECRET_NAME.test(name)) {
    throw new TypeError("a secret's name is capitals, digits and _, not beginning with a digit, such as DATABASE_URL");
  }
  return name;
}

/** Secrets' names, as a machine is given them. */
export function secretNames(names: readonly string[]): string[] {
  if (!Array.isArray(names)) throw new TypeError('secrets is a list of names, such as ["DATABASE_URL"]');
  return names.map(secretName);
}

/** A value checked before it is sent, saying nothing of it: never a word of the value in an error. */
function checkValue(value: string): void {
  if (typeof value !== "string") throw new TypeError("a secret's value is a string");
  if (value === "") throw new TypeError("a secret's value cannot be empty");
  if (LONE_SURROGATE.test(value)) {
    throw new TypeError("a secret's value is text that UTF-8 can carry: it holds a lone surrogate");
  }
  if (value.includes("\0")) throw new TypeError("a secret's value cannot hold a NUL: an environment variable cannot carry one");
  if (new TextEncoder().encode(value).length > SECRET_VALUE_MAX) throw new TypeError("a secret's value is at most 32 KiB");
}

export class Secrets {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * Sets a secret, or sets it anew; the machines that get it get the new value from their next command. The value goes
   * in the call's body alone, and the answer is the secret without it. Setting needs a key with the
   * `cloud:secrets:write` scope, of the whole account; an account keeps at most 200.
   *
   * An AI key - a name `presets()` lists, such as `ANTHROPIC_API_KEY` - is kept outside the machines by its preset and
   * is on every machine, unless `outside: false` (an ordinary secret, its value in the machines given it) or
   * `every_machine: false` (only on the machines given it). Said whole: what is left out is the API's default for the
   * name, whatever the secret was before.
   */
  async set(name: string, value: string, options: SecretSetOptions = {}): Promise<WithResponse<Secret>> {
    const path = `/v1/secrets/${secretName(name)}`;
    checkValue(value);
    const { outside, every_machine, ...request } = options;
    const json: Record<string, unknown> = { value };
    if (outside !== undefined) {
      if (typeof outside !== "boolean" && (typeof outside !== "object" || outside === null)) {
        throw new TypeError("outside is true, false, or a policy of hosts, header and format");
      }
      json.outside = outside;
    }
    if (every_machine !== undefined) {
      if (typeof every_machine !== "boolean") throw new TypeError("every_machine is true or false");
      json.every_machine = every_machine;
    }
    const { data, info } = await this.#core.json<Secret>({ method: "PUT", path, json, options: request });
    return withResponse(data ?? ({} as Secret), info);
  }

  /**
   * The AI keys the API knows by their names, in the order agents choose between them: for each, the names it is set
   * under and the hosts its key is sent to.
   */
  async presets(options: RequestOptions = {}): Promise<SecretPreset[]> {
    const { data } = await this.#core.json<Page<SecretPreset>>({ method: "GET", path: "/v1/secrets/presets", options });
    return data?.data ?? [];
  }

  /** The account's secrets by name, each with the machines given it. */
  async list(options: RequestOptions = {}): Promise<Secret[]> {
    const { data } = await this.#core.json<Page<Secret>>({ method: "GET", path: "/v1/secrets", options });
    return data?.data ?? [];
  }

  /** Deletes it, and takes it off every machine given it: set again later, it goes to none until given anew. */
  async delete(name: string, options: RequestOptions = {}): Promise<void> {
    await this.#core.send({ method: "DELETE", path: `/v1/secrets/${secretName(name)}`, options });
  }
}
