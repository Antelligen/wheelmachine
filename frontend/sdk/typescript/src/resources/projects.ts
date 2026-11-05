/** `client.projects`: the account's projects, which hold its machines. */

import { type Core, withResponse } from "../core.js";
import { PageIterator } from "../pagination.js";
import type {
  CreateOptions,
  Page,
  Project,
  ProjectCreateParams,
  ProjectListParams,
  ProjectUpdateParams,
  RequestOptions,
  TeamRules,
  WithResponse,
} from "../types.js";
import { id, keyFor } from "./shared.js";

export class Projects {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /** Makes a project. Like every create, a retry after a lost connection never makes two. */
  async create(params: ProjectCreateParams, options: CreateOptions = {}): Promise<WithResponse<Project>> {
    const { data, info } = await this.#core.json<Project>({
      method: "POST",
      path: "/v1/projects",
      json: { name: params.name },
      idempotencyKey: await keyFor(options),
      options,
    });
    return withResponse(data ?? ({} as Project), info);
  }

  async get(projectId: string, options: RequestOptions = {}): Promise<WithResponse<Project>> {
    const { data, info } = await this.#core.json<Project>({
      method: "GET",
      path: `/v1/projects/${id(projectId, "projectId")}`,
      options,
    });
    return withResponse(data ?? ({} as Project), info);
  }

  /** Every project, oldest first, page after page as it is iterated. */
  list(params: ProjectListParams = {}, options: RequestOptions = {}): PageIterator<Project> {
    return new PageIterator<Project>(async (after) => {
      const query: Array<[string, string]> = [];
      if (params.limit !== undefined) query.push(["limit", String(params.limit)]);
      if (after !== null) query.push(["after", after]);
      const { data } = await this.#core.json<Page<Project>>({ method: "GET", path: "/v1/projects", query, options });
      return { items: data?.data ?? [], next: data?.next || null };
    });
  }

  /** Renames it. */
  async update(projectId: string, params: ProjectUpdateParams, options: RequestOptions = {}): Promise<WithResponse<Project>> {
    const { data, info } = await this.#core.json<Project>({
      method: "PATCH",
      path: `/v1/projects/${id(projectId, "projectId")}`,
      json: { name: params.name },
      options,
    });
    return withResponse(data ?? ({} as Project), info);
  }

  /** Deletes an empty project; one that still holds machines is refused (ConflictError, `project_not_empty`). */
  async delete(projectId: string, options: RequestOptions = {}): Promise<void> {
    await this.#core.send({ method: "DELETE", path: `/v1/projects/${id(projectId, "projectId")}`, options });
  }

  /**
   * In an organisation, a team's rules: whether its machines are handed off freely, with a manager's OK or not at all;
   * whether its members fork them, every fork waiting for the OK of a full manager of the team, an owner or an admin, or
   * not at all; whether GPUs and open internet are allowed in it; its budget for the month and what its machines came to
   * so far. Those set, else the defaults. Setting them is its full managers', owners' and admins', signed in to the
   * dashboard.
   */
  async rules(projectId: string, options: RequestOptions = {}): Promise<WithResponse<TeamRules>> {
    const { data, info } = await this.#core.json<TeamRules>({
      method: "GET",
      path: `/v1/projects/${id(projectId, "projectId")}/rules`,
      options,
    });
    return withResponse(data ?? ({} as TeamRules), info);
  }
}
