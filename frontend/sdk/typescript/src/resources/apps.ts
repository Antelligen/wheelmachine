/**
 * `client.apps`: the apps a machine carries, turned on and off - a desktop and a code editor in the browser, an
 * agent's own page, a model server inside the machine, and agents run in its terminal. Each call asks the machine, so
 * a paused one is woken for it.
 */

import { type Core, withResponse } from "../core.js";
import type { App, AppLinkParams, Page, RequestOptions, WithResponse } from "../types.js";
import { given, id } from "./shared.js";

/** How long the API may hold turning an app on (seconds): the app starts, and is waited for. */
const HOLDS_APP_ON = 60;
/** How long the API may hold showing an app on the machine's screen (seconds): its desktop may start first. */
const HOLDS_APP_SHOW = 120;

/** An app's name, checked before anything is sent: as the machine's list of apps gives it, such as `desktop`. */
function appName(value: string): string {
  if (typeof value !== "string" || !/^[a-z0-9-]{1,32}$/.test(value)) {
    throw new TypeError("app is an app's name, such as desktop: lower-case letters, digits and -, at most 32");
  }
  return value;
}

export class Apps {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /** The machine's apps, and whether each is on. */
  async list(machineId: string, options: RequestOptions = {}): Promise<App[]> {
    const { data } = await this.#core.json<Page<App>>({
      method: "GET",
      path: `/v1/machines/${id(machineId, "machineId")}/apps`,
      options,
    });
    return data?.data ?? [];
  }

  /**
   * Turns an app on; it may take up to a minute. A web app's port is opened private when it is not open, and the
   * answer carries a one-time `link` that
   * lands on its page: the browser that opens it may reach the address for `access_s` seconds (1800). An app with a
   * token of its own answers it once, in `token`. The answer is shown once, so a call that may have reached the API
   * is never sent again; only one that could not go out at all is.
   */
  async turnOn(machineId: string, app: string, params: AppLinkParams = {}, options: RequestOptions = {}): Promise<WithResponse<App>> {
    const { data, info } = await this.#core.json<App>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/apps/${appName(app)}/on`,
      json: given(params),
      once: true,
      timeout: this.#core.held(HOLDS_APP_ON),
      options,
    });
    return withResponse(data ?? ({} as App), info);
  }

  /**
   * Turns an app off. A web app's port is closed with it when turning it on opened it and it is still private; a
   * port you opened yourself, or made public, stays open. Turning off an app that is off is not an error, so a call
   * that lost its connection is sent again. The code editor is always on: turning it off is refused with
   * ConflictError.
   */
  async turnOff(machineId: string, app: string, options: RequestOptions = {}): Promise<WithResponse<App>> {
    const { data, info } = await this.#core.json<App>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/apps/${appName(app)}/off`,
      repeatable: true,
      options,
    });
    return withResponse(data ?? ({} as App), info);
  }

  /**
   * Shows an app on the machine's own screen: its window opened on the desktop, signed in where it signs in, for a
   * person who watches that screen in the dashboard. It may take up to two minutes. Showing an app that is shown already
   * does no harm, so a call that lost its connection is sent again.
   */
  async show(machineId: string, app: string, options: RequestOptions = {}): Promise<WithResponse<App>> {
    const { data, info } = await this.#core.json<App>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/apps/${appName(app)}/show`,
      repeatable: true,
      timeout: this.#core.held(HOLDS_APP_SHOW),
      options,
    });
    return withResponse(data ?? ({} as App), info);
  }

  /**
   * A fresh one-time `link` into a web app that is on, made as a port's link is: it opens once, within five minutes,
   * and lands on the app's page. Each call makes a new link, so a call that may have reached the API is never sent
   * again.
   */
  async link(machineId: string, app: string, params: AppLinkParams = {}, options: RequestOptions = {}): Promise<WithResponse<App>> {
    const { data, info } = await this.#core.json<App>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/apps/${appName(app)}/link`,
      json: given(params),
      once: true,
      options,
    });
    return withResponse(data ?? ({} as App), info);
  }
}
