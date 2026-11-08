/**
 * `client.deployments`: apps put on the internet from a repository on GitHub or GitLab, the https address of a public
 * one, or files, each on a machine of its own at a public address.
 *
 * Not open yet: until Deploy opens, each call fails with the code `not_open`.
 */

import { type Core, withResponse } from "../core.js";
import { WaitTimeoutError } from "../errors.js";
import { type FileData, fileBody } from "../files.js";
import type {
  CreateOptions,
  DeployBranchList,
  DeployBranchesParams,
  DeployDetectParams,
  DeployGuess,
  DeployLog,
  DeployLogsParams,
  DeployRepo,
  DeployReposParams,
  DeployRun,
  Deployment,
  DeploymentCreateParams,
  DeploymentListParams,
  DeploymentUpdateParams,
  DeploymentWaitParams,
  Page,
  RequestOptions,
  SendFilesOptions,
  WithResponse,
} from "../types.js";
import { given, id, keyFor } from "./shared.js";

/** The longest the API waits in one call for a run to end. */
const LONGEST_HOLD_S = 600;

/**
 * How long files going up are given, by default, in milliseconds: the hour the API gives them, and a margin. A call's
 * timeout covers the whole of it, even while its bytes are moving, and 500 MB on a slow line takes a while.
 */
const SEND_FILES_MS = 65 * 60 * 1000;

/** How long files wait for their machine by default, in seconds, and how often it is looked at, in milliseconds. */
const READY_TIMEOUT_S = 600;
const READY_POLL_MS = 2000;

function query(params: object): [string, string][] | undefined {
  const out = Object.entries(params)
    .filter(([, v]) => v !== undefined && v !== "")
    .map(([k, v]): [string, string] => [k, String(v)]);
  return out.length > 0 ? out : undefined;
}

/**
 * `client.deployments`: apps put on the internet, each on a machine of its own at a public address.
 *
 * Not open yet: until Deploy opens, each call fails with the code `not_open`.
 */
export class Deployments {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * Makes a deployment and begins its first run, which goes on after this answer: `wait()` follows it. Its code comes
   * from a repository of one of your connections (`source: { connection, repo }`), a public repository's https address
   * (`source: { url }`), or files (`source: { upload: true }`, then `sendFiles()`). `start` is what starts it, with its
   * port in `$PORT` and `HOST` as 0.0.0.0, and `port` the port it listens on; `detect()` suggests them. It is charged
   * as any machine, builds included. A retry after a lost connection never makes two.
   */
  async create(params: DeploymentCreateParams, options: CreateOptions = {}): Promise<WithResponse<Deployment>> {
    return this.#object({
      method: "POST",
      path: "/v1/deployments",
      json: given(params),
      idempotencyKey: await keyFor(options),
      options,
    });
  }

  /** The deployments you see, the newest first, each with its current or last run. */
  async list(params: DeploymentListParams = {}, options: RequestOptions = {}): Promise<Deployment[]> {
    const { data } = await this.#core.json<Page<Deployment>>({ method: "GET", path: "/v1/deployments", query: query(params), options });
    return data?.data ?? [];
  }

  async get(deploymentId: string, options: RequestOptions = {}): Promise<WithResponse<Deployment>> {
    return this.#object({ method: "GET", path: `/v1/deployments/${id(deploymentId, "deploymentId")}`, options });
  }

  /**
   * Changes its settings. Its name, branch, commands and port apply from its next run; `keep_awake` at once. Only the
   * person who deployed it can.
   */
  async update(deploymentId: string, params: DeploymentUpdateParams, options: RequestOptions = {}): Promise<WithResponse<Deployment>> {
    return this.#object({ method: "PATCH", path: `/v1/deployments/${id(deploymentId, "deploymentId")}`, json: given(params), options });
  }

  /** Deletes it, and its machine with all its files. Its address answers nothing from then on. */
  async delete(deploymentId: string, options: RequestOptions = {}): Promise<void> {
    await this.#core.send({ method: "DELETE", path: `/v1/deployments/${id(deploymentId, "deploymentId")}`, options });
  }

  /**
   * Fetches its branch as it is now, or uses its files again, and builds it. A build that fails leaves the live version
   * running. Once built, the live version stops and the new one starts, and one that does not answer within two minutes
   * is replaced by the last one. `wait()` follows it. A call that may have reached the API is not sent again: a
   * deployment has one run at a time.
   */
  async redeploy(deploymentId: string, options: RequestOptions = {}): Promise<WithResponse<Deployment>> {
    return this.#object({ method: "POST", path: `/v1/deployments/${id(deploymentId, "deploymentId")}/redeploy`, once: true, options });
  }

  /**
   * Sends a tar archive, or a gzipped one, as its files: its first, or new ones in place of its last. It is then built
   * and started. `data` is bytes, a Blob, a stream with `contentLength`, or a file on this computer as `{ path }`. Each
   * entry is a file or a folder inside the folder sent, with no link. At most 20,000 files, 100 MB a file and 500 MB in
   * all. Leave out what is not needed, and keys, tokens and settings: what goes can be read at its address if it is
   * served as plain files, so give its secrets by name. It waits for its machine first, at most `readyTimeoutS` seconds
   * (600), and is given an hour to go up unless `timeout` says otherwise.
   */
  async sendFiles(deploymentId: string, data: FileData, options: SendFilesOptions = {}): Promise<WithResponse<Deployment>> {
    const { contentLength, readyTimeoutS, ...rest } = options;
    await this.#untilFilesWanted(deploymentId, readyTimeoutS ?? READY_TIMEOUT_S, rest);
    return this.#object({
      method: "PUT",
      path: `/v1/deployments/${id(deploymentId, "deploymentId")}/files`,
      body: fileBody(data, contentLength),
      timeout: SEND_FILES_MS,
      options: rest,
    });
  }

  /**
   * Waits while its run gets its machine ready: the API holds a call only a minute for that, and a machine may take longer,
   * and it is better not to send it all to be refused.
   */
  async #untilFilesWanted(deploymentId: string, timeoutS: number, options: RequestOptions): Promise<void> {
    const { timeout: _ignored, ...looks } = options;
    const deadline = Date.now() + timeoutS * 1000;
    for (;;) {
      const now = await this.get(deploymentId, looks);
      if (!now.run || now.run.ended_at !== null || now.run.step !== "machine") return;
      const left = deadline - Date.now();
      if (left <= 0) {
        throw new WaitTimeoutError(`its machine is not ready yet, so its files were not sent: ${now.id} is made, and sendFiles() can be called again`, {
          status: 408,
          code: "timeout",
        });
      }
      await this.#core.pause(Math.min(READY_POLL_MS, left), options.signal);
    }
  }

  /**
   * Answers once its current run has ended (at once when it has none going): its `state` and its `run` say how it ended.
   * Fails with WaitTimeoutError when it is still deploying after `timeout_s` seconds (300). Waits longer than the API's
   * 600 seconds are asked for again.
   */
  async wait(deploymentId: string, params: DeploymentWaitParams = {}, options: RequestOptions = {}): Promise<WithResponse<Deployment>> {
    const total = params.timeout_s ?? 300;
    if (!(total >= 0)) throw new TypeError("timeout_s is a number of seconds, 0 or more");
    const path = `/v1/deployments/${id(deploymentId, "deploymentId")}/wait`;
    const deadline = Date.now() + total * 1000;
    for (;;) {
      const left = Math.max(0, (deadline - Date.now()) / 1000);
      const step = Math.min(left, LONGEST_HOLD_S);
      const began = Date.now();
      try {
        return await this.#object({ method: "GET", path, query: [["timeout_s", String(Math.ceil(step))]], timeout: this.#core.held(step), options });
      } catch (err) {
        if (!(err instanceof WaitTimeoutError) || left <= LONGEST_HOLD_S || Date.now() - began < (step - 1) * 1000) throw err;
      }
    }
  }

  /** Its last 20 runs, the newest first. */
  async runs(deploymentId: string, options: RequestOptions = {}): Promise<DeployRun[]> {
    const { data } = await this.#core.json<Page<DeployRun>>({ method: "GET", path: `/v1/deployments/${id(deploymentId, "deploymentId")}/runs`, options });
    return data?.data ?? [];
  }

  /**
   * Its log: `kind: "build"`, a run's own, its last 256 KB (`run`, or its last run's); or `kind: "app"`, the end of what
   * the app itself writes, which wakes its machine. Only the person who deployed it can.
   */
  async logs(deploymentId: string, params: DeployLogsParams = {}, options: RequestOptions = {}): Promise<WithResponse<DeployLog>> {
    return this.#object({ method: "GET", path: `/v1/deployments/${id(deploymentId, "deploymentId")}/logs`, query: query(params), options });
  }

  /**
   * How code most likely builds and starts, a suggestion to change: a repository's (`source`, `branch`), or that of files
   * about to be sent (`files`: by name, each with its contents, or null for one that is there, unread).
   */
  async detect(params: DeployDetectParams, options: RequestOptions = {}): Promise<WithResponse<DeployGuess>> {
    return this.#object({ method: "POST", path: "/v1/deployments/detect", json: given(params), repeatable: true, options });
  }

  /** The repositories you may deploy into a team (your first when left out), the most recently pushed to first. */
  async repos(params: DeployReposParams = {}, options: RequestOptions = {}): Promise<DeployRepo[]> {
    const { data } = await this.#core.json<Page<DeployRepo>>({ method: "GET", path: "/v1/deployments/repos", query: query(params), options });
    return data?.data ?? [];
  }

  /** A repository's branches, and which is its default: of one of your connections (`connection` and `repo`), or a public one (`url`). */
  async branches(params: DeployBranchesParams, options: RequestOptions = {}): Promise<WithResponse<DeployBranchList>> {
    return this.#object({ method: "GET", path: "/v1/deployments/branches", query: query(params), options });
  }

  async #object<T extends object>(call: Parameters<Core["json"]>[0]): Promise<WithResponse<T>> {
    const { data, info } = await this.#core.json<T>(call);
    return withResponse((data ?? {}) as T, info);
  }
}
