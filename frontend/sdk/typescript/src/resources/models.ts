/**
 * `client.models`: the models the account's AI keys can use, as each model's provider lists them for the key. The lists
 * are read from each provider with your key, and kept for an hour; only the models an agent codes with are listed.
 *
 * `client.agents`: the agents of the account's machines, and the model each starts on - chosen for one agent, or one
 * for every agent. A choice reaches running machines at once, and the others when they next start; an agent
 * already running uses it from its next start. In an organisation the choices are the caller's own, in the team
 * `project` names (left out, their one team).
 */

import { type Core, withResponse } from "../core.js";
import type {
  Agent,
  AgentList,
  AllowedAt,
  AllowedModels,
  ModelList,
  ModelListParams,
  RequestOptions,
  TeamParams,
  WithResponse,
} from "../types.js";
import { id } from "./shared.js";

/** An agent's name, checked before anything is sent: as `client.agents.list()` gives it, such as `codex`. */
function agentName(value: string): string {
  if (typeof value !== "string" || !/^[a-z0-9-]{1,32}$/.test(value)) {
    throw new TypeError("agent is an agent's name, such as codex: lower-case letters, digits and -, at most 32");
  }
  return value;
}

/** A model as a choice names it: its provider and the provider's own id for it. */
function modelId(value: string): string {
  if (typeof value !== "string" || !value.includes("/")) {
    throw new TypeError("model is its provider and the provider's own id for it, such as openai/gpt-6.1-sol");
  }
  return value;
}

function team(params: TeamParams): Array<[string, string]> {
  return params.project === undefined ? [] : [["project", decodeURIComponent(id(params.project, "project"))]];
}

export class Models {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * The models, grouped by provider in the order agents choose between keys (`data`), and how each provider's list stands
   * (`suppliers`). `agent` keeps the models of the providers that agent can use; `refresh` reads each list again when it was
   * read more than a minute ago.
   */
  async list(params: ModelListParams = {}, options: RequestOptions = {}): Promise<WithResponse<ModelList>> {
    const query: Array<[string, string]> = [];
    if (params.agent !== undefined) query.push(["agent", agentName(params.agent)]);
    if (params.refresh) query.push(["refresh", "true"]);
    query.push(...team({ project: params.project }));
    const { data, info } = await this.#core.json<ModelList>({ method: "GET", path: "/v1/models", query, options });
    return withResponse(data ?? ({ object: "list", data: [], suppliers: [], allowed: null } as ModelList), info);
  }

  /** Every list of the models the organisation's people may use, the widest first: for its owners and admins. */
  async allowed(options: RequestOptions = {}): Promise<AllowedModels[]> {
    const { data } = await this.#core.json<{ data: AllowedModels[] }>({ method: "GET", path: "/v1/models/allowed", options });
    return data?.data ?? [];
  }

  /**
   * Sets the models people may use - one to ten, as `list()` gives their ids, in order: the first is the one an agent
   * starts on where its person chose none of them - for the whole organisation, a team, a role or one person. A choice a
   * person made that is not on the list is set aside: their agent starts on the list's first model it can use. For
   * owners and admins, to whom no list applies.
   */
  async allow(models: readonly string[], at: AllowedAt, options: RequestOptions = {}): Promise<WithResponse<AllowedModels>> {
    if (!Array.isArray(models) || models.length < 1 || models.length > 10) {
      throw new TypeError("models is a list of one to ten models, such as [\"openai/gpt-6.1-sol\"]");
    }
    const { data, info } = await this.#core.json<AllowedModels>({
      method: "PUT",
      path: allowedPath(at),
      json: { models: models.map(modelId) },
      options,
    });
    return withResponse(data ?? ({} as AllowedModels), info);
  }

  /** Takes a list away: those it applied to may use every model again, unless another list applies to them. */
  async allowAny(at: AllowedAt, options: RequestOptions = {}): Promise<void> {
    await this.#core.json({ method: "DELETE", path: allowedPath(at), options });
  }
}

/** The route of a list of allowed models: the organisation's, a team's, a role's or a person's - one of the four. */
function allowedPath(at: AllowedAt): string {
  const named = at && typeof at === "object" ? Object.keys(at).filter((k) => (at as Record<string, unknown>)[k] !== undefined) : [];
  if (named.length !== 1) throw new TypeError("name one list: { organisation: true }, { team }, { role } or { person }");
  if ("organisation" in at && at.organisation === true) return "/v1/models/allowed/organisation";
  if ("team" in at) return `/v1/models/allowed/teams/${id(at.team, "team")}`;
  if ("role" in at) return `/v1/models/allowed/roles/${id(at.role, "role")}`;
  if ("person" in at) return `/v1/models/allowed/people/${id(at.person, "person")}`;
  throw new TypeError("name one list: { organisation: true }, { team }, { role } or { person }");
}

export class Agents {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /** The agents, each with the providers it can use, the model chosen for it and what it starts on. */
  async list(params: TeamParams = {}, options: RequestOptions = {}): Promise<WithResponse<AgentList>> {
    const { data, info } = await this.#core.json<AgentList>({ method: "GET", path: "/v1/agents", query: team(params), options });
    return withResponse(data ?? ({ object: "list", data: [], every_agent: null, allowed: null } as AgentList), info);
  }

  /**
   * Chooses the model an agent starts on, as `client.models.list()` gives its id (`openai/gpt-6.1-sol`). The model is
   * checked with its provider as it is chosen.
   */
  async choose(agent: string, model: string, params: TeamParams = {}, options: RequestOptions = {}): Promise<WithResponse<Agent>> {
    const { data, info } = await this.#core.json<Agent>({
      method: "PUT",
      path: `/v1/agents/${agentName(agent)}/model`,
      query: team(params),
      json: { model: modelId(model) },
      options,
    });
    return withResponse(data ?? ({} as Agent), info);
  }

  /** Chooses one model for every agent: each that can use its provider starts on it, and each agent's own choice is taken away. */
  async chooseForEvery(model: string, params: TeamParams = {}, options: RequestOptions = {}): Promise<WithResponse<AgentList>> {
    const { data, info } = await this.#core.json<AgentList>({
      method: "PUT",
      path: "/v1/agents/model",
      query: team(params),
      json: { model: modelId(model) },
      options,
    });
    return withResponse(data ?? ({ object: "list", data: [], every_agent: null, allowed: null } as AgentList), info);
  }

  /** Takes an agent's choice away: it starts on the model for every agent where it can, else on its default. */
  async reset(agent: string, params: TeamParams = {}, options: RequestOptions = {}): Promise<void> {
    await this.#core.json({ method: "DELETE", path: `/v1/agents/${agentName(agent)}/model`, query: team(params), options });
  }

  /** Takes the one model for every agent away. */
  async resetEvery(params: TeamParams = {}, options: RequestOptions = {}): Promise<void> {
    await this.#core.json({ method: "DELETE", path: "/v1/agents/model", query: team(params), options });
  }
}
