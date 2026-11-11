// A stand-in for the Wheelmachine API on 127.0.0.1, for the tests: no network.
//
// It answers the way the API does - problem+json errors with a code and the call's id, X-Request-Id and RateLimit
// headers, pages of {object, data, next} followed with ?after=, 201/202 on create, Idempotency-Key replays - and
// refuses a field it does not know, so a test fails when the library sends something the API would refuse. Faults can be
// set on a route: a status, an answer that never comes (before or after the call was acted on), or a slow answer.

import { createHash, randomBytes } from "node:crypto";
import http from "node:http";

import { Wheelmachine } from "../dist/index.js";

export const KEY = "ak_live_" + "a".repeat(40);
export const REVOKED_KEY = "ak_live_" + "r".repeat(40);

const TITLES = {
  invalid_request: "The request is not right",
  unauthorized: "No key, or a key that does not work",
  key_revoked: "This key was revoked",
  not_found: "Not found",
  conflict: "Not possible in the state it is in",
  project_not_empty: "The project still holds machines",
  rate_limited: "Too many calls just now",
  limit_reached: "A limit is reached",
  unavailable: "Not available just now",
  timeout: "Not in time",
  not_open: "Not open yet",
  no_apps: "This machine has no apps",
  login_denied: "The sign-in was refused",
  login_expired: "The sign-in's code is no longer good",
};

/** the apps a machine made since apps came carries, as each is when the machine is new */
const APPS = [
  { name: "desktop", title: "Desktop", kind: "web", port: 6901, path: "/vnc.html?autoconnect=1&resize=remote&reconnect=1", on: false, running: false, command: null },
  { name: "editor", title: "Code editor", kind: "web", port: 13337, path: "/", on: false, running: false, command: null },
  { name: "openclaw", title: "OpenClaw", kind: "web", port: 18789, path: "/", on: false, running: false, command: null },
  { name: "ollama", title: "Ollama", kind: "service", port: null, path: null, on: false, running: false, command: null },
  { name: "codex", title: "Codex", kind: "agent", port: null, path: null, on: true, running: false, command: "codex" },
  { name: "gemini", title: "Gemini CLI", kind: "terminal", port: null, path: null, on: null, running: null, command: "gemini" },
];
/** what turning openclaw on answers in `token`: made up, nobody's */
export const APP_TOKEN = "oc" + "0123456789abcdef".repeat(2);

// A part of the API's table of AI keys (GET /v1/secrets/presets), as much of it as the tests ask.
const PRESETS = [
  { object: "secret_preset", id: "anthropic", title: "Anthropic", names: ["ANTHROPIC_API_KEY"], hosts: ["api.anthropic.com"],
    headers: [{ name: "x-api-key", format: "{value}" }, { name: "authorization", format: "Bearer {value}" }], query: [] },
  { object: "secret_preset", id: "gemini", title: "Gemini", names: ["GEMINI_API_KEY", "GOOGLE_API_KEY"], hosts: ["generativelanguage.googleapis.com"],
    headers: [{ name: "x-goog-api-key", format: "{value}" }, { name: "authorization", format: "Bearer {value}" }], query: ["key"] },
];

/** the models of the account's AI keys, and the agents of its machines, as GET /v1/models and /v1/agents answer them */
const MODELS = [
  { object: "model", id: "openai/gpt-6.1-sol", supplier: "openai", model: "gpt-6.1-sol", name: "gpt-6.1-sol", created_at: null, retiring_at: null,
    agents: ["codex", "aider"],
    price: { input_per_million_usd: 2, output_per_million_usd: 10, source: "OpenAI", url: "https://developers.openai.com/api/docs/pricing", as_of: "2026-11-02" },
    intelligence: { score: 1481, source: "LMArena text leaderboard", url: "https://lmarena.ai/leaderboard/text", license: "CC BY 4.0", as_of: "2026-10-28" } },
  { object: "model", id: "deepseek/deepseek-v4-pro", supplier: "deepseek", model: "deepseek-v4-pro", name: "DeepSeek-V4-Pro", created_at: null,
    retiring_at: null, agents: ["aider"],
    price: { input_per_million_usd: 1.32, output_per_million_usd: 3.96, source: "DeepSeek", url: "https://api-docs.deepseek.com/quick_start/pricing", as_of: "2026-11-02" },
    intelligence: null },
];
const SUPPLIERS = [
  { object: "model_supplier", id: "openai", title: "OpenAI", key: "OPENAI_API_KEY", whose: "account", state: "listed", listed_at: "2026-10-23T07:00:00Z",
    detail: null },
  { object: "model_supplier", id: "deepseek", title: "DeepSeek", key: "DEEPSEEK_API_KEY", whose: "account", state: "unreachable",
    listed_at: "2026-10-23T06:20:00Z", detail: "DeepSeek did not answer; this list is from 40 minutes ago." },
];
const AGENTS = [
  { name: "codex", title: "Codex", suppliers: [{ id: "openai", title: "OpenAI", default: null }] },
  { name: "aider", title: "Aider", suppliers: [{ id: "openai", title: "OpenAI", default: "gpt-6.1-sol" }, { id: "deepseek", title: "DeepSeek", default: null }] },
];

const CREATES = [
  ["POST", /^\/v1\/machines$/],
  ["POST", /^\/v1\/machines\/[^/]+\/(fork|copy)$/],
  ["POST", /^\/v1\/machines\/[^/]+\/(fork-for|hand-off|take-back|give-back)$/],
  ["POST", /^\/v1\/requests$/],
  ["POST", /^\/v1\/machines\/[^/]+\/backups$/],
  ["POST", /^\/v1\/machines\/[^/]+\/pull-requests$/],
  ["POST", /^\/v1\/projects$/],
  ["POST", /^\/v1\/keys$/],
];

class Problem extends Error {
  constructor(status, code, detail, retryAfter, confirm) {
    super(detail);
    this.status = status;
    this.code = code;
    this.detail = detail;
    this.retryAfter = retryAfter;
    this.confirm = confirm;
  }
}

function only(body, allowed) {
  if (body === undefined || body === null) return {};
  if (typeof body !== "object" || Array.isArray(body)) throw new Problem(400, "invalid_request", "the body is not JSON");
  for (const k of Object.keys(body)) {
    if (!allowed.includes(k)) throw new Problem(400, "invalid_request", `no field "${k}" here`);
  }
  return body;
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const parts = [];
    req.on("data", (c) => parts.push(c));
    req.on("end", () => resolve(Buffer.concat(parts)));
    req.on("error", reject);
  });
}

const pause = (ms) => new Promise((r) => setTimeout(r, ms));

export class FakeAPI {
  constructor() {
    this.seen = [];
    this.faults = [];
    this.machines = new Map();
    /** the credit as GET /v1/credit answers it: what is left, and its entries, the newest first; and whether credit can
     * be bought (POST /v1/credit/purchases) */
    this.buyOpen = false;
    /** the credit's balance, in the account's currency (`currency`; fixed once `currencyFixed`), and each currency's
     * units to a dollar, as the API says its `*_usd` amounts by */
    this.creditUSD = 5;
    this.currency = "USD";
    this.currencyFixed = false;
    this.rates = { USD: 1, GBP: 0.8, EUR: 0.9 };
    this.creditEntries = [{ id: "ce_" + "w".repeat(26), object: "credit_entry", kind: "welcome", amount_usd: 5,
      reason: "Free credit to start with", machine: null, machine_name: null, at: "2026-10-23T12:00:00Z", updated_at: "2026-10-23T12:00:00Z" }];
    this.projects = new Map();
    this.keys = new Map();
    this.files = new Map();
    // My files' files, by path
    this.folderFiles = new Map();
    /** every backup taken, the oldest first */
    this.backups = [];
    /** what a machine reaches of its team's repositories, as GET /v1/machines/{id}/repositories answers them */
    this.repositories = [];
    /** by name: the value as it reached the API (never in an answer), and when it was made and set */
    this.secrets = new Map();
    /** the model chosen, by agent ("*": every agent), as GET /v1/agents shows it */
    this.chosen = new Map();
    /** the organisation's lists of the models its people may use, as /v1/models/allowed keeps them; and the one that holds
     * the key's own choices, which the models listed, the agents and a choice stand by */
    this.allowed = [];
    this.heldTo = null;
    /** each machine's points, by its id, as GET /v1/machines/{id}/metrics answers them */
    this.metrics = new Map();
    this.openPorts = new Map();
    /** each machine's apps, by name, made as APPS the first time they are asked for; the machines with no apps
     * came, which have none; and the ports an app opened as it was turned on, by machine and port: the app's name */
    this.apps = new Map();
    this.oldMachines = new Set();
    this.appPorts = new Map();
    /** the links made, without their secrets */
    this.links = [];
    this.idem = new Map();
    this.execs = [];
    this.screenActions = [];
    this.connections = 0;
    /** close every connection after its answer without saying so, as a server that times idle ones out does */
    this.closeAfterAnswer = false;
    /** every new machine answers 202, still starting, until a wait finds it running */
    this.startSlowly = false;
    /** false: web addresses are not served yet, and opening a port or making a link answers 503 not_open */
    this.addressesServed = true;
    /** programs signing in through the browser, by device code; `decide` says what the person did at each ask (from 1) */
    this.logins = new Map();
    this.decide = () => "approved";
    /** the organisation the one key that works was made in, with the person who made it, their role, teams and limits
     * there ({ id, name, person, role, teams, limits }); null for a key of a person's own account */
    this.org = null;
    /** what GET /v1/orgs answers a key of a person's own account */
    this.orgs = [];
    /** the account's events, the newest first, as GET /v1/audit pages them and GET /v1/audit/export gives them */
    this.audit = [];
    /** the organisation's other people a machine is given to, or asked of, by id ({ id, name, email, github_login }); the
     * requests to fork a machine for one of them or hand it off, by id; each team's rules as set, by its project; and
     * whether a fork for someone or a hand-off waits for a manager - as the rules' Ask has it - or is done at once */
    this.people = new Map();
    this.machineRequests = new Map();
    this.rules = new Map();
    this.givesWait = false;
    this.n = 0;
    this.sockets = new Set();
    this.deployClosed = false;
    this.deployStuck = false;
    // how many looks at a new deployment of files find it still getting its machine ready: files sent until then are refused
    this.deployFilesAfter = 0;
    this.deployments = [];
    this.deployRuns = new Map();
    this.deploySent = new Map();
    this.deployRepos = [];
    this.firstProject = this.addProject("Default").id;
    this.server = http.createServer((req, res) => {
      this.handle(req, res).catch((err) => {
        res.destroy(err);
      });
    });
    this.server.on("connection", (socket) => {
      this.connections += 1;
      socket.setNoDelay(true);
      this.sockets.add(socket);
      socket.on("close", () => this.sockets.delete(socket));
    });
  }

  async start() {
    await new Promise((resolve) => this.server.listen(0, "127.0.0.1", resolve));
    return this;
  }

  async stop() {
    for (const s of this.sockets) s.destroy();
    await new Promise((resolve) => this.server.close(resolve));
  }

  get url() {
    return `http://127.0.0.1:${this.server.address().port}`;
  }

  fault(method, path, opts = {}) {
    const f = { method, pattern: new RegExp(path), times: 1, ...opts };
    this.faults.push(f);
    return f;
  }

  requests(method, path) {
    const re = new RegExp(path);
    return this.seen.filter((s) => s.method === method && re.test(s.path));
  }

  next() {
    this.n += 1;
    return this.n;
  }

  addProject(name) {
    const p = { id: `prj_${String(this.next()).padStart(26, "0")}`, object: "project", name, created_at: "2026-10-23T07:00:00Z" };
    this.projects.set(p.id, p);
    return p;
  }

  addMachine({ name = "", project, labels, state = "running" } = {}) {
    const m = {
      id: `m_${String(this.next()).padStart(26, "0")}`,
      object: "machine",
      name,
      project: project ?? this.firstProject,
      state,
      gpu: null,
      size: { cpus: 2, memory_gb: 4, disk_gb: 32 },
      location: { city: "Helsinki", country: "FI" },
      cpus: 2,
      memory_gb: 4,
      sleep_when_idle: true,
      idle_after_s: 1800,
      labels: { ...(labels ?? {}) },
      secrets: [],
      network: { internet: "open", allow: [] },
      files_sealed: true,
      running_seconds: 1.5,
      created_at: "2026-10-23T07:00:00Z",
      last_used_at: null,
      made_by: this.madeBy(),
      held_by: this.heldBy(),
    };
    this.machines.set(m.id, m);
    return m;
  }

  /** Who what is made now is said to be made by: in an organisation, the person who made the key. */
  madeBy() {
    return this.org ? this.org.person.id : null;
  }

  /** Whose what is made now is said to be: in an organisation, the person who made the key, with their role there. */
  heldBy() {
    return this.org ? { id: this.org.person.id, name: this.org.person.name, role: this.org.role, level: null } : null;
  }

  keyView(n = 1) {
    return {
      id: `key_${n.toString(16).padStart(16, "0")}`,
      object: "key",
      name: "test",
      scopes: ["cloud:*"],
      projects: [],
      shown: "ak_live_aaaa…aaaa",
      state: "active",
      rate_per_minute: null,
      created_at: "2026-10-23T07:00:00Z",
      expires_at: null,
      last_used_at: null,
      made_by: this.madeBy(),
    };
  }

  problem(rid, p) {
    const doc = {
      type: `https://docs.wheelmachine.ai/errors/${p.code}`,
      title: TITLES[p.code] ?? "",
      status: p.status,
      detail: p.detail,
      code: p.code,
      request_id: rid,
    };
    const extra = { "content-type": "application/problem+json" };
    if (p.confirm) doc.confirm = p.confirm;
    if (p.notRun) doc.not_run = true;
    if (p.retryAfter) {
      doc.retry_after_s = p.retryAfter;
      extra["retry-after"] = String(Math.ceil(p.retryAfter));
    }
    return [p.status, doc, extra];
  }

  send(req, res, rid, status, payload, extra = {}, close = false) {
    let data;
    const headers = { "x-request-id": rid, "ratelimit-policy": '"key";q=600;w=60', ratelimit: '"key";r=599;t=1', ...extra };
    if (Buffer.isBuffer(payload)) {
      data = payload;
      headers["content-type"] ??= "application/octet-stream";
    } else if (payload === null || payload === undefined) {
      data = Buffer.alloc(0);
    } else {
      data = Buffer.from(JSON.stringify(payload));
      headers["content-type"] ??= "application/json";
    }
    if (close) headers.connection = "close";
    if (status !== 204) headers["content-length"] = String(data.length);
    res.writeHead(status, headers);
    if (this.closeAfterAnswer) res.on("finish", () => req.socket.end());
    res.end(status === 204 ? undefined : data);
  }

  async handle(req, res) {
    const url = new URL(req.url, "http://fake");
    const path = url.pathname;
    const method = req.method;
    const rid = `req_${String(this.next()).padStart(6, "0")}`;
    const headers = req.headers;
    const files = /^\/v1\/machines\/([^/]+)\/files$/.exec(path);
    if (method === "PUT" && files && !this.machines.has(files[1])) {
      // refused before the body is read, as the API does, and the connection closed
      this.seen.push({ method, path, query: url.searchParams, headers, body: Buffer.alloc(0), rid });
      this.send(req, res, rid, ...this.problem(rid, new Problem(404, "not_found", "no such machine")), true);
      return;
    }
    if ((headers["transfer-encoding"] ?? "").includes("chunked")) {
      await readBody(req);
      this.seen.push({ method, path, query: url.searchParams, headers, body: Buffer.alloc(0), rid });
      this.send(req, res, rid, ...this.problem(rid, new Problem(411, "invalid_request", "say how long the body is")), true);
      return;
    }
    const body = await readBody(req);
    const seen = { method, path, query: url.searchParams, headers, body, rid, json: () => (body.length ? JSON.parse(body) : undefined) };
    this.seen.push(seen);

    // a program signing in has no key yet
    const signingIn = method === "POST" && (path === "/v1/login/devices" || path === "/v1/login/devices/token");
    if (!signingIn && headers.authorization !== `Bearer ${KEY}`) {
      const code = headers.authorization === `Bearer ${REVOKED_KEY}` ? "key_revoked" : "unauthorized";
      this.send(req, res, rid, ...this.problem(rid, new Problem(401, code, "this key is not known")));
      return;
    }

    const fault = this.faults.find((f) => f.times > 0 && f.method === method && f.pattern.test(path));
    if (fault) {
      fault.times -= 1;
      if (fault.delay) await pause(fault.delay);
      if (fault.drop === "before") {
        req.socket.destroy();
        return;
      }
      if (fault.raw) {
        this.send(req, res, rid, fault.status ?? 502, Buffer.from(fault.raw[1]), { "content-type": fault.raw[0] });
        return;
      }
      if (fault.status) {
        const p = new Problem(fault.status, fault.code ?? "conflict", fault.detail ?? "a fault set by the test", fault.retryAfter);
        p.notRun = fault.notRun;
        this.send(req, res, rid, ...this.problem(rid, p));
        return;
      }
    }

    const [status, payload, extra] = this.idempotent(method, path, url.searchParams, headers, body, rid);
    if (fault?.drop === "after") {
      req.socket.destroy(); // acted on, and the answer lost
      return;
    }
    this.send(req, res, rid, status, payload, extra);
  }

  idempotent(method, path, query, headers, body, rid) {
    const key = headers["idempotency-key"];
    const create = CREATES.some(([m, p]) => m === method && p.test(path));
    if (!key || !create) return this.route(method, path, query, headers, body, rid);
    const digest = createHash("sha256").update(`${method} ${path}\n`).update(body).digest("hex");
    const kept = this.idem.get(key);
    if (kept) {
      if (kept.digest !== digest) {
        return this.problem(rid, new Problem(422, "invalid_request", "this Idempotency-Key was used with a different request"));
      }
      return [kept.status, kept.payload, { ...kept.extra, "idempotent-replayed": "true" }];
    }
    const [status, payload, extra] = this.route(method, path, query, headers, body, rid);
    if (status < 500 && status !== 429) {
      this.idem.set(key, { digest, status, payload, extra: extra.location ? { location: extra.location } : {} });
    }
    return [status, payload, extra];
  }

  // an amount in the account's currency, in dollars at its rate - as the API says each `*_usd` amount
  usd(v) {
    return v === null || v === undefined ? v : Math.round((v / this.rates[this.currency]) * 1e6) / 1e6;
  }

  // GET /v1/usage/limits as the API answers it, with the account's own limit as last set
  limitsAnswer() {
    const cap = this.ownLimit ?? null;
    const percent = cap ? Math.floor((12.5 / cap) * 100) : null;
    return {
      object: "usage_limits", month: "2026-10", currency: this.currency, resets_at: "2026-11-01T00:00:00Z", alert_percents: [50, 75, 90, 100],
      account: { name: "Test", kind: "person", currency: this.currency, month_cap: cap, month_cap_usd: this.usd(cap), in_force_amount: cap,
        in_force_usd: this.usd(cap), in_force: cap === null ? "" : "account", spent: 12.5, spent_usd: this.usd(12.5), percent,
        limited: cap !== null, set_by: null, set_at: null, wheelmachine: { month_cap: null, month_cap_usd: null, max_machines: 100, max_vcpus: null },
        changed_by: ["owner"], you_change: true },
      teams: [], people: [], yours: cap === null ? [] : [{ scope: "account", name: "Test", limit: cap, limit_usd: this.usd(cap), spent: 12.5,
        spent_usd: this.usd(12.5), percent, reached: false }],
      passed: [],
    };
  }

  // GET /v1/credit as the API answers it: its amounts in the account's currency, and in dollars beside them
  creditAnswer(data, next) {
    const buy = this.buyOpen ? { open: true, amounts: [10, 25, 50, 100], amounts_usd: [10, 25, 50, 100], paddle: { environment: "sandbox", client_token: "test_madeup0123" } }
      : { open: false, amounts: [], amounts_usd: [], paddle: null };
    const entries = data.map((e) => ({ ...e, amount: e.amount ?? e.amount_usd, amount_usd: this.usd(e.amount ?? e.amount_usd) }));
    return { object: "credit", account: "acct_test", currency: this.currency, currency_fixed: this.currencyFixed, balance: this.creditUSD,
      balance_usd: this.usd(this.creditUSD), empty: this.creditUSD <= 0, buy, low: 1, low_usd: this.usd(1), data: entries, next };
  }

  route(method, path, query, headers, raw, rid) {
    let body;
    try {
      body = raw.length && (headers["content-type"] ?? "").startsWith("application/json") ? JSON.parse(raw) : undefined;
    } catch {
      return this.problem(rid, new Problem(400, "invalid_request", "the body is not JSON"));
    }
    try {
      // (the call's headers, for the routes that read one: a risky step's code)
      this.headers = headers;
      const [status, payload, extra] = this.routes(method, path, query, body, raw);
      return [status, this.withAIKeys(payload), extra];
    } catch (err) {
      if (err instanceof Problem) return this.problem(rid, err);
      throw err;
    }
  }

  page(query) {
    const limit = Number(query.get("limit") ?? "50");
    if (!(limit >= 1 && limit <= 200)) throw new Problem(400, "invalid_request", "limit is between 1 and 200");
    return [limit, query.get("after")];
  }

  /** Backups as the API lists them, the newest first and as many as `limit` (100 when left out): one machine's, or
   * every machine's; a deleted machine's go with it. */
  backupsOf(query, machine) {
    const limit = Number(query.get("limit") ?? "100");
    if (!(limit >= 1 && limit <= 200)) throw new Problem(400, "invalid_request", "limit is between 1 and 200");
    return [...this.backups].reverse()
      .filter((b) => (machine === undefined || b.machine === machine) && this.machines.get(b.machine).state !== "deleted")
      .slice(0, limit);
  }

  machine(id) {
    const m = this.machines.get(id);
    if (!m) throw new Problem(404, "not_found", "no such machine");
    return m;
  }

  inOrg() {
    if (!this.org) throw new Problem(409, "conflict", "Only an organisation's machines are handed off");
  }

  /** A person of the organisation by their id, e-mail address or GitHub login, as the API finds them. */
  teammate(who) {
    for (const p of [...this.people.values(), ...(this.org ? [this.org.person] : [])]) {
      if (who && [p.id, p.email, p.github_login].includes(who)) return { id: p.id, name: p.name };
    }
    throw new Problem(404, "not_found", "no such teammate");
  }

  rulesOf(project) {
    return { object: "team_rules", project, hand_offs: "ask", forks: "ask", gpus: true, open_internet: true, month_budget_usd: null,
      spent_usd: 0, open_machines: 0, models: "any", set_by: null, set_at: null, ...(this.rules.get(project) ?? {}) };
  }

  newRequest(kind, way, mach, holder, receiver, why) {
    const me = this.org.person;
    const q = { id: `rq_${String(this.next()).padStart(26, "0")}`, object: "request", kind, way,
      machine: mach ? { id: mach.id, name: mach.name } : null, holder: { id: holder.id, name: holder.name },
      receiver: { id: receiver.id, name: receiver.name }, asked_by: { id: me.id, name: me.name }, from_team: mach ? mach.project : null,
      to_team: null, gpu: false, why, state: "waiting", waiting_on: null, decided_by: null, decision_why: "", result: null, backup: null,
      cpus: null, failure: null, restarted: false, keys_revoked: 0, created_at: "2026-10-27T07:00:00Z", expires_at: "2026-10-28T07:00:00Z",
      done_at: null };
    this.machineRequests.set(q.id, q);
    return q;
  }

  /** A machine forked for a teammate, or handed off to them, with a key: waiting on the OK of the person who made the key (approve()), then
   * done, or waiting on a manager when gives wait; refused where the team's rules say Off, and for the person who holds
   * it already. */
  give(mach, kind, body) {
    this.inOrg();
    const b = only(body, ["to", "team", "why"]);
    if (!b.to) throw new Problem(400, "invalid_request", "to is the teammate: their id, e-mail address or GitHub login");
    const receiver = this.teammate(b.to);
    const holder = mach.held_by ?? { id: "", name: "" };
    if (receiver.id === holder.id) throw new Problem(409, "conflict", "it is theirs already");
    const rule = this.rulesOf(mach.project)[kind === "fork" ? "forks" : "hand_offs"];
    if (rule === "off") {
      throw new Problem(403, "forbidden", `${kind === "fork" ? "Forks" : "Hand-offs"}: Off in ${this.projects.get(mach.project).name}`);
    }
    for (const r of this.machineRequests.values()) {
      if (r.state === "waiting" && r.kind === kind && r.machine?.id === mach.id && r.receiver.id === receiver.id) return [200, r, {}];
    }
    const q = this.newRequest(kind, "give", mach, holder, receiver, b.why ?? "");
    if (b.team) {
      if (!this.projects.has(b.team)) throw new Problem(404, "not_found", "no such project of theirs");
      q.to_team = b.team;
    }
    q.waiting_on = { who: "asker", name: q.asked_by.name };
    return [201, q, { location: `/v1/requests/${q.id}` }];
  }

  /** A machine handed off, back with the one who handed it off - taken back by them, or given back by its holder - with a
   * key: waiting on the OK of the person who made the key (approve()); refused for one handed off by nobody, and for its holder taking it
   * back. */
  back(mach, taking, body) {
    this.inOrg();
    const b = only(body ?? {}, ["team"]);
    const me = { id: this.org.person.id, name: this.org.person.name };
    const holder = mach.held_by ?? { id: "", name: "" };
    if (taking && holder.id === me.id) throw new Problem(409, "conflict", "it is yours already");
    if (taking && mach.given_by?.id !== me.id) throw new Problem(403, "forbidden", "only the person who handed this machine off takes it back");
    if (!taking && !mach.given_by) throw new Problem(409, "conflict", "it was not handed to you: there is nobody to give it back to");
    const q = this.newRequest("hand_off", "back", mach, holder, mach.given_by ?? me, "");
    if (b.team) q.to_team = b.team;
    q.waiting_on = { who: "asker", name: q.asked_by.name };
    return [201, q, { location: `/v1/requests/${q.id}` }];
  }

  /** The backup a fork is asked from, by its id - the machine's newest for `latest` - checked with the size asked of it
   * as the API checks them; null for a fork of the machine as it is, which keeps its size. */
  forkFrom(mach, b) {
    if (b.cpus === 16) throw new Problem(400, "invalid_request", "16 CPUs are not offered yet: choose 2, 4 or 8");
    if (b.cpus !== undefined && ![2, 4, 8].includes(b.cpus)) throw new Problem(400, "invalid_request", "cpus is 2, 4 or 8");
    if (b.backup === undefined) {
      if (b.cpus !== undefined) {
        throw new Problem(400, "invalid_request", "cpus is only for a fork from a backup: a fork of the machine as it is now keeps its size");
      }
      return null;
    }
    const its = [...this.backups].reverse().find((x) => x.machine === mach.id && (b.backup === "latest" || x.id === b.backup));
    if (its) return its.id;
    if (b.backup !== "latest") throw new Problem(404, "not_found", "no such backup");
    throw new Problem(409, "conflict", "no backup of it yet: back it up first");
  }

  /** A member's fork of a machine they hold - of it as it is, or from one of its backups - but a full manager's of its
   * team, with a key: nothing made, but a request of theirs to them, its backup and size as asked, answered 202 and
   * waiting on the OK of the person who made the key (approve()), then on a full manager's; the one there is when asked again while it
   * waits; refused where the team's rules say Off. */
  ownFork(mach, backup, cpus) {
    const me = { id: this.org.person.id, name: this.org.person.name };
    if (this.rulesOf(mach.project).forks === "off") throw new Problem(403, "forbidden", `Forks: Off in ${this.projects.get(mach.project).name}`);
    for (const r of this.machineRequests.values()) {
      if (r.state === "waiting" && r.kind === "fork" && r.machine?.id === mach.id && r.receiver.id === me.id) return [200, r, {}];
    }
    const q = this.newRequest("fork", "give", mach, me, me, "");
    Object.assign(q, { backup, cpus, waiting_on: { who: "asker", name: me.name } });
    return [202, q, { location: `/v1/requests/${q.id}` }];
  }

  /** The OK of the person who made the key, of a request that waits for it, as they give it signed in to the dashboard: an ask then waits
   * for its holder's pick; a member's own fork for a full manager; a hand-off for a manager when gives wait and the team's
   * rules ask, else it is carried out - a fork made for its receiver, or the machine theirs, paused. */
  approve(requestId) {
    const q = this.machineRequests.get(requestId);
    if (q?.state !== "waiting" || q.waiting_on?.who !== "asker") throw new Error(`no request ${requestId} waits for the person who made the key`);
    if (q.way === "ask") {
      q.waiting_on = { who: "holder", name: q.holder.name };
      return q;
    }
    if (q.kind === "fork" && q.holder.id === q.receiver.id) {
      // (every member's fork waits for a full manager, whatever givesWait says)
      q.waiting_on = { who: "manager", name: null };
      return q;
    }
    const mach = this.machines.get(q.machine.id);
    const rule = this.rulesOf(mach.project)[q.kind === "fork" ? "forks" : "hand_offs"];
    if (this.givesWait && rule === "ask") {
      q.waiting_on = { who: "manager", name: null };
      return q;
    }
    const given = { id: q.receiver.id, name: q.receiver.name, role: "member", level: null };
    const origin = { kind: q.kind, from: q.holder, machine_name: mach.name, at: "2026-10-27T07:00:00Z" };
    if (q.kind === "fork") {
      const made = this.addMachine({ name: `${mach.name}-fork`, project: q.to_team ?? mach.project, labels: mach.labels });
      Object.assign(made, { held_by: given, origin });
      q.result = made.id;
    } else if (q.way === "back") {
      Object.assign(mach, { held_by: given, origin: null, given_by: null, state: "paused" });
    } else {
      Object.assign(mach, { held_by: given, origin, given_by: q.holder, state: "paused" });
      if (q.to_team) mach.project = q.to_team;
    }
    Object.assign(q, { state: "done", waiting_on: null, decided_by: q.asked_by, done_at: "2026-10-27T07:00:01Z" });
    return q;
  }

  secretView(name) {
    const kept = this.secrets.get(name);
    const used = [...this.machines.values()].filter((m) => m.state !== "deleted" && m.secrets.includes(name)).map((m) => m.id).sort();
    return { object: "secret", name, machines: used, outside: kept.outside ?? null, every_machine: !!kept.every_machine,
      created_at: kept.created_at, updated_at: kept.updated_at };
  }

  // How a secret is kept, as the API says it: `outside` left out (or null) the name's preset when it has one, true the
  // preset (refused for a name with none), false an ordinary secret, an object a policy of its own; `every_machine`
  // left out true for a preset's, and only for a secret kept outside.
  keptAs(name, b) {
    const preset = PRESETS.find((p) => p.names.includes(name));
    const asPreset = preset && { preset: preset.id, hosts: preset.hosts, header: preset.headers[0].name, format: preset.headers[0].format,
      headers: preset.headers, query: preset.query };
    let outside;
    if (b.outside === undefined || b.outside === null) outside = asPreset ?? null;
    else if (b.outside === true) {
      if (!asPreset) throw new Problem(400, "invalid_request", `${name} is not a name the API knows as an AI key`);
      outside = asPreset;
    } else if (b.outside === false) outside = null;
    else if (typeof b.outside === "object" && Array.isArray(b.outside.hosts) && b.outside.header) {
      const format = b.outside.format || "{value}";
      outside = { preset: null, hosts: [...b.outside.hosts], header: b.outside.header, format, headers: [{ name: b.outside.header, format }], query: [] };
    } else throw new Problem(400, "invalid_request", "`outside` is true, false, or an object of hosts, header and format");
    let every = b.every_machine;
    if (every === undefined) every = !!(outside && outside.preset);
    if (typeof every !== "boolean") throw new Problem(400, "invalid_request", "every_machine must be bool");
    if (every && !outside) throw new Problem(400, "invalid_request", "only a secret kept outside the machines can be on every machine");
    return { outside, every };
  }

  // A machine as answered, with the AI keys that reach it as the API says them: an open machine every key on every
  // machine, one that reaches only its list those with a host on it, a closed one none.
  withAIKeys(payload) {
    if (payload && payload.object === "list" && Array.isArray(payload.data)) return { ...payload, data: payload.data.map((x) => this.withAIKeys(x)) };
    if (!(payload && payload.object === "machine")) return payload;
    const net = payload.network ?? { internet: "open", allow: [] };
    const reaches = (outside) => {
      if (payload.state === "deleted" || net.internet === "closed") return false;
      if (net.internet !== "allowed") return true;
      return outside.hosts.some((h) => net.allow.some((a) => a === h || (a.startsWith("*.") && (h === a.slice(2) || h.endsWith(a.slice(1))))));
    };
    const ai_keys = [...this.secrets.keys()].sort().filter((n) => {
      const kept = this.secrets.get(n);
      return kept.every_machine && kept.outside && reaches(kept.outside);
    });
    return { ...payload, ai_keys };
  }

  secretNames(names) {
    if (names === undefined) return undefined;
    if (!Array.isArray(names)) throw new Problem(400, "invalid_request", "secrets is a list of names");
    for (const n of names) {
      if (!this.secrets.has(n)) throw new Problem(400, "invalid_request", `no secret ${n}: set it first (PUT /v1/secrets/${n})`);
    }
    return [...new Set(names)].sort();
  }

  /** A port as the API takes one: a whole number from 1 to 65535 (in a path, its digits). */
  port(value) {
    if (!Number.isInteger(value) || value < 1 || value > 65535) throw new Problem(400, "invalid_request", "a port is a whole number from 1 to 65535");
    return value;
  }

  /** Deploy, as the API answers it: a run ends at a wait, or goes on for ever while `deployStuck`. */
  deployRoutes(method, path, query, body, raw) {
    if (this.deployClosed) throw new Problem(501, "not_open", "Deploy is not open yet");
    const list = (data) => [200, { object: "list", data, next: null }, {}];
    const asked = (name) => query.get(name) ?? undefined;
    if (path === "/v1/deployments" && method === "GET") {
      return list(this.deployments.filter((d) => !asked("project") || d.project === asked("project")).reverse());
    }
    if (path === "/v1/deployments" && method === "POST") {
      const b = only(body, ["name", "project", "source", "branch", "build", "start", "port", "cpus", "type", "region", "secrets", "keep_awake"]);
      const src = only(b.source, ["connection", "repo", "url", "upload"]);
      if (Boolean(src.connection) !== Boolean(src.repo) || [src.repo, src.url, src.upload].filter(Boolean).length !== 1) {
        throw new Problem(400, "invalid_request", "say which code");
      }
      if (!b.start || !b.port) throw new Problem(400, "invalid_request", "say how the app starts, and on which port");
      const n = this.next();
      const kind = src.upload ? "upload" : src.url ? "url" : "repo";
      const d = {
        object: "deployment", id: `dep_${String(n).padStart(26, "0")}`, name: b.name ?? "app", project: b.project ?? this.firstProject, held_by: null,
        kind: "production", source: { kind, provider: null, connection: src.connection ?? null, host: null, repo: src.repo ?? null, url: src.url ?? null },
        branch: b.branch ?? null, commit: null, build: b.build ?? "", start: b.start, port: b.port, cpus: b.cpus ?? 2, type: null, region: b.region ?? null,
        keep_awake: b.keep_awake ?? false, secrets: b.secrets ?? [], machine: `m_${String(n).padStart(26, "0")}`, url: null,
        state: kind === "upload" ? "waiting_for_files" : "building", last_status: null, run: null,
        created_at: "2026-10-23T07:00:00Z", updated_at: "2026-10-23T07:00:00Z", live_at: null,
      };
      this.beginDeployRun(d, "first", kind === "upload" && !this.deployFilesAfter ? "files" : "machine");
      this.deployments.push(d);
      return [201, d, {}];
    }
    if (path === "/v1/deployments/detect" && method === "POST") {
      const b = only(body, ["source", "branch", "files"]);
      if (!b.source && b.files == null) throw new Problem(400, "invalid_request", "say which code");
      return [200, { object: "deploy_guess", runtime: "node", build: "npm ci", start: "npm start", port: 3000, note: null }, {}];
    }
    if (path === "/v1/deployments/repos" && method === "GET") {
      return list(this.deployRepos.filter((r) => !asked("connection") || r.connection === asked("connection")));
    }
    if (path === "/v1/deployments/branches" && method === "GET") {
      return [200, { object: "list", data: [{ object: "deploy_branch", name: "main", commit: "abc" }], default: "main", known: true, next: null }, {}];
    }
    const m = /^\/v1\/deployments\/([^/]+)(?:\/([a-z]+))?$/.exec(path);
    if (!m) throw new Problem(404, "not_found", "no such route");
    const d = this.deployments.find((x) => x.id === m[1]);
    if (!d) throw new Problem(404, "not_found", "no such deployment");
    const action = m[2];
    if (action === undefined && method === "GET") {
      if (d.state === "waiting_for_files" && d.run.step === "machine" && --this.deployFilesAfter <= 0) d.run.step = "files";
      return [200, d, {}];
    }
    if (action === undefined && method === "PATCH") {
      Object.assign(d, only(body, ["name", "branch", "build", "start", "port", "keep_awake"]));
      return [200, d, {}];
    }
    if (action === undefined && method === "DELETE") {
      this.deployments.splice(this.deployments.indexOf(d), 1);
      return [204, null, {}];
    }
    if (action === "redeploy" && method === "POST") {
      if (d.run && d.run.ended_at === null) throw new Problem(409, "conflict", "a deploy of it is under way: wait for it to end");
      this.beginDeployRun(d, "redeploy", "fetch");
      return [202, d, {}];
    }
    if (action === "files" && method === "PUT") {
      if (d.run && d.run.ended_at === null && d.run.step === "machine") throw new Problem(409, "conflict", "its machine is not ready yet: send the files again in a moment");
      this.deploySent.set(d.id, Buffer.from(raw));
      if (d.state === "waiting_for_files") {
        d.state = "building";
        d.run.step = "fetch";
      } else {
        this.beginDeployRun(d, "files", "fetch");
      }
      return [202, d, {}];
    }
    if (action === "wait" && method === "GET") {
      if (this.deployStuck) throw new Problem(408, "timeout", "it is still deploying");
      if (d.run && d.run.ended_at === null) {
        Object.assign(d.run, { state: "live", ended_at: "2026-10-23T07:05:00Z" });
        Object.assign(d, { state: "live", live_at: "2026-10-23T07:05:00Z", url: `https://${d.port}-${d.machine.slice(2)}.run.wheelmachine.ai` });
      }
      return [200, d, {}];
    }
    if (action === "runs" && method === "GET") return list(this.deployRuns.get(d.id) ?? []);
    if (action === "logs" && method === "GET") {
      return [200, { object: "deploy_log", kind: asked("kind") ?? "build", run: asked("run") ?? d.run.id, text: "built", done: true }, {}];
    }
    throw new Problem(404, "not_found", "no such route");
  }

  beginDeployRun(d, why, step) {
    const run = {
      object: "deploy_run", id: `dpr_${String(this.next()).padStart(26, "0")}`, why, state: "working", step, progress: 0, note: null, failure: null,
      commit: null, branch: null, files: null, created_at: "2026-10-23T07:00:00Z", started_at: null, ended_at: null,
    };
    d.run = run;
    this.deployRuns.set(d.id, [run, ...(this.deployRuns.get(d.id) ?? [])]);
  }

  portRoutes(method, mid, at, link, body) {
    if (this.machine(mid).state === "deleted") throw new Problem(404, "not_found", "no such machine");
    if (at === undefined && method === "GET") {
      const data = [...this.openPorts.values()].filter((p) => p.machine === mid).sort((a, b) => a.port - b.port);
      return [200, { object: "list", data }, {}];
    }
    if (at === undefined && method === "POST") {
      const b = only(body, ["port", "public"]);
      const port = this.port(b.port);
      if (b.public !== undefined && typeof b.public !== "boolean") throw new Problem(400, "invalid_request", "public is true or false");
      if (!this.addressesServed) throw new Problem(503, "not_open", "web addresses are not served yet");
      this.appPorts.delete(`${mid} ${port}`);
      const was = this.openPorts.get(`${mid} ${port}`);
      const p = was ?? { object: "port", machine: mid, port, public: false, url: `https://${port}-${mid.slice(2)}.run.wheelmachine.ai`, created_at: "2026-10-23T07:00:00Z" };
      p.public = b.public ?? false; // made public or private as asked: private when left out
      this.openPorts.set(`${mid} ${port}`, p);
      return [was ? 200 : 201, { ...p }, {}];
    }
    if (at === undefined) throw new Problem(404, "not_found", "no such route");
    const port = this.port(/^[0-9]{1,5}$/.test(at) ? Number(at) : NaN);
    if (!link && method === "DELETE") {
      this.openPorts.delete(`${mid} ${port}`); // not open: 204 all the same
      this.appPorts.delete(`${mid} ${port}`);
      return [204, null, {}];
    }
    if (link && method === "POST") {
      const b = only(body, ["access_s", "path"]);
      const access = b.access_s ?? 1800;
      if (!Number.isInteger(access) || access < 60 || access > 3600) throw new Problem(400, "invalid_request", "access_s is between 60 and 3600");
      const where = b.path ?? "/";
      if (typeof where !== "string" || !where.startsWith("/") || where.length > 2048) throw new Problem(400, "invalid_request", "a path begins with /");
      if (!this.addressesServed) throw new Problem(503, "not_open", "web addresses are not served yet");
      const open = this.openPorts.get(`${mid} ${port}`);
      if (!open) throw new Problem(404, "not_found", "that port is not open");
      this.links.push({ machine: mid, port, access_s: access, path: where });
      const url = `${open.url}/.wheelmachine/link/${randomBytes(24).toString("base64url")}`;
      return [201, { object: "port_link", machine: mid, port, url, expires_at: "2026-10-23T07:05:00Z", access_s: access }, {}];
    }
    throw new Problem(404, "not_found", "no such route");
  }

  /**
   * A machine's apps: listed, turned on (a web app's port opened private when it is not, and a new link into it;
   * openclaw's token, once) and off (the port closed when it was opened for the app and is still private), and a new
   * link into a web app that is on (its port opened private first when it is not). A machine with no apps
   * has none (409 no_apps).
   */
  appRoutes(method, mid, name, verb, body) {
    if (this.machine(mid).state === "deleted") throw new Problem(404, "not_found", "no such machine");
    if (this.oldMachines.has(mid)) throw new Problem(409, "no_apps", "this machine has no apps; make a new machine to have them");
    if (!this.apps.has(mid)) {
      const url = (a) => (a.kind === "web" ? `https://${a.port}-${mid.slice(2)}.run.wheelmachine.ai` : null);
      this.apps.set(mid, new Map(APPS.map((a) => [a.name, { object: "app", machine: mid, ...a, url: url(a) }])));
    }
    const held = this.apps.get(mid);
    if (name === undefined) {
      if (method !== "GET") throw new Problem(404, "not_found", "no such route");
      return [200, { object: "list", data: [...held.values()] }, {}];
    }
    if (method !== "POST") throw new Problem(404, "not_found", "no such route");
    const app = held.get(name);
    if (!app) throw new Problem(404, "not_found", "no such app in this machine");
    const b = only(body, verb === "off" || verb === "show" ? [] : ["access_s"]);
    const access = b.access_s ?? 1800;
    if (!Number.isInteger(access) || access < 60 || access > 3600) throw new Problem(400, "invalid_request", "access_s is between 60 and 3600");
    if (verb === "show") {
      // shown on the machine's screen when it is on: nothing opened, linked or given
      if (app.on === false) throw new Problem(409, "conflict", `${name} could not be shown on the machine's screen: ${app.title} is off: turn it on first`);
      return [200, { ...app }, {}];
    }
    if (verb === "link" && app.kind !== "web") throw new Problem(400, "invalid_request", `${app.title} has no web address`);
    if (verb === "link" && !app.on) throw new Problem(409, "conflict", `${app.title} is off: turn it on first`);
    if (verb !== "link" && app.kind !== "terminal") app.on = app.running = verb === "on";
    const answer = { ...app };
    const at = `${mid} ${app.port}`;
    if (app.kind === "web" && verb !== "off") {
      if (!this.openPorts.has(at)) {
        this.openPorts.set(at, { object: "port", machine: mid, port: app.port, public: false, url: app.url, created_at: "2026-10-23T07:00:00Z" });
        this.appPorts.set(at, name);
      }
      this.links.push({ machine: mid, port: app.port, access_s: access, path: app.path });
      Object.assign(answer, { link: `${app.url}/.wheelmachine/link/${randomBytes(24).toString("base64url")}`, expires_at: "2026-10-23T07:05:00Z", access_s: access });
    }
    if (app.kind === "web" && verb === "off" && this.appPorts.get(at) === name && !this.openPorts.get(at)?.public) {
      this.openPorts.delete(at);
      this.appPorts.delete(at);
    }
    if (verb === "on" && name === "openclaw") answer.token = APP_TOKEN;
    return [verb === "link" ? 201 : 200, answer, {}];
  }

  /**
   * The audit, as the API reads it: in an organisation by its owners and admins alone; a page of it, the newest first,
   * or the whole of a year at most as a file - CSV quoted as the API quotes it, or JSON.
   */
  auditRoutes(isExport, query) {
    if (this.org && !["owner", "admin"].includes(this.org.role)) throw new Problem(403, "forbidden", "your role in this organisation does not allow this");
    const format = query.get("format");
    if (isExport && format !== "csv" && format !== "json") throw new Problem(400, "invalid_request", "say format=csv or format=json");
    const when = {};
    for (const name of ["since", "until"]) {
      const given = query.get(name);
      when[name] = null;
      if (given) {
        const t = Date.parse(given);
        if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$/.test(given) || Number.isNaN(t)) {
          throw new Problem(400, "invalid_request", `${name} is a time like 2026-10-01T00:00:00Z`);
        }
        when[name] = t;
      }
    }
    if (isExport) {
      const span = 366 * 24 * 3600 * 1000;
      const until = when.until ?? Date.parse("2026-10-23T12:00:00Z");
      const since = when.since ?? until - span;
      if (!(since < until) || until - since > span) throw new Problem(400, "invalid_request", "an export covers a year at most, from since to until");
      Object.assign(when, { since, until });
    }
    const person = query.get("person");
    const action = query.get("action") ?? "";
    let rows = this.audit.filter((e) => (person === null || e.person?.id === person) && e.action.startsWith(action) &&
      (when.since === null || Date.parse(e.at) >= when.since) && (when.until === null || Date.parse(e.at) < when.until));
    if (!isExport) {
      const [limit, after] = this.page(query);
      if (after !== null) {
        const i = rows.findIndex((e) => e.id === after);
        if (i < 0) throw new Problem(400, "invalid_request", "after is the `next` of the page before");
        rows = rows.slice(i + 1);
      }
      return [200, { object: "list", data: rows.slice(0, limit), next: rows.length > limit ? rows[limit - 1].id : null }, {}];
    }
    const named = { "content-disposition": `attachment; filename="audit-acct_test.${format}"` };
    if (format === "json") return [200, rows, named];
    // every field quoted, and one a spreadsheet would run as a formula begun with ', so that it is shown, never run
    const line = (...fields) => fields.map((f) => `"${(/^[=+\-@\t\r]/.test(f) ? "'" + f : f).replaceAll('"', '""')}"`).join(",") + "\r\n";
    let csv = line("id", "at", "action", "target", "person", "person_name", "by", "address", "request_id", "detail");
    for (const e of rows) {
      const detail = Object.keys(e.detail).sort().map((k) => `${k}=${e.detail[k]}`).join("; ");
      csv += line(e.id, e.at, e.action, e.target, e.person?.id ?? "", e.person?.name ?? "", e.by, e.address, e.request_id, detail);
    }
    return [200, Buffer.from(csv), { ...named, "content-type": "text/csv; charset=utf-8" }];
  }

  policy(network) {
    if (network === undefined || network === null) return undefined;
    only(network, ["internet", "allow"]);
    if (!["open", "closed", "allowed"].includes(network.internet)) {
      throw new Problem(400, "invalid_request", 'network.internet is "open", "closed" or "allowed"');
    }
    const allow = network.allow ?? [];
    if (network.internet !== "allowed" && allow.length > 0) {
      throw new Problem(400, "invalid_request", 'network.allow is only for "allowed"');
    }
    return { internet: network.internet, allow: [...allow] };
  }

  /** The agents as the API shows them with the choices made: an agent's own, then the one for every agent where it can
   * use its provider, else its default with its first provider's key. */
  agentList() {
    const held = this.heldTo ? this.heldTo.models : null;
    const data = AGENTS.map((a) => {
      const first = a.suppliers[0];
      const v = { object: "agent", ...a, model: null, chosen_by: null, note: null, starts_on: first.default ? `${first.id}/${first.default}` : null,
        state: first.default ? "default" : "own" };
      let taken = false;
      for (const [who, by] of [[a.name, "you"], ["*", "every_agent"]]) {
        const model = this.chosen.get(who);
        if (model && a.suppliers.some((x) => x.id === model.split("/")[0]) && (!held || held.includes(model))) {
          Object.assign(v, { model, chosen_by: by, starts_on: model, state: "chosen" });
          taken = true;
          break;
        }
      }
      if (held && !taken) {
        // limited to a list, with no choice it holds: the list's first model it can use, else none
        const at = held.find((m) => a.suppliers.some((x) => x.id === m.split("/")[0]));
        if (at) Object.assign(v, { model: at, chosen_by: "allowed", starts_on: at, state: "chosen" });
        else Object.assign(v, { state: "not_allowed", note: `None of the models you may use works with ${a.title}.` });
      }
      return v;
    });
    let every = this.chosen.get("*");
    if (every && held && !held.includes(every)) every = undefined;
    return { object: "list", data, every_agent: every ? { model: every, chosen_by: "you" } : null, allowed: this.heldTo };
  }

  routes(method, path, query, body, raw) {
    const route = `${method} ${path}`;
    if (route === "POST /v1/login/devices") {
      const b = only(body, ["name", "scopes", "account"]);
      if (b.account !== undefined && !/^acc_[0-9a-z]{26}$/.test(b.account)) {
        throw new Problem(400, "invalid_request", "account is an organisation's id, acc_ and 26 letters and digits");
      }
      const device = "wmd_" + "d".repeat(40) + this.logins.size;
      this.logins.set(device, { name: b.name ?? "A program", scopes: b.scopes ?? ["cloud:*"], asks: 0, done: false });
      return [200, { object: "device_login_start", device_code: device, user_code: "BCDF-GHJK", verification_uri: this.url + "/device",
        expires_in: 600, expires_at: "2026-10-23T12:10:00Z", interval: 5 }, {}];
    }
    if (route === "POST /v1/login/devices/token") {
      const login = this.logins.get(only(body, ["device_code"]).device_code ?? "");
      if (!login) throw new Problem(404, "not_found", "no such sign-in: start again");
      if (login.done) throw new Problem(410, "login_expired", "this sign-in was used already: start again");
      login.asks += 1;
      const said = this.decide(login.asks);
      if (said === "pending") return [202, { object: "device_login", state: "pending", interval: 5 }, {}];
      if (said === "denied") throw new Problem(403, "login_denied", "the sign-in was refused at the dashboard");
      if (said === "expired") throw new Problem(410, "login_expired", "this sign-in's code expired: start again");
      if (said === "slow") throw new Problem(429, "rate_limited", "ask once every 5 seconds", 5);
      if (said === "full") {
        login.done = true;
        throw new Problem(429, "limit_reached", "an account holds at most 50 API keys; revoke one first");
      }
      login.done = true;
      return [201, { ...this.keyView(this.next()), name: login.name, scopes: login.scopes, secret: KEY }, {}];
    }
    if (route === "GET /v1/me") {
      const me = { object: "me", account: { id: "acct_test", kind: "person", name: "Test Person" },
        person: { id: "acct_test", name: "Test Person", email: null, github_login: null }, role: "owner", teams: null, limits: null,
        scopes: ["cloud:*"], key: this.keyView(), standing: { can_spend: true, currency: this.currency, balance: 10, balance_usd: this.usd(10),
          credit: this.creditUSD, credit_usd: this.usd(this.creditUSD) }, levels: {} };
      if (this.org) {
        Object.assign(me, { account: { id: this.org.id, kind: "org", name: this.org.name }, person: this.org.person, role: this.org.role,
          teams: this.org.teams, limits: this.org.limits, levels: this.org.levels ?? {} });
      }
      return [200, me, {}];
    }
    if (route === "GET /v1/orgs") {
      if (this.org) throw new Problem(403, "forbidden", "an organisation's key does not list the other organisations of the person who made it");
      return [200, { object: "list", data: this.orgs }, {}];
    }
    if (route === "GET /v1/audit" || route === "GET /v1/audit/export") return this.auditRoutes(path.endsWith("/export"), query);
    if (route === "GET /v1/regions") {
      return [200, { object: "list", data: [{ id: "mumbai", city: "Mumbai", country: "IN" }, { id: "helsinki", city: "Helsinki", country: "FI" }] }, {}];
    }
    if (route === "GET /v1/gpus") {
      const offer = { id: "h100x1", model: "H100 80GB", count: 1, memory_gb: 80, cpus: 16, ram_gb: 128, disk_gb: 500, location: { city: "Helsinki", country: "FI" },
        currency: this.currency, price_per_hour: Math.round(2.53 * this.rates[this.currency] * 1e4) / 1e4, price_per_hour_usd: 2.53, interruptible: false, available: true };
      return [200, { object: "list", currency: this.currency, for_sale: false, data: [offer] }, {}];
    }
    if (route === "GET /v1/credit") {
      const [limit, after] = this.page(query);
      const start = after === null ? 0 : Number(after);
      const data = this.creditEntries.slice(start, start + limit);
      const next = start + limit < this.creditEntries.length ? String(start + limit) : null;
      return [200, this.creditAnswer(data, next), {}];
    }
    if (route === "PUT /v1/credit/currency") {
      const b = only(body, ["currency"]);
      if (!["USD", "GBP", "EUR"].includes(b.currency)) throw new Problem(400, "invalid_request", "say currency: USD, GBP or EUR");
      if (this.currencyFixed) throw new Problem(409, "conflict", "this account's currency is fixed: Wheelmachine's support can change it if it was set wrongly");
      this.currency = b.currency;
      return [200, this.creditAnswer([], null), {}];
    }
    if (route === "POST /v1/credit/purchases") {
      const b = only(body, ["amount", "amount_usd"]);
      if (!this.buyOpen) throw new Problem(501, "not_open", "buying credit is not open yet");
      if (b.amount === undefined && b.amount_usd !== undefined && this.currency !== "USD") {
        throw new Problem(400, "invalid_request", "this account is counted in another currency: send amount");
      }
      const amount = b.amount ?? b.amount_usd;
      if (![10, 25, 50, 100].includes(amount)) throw new Problem(400, "invalid_request", "amount is 10, 25, 50 or 100");
      const prices = [10, 25, 50, 100].map((a) => ({ amount: a, amount_usd: a, price_id: "pri_" + String(a).padStart(26, "0") }));
      return [201, { object: "credit_purchase", id: "cp_" + "t".repeat(26), account: "acct_test", currency: this.currency, amount,
        amount_usd: this.usd(amount),
        checkout: { provider: "paddle", environment: "sandbox", client_token: "test_madeup0123", currency: this.currency, transaction_id: null,
          price_id: "pri_" + String(amount).padStart(26, "0"), prices,
          custom_data: { wheelmachine_account: "acct_test", wheelmachine_purchase: "cp_" + "t".repeat(26) },
          url: "https://app.wheelmachine.ai/billing?purchase=cp_" + "t".repeat(26) }, created_at: "2026-11-02T09:30:00Z" }, {}];
    }
    if (route === "GET /v1/folders") {
      let bytes = 0;
      for (const b of this.folderFiles.values()) bytes += b.length;
      return [200, { object: "list", on: true, file_most_bytes: 5000000000, data: [{ object: "shared_folder", id: "fld_own", kind: "own", name: "My files", person: null,
        left_at: null, goes_at: null, access: "edit", teams: [], bytes, changed_at: null, made_at: "2026-11-04T10:00:00Z" }] }, {}];
    }
    const fm = /^\/v1\/folders\/fld_own\/(list|files|move)$/.exec(path);
    if (fm) {
      const where = query.get("path") ?? "";
      if (fm[1] === "files" && method === "PUT") {
        this.folderFiles.set(where, raw);
        return [200, { name: where.split("/").at(-1), type: "file", size: raw.length, changed_at: "2026-11-04T10:00:00Z", changed_by: { id: "acc_1", name: "Ada" } }, {}];
      }
      if (fm[1] === "files" && (method === "GET" || method === "DELETE")) {
        const got = this.folderFiles.get(where);
        if (!got) throw new Problem(404, "not_found", `no file is at ${where}`);
        if (method === "GET") return [200, got, { "content-type": "application/octet-stream" }];
        this.folderFiles.delete(where);
        return [200, { object: "shared_trash", id: "ftr_1", folder: "fld_own", path: where, type: "file", bytes: 0, deleted_at: "2026-11-04T10:00:00Z", deleted_by: null,
          goes_at: "2026-12-04T10:00:00Z" }, {}];
      }
      if (fm[1] === "list") {
        // a page of `limit` entries at most, from the place in the listing `after` says
        const entries = [...this.folderFiles.entries()].sort().map(([name, b]) => ({ name, type: "file", size: b.length, changed_at: "2026-11-04T10:00:00Z", changed_by: null }));
        const start = Number(query.get("after") ?? 0);
        const end = start + Number(query.get("limit") ?? (entries.length || 1));
        return [200, { object: "shared_listing", folder: "fld_own", path: where, entries: entries.slice(start, end), next: end < entries.length ? String(end) : null }, {}];
      }
      if (fm[1] === "move") {
        this.folderFiles.set(body.to, this.folderFiles.get(body.from));
        this.folderFiles.delete(body.from);
        return [200, { object: "shared_moved", folder: "fld_own", path: body.to }, {}];
      }
    }
    if (route === "GET /v1/code") {
      return [200, { object: "code", github: true, connections: [{ object: "code_connection", id: "code_1", provider: "github", host: "github.com", name: "acme", suspended: false, added_at: "2026-11-03T10:00:00Z" }], grants: [] }, {}];
    }
    if (route === "GET /v1/code/connections/code_1/repos") return [200, { object: "list", data: [{ object: "code_repo", repo: "acme/api", host: "github.com", connection: "code_1" }] }, {}];
    if (method === "PUT" && path.startsWith("/v1/projects/") && path.endsWith("/code")) {
      return [200, { object: "code_grant", id: "cg_1", project: path.split("/")[3], connection: body?.connection, host: "github.com", repo: body?.repo, access: body?.access, granted_at: "2026-11-03T10:00:00Z" }, {}];
    }
    if (route === "GET /v1/usage/limits") return [200, this.limitsAnswer(), {}];
    if (route === "PUT /v1/usage/limits/account") {
      const b = only(body, ["month_cap", "month_cap_usd"]);
      if (!("month_cap" in b) && "month_cap_usd" in b && this.currency !== "USD") {
        throw new Problem(400, "invalid_request", "this account is counted in another currency: set month_cap");
      }
      this.ownLimit = ("month_cap" in b ? b.month_cap : b.month_cap_usd) ?? null;
      return [200, this.limitsAnswer(), {}];
    }
    if (route === "GET /v1/usage") {
      const project = query.get("project");
      const lines = [...this.machines.values()]
        .filter((m) => project === null || m.project === project)
        .map((m) => ({ machine: m.id, name: m.name, project: m.project, made_by: m.made_by, held_by: m.held_by, running_seconds: 60,
          amount: Math.round(0.01 * this.rates[this.currency] * 1e4) / 1e4, usd: 0.01 }));
      const total = Math.round(lines.length * 100) / 10000;
      return [200, { object: "usage", month: query.get("month") ?? "2026-10", currency: this.currency, machines: lines,
        total: Math.round(total * this.rates[this.currency] * 1e4) / 1e4, total_usd: total }, {}];
    }

    if (route === "GET /v1/projects") {
      const [limit, after] = this.page(query);
      const rows = [...this.projects.values()].filter((p) => after === null || p.id > after).sort((a, b) => (a.id < b.id ? -1 : 1));
      const page = rows.slice(0, limit);
      return [200, { object: "list", data: page, next: rows.length > limit ? page[page.length - 1].id : null }, {}];
    }
    if (route === "POST /v1/projects") {
      const b = only(body, ["name"]);
      if (!b.name) throw new Problem(400, "invalid_request", "a project needs a name");
      const p = this.addProject(b.name);
      return [201, p, { location: `/v1/projects/${p.id}` }];
    }
    let m = /^\/v1\/projects\/([^/]+)$/.exec(path);
    if (m) {
      const p = this.projects.get(m[1]);
      if (!p) throw new Problem(404, "not_found", "no such project");
      if (method === "GET") return [200, p, {}];
      if (method === "PATCH") {
        p.name = only(body, ["name"]).name ?? p.name;
        return [200, p, {}];
      }
      if (method === "DELETE") {
        if ([...this.machines.values()].some((x) => x.project === p.id && x.state !== "deleted")) {
          throw new Problem(409, "project_not_empty", "delete its machines first");
        }
        this.projects.delete(p.id);
        return [204, null, {}];
      }
    }

    m = /^\/v1\/projects\/([^/]+)\/rules$/.exec(path);
    if (m && method === "GET") {
      this.inOrg();
      if (!this.projects.has(m[1])) throw new Problem(404, "not_found", "no such project");
      return [200, this.rulesOf(m[1]), {}];
    }

    // the requests to fork a machine for someone, or hand it off
    if (route === "GET /v1/requests") {
      this.inOrg();
      const [limit, after] = this.page(query);
      let rows = [...this.machineRequests.values()].sort((a, b) => (a.id < b.id ? 1 : -1));
      if (query.get("open") === "true") rows = rows.filter((r) => r.state === "waiting" || r.state === "approved");
      const of = query.get("machine");
      if (of) rows = rows.filter((r) => r.machine?.id === of || r.result === of);
      if (after !== null) rows = rows.filter((r) => r.id < after);
      const page = rows.slice(0, limit);
      return [200, { object: "list", data: page, next: rows.length > limit ? page[page.length - 1].id : null }, {}];
    }
    if (route === "POST /v1/requests") {
      this.inOrg();
      const b = only(body, ["kind", "of", "team", "why"]);
      if (b.kind !== "fork" && b.kind !== "hand_off") throw new Problem(400, "invalid_request", "kind is fork or hand_off");
      const holder = this.teammate(b.of);
      for (const r of this.machineRequests.values()) {
        if (r.state === "waiting" && r.way === "ask" && r.kind === b.kind && r.holder.id === holder.id) return [200, r, {}];
      }
      const q = this.newRequest(b.kind, "ask", null, holder, this.org.person, b.why ?? "");
      // (made with a key: the OK of the person who made the key first, in the dashboard - approve() here)
      q.waiting_on = { who: "asker", name: this.org.person.name };
      return [201, q, { location: `/v1/requests/${q.id}` }];
    }
    m = /^\/v1\/requests\/([^/]+)$/.exec(path);
    if (m) {
      this.inOrg();
      const r = this.machineRequests.get(m[1]);
      if (!r) throw new Problem(404, "not_found", "no such request");
      if (method === "GET") return [200, r, {}];
      if (method === "DELETE") {
        if (r.asked_by.id !== this.org.person.id) throw new Problem(403, "forbidden", `Only ${r.asked_by.name} takes it back`);
        if (r.state === "waiting") Object.assign(r, { state: "taken_back", waiting_on: null, decided_by: { ...r.asked_by } });
        else if (r.state !== "taken_back") throw new Problem(409, "conflict", "Decided already");
        return [200, r, {}];
      }
    }

    if (route === "GET /v1/keys") return [200, { object: "list", data: [...this.keys.values()] }, {}];
    if (route === "POST /v1/confirm-codes") {
      const b = only(body, ["step", "org", "to", "account"]);
      return [201, { ...b, object: "confirm_code", expires_at: "2026-11-04T15:30:00Z", sent_to: "a…@example.com" }, {}];
    }
    if (route === "POST /v1/keys") {
      const b = only(body, ["name", "scopes", "projects", "expires_at", "rate_per_minute"]);
      // a key with full access, while a code is asked: the one the test set (this.confirmCode)
      if (this.confirmCode && (b.scopes ?? []).includes("cloud:*")) {
        const ask = { step: "full_access_key", account: "acc_01jb2xk3n4p5q6r7s8t9v0w1x2" };
        const said = this.headers["x-confirm-code"];
        if (!said) throw new Problem(403, "confirm_required", "this needs a code we e-mail you", undefined, ask);
        if (said !== this.confirmCode) throw new Problem(403, "confirm_wrong", "that code is not right: 4 tries left", undefined, ask);
      }
      const k = { ...this.keyView(this.next()), name: b.name, scopes: b.scopes ?? [], projects: b.projects ?? [], expires_at: b.expires_at ?? null, rate_per_minute: b.rate_per_minute ?? null };
      this.keys.set(k.id, k);
      return [201, { ...k, secret: "ak_live_" + "s".repeat(40) }, { location: `/v1/keys/${k.id}` }];
    }
    m = /^\/v1\/keys\/([^/]+)$/.exec(path);
    if (m && method === "DELETE") {
      if (!this.keys.delete(m[1])) throw new Problem(404, "not_found", "no such key");
      return [204, null, {}];
    }

    // secrets: never a value in an answer
    // the models of the keys, and the model each agent starts on
    if (route === "GET /v1/models") {
      const agent = AGENTS.find((a) => a.name === query.get("agent"));
      if (query.get("agent") !== null && !agent) throw new Problem(404, "not_found", "no such agent");
      const providers = agent ? agent.suppliers.map((x) => x.id) : null;
      const held = this.heldTo ? this.heldTo.models : null;
      return [200, { object: "list", data: MODELS.filter((m) => (!providers || providers.includes(m.supplier)) && (!held || held.includes(m.id))),
        suppliers: SUPPLIERS.filter((x) => !providers || providers.includes(x.id)), allowed: this.heldTo }, {}];
    }
    // the organisation's lists of the models its people may use: its owners' and admins'
    if (path === "/v1/models/allowed" || path.startsWith("/v1/models/allowed/")) {
      if (!this.org) throw new Problem(409, "conflict", "lists of the models people may use are an organisation's: a person's own account keeps none");
      if (!["owner", "admin"].includes(this.org.role)) throw new Problem(403, "forbidden", "the models people may use are set by the organisation's owners and admins");
      if (route === "GET /v1/models/allowed") return [200, { object: "list", data: this.allowed }, {}];
      const at = /^\/v1\/models\/allowed\/(?:(organisation)|(teams|roles|people)\/([^/]+))$/.exec(path);
      if (at && (method === "PUT" || method === "DELETE")) {
        const level = { teams: "team", roles: "role", people: "person" }[at[2]] ?? "organisation";
        const lid = level === "organisation" ? this.org.id : decodeURIComponent(at[3]);
        if (level === "role" && !["member", "approver", "manager", "full"].includes(lid)) throw new Problem(404, "not_found", "no such role");
        this.allowed = this.allowed.filter((x) => !(x.level === level && x.id === lid));
        if (method === "DELETE") return [204, null, {}];
        const models = only(body, ["models"]).models;
        if (!Array.isArray(models) || models.length < 1 || models.length > 10) throw new Problem(400, "invalid_request", "name the models people may use in `models`, one to 10");
        if (models.some((x) => !MODELS.some((m) => m.id === x))) throw new Problem(400, "invalid_request", "the provider does not list that model for the organisation's key");
        const name = level === "organisation" ? this.org.name
          : level === "role" ? { member: "Members", approver: "Approvers", manager: "Team managers", full: "Full managers" }[lid] : "";
        const row = { object: "allowed_models", level, id: lid, name, models, set_by: { id: this.org.person.id, name: this.org.person.name },
          set_at: "2026-11-02T09:00:00Z" };
        this.allowed.push(row);
        return [200, row, {}];
      }
    }
    if (route === "GET /v1/agents") return [200, this.agentList(), {}];
    const choice = /^\/v1\/agents\/(?:([^/]+)\/)?model$/.exec(path);
    if (choice && (method === "PUT" || method === "DELETE")) {
      const who = choice[1] ?? "*";
      const agent = AGENTS.find((a) => a.name === who);
      if (who !== "*" && !agent) throw new Problem(404, "not_found", "no such agent");
      if (method === "DELETE") {
        this.chosen.delete(who);
        return [204, null, {}];
      }
      const model = only(body, ["model"]).model;
      if (!MODELS.some((m) => m.id === model)) throw new Problem(400, "invalid_request", "the provider does not list that model for your key");
      if (this.heldTo && !this.heldTo.models.includes(model)) {
        throw new Problem(403, "forbidden", `${model} is not one of the models you may use: ${this.heldTo.models.join(", ")}`);
      }
      if (agent && !agent.suppliers.some((x) => x.id === String(model).split("/")[0])) {
        throw new Problem(400, "invalid_request", `${agent.title} cannot use that provider's models`);
      }
      if (who === "*") this.chosen.clear();
      this.chosen.set(who, model);
      const list = this.agentList();
      return [200, who === "*" ? list : list.data.find((a) => a.name === who), {}];
    }
    if (route === "GET /v1/secrets/presets") return [200, { object: "list", data: PRESETS }, {}];
    if (route === "GET /v1/secrets") {
      return [200, { object: "list", data: [...this.secrets.keys()].sort().map((n) => this.secretView(n)) }, {}];
    }
    m = /^\/v1\/secrets\/([^/]+)$/.exec(path);
    if (m) {
      const name = m[1];
      if (!/^[A-Z_][A-Z0-9_]{0,63}$/.test(name)) throw new Problem(400, "invalid_request", "a secret's name is capitals, digits and _");
      if (method === "PUT") {
        const b = only(body, ["value", "outside", "every_machine"]);
        if (typeof b.value !== "string" || b.value === "" || Buffer.byteLength(b.value) > 32768) {
          throw new Problem(400, "invalid_request", "give the secret's value in `value`, at most 32 KiB");
        }
        const { outside, every } = this.keptAs(name, b);
        const made = !this.secrets.has(name);
        const kept = this.secrets.get(name) ?? { created_at: "2026-10-23T07:00:00Z" };
        Object.assign(kept, { value: b.value, updated_at: "2026-10-23T07:01:00Z", outside, every_machine: every });
        this.secrets.set(name, kept);
        return [made ? 201 : 200, this.secretView(name), {}];
      }
      if (method === "DELETE") {
        if (!this.secrets.delete(name)) throw new Problem(404, "not_found", "no such secret");
        for (const mach of this.machines.values()) mach.secrets = mach.secrets.filter((n) => n !== name);
        return [204, null, {}];
      }
    }

    if (route === "GET /v1/machines") {
      const [limit, after] = this.page(query);
      let rows = [...this.machines.values()].filter((x) => x.state !== "deleted").sort((a, b) => (a.id < b.id ? 1 : -1));
      if (query.get("project")) rows = rows.filter((x) => x.project === query.get("project"));
      if (query.get("state")) rows = rows.filter((x) => x.state === query.get("state"));
      for (const label of query.getAll("label")) {
        const at = label.indexOf("=");
        rows = rows.filter((x) => x.labels[label.slice(0, at)] === label.slice(at + 1));
      }
      if (after !== null) rows = rows.filter((x) => x.id < after);
      const page = rows.slice(0, limit);
      return [200, { object: "list", data: page, next: rows.length > limit ? page[page.length - 1].id : null }, {}];
    }
    if (route === "POST /v1/machines") {
      const b = only(body, ["name", "project", "region", "gpu", "cpus", "labels", "secrets", "network", "sleep_when_idle", "idle_after_s", "desktop", "wait_s"]);
      const secrets = this.secretNames(b.secrets);
      const network = this.policy(b.network);
      if (b.cpus === 16) throw new Problem(400, "invalid_request", "16 CPUs are not offered yet: choose 2, 4 or 8");
      if (b.cpus !== undefined && ![2, 4, 8].includes(b.cpus)) throw new Problem(400, "invalid_request", "cpus is 2, 4 or 8");
      const starting = b.wait_s === 0 || this.startSlowly;
      const made = this.addMachine({ name: b.name ?? "", project: b.project, labels: b.labels, state: starting ? "starting" : "running" });
      made.secrets = secrets ?? [];
      if (network) made.network = network;
      if (b.cpus !== undefined) Object.assign(made, { cpus: b.cpus, memory_gb: 2 * b.cpus, size: { cpus: b.cpus, memory_gb: 2 * b.cpus, disk_gb: 32 } });
      if (b.sleep_when_idle === false) Object.assign(made, { sleep_when_idle: false, idle_after_s: null });
      else if (b.idle_after_s !== undefined) made.idle_after_s = b.idle_after_s;
      return [starting ? 202 : 201, starting ? { ...made } : { ...made, ready_ms: 12.5 }, { location: `/v1/machines/${made.id}` }];
    }
    if (route === "GET /v1/backups") return [200, { object: "list", data: this.backupsOf(query) }, {}];

    m = /^\/v1\/machines\/([^/]+)\/(fork-for|hand-off)$/.exec(path);
    if (m && method === "POST") return this.give(this.machine(m[1]), m[2] === "fork-for" ? "fork" : "hand_off", body);
    m = /^\/v1\/machines\/([^/]+)\/(take-back|give-back)$/.exec(path);
    if (m && method === "POST") return this.back(this.machine(m[1]), m[2] === "take-back", body);
    m = /^\/v1\/machines\/([^/]+)\/screen\/(picture|actions)$/.exec(path);
    if (m) {
      const mach = this.machine(m[1]);
      mach.state = "running";
      const picture = (scale, format) => ({
        width: Math.floor(2880 * scale),
        height: Math.floor(1800 * scale),
        scale,
        format,
        data_base64: Buffer.from("the picture's file").toString("base64"),
      });
      if (method === "GET" && m[2] === "picture") {
        return [200, picture(Number(query.get("scale") ?? 0.5), query.get("format") ?? "png"), {}];
      }
      if (method === "POST" && m[2] === "actions") {
        const b = only(body, ["actions", "scale", "picture", "settle_ms"]);
        if (!b.actions?.length || b.actions.length > 64) throw new Problem(400, "invalid_request", "actions holds 1 to 64 actions");
        this.screenActions.push(b);
        const out = { done: b.actions.length };
        if (b.picture) out.picture = picture(b.scale ?? 0.5, "png");
        return [200, out, {}];
      }
      throw new Problem(404, "not_found", "no such route");
    }
    m = /^\/v1\/machines\/([^/]+)\/apps(?:\/([^/]+)\/(on|off|link|show))?$/.exec(path);
    if (m) return this.appRoutes(method, m[1], m[2], m[3], body);
    if (path === "/v1/deployments" || path.startsWith("/v1/deployments/")) return this.deployRoutes(method, path, query, body, raw);
    m = /^\/v1\/machines\/([^/]+)\/ports(?:\/([^/]+)(\/link)?)?$/.exec(path);
    if (m) return this.portRoutes(method, m[1], m[2], m[3] !== undefined, body);
    m = /^\/v1\/machines\/([^/]+)(?:\/([a-z-]+))?$/.exec(path);
    if (!m) throw new Problem(404, "not_found", "no such route");
    const [, mid, action] = m;
    const mach = this.machine(mid);
    if (action === undefined) {
      if (method === "GET") return [200, mach, {}];
      if (method === "PATCH") {
        const b = only(body, ["name", "labels", "secrets", "network"]);
        if ("secrets" in b) mach.secrets = this.secretNames(b.secrets);
        if (b.network !== undefined) mach.network = this.policy(b.network);
        if ("name" in b) mach.name = b.name;
        for (const [k, v] of Object.entries(b.labels ?? {})) {
          if (v === null) delete mach.labels[k];
          else mach.labels[k] = v;
        }
        return [200, mach, {}];
      }
      if (method === "DELETE") {
        mach.state = "deleted";
        return [204, null, {}];
      }
    }
    // (a deleted machine has no backups)
    if (method === "GET" && action === "backups") return [200, { object: "list", data: this.backupsOf(query, mid) }, {}];
    if (mach.state === "deleted") throw new Problem(404, "not_found", "no such machine");
    if (method === "POST" && ["start", "pause", "stop", "archive"].includes(action)) {
      only(body, action === "start" ? ["wait_s"] : []);
      mach.state = { start: "running", pause: "paused", stop: "stopped", archive: "archived" }[action];
      return [200, mach, {}];
    }
    if (method === "POST" && (action === "fork" || action === "copy")) {
      const b = only(body, ["name", "labels", "backup", "cpus", "wait_s"]);
      const backup = this.forkFrom(mach, b);
      if (this.org?.role === "member" && this.org.levels?.[mach.project] !== "full") return this.ownFork(mach, backup, b.cpus ?? null);
      const cp = this.addMachine({ name: b.name ?? mach.name, project: mach.project, labels: b.labels ?? mach.labels });
      cp.secrets = [...mach.secrets];
      // the original's size, or the one asked of a fork from a backup
      const cpus = b.cpus ?? mach.cpus;
      Object.assign(cp, { cpus, memory_gb: 2 * cpus, size: { cpus, memory_gb: 2 * cpus, disk_gb: 32 } });
      return [201, { ...cp, ready_ms: 3 }, { location: `/v1/machines/${cp.id}` }];
    }
    if (method === "POST" && action === "backups") {
      const b = only(body, ["note"]);
      const made = { id: `20261023t070000z-${this.next()}`, object: "backup", machine: mid, note: b.note ?? "", created_at: "2026-10-23T07:00:00Z" };
      this.backups.push(made);
      return [201, made, {}];
    }
    if (method === "GET" && action === "repositories") {
      return [200, { object: "list", data: this.repositories, protected_branches: ["main", "master", "release/**"] }, {}];
    }
    if (method === "POST" && action === "pull-requests") {
      const b = only(body, ["repository", "host", "head", "base", "title", "body", "draft"]);
      const r = this.repositories.find((x) => x.repository === b.repository && x.access === "write");
      if (!r) throw new Problem(404, "not_found", "this machine does not reach that repository");
      const made = { object: "pull_request", opened: true, provider: r.provider, host: r.host, repository: r.repository, head: b.head, base: b.base,
        draft: b.draft ?? false, number: null, url: null, run: null, note: null };
      if (r.provider === "gitlab") return [200, { ...made, opened: false, run: `git push -o merge_request.create origin ${b.head}`, note: "Nothing is open yet." }, {}];
      const number = this.next();
      return [201, { ...made, number, url: `https://${r.host}/${r.repository}/pull/${number}` }, {}];
    }
    if (method === "GET" && action === "metrics") {
      const step = query.get("step") ?? "60";
      if (!["60", "300", "3600"].includes(step)) throw new Problem(400, "invalid_request", "step is whole minutes from 1 to 60");
      const points = this.metrics.get(mid) ?? [];
      return [200, { object: "metrics", machine: mid, step_s: Number(step), cpus: mach.cpus, kept_since: points[0]?.at ?? null, points }, {}];
    }
    if (method === "GET" && action === "wait") {
      const want = query.get("state");
      if (want === "running" && mach.state === "starting") mach.state = "running";
      if (mach.state === want) return [200, mach, {}];
      throw new Problem(408, "timeout", `the machine is ${mach.state}, not ${want}`);
    }
    if (method === "POST" && action === "exec") {
      const b = only(body, ["command", "shell", "env", "cwd", "user", "binary_output"]);
      if (Boolean(b.command?.length) === Boolean(b.shell)) throw new Problem(400, "invalid_request", "give `command` or `shell`, not both");
      this.execs.push(b);
      const argv = b.command ?? ["/bin/sh", "-lc", b.shell];
      mach.state = "running";
      const out = { exit_code: 0, stderr: "", duration_ms: 3.2, truncated: false };
      if (argv[0] === "exit") Object.assign(out, { exit_code: Number(argv[1]), stderr: "bye\n" });
      if (b.binary_output) out.stdout_base64 = Buffer.from([0, 1, 255, ...Buffer.from("binary")]).toString("base64");
      else out.stdout = argv[0] === "echo" ? `${argv.slice(1).join(" ")}\n` : `ran ${JSON.stringify(argv)}\n`;
      return [200, out, {}];
    }
    if (action === "files") {
      const where = query.get("path") ?? "";
      if (!/^(home|work)\/.+/.test(where)) throw new Problem(400, "invalid_request", "a path begins with home/ or work/");
      if (method === "PUT") {
        this.files.set(`${mid} ${where}`, raw);
        return [204, null, {}];
      }
      if (method === "GET") {
        const got = this.files.get(`${mid} ${where}`);
        if (!got) throw new Problem(404, "not_found", "no such file in the machine");
        return [200, got, { "content-type": "application/octet-stream" }];
      }
    }
    throw new Problem(404, "not_found", "no such route");
  }
}

/** A fake API of a test's own, and a client of it whose pauses are counted, never slept. */
export async function setup(t, options = {}) {
  const api = await new FakeAPI().start();
  const sleeps = [];
  const make = (opts = {}) => {
    const client = new Wheelmachine({ apiKey: KEY, baseURL: api.url, ...options, ...opts });
    client._sleep = async (ms) => {
      sleeps.push(ms);
    };
    client._jitter = () => 0.5;
    return client;
  };
  t.after(() => api.stop());
  return { api, sleeps, client: make(), make };
}

/** A port on this computer that nothing listens on. */
export async function closedPort() {
  const s = http.createServer();
  await new Promise((resolve) => s.listen(0, "127.0.0.1", resolve));
  const { port } = s.address();
  await new Promise((resolve) => s.close(resolve));
  return port;
}
