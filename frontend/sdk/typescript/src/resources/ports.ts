/**
 * `client.ports`: a program listening on a port inside a machine, at a web address of its own
 * (`https://<port>-<the machine's id without m_>.run.wheelmachine.ai`), and links that let a browser into one.
 */

import { type Core, withResponse } from "../core.js";
import type { Page, Port, PortLink, PortLinkParams, PortOpenParams, RequestOptions, WithResponse } from "../types.js";
import { given, id } from "./shared.js";

/** A port, checked before anything is sent. */
function portNumber(value: number): number {
  if (!Number.isInteger(value) || value < 1 || value > 65535) throw new TypeError("port is a whole number from 1 to 65535");
  return value;
}

export class Ports {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * Opens a port of the machine at a web address of its own (`url`). Private unless `public` is true: it answers a
   * call with an API key that can see the machine, and a browser that opened a `link()` made for it. A port open
   * already is answered as it is, made public or private as asked (`lastResponse.status` 200, not 201), so a call
   * that lost its connection is sent again. The few ports the machine itself uses cannot be opened, and a
   * machine has at most 20 open.
   */
  async open(machineId: string, port: number, params: PortOpenParams = {}, options: RequestOptions = {}): Promise<WithResponse<Port>> {
    const { data, info } = await this.#core.json<Port>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/ports`,
      json: given({ port: portNumber(port), public: params.public }),
      repeatable: true,
      options,
    });
    return withResponse(data ?? ({} as Port), info);
  }

  /** The machine's ports open at web addresses, lowest first. */
  async list(machineId: string, options: RequestOptions = {}): Promise<Port[]> {
    const { data } = await this.#core.json<Page<Port>>({
      method: "GET",
      path: `/v1/machines/${id(machineId, "machineId")}/ports`,
      options,
    });
    return data?.data ?? [];
  }

  /** Closes a port's web address: it answers 404 from then on, and links made for it no longer open it. Closing a
   * port that is not open is not an error. */
  async close(machineId: string, port: number, options: RequestOptions = {}): Promise<void> {
    await this.#core.send({
      method: "DELETE",
      path: `/v1/machines/${id(machineId, "machineId")}/ports/${portNumber(port)}`,
      options,
    });
  }

  /**
   * A link that lets one browser into a private address: it opens once, within five minutes, and the browser that
   * opens it may reach the address for `access_s` seconds (1800), landing on `path` (`/`). Its `url` is in this
   * answer only: give it to the one person it is for, and keep it out of logs. Each call makes a new link, so a call
   * that may have reached the API is never sent again, not even one refused for a moment; only a call that could
   * not go out at all is.
   */
  async link(
    machineId: string,
    port: number,
    params: PortLinkParams = {},
    options: RequestOptions = {},
  ): Promise<WithResponse<PortLink>> {
    const { data, info } = await this.#core.json<PortLink>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/ports/${portNumber(port)}/link`,
      json: given(params),
      once: true,
      options,
    });
    return withResponse(data ?? ({} as PortLink), info);
  }
}
