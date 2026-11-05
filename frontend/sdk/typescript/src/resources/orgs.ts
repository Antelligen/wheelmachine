/**
 * `client.orgs`: the organisations the key's person is in. Their people, invitations and teams are managed in the
 * dashboard, signed in.
 */

import type { Core } from "../core.js";
import type { Org, Page, RequestOptions } from "../types.js";

export class Orgs {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * The organisations the person who made this key is in, the newest first, with their role in each. A key made in an
   * organisation lists none - what else the person who made it is in is theirs - and is refused (PermissionDeniedError); a key of
   * the person's own account lists them.
   */
  async list(options: RequestOptions = {}): Promise<Org[]> {
    const { data } = await this.#core.json<Page<Org>>({ method: "GET", path: "/v1/orgs", options });
    return data?.data ?? [];
  }
}
