/** The client: the key, the address, and the calls on the account and the catalogue. */

import { Core, defaultSleep, withResponse } from "./core.js";
import { WheelmachineError } from "./errors.js";
import { Apps } from "./resources/apps.js";
import { Audit } from "./resources/audit.js";
import { Credit } from "./resources/credit.js";
import { ConfirmCodes, Keys } from "./resources/keys.js";
import { Machines } from "./resources/machines.js";
import { Orgs } from "./resources/orgs.js";
import { Ports } from "./resources/ports.js";
import { Projects } from "./resources/projects.js";
import { Requests } from "./resources/requests.js";
import { Agents, Models } from "./resources/models.js";
import { Secrets } from "./resources/secrets.js";
import { env, inBrowser } from "./runtime.js";
import { type FileData, fileBody } from "./files.js";
import { PageIterator } from "./pagination.js";
import type {
  Code, CodeConnection, CodeGrant, FolderEntry, FolderGrant, FolderList, FolderListing, FolderListParams, FolderTrash, GpuList, Me, Page, Region,
  RequestOptions, Usage, UsageLimits, UsageParams, WithResponse,
} from "./types.js";

export const DEFAULT_BASE_URL = "https://api.wheelmachine.ai";

export interface ClientOptions {
  /** The API key; `WHEELMACHINE_API_KEY` when left out. */
  apiKey?: string;
  /** Where the API is; `WHEELMACHINE_BASE_URL`, else https://api.wheelmachine.ai. */
  baseURL?: string;
  /** Milliseconds a call may take (60 000). Calls the API holds by design get the time they need on top. */
  timeout?: number;
  /** How many times a call refused for a moment, or that lost its connection, is tried again (3). */
  maxRetries?: number;
  /** The fetch to call with: the runtime's own when left out. Give one to reach the API through a proxy. */
  fetch?: typeof fetch;
  /**
   * This library refuses to run in a web page, where the API key would reach everyone who opens it. Set this only for
   * a page that only you open, and know that anyone who can read the page can read the key.
   */
  dangerouslyAllowBrowser?: boolean;
}

export function checkedBaseURL(given: string): string {
  let url: URL;
  try {
    url = new URL(given.trim());
  } catch {
    throw new WheelmachineError("baseURL must be an address such as https://api.wheelmachine.ai");
  }
  if (
    (url.protocol !== "https:" && url.protocol !== "http:") ||
    url.username !== "" ||
    url.password !== "" ||
    url.search !== "" ||
    url.hash !== ""
  ) {
    throw new WheelmachineError(
      "baseURL must be an address such as https://api.wheelmachine.ai, with no user name, query or fragment",
    );
  }
  return url.origin + url.pathname.replace(/\/+$/, "");
}

/**
 * A client of the Wheelmachine API.
 *
 * ```ts
 * import { Wheelmachine } from "wheelmachine";
 * const client = new Wheelmachine(); // the key from WHEELMACHINE_API_KEY
 * const machine = await client.machines.create({ name: "build" });
 * ```
 *
 * Calls refused for a moment (429, 503), and calls that lost their connection, are tried again up to `maxRetries`
 * times, with a growing wait or the one the API asked for; a call that creates something carries an
 * Idempotency-Key, so a retry never makes two.
 */
export class Wheelmachine {
  readonly baseURL: string;
  readonly timeout: number;
  readonly maxRetries: number;
  readonly machines: Machines;
  readonly ports: Ports;
  readonly apps: Apps;
  readonly projects: Projects;
  readonly keys: Keys;
  readonly confirmCodes: ConfirmCodes;
  readonly secrets: Secrets;
  readonly models: Models;
  readonly agents: Agents;
  readonly orgs: Orgs;
  readonly requests: Requests;
  readonly audit: Audit;
  readonly credit: Credit;
  // the key lives only in here: a private field, out of JSON, console.log and util.inspect
  readonly #core: Core;

  /** @internal How a pause between attempts is taken (ms), a seam for tests. */
  _sleep: (ms: number, signal?: AbortSignal) => Promise<void> = defaultSleep;
  /** @internal The jitter of a backoff, between 0 and 1, a seam for tests. */
  _jitter: () => number = Math.random;

  constructor(options: ClientOptions = {}) {
    if (inBrowser() && !options.dangerouslyAllowBrowser) {
      throw new WheelmachineError(
        "this library does not run in a web page: the API key would reach everyone who opens it. Call the API from your " +
          "server; pass dangerouslyAllowBrowser: true only for a page that only you open",
      );
    }
    const apiKey = (options.apiKey ?? env("WHEELMACHINE_API_KEY") ?? "").trim();
    if (apiKey === "") throw new WheelmachineError("no API key: pass apiKey or set WHEELMACHINE_API_KEY");
    // never echoed: it would put the key in a log
    if (/[^\x21-\x7e]/.test(apiKey)) throw new WheelmachineError("the API key holds characters an API key cannot hold");
    const timeout = options.timeout ?? 60_000;
    if (!(timeout > 0)) throw new TypeError("timeout is a number of milliseconds above 0");
    const maxRetries = options.maxRetries ?? 3;
    if (!Number.isInteger(maxRetries) || maxRetries < 0) throw new TypeError("maxRetries is a whole number, 0 or more");
    const fetcher = options.fetch ?? (globalThis as { fetch?: typeof fetch }).fetch;
    if (typeof fetcher !== "function") {
      throw new WheelmachineError("this runtime has no fetch: use Node 18 or later, or pass one as fetch");
    }
    this.baseURL = checkedBaseURL(options.baseURL ?? env("WHEELMACHINE_BASE_URL") ?? DEFAULT_BASE_URL);
    this.timeout = timeout;
    this.maxRetries = maxRetries;
    this.#core = new Core({
      apiKey,
      baseURL: this.baseURL,
      timeout,
      maxRetries,
      // called as a plain function: a browser's fetch refuses any other `this`
      fetch: (input, init) => fetcher(input, init),
      hooks: { sleep: (ms, signal) => this._sleep(ms, signal), jitter: () => this._jitter() },
    });
    this.machines = new Machines(this.#core);
    this.ports = new Ports(this.#core);
    this.apps = new Apps(this.#core);
    this.projects = new Projects(this.#core);
    this.keys = new Keys(this.#core);
    this.confirmCodes = new ConfirmCodes(this.#core);
    this.secrets = new Secrets(this.#core);
    this.models = new Models(this.#core);
    this.agents = new Agents(this.#core);
    this.orgs = new Orgs(this.#core);
    this.requests = new Requests(this.#core);
    this.audit = new Audit(this.#core);
    this.credit = new Credit(this.#core);
    Object.defineProperty(this, "_sleep", { enumerable: false });
    Object.defineProperty(this, "_jitter", { enumerable: false });
  }

  /**
   * The account and key this client calls with, and whether the account can spend now. In an organisation, also the
   * person who made the key, their role and teams there, and their own limits there.
   */
  async me(options: RequestOptions = {}): Promise<WithResponse<Me>> {
    const { data, info } = await this.#core.json<Me>({ method: "GET", path: "/v1/me", options });
    return withResponse(data ?? ({} as Me), info);
  }

  /** Places a machine can be asked to run near (`machines.create({ region })`). */
  async regions(options: RequestOptions = {}): Promise<Region[]> {
    const { data } = await this.#core.json<Page<Region>>({ method: "GET", path: "/v1/regions", options });
    return data?.data ?? [];
  }

  /** GPUs on offer now, where they are and what an hour costs. */
  async gpus(options: RequestOptions = {}): Promise<WithResponse<GpuList>> {
    const { data, info } = await this.#core.json<GpuList>({ method: "GET", path: "/v1/gpus", options });
    return withResponse(data ?? ({} as GpuList), info);
  }

  /** The GitHub and GitLab the account connects, and the repositories its teams are given. */
  /** The folders you see: your own (My files) first, then your teams'. `on` is false where folders are not set up yet. */
  async folders(options: RequestOptions = {}): Promise<WithResponse<FolderList>> {
    const { data, info } = await this.#core.json<FolderList>({ method: "GET", path: "/v1/folders", options });
    return withResponse(data ?? ({} as FolderList), info);
  }

  /** One page of what is in a folder of a folder (its top for ""): folders first, then files, each by name. `folderEntries` walks them all. */
  async listFolder(folder: string, path = "", params: FolderListParams = {}, options: RequestOptions = {}): Promise<WithResponse<FolderListing>> {
    const query: Array<[string, string]> = [["path", path]];
    if (params.after) query.push(["after", params.after]);
    if (params.limit !== undefined) query.push(["limit", String(params.limit)]);
    const { data, info } = await this.#core.json<FolderListing>({ method: "GET", path: `/v1/folders/${encodeURIComponent(folder)}/list`, query, options });
    return withResponse(data ?? ({} as FolderListing), info);
  }

  /** Everything in a folder of a folder, folders first, page after page as it is iterated. */
  folderEntries(folder: string, path = "", params: Omit<FolderListParams, "after"> = {}, options: RequestOptions = {}): PageIterator<FolderEntry> {
    return new PageIterator<FolderEntry>(async (after) => {
      const page = await this.listFolder(folder, path, { ...params, after }, options);
      return { items: page.entries ?? [], next: page.next || null };
    });
  }

  /** A file of a folder, its bytes. */
  async readFolderFile(folder: string, path: string, options: RequestOptions = {}): Promise<Uint8Array> {
    const answer = await this.#core.send({ method: "GET", path: `/v1/folders/${encodeURIComponent(folder)}/files`, query: [["path", path]], accept: "application/octet-stream", options });
    return answer.bytes;
  }

  /**
   * Writes a file of a folder, in the place of the one there, making the folders on the way: `data` is bytes, a Blob, a
   * file on this computer as `{ path }`, or a stream of bytes with `contentLength`. At most 5 GB a file; the account
   * keeps 25 GB in its folders, the Trash included, while buying is closed.
   */
  async writeFolderFile(folder: string, path: string, data: FileData, options: RequestOptions & { contentLength?: number } = {}): Promise<FolderEntry> {
    const { contentLength, ...rest } = options;
    const answer = await this.#core.send({ method: "PUT", path: `/v1/folders/${encodeURIComponent(folder)}/files`, query: [["path", path]], body: fileBody(data, contentLength), options: rest });
    return JSON.parse(new TextDecoder().decode(answer.bytes) || "{}") as FolderEntry;
  }

  /** Puts a file or folder of a folder in its Trash, from which it can be put back for 30 days. */
  async deleteFolderFile(folder: string, path: string, options: RequestOptions = {}): Promise<WithResponse<FolderTrash>> {
    const { data, info } = await this.#core.json<FolderTrash>({ method: "DELETE", path: `/v1/folders/${encodeURIComponent(folder)}/files`, query: [["path", path]], options });
    return withResponse(data ?? ({} as FolderTrash), info);
  }

  /** Moves or renames a file or folder of a folder - into `toFolder`, another folder you edit, too. */
  async moveFolderItem(folder: string, from: string, to: string, toFolder?: string, options: RequestOptions = {}): Promise<void> {
    const json: Record<string, string> = { from, to };
    if (toFolder) json.to_folder = toFolder;
    await this.#core.json({ method: "POST", path: `/v1/folders/${encodeURIComponent(folder)}/move`, json, options });
  }

  /** Gives a team a folder, to `view` or to `edit`: its full managers, the organisation's owners and admins, or a person in their own account. */
  async giveTeamFolder(project: string, folder: string, access: "view" | "edit" = "view", options: RequestOptions = {}): Promise<WithResponse<FolderGrant>> {
    const { data, info } = await this.#core.json<FolderGrant>({ method: "PUT", path: `/v1/projects/${encodeURIComponent(project)}/folders/${encodeURIComponent(folder)}`, json: { access }, options });
    return withResponse(data ?? ({} as FolderGrant), info);
  }

  /** Takes a folder from a team: its machines lose it at once. */
  async takeTeamFolder(project: string, folder: string, options: RequestOptions = {}): Promise<void> {
    await this.#core.send({ method: "DELETE", path: `/v1/projects/${encodeURIComponent(project)}/folders/${encodeURIComponent(folder)}`, options });
  }

  async code(options: RequestOptions = {}): Promise<WithResponse<Code>> {
    const { data, info } = await this.#core.json<Code>({ method: "GET", path: "/v1/code", options });
    return withResponse(data ?? ({} as Code), info);
  }

  /** Where to install Wheelmachine's GitHub App for the account (an owner's or an admin's; good for half an hour). */
  async githubInstallURL(options: RequestOptions = {}): Promise<string> {
    const { data } = await this.#core.json<{ url: string }>({ method: "POST", path: "/v1/code/github", options });
    return data?.url ?? "";
  }

  /**
   * Connect a GitLab group by its access token (`read_repository`, `write_repository`); `address` is the GitLab's https
   * address, gitlab.com when left out. The token is never shown again.
   */
  async connectGitLab(token: string, address?: string, options: RequestOptions = {}): Promise<WithResponse<CodeConnection>> {
    const json: Record<string, string> = { token };
    if (address) json.address = address;
    const { data, info } = await this.#core.json<CodeConnection>({ method: "POST", path: "/v1/code/gitlab", json, options });
    return withResponse(data ?? ({} as CodeConnection), info);
  }

  /** Take a connection away, with every repository a team was given of it. */
  async disconnectCode(connection: string, options: RequestOptions = {}): Promise<void> {
    await this.#core.send({ method: "DELETE", path: `/v1/code/connections/${encodeURIComponent(connection)}`, options });
  }

  /** The repositories a connection reaches, by their paths. */
  async codeRepos(connection: string, options: RequestOptions = {}): Promise<string[]> {
    const { data } = await this.#core.json<Page<{ repo: string }>>({ method: "GET", path: `/v1/code/connections/${encodeURIComponent(connection)}/repos`, options });
    return (data?.data ?? []).map((r) => r.repo);
  }

  /** The repositories a team is given. */
  async teamCode(project: string, options: RequestOptions = {}): Promise<CodeGrant[]> {
    const { data } = await this.#core.json<Page<CodeGrant>>({ method: "GET", path: `/v1/projects/${encodeURIComponent(project)}/code`, options });
    return data?.data ?? [];
  }

  /** Give a team a repository of a connection, to `read` or to `write` (push) too: its full managers', owners' and admins'. */
  async grantCode(project: string, connection: string, repo: string, access: "read" | "write" = "read", options: RequestOptions = {}): Promise<WithResponse<CodeGrant>> {
    const { data, info } = await this.#core.json<CodeGrant>({ method: "PUT", path: `/v1/projects/${encodeURIComponent(project)}/code`, json: { connection, repo, access }, options });
    return withResponse(data ?? ({} as CodeGrant), info);
  }

  /** Take a repository from a team. */
  async revokeCode(project: string, grant: string, options: RequestOptions = {}): Promise<void> {
    await this.#core.send({ method: "DELETE", path: `/v1/projects/${encodeURIComponent(project)}/code/${encodeURIComponent(grant)}`, options });
  }

  /** Every limit for the month that applies to the account, as you may see it, and how near each is. */
  async limits(options: RequestOptions = {}): Promise<WithResponse<UsageLimits>> {
    const { data, info } = await this.#core.json<UsageLimits>({ method: "GET", path: "/v1/usage/limits", options });
    return withResponse(data ?? ({} as UsageLimits), info);
  }

  /**
   * Set the account's own limit for the month, in dollars, or take it away with null: in your own account, with a key
   * that holds `cloud:*`; in an organisation, an owner's.
   */
  async setMonthLimit(usd: number | null, options: RequestOptions = {}): Promise<WithResponse<UsageLimits>> {
    const { data, info } = await this.#core.json<UsageLimits>({ method: "PUT", path: "/v1/usage/limits/account", json: { month_cap_usd: usd }, options });
    return withResponse(data ?? ({} as UsageLimits), info);
  }

  /** Each machine's running time and cost in a month (`"2026-10"`, or a Date in it; this month when left out). */
  async usage(params: UsageParams = {}, options: RequestOptions = {}): Promise<WithResponse<Usage>> {
    const query: Array<[string, string]> = [];
    if (params.month !== undefined) {
      query.push(["month", params.month instanceof Date ? params.month.toISOString().slice(0, 7) : params.month]);
    }
    if (params.project !== undefined) query.push(["project", params.project]);
    const { data, info } = await this.#core.json<Usage>({ method: "GET", path: "/v1/usage", query, options });
    return withResponse(data ?? ({} as Usage), info);
  }
}
