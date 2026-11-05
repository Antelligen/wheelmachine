/**
 * `client.requests`: in an organisation, the requests to fork a machine for someone or to hand it off to them - given
 * with `machines.forkFor` and `machines.handOff`, or asked for here - and a member's own forks, which `machines.fork`
 * answers while they wait. Approving and declining are a person's, signed in to the dashboard.
 */

import { type Core, withResponse } from "../core.js";
import { PageIterator } from "../pagination.js";
import type { CreateOptions, MachineRequest, Page, RequestAskParams, RequestListParams, RequestOptions, WithResponse } from "../types.js";
import { given, id, keyFor } from "./shared.js";

export class Requests {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * The newest first, page after page as it is iterated: those the key's person asked, those of machines they hold,
   * those that would give them a machine - and to a manager of a team its machines' requests, to owners and admins every
   * one. `open: true` gives those still waiting or being carried out alone; `machine` those of one machine, its own and
   * the forks made of it.
   */
  list(params: RequestListParams = {}, options: RequestOptions = {}): PageIterator<MachineRequest> {
    const base: Array<[string, string]> = [];
    if (params.open) base.push(["open", "true"]);
    if (params.machine !== undefined) base.push(["machine", params.machine]);
    if (params.limit !== undefined) base.push(["limit", String(params.limit)]);
    return new PageIterator<MachineRequest>(async (after) => {
      const query = after !== null ? [...base, ["after", after] as [string, string]] : base;
      const { data } = await this.#core.json<Page<MachineRequest>>({ method: "GET", path: "/v1/requests", query, options });
      return { items: data?.data ?? [], next: data?.next || null };
    });
  }

  /**
   * A request as it is now: how one answered `approved`, and going on behind, is followed. One the key does not see is
   * not found (NotFoundError).
   */
  async get(requestId: string, options: RequestOptions = {}): Promise<WithResponse<MachineRequest>> {
    const { data, info } = await this.#core.json<MachineRequest>({
      method: "GET",
      path: `/v1/requests/${id(requestId, "requestId")}`,
      options,
    });
    return withResponse(data ?? ({} as MachineRequest), info);
  }

  /**
   * Asks a teammate - `of` is their id, e-mail address or GitHub login - for a fork of one of their machines (`fork`), or
   * for one of them (`hand_off`). Made with a key, it waits first for the OK of the person who made the key, signed in to the dashboard
   * (`waiting_on.who` `asker`); then they pick which as they approve, and what the team's rules need of it follows.
   * `team` is the team of the key's person it goes into, when it is another than the machine's. The same ask while it
   * waits answers the one there is; it runs out after a day.
   */
  async ask(params: RequestAskParams, options: CreateOptions = {}): Promise<WithResponse<MachineRequest>> {
    if (params?.kind !== "fork" && params?.kind !== "hand_off") throw new TypeError('kind is "fork" or "hand_off"');
    if (typeof params.of !== "string" || params.of === "") {
      throw new TypeError("of is the teammate: their id, e-mail address or GitHub login");
    }
    const { data, info } = await this.#core.json<MachineRequest>({
      method: "POST",
      path: "/v1/requests",
      json: given({ kind: params.kind, of: params.of, team: params.team, why: params.why }),
      idempotencyKey: await keyFor(options),
      options,
    });
    return withResponse(data ?? ({} as MachineRequest), info);
  }

  /** Withdraws a request the key's person asked, while it waits (ConflictError once it is decided or expired). */
  async takeBack(requestId: string, options: RequestOptions = {}): Promise<WithResponse<MachineRequest>> {
    const { data, info } = await this.#core.json<MachineRequest>({
      method: "DELETE",
      path: `/v1/requests/${id(requestId, "requestId")}`,
      options,
    });
    return withResponse(data ?? ({} as MachineRequest), info);
  }
}
