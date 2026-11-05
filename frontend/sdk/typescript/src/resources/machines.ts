/** `client.machines`: make, follow, run commands in, fork and delete machines. */

import { type Call, type Core, withResponse } from "../core.js";
import { WaitTimeoutError } from "../errors.js";
import { type FileData, fileBody, saveTo, writeTo } from "../files.js";
import { PageIterator } from "../pagination.js";
import { secretNames } from "./secrets.js";
import { given, id, keyFor } from "./shared.js";
import type {
  BackParams,
  Backup,
  BackupListParams,
  CreateOptions,
  ExecParams,
  ExecResult,
  GiveParams,
  Machine,
  MachineBackupParams,
  MachineForkParams,
  MachineMetrics,
  MachineMetricsParams,
  MachineCreateParams,
  MachineRequest,
  MachineListParams,
  MachineStartParams,
  MachineState,
  MachineUpdateParams,
  MachineWaitParams,
  Page,
  ReadFileOptions,
  RequestOptions,
  WithResponse,
  WriteFileOptions,
} from "../types.js";

/**
 * The longest one call waits for a machine (seconds). The API holds a call up to 600 seconds, but Node's fetch
 * waits at most 300 for an answer, so longer waits are asked for in steps no longer than this.
 */
export const LONGEST_HOLD_S = 240;
/** How long the API may hold each kind of call before it answers (seconds). */
const HOLDS_READY = 30; // create, start, fork: wait_s and this
const HOLDS_EXEC = 600;
const HOLDS_ACT = 120; // pause, stop, delete
const HOLDS_KEEP = 1800; // archive, backup
const HOLDS_GIVE = 60; // a fork for a teammate, or a hand-off: carried out in the call for a minute, then behind it

/** A backup as a fork names it: its id, or `latest`; a `Backup` by its id. */
function backupRef(backup: string | Backup | undefined): string | undefined {
  return typeof backup === "object" && backup !== null ? backup.id : backup;
}

export class Machines {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * Makes a machine. It comes back `running` (usually in about a second), or `starting` if it is not ready after
   * `wait_s` (60 by default): then `wait()` follows it. A retry after a lost
   * connection never makes two: the call carries an Idempotency-Key (`options.idempotencyKey`, or one made for it).
   */
  async create(params: MachineCreateParams = {}, options: CreateOptions = {}): Promise<WithResponse<Machine>> {
    const asked = params.wait_s;
    const sent = asked !== undefined && asked > LONGEST_HOLD_S ? LONGEST_HOLD_S : asked;
    const machine = await this.#object<Machine>({
      method: "POST",
      path: "/v1/machines",
      json: given({
        ...params,
        secrets: params.secrets === undefined ? undefined : secretNames(params.secrets),
        wait_s: sent,
      }),
      idempotencyKey: await keyFor(options),
      timeout: this.#readyTimeout(sent),
      options,
    });
    return this.#rest(machine, asked, options);
  }

  async get(machineId: string, options: RequestOptions = {}): Promise<WithResponse<Machine>> {
    return this.#object<Machine>({ method: "GET", path: `/v1/machines/${id(machineId, "machineId")}`, options });
  }

  /** Every machine, newest first, page after page as it is iterated: `for await (const m of machines.list())`. */
  list(params: MachineListParams = {}, options: RequestOptions = {}): PageIterator<Machine> {
    const base: Array<[string, string]> = [];
    if (params.project !== undefined) base.push(["project", params.project]);
    if (params.state !== undefined) base.push(["state", params.state]);
    for (const [name, value] of Object.entries(params.labels ?? {})) base.push(["label", `${name}=${value}`]);
    if (params.limit !== undefined) base.push(["limit", String(params.limit)]);
    return new PageIterator<Machine>(async (after) => {
      const query = after === null ? base : [...base, ["after", after] as [string, string]];
      const { data } = await this.#core.json<Page<Machine>>({ method: "GET", path: "/v1/machines", query, options });
      return { items: data?.data ?? [], next: data?.next || null };
    });
  }

  /**
   * Renames it, changes its labels or says which secrets it gets. Labels merge: a label given `null` is taken off.
   * `secrets` is every secret it is to get: the names take the place of the ones it had, and `[]` takes them away.
   */
  async update(machineId: string, params: MachineUpdateParams, options: RequestOptions = {}): Promise<WithResponse<Machine>> {
    return this.#object<Machine>({
      method: "PATCH",
      path: `/v1/machines/${id(machineId, "machineId")}`,
      json: given({ ...params, secrets: params.secrets === undefined ? undefined : secretNames(params.secrets) }) ?? {},
      options,
    });
  }

  /** Deletes it and its files. Deleting a machine already deleted is not an error. */
  async delete(machineId: string, options: RequestOptions = {}): Promise<void> {
    await this.#core.send({
      method: "DELETE",
      path: `/v1/machines/${id(machineId, "machineId")}`,
      timeout: this.#core.held(HOLDS_ACT),
      options,
    });
  }

  /** Runs it again, from paused, stopped or archived; `starting` when it is not ready after `wait_s`. */
  async start(machineId: string, params: MachineStartParams = {}, options: RequestOptions = {}): Promise<WithResponse<Machine>> {
    const asked = params.wait_s;
    const sent = asked !== undefined && asked > LONGEST_HOLD_S ? LONGEST_HOLD_S : asked;
    const machine = await this.#object<Machine>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/start`,
      json: given({ wait_s: sent }),
      timeout: this.#readyTimeout(sent),
      options,
    });
    return this.#rest(machine, asked, options);
  }

  /**
   * What it used - its CPUs (a share of its own size), memory, disk and network - a point a `step` from `since`
   * on. A point a minute is kept for up to the last day.
   */
  async metrics(machineId: string, params: MachineMetricsParams = {}, options: RequestOptions = {}): Promise<WithResponse<MachineMetrics>> {
    const query: Array<[string, string]> = [];
    if (params.since !== undefined) {
      if (params.since instanceof Date) {
        if (Number.isNaN(params.since.getTime())) throw new TypeError("since is not a valid Date");
        query.push(["since", params.since.toISOString().replace(/\.\d{3}Z$/, "Z")]);
      } else {
        query.push(["since", params.since]);
      }
    }
    if (params.step !== undefined) {
      if (!Number.isInteger(params.step)) throw new TypeError("step is a whole number of seconds");
      query.push(["step", String(params.step)]);
    }
    const { data, info } = await this.#core.json<MachineMetrics>({
      method: "GET",
      path: `/v1/machines/${id(machineId, "machineId")}/metrics`,
      query,
      options,
    });
    return withResponse(data ?? ({} as MachineMetrics), info);
  }

  /** Pauses it with its memory kept: nothing runs and no running time is charged. */
  async pause(machineId: string, options: RequestOptions = {}): Promise<WithResponse<Machine>> {
    return this.#act(machineId, "pause", HOLDS_ACT, options);
  }

  /** Stops it; its files stay. */
  async stop(machineId: string, options: RequestOptions = {}): Promise<WithResponse<Machine>> {
    return this.#act(machineId, "stop", HOLDS_ACT, options);
  }

  /** Archives its files; `start` brings it back. */
  async archive(machineId: string, options: RequestOptions = {}): Promise<WithResponse<Machine>> {
    return this.#act(machineId, "archive", HOLDS_KEEP, options);
  }

  /**
   * Forks it: a new machine of its own, made from it as it is now and at its size - `running`, or `starting` as
   * `create` says. With `backup` - a `Backup`, its id, or `"latest"` for its newest - the fork is made from that
   * backup's files instead, starting afresh, at the size `cpus` asks (2, 4 or 8, with 2 GB of memory each; not for a
   * GPU machine) or the machine's own. `"latest"` of a machine with no backup yet is refused (ConflictError).
   *
   * In an organisation, a member's fork is not made at once: it waits for the OK of a full manager of the machine's team,
   * an owner or an admin, and the answer is the request (a `MachineRequest`: its `object` is `request`, its `holder` and
   * `receiver` both the key's person, its `backup` and `cpus` as asked). Made with a key, as every call of this library
   * is, it waits first for the OK of the person who made the key, signed in to the dashboard (`waiting_on.who` `asker`). Asked again while
   * it waits, it is the one there is. Once it is approved the fork is made, named, labelled and sized as asked, and its
   * id is the request's `result`: `requests.get` follows it. A team whose Forks rule is Off refuses every member's fork
   * (PermissionDeniedError); a full manager's own fork, an owner's and an admin's are made at once. Like `create`, a
   * retry never makes two.
   */
  async fork(
    machineId: string,
    params: MachineForkParams = {},
    options: CreateOptions = {},
  ): Promise<WithResponse<Machine> | WithResponse<MachineRequest>> {
    const asked = params.wait_s;
    const sent = asked !== undefined && asked > LONGEST_HOLD_S ? LONGEST_HOLD_S : asked;
    const answer = await this.#object<Machine | MachineRequest>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/fork`,
      json: given({ ...params, backup: backupRef(params.backup), wait_s: sent }),
      idempotencyKey: await keyFor(options),
      timeout: this.#readyTimeout(sent),
      options,
    });
    // a member's fork that waits for an OK is no machine yet: there is nothing to follow until it is approved
    if (answer.object === "request") return answer;
    return this.#rest(answer, asked, options);
  }

  /** @deprecated `fork`, by the name it had before: the same call, word for word. */
  async copy(
    machineId: string,
    params: MachineForkParams = {},
    options: CreateOptions = {},
  ): Promise<WithResponse<Machine> | WithResponse<MachineRequest>> {
    return this.fork(machineId, params, options);
  }

  /**
   * In an organisation, forks it for a teammate - `to` is their id, e-mail address or GitHub login - by its holder, or
   * an owner or admin of the organisation. The fork is live - its memory, its programs and its screen - and cleared
   * before it is theirs: the browser's tabs stay and open again signed out, while its cookies, saved passwords and
   * sites' data, the apps' and agents' own sign-ins and what signs a person in from a terminal go; the machine itself is
   * untouched. `team` is the team of theirs it goes into, when it is another than the machine's.
   *
   * Made with a key, as every call of this library is, it decides nothing by itself: the answer is the request,
   * `waiting` on the OK of the person who made the key (`waiting_on.who` `asker`), which they give signed in to the dashboard. From then
   * their right counts as it would have, had they given it there - one from an owner or an admin is carried out
   * then, and so is a full manager's - and what else it needs follows: the OK of a full manager of the team, an owner or
   * an admin, as every member's fork needs; an admin's for another team. Done, the fork is in `result`; `failed`,
   * `failure` says why; `requests.get` follows it. A rule of the machine's team that is Off refuses it
   * (PermissionDeniedError), into another team too, unless the key's person is an owner or an admin. Like `create`, a
   * retry never makes two. The fork is then handed to them as a machine is: the person who forked it takes it back (`takeBack`), and
   * they give it back (`giveBack`) and never delete it.
   */
  async forkFor(machineId: string, params: GiveParams, options: CreateOptions = {}): Promise<WithResponse<MachineRequest>> {
    return this.#give(machineId, "fork-for", params, options);
  }

  /**
   * In an organisation, hands off a machine you made to a teammate (`to`) - or, as an owner or admin, one whose holder has
   * left - decided as `forkFor` is. A machine handed to you is given back (`giveBack`), never handed off again. It goes
   * exactly as it is: its programs, terminals, files and every sign-in in it, its browsers' too - so whoever holds it can
   * sign in to Wheelmachine as you through GitHub there: sign out of them first if that is not what you want.
   * Your Wheelmachine keys found in it are revoked once it is theirs (`keys_revoked`), and your sign-ins to the dashboard
   * in it end. What is open into it ends, and every port of it is opened anew so no pass or link from before opens
   * anything. A hand-off arrives paused, its memory kept, so nothing runs on the receiver's month until they start it; you
   * watch it, and take it back whenever you choose (`takeBack`). What it came to this month so far stays the giver's.
   * One hand-off of a machine waits at a time (ConflictError for another).
   */
  async handOff(machineId: string, params: GiveParams, options: CreateOptions = {}): Promise<WithResponse<MachineRequest>> {
    return this.#give(machineId, "hand-off", params, options);
  }

  /**
   * In an organisation, takes back a machine you handed off (its `given_by`), whenever you choose, with nobody's OK: it
   * comes back exactly as it is - what its holder put in it, their sign-ins included - but for their Wheelmachine keys
   * found in it, revoked, and their sign-ins to the dashboard in it, ended; paused, its memory kept. `team` is the team
   * of yours it goes into, when it should be another than the one it is in or was handed off from. Made with a key, as
   * every call of this library is, the request waits on the OK of the person who made the key (`waiting_on.who` `asker`), which they give
   * signed in to the dashboard. Like `create`, a retry never makes two.
   */
  async takeBack(machineId: string, params: BackParams = {}, options: CreateOptions = {}): Promise<WithResponse<MachineRequest>> {
    return this.#back(machineId, "take-back", params, options);
  }

  /**
   * In an organisation, gives a machine handed to you back to the person who handed it off, as `takeBack` takes one back
   * - with what you put in it, but for your Wheelmachine keys found in it, revoked, and your sign-ins to the dashboard in
   * it, ended. A machine handed to you is given back, never handed off again, and not deleted while they may still take
   * it back.
   */
  async giveBack(machineId: string, params: BackParams = {}, options: CreateOptions = {}): Promise<WithResponse<MachineRequest>> {
    return this.#back(machineId, "give-back", params, options);
  }

  async #back(machineId: string, route: string, params: BackParams, options: CreateOptions): Promise<WithResponse<MachineRequest>> {
    return this.#object<MachineRequest>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/${route}`,
      json: given({ team: params?.team }),
      idempotencyKey: await keyFor(options),
      timeout: this.#core.held(HOLDS_GIVE),
      options,
    });
  }

  async #give(machineId: string, route: string, params: GiveParams, options: CreateOptions): Promise<WithResponse<MachineRequest>> {
    if (typeof params?.to !== "string" || params.to === "") {
      throw new TypeError("to is the teammate: their id, e-mail address or GitHub login");
    }
    return this.#object<MachineRequest>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/${route}`,
      json: given({ to: params.to, team: params.team, why: params.why }),
      idempotencyKey: await keyFor(options),
      timeout: this.#core.held(HOLDS_GIVE),
      options,
    });
  }

  /**
   * Backs it up: keeps its files as they are now, not what runs in it, with a `note` (200 characters at most).
   * `fork(machineId, { backup })` makes a new machine from the backup, at the machine's size or another. The person
   * who holds the machine backs it up, and in an organisation its owners and admins. Like `create`, a retry never
   * makes two.
   */
  async backup(machineId: string, params: MachineBackupParams = {}, options: CreateOptions = {}): Promise<WithResponse<Backup>> {
    return this.#object<Backup>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/backups`,
      json: given(params),
      idempotencyKey: await keyFor(options),
      timeout: this.#core.held(HOLDS_KEEP),
      options,
    });
  }

  /**
   * A machine's backups, newest first - or, with no machine, every backup the key reaches, of the machines that are not
   * deleted: `backups(machine.id)`, `backups()`, `backups(undefined, { limit: 10 })`. In an organisation, those taken
   * before a machine was last handed off are its owners' and admins' alone. `limit` is how many at most (1 to 200; 100).
   */
  async backups(machineId?: string, params: BackupListParams = {}, options: RequestOptions = {}): Promise<Backup[]> {
    const path = machineId === undefined ? "/v1/backups" : `/v1/machines/${id(machineId, "machineId")}/backups`;
    const query: Array<[string, string]> = params.limit === undefined ? [] : [["limit", String(params.limit)]];
    const { data } = await this.#core.json<Page<Backup>>({ method: "GET", path, query, options });
    return data?.data ?? [];
  }

  /**
   * Answers when the machine is in `state` (at once when it already is). Throws WaitTimeoutError when it is not
   * there after `timeout_s` seconds (60). Long waits are asked for in steps, each one call.
   */
  async wait(
    machineId: string,
    state: MachineState = "running",
    params: MachineWaitParams = {},
    options: RequestOptions = {},
  ): Promise<WithResponse<Machine>> {
    const total = params.timeout_s ?? 60;
    if (!(total >= 0)) throw new TypeError("timeout_s is a number of seconds, 0 or more");
    const path = `/v1/machines/${id(machineId, "machineId")}/wait`;
    const deadline = Date.now() + total * 1000;
    for (;;) {
      const left = Math.max(0, (deadline - Date.now()) / 1000);
      const step = Math.min(left, LONGEST_HOLD_S);
      const began = Date.now();
      try {
        return await this.#object<Machine>({
          method: "GET",
          path,
          query: [
            ["state", state],
            ["timeout_s", String(Math.round(step * 1000) / 1000)],
          ],
          timeout: this.#core.held(step),
          options,
        });
      } catch (err) {
        // the API waited the whole step and there is time left: ask again. An early answer (the machine was
        // deleted, say) is the answer.
        if (!(err instanceof WaitTimeoutError) || left <= LONGEST_HOLD_S || Date.now() - began < (step - 1) * 1000) throw err;
      }
    }
  }

  /**
   * Runs a command and gives what it wrote: `exec(id, ["python3", "-c", "print(2+2)"])` runs the words with no
   * shell, `exec(id, { shell: "ls -la | wc -l" })` a line for `sh -lc`. It runs in the work folder as the machine's
   * user unless `cwd` or `user: "root"` says otherwise; a paused machine is woken for it. It runs at most 10
   * minutes (in Node, an answer is waited for 300 seconds); a call that loses its connection is not sent again,
   * since the command may have run.
   */
  async exec(machineId: string, params: ExecParams | string[], options: RequestOptions = {}): Promise<WithResponse<ExecResult>> {
    const p: ExecParams = Array.isArray(params) ? { command: params } : params;
    if (typeof (p as { command?: unknown }).command === "string") {
      throw new TypeError('command is a list of words, such as ["ls", "-la"]; a line for the shell goes in shell');
    }
    if ((p.command === undefined) === (p.shell === undefined)) {
      throw new TypeError("give command (a list of words) or shell (a line for sh), one of the two");
    }
    const body = given({ ...p, binary_output: p.binary_output ? true : undefined }) ?? {};
    return this.#object<ExecResult>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/exec`,
      json: body,
      timeout: this.#core.held(HOLDS_EXEC),
      options,
    });
  }

  /**
   * Reads a file in the machine: `home/...` (its home) or `work/...` (where commands start). Gives its bytes, or
   * with `to` streams it into that local path (written whole or not at all: the bytes go to a file beside it that
   * takes its name at the end) or WritableStream, and gives how many bytes came.
   */
  readFile(machineId: string, path: string, options?: RequestOptions & { to?: undefined }): Promise<Uint8Array>;
  readFile(machineId: string, path: string, options: RequestOptions & { to: string | WritableStream<Uint8Array> }): Promise<number>;
  readFile(machineId: string, path: string, options?: ReadFileOptions): Promise<Uint8Array | number>;
  async readFile(machineId: string, path: string, options: ReadFileOptions = {}): Promise<Uint8Array | number> {
    const { to, ...rest } = options;
    const answer = await this.#core.send({
      method: "GET",
      path: `/v1/machines/${id(machineId, "machineId")}/files`,
      query: [["path", path]],
      accept: "application/octet-stream",
      options: rest,
      stream: to !== undefined,
    });
    if (to === undefined) return answer.bytes;
    const idle = rest.timeout ?? this.#core.timeout;
    try {
      return typeof to === "string"
        ? await saveTo(answer.response, to, idle, this.#core.baseURL)
        : await writeTo(answer.response, to, idle, this.#core.baseURL);
    } finally {
      answer.finish();
    }
  }

  /**
   * Writes a file in the machine (`home/...` or `work/...`), making the folders on the way, streamed with its
   * length: `data` is bytes, a Blob, a file on this computer as `{ path }`, or a ReadableStream or async iterable
   * of bytes with `contentLength`. At most 5 GiB a call.
   */
  async writeFile(machineId: string, path: string, data: FileData, options: WriteFileOptions = {}): Promise<void> {
    const { contentLength, ...rest } = options;
    await this.#core.send({
      method: "PUT",
      path: `/v1/machines/${id(machineId, "machineId")}/files`,
      query: [["path", path]],
      body: fileBody(data, contentLength),
      options: rest,
    });
  }

  async #object<T extends object>(call: Call): Promise<WithResponse<T>> {
    const { data, info } = await this.#core.json<T>(call);
    return withResponse((data ?? {}) as T, info);
  }

  async #act(machineId: string, action: string, holds: number, options: RequestOptions): Promise<WithResponse<Machine>> {
    return this.#object<Machine>({
      method: "POST",
      path: `/v1/machines/${id(machineId, "machineId")}/${action}`,
      timeout: this.#core.held(holds),
      options,
    });
  }

  #readyTimeout(waitS: number | undefined): number {
    return this.#core.held((waitS ?? 60) + HOLDS_READY);
  }

  /** A wait longer than one call can hold: the rest of it, followed with `wait()`; still `starting` after it all,
   * the machine is given back as it is, as the API itself does. */
  async #rest(machine: WithResponse<Machine>, asked: number | undefined, options: RequestOptions): Promise<WithResponse<Machine>> {
    if (asked === undefined || asked <= LONGEST_HOLD_S || machine.state !== "starting") return machine;
    try {
      return await this.wait(machine.id, "running", { timeout_s: asked - LONGEST_HOLD_S }, options);
    } catch (err) {
      if (err instanceof WaitTimeoutError) return machine;
      throw err;
    }
  }
}
