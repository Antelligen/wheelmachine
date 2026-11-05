/**
 * The API's objects, written by hand from its OpenAPI contract. Field names are the API's own (snake_case), so the
 * API's reference reads one to one; times are ISO 8601 strings in UTC, such as `2026-10-23T07:00:00Z`.
 */

/**
 * `starting` being made ready; `running`; `paused` memory kept, nothing running; `stopped` files kept, memory not;
 * `archived` files in storage; `recovering` a fault on our side stopped it, and it is being brought back; `deleted`; `failed` it
 * never came up, and `failure` says why - nothing is charged for it, and it is gone as a deleted machine is.
 */
export type MachineState = "starting" | "running" | "paused" | "stopped" | "archived" | "recovering" | "deleted" | "failed";

/** What a key may do. `cloud:*` holds all of the others. */
export type Scope =
  | "cloud:*"
  | "cloud:machines:read"
  | "cloud:machines:write"
  | "cloud:machines:exec"
  | "cloud:gpus"
  | "cloud:projects:write"
  | "cloud:keys:write"
  | "cloud:secrets:write"
  | "cloud:folders:read"
  | "cloud:folders:write";

export type KeyState = "active" | "revoked" | "expired";

/**
 * A person's role in an organisation: `owner` may do everything; `admin` everything but change owners; `billing` sees
 * what machines cost and the people, no machine, and changes nothing but a person's limit for the month; `member` makes
 * and uses machines in their teams - those they hold, and in a team they lead those its members hold. In a person's own
 * account they are its `owner`.
 */
export type Role = "owner" | "admin" | "billing" | "member";

/**
 * A team manager's level: `approver` sees the team's members' machines, watches their screens and decides the team's
 * hand-offs; `manager` also starts, stops, pauses and deletes those machines and puts people in the team; `full` also
 * decides its forks, sets its rules and makes approvers. None uses or forks a member's machine.
 */
export type Level = "approver" | "manager" | "full";

/**
 * How a team's machines are handed off: `free`; `ask`, with a manager's OK; `off`, not at all. Forks are `ask` or `off`
 * alone (`TeamRules.forks`).
 */
export type RuleWay = "free" | "ask" | "off";

/** An error's stable code. */
export type ErrorCode =
  | "invalid_request"
  | "unauthorized"
  | "key_revoked"
  | "key_expired"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "project_not_empty"
  | "payment_required"
  | "rate_limited"
  | "limit_reached"
  | "unavailable"
  | "timeout"
  | "too_large"
  | "not_open"
  | "no_apps"
  | "internal"
  | "login_denied"
  | "login_expired"
  /** A risky step needs a code we e-mail the person, sent with `X-Confirm-Code`; or the one sent is not right. */
  | "confirm_required"
  | "confirm_wrong";

/** An error as the API gives it: `application/problem+json` (RFC 9457). */
export interface Problem {
  /** A link to the page that explains this `code`. */
  type: string;
  title: string;
  status: number;
  detail?: string;
  code: ErrorCode;
  request_id?: string;
  retry_after_s?: number;
  /** The limit that stopped the call, where one did. */
  limit?: LimitHit;
  /** What the code a risky step needs must be for, where it needs one (`confirm_required`, `confirm_wrong`). */
  confirm?: ConfirmAsk;
}

/**
 * The limit that stopped a call: Wheelmachine's for the account, the account's own, a team's budget or a person's own;
 * of what, at what and how much is used (null when you are not shown them), when a limit for the month lifts, and who
 * can raise it.
 */
export interface LimitHit {
  scope: "wheelmachine" | "account" | "team" | "person";
  measure: "month_usd" | "machines" | "vcpus" | "gpus" | "folders_bytes" | "file_bytes" | "folder_entries";
  limit: number | null;
  used: number | null;
  resets_at: string | null;
  raised_by: string[];
}

/** Why a machine is paused, while a limit holds it so. */
export interface PauseReason {
  why: string;
  limit: LimitHit;
}

/** The account a call acts in: a person's own, or an organisation's. */
export interface Account {
  id: string;
  /** `person` for a person's own account; `org` for an organisation's. */
  kind?: "person" | "org";
  /** An organisation's name, or the person's in their own account. */
  name?: string;
}

/** A person, by their own account's id. Their e-mail address is said with the dashboard's sign-in only, never to a key. */
export interface Person {
  id: string;
  name: string;
  /** Their @handle, without its @: theirs alone; empty for a person not yet given one. */
  handle: string;
  email: string | null;
  github_login: string | null;
}

/** Whose a machine of an organisation is: the person who holds it, by their own account's id and their name. */
export interface HeldBy {
  id: string;
  name: string;
  /** Their @handle, without its @: theirs alone; empty for a person not yet given one. */
  handle: string;
  /** Their role there; null once they are no longer in it. */
  role: Role | null;
  /** Their level when they manage the machine's team; null otherwise. */
  level: Level | null;
}

/** A person, by their own account's id and their name alone. */
export interface PersonName {
  id: string;
  /** As they signed in with it; empty when they gave none. */
  name: string;
  /** Their @handle, without its @: theirs alone; empty for a person not yet given one. */
  handle: string;
}

/** A person's own limits in an organisation, beside the organisation's own: null is no limit of their own. */
export interface PersonLimits {
  /** The machines they may hold. */
  max_machines: number | null;
  /** The CPUs their machines may run at once. */
  max_vcpus: number | null;
  /** Whether they may make GPU machines. */
  gpus: boolean;
  /** What their machines may come to in a month, in dollars. */
  month_cap_usd: number | null;
}

export interface Standing {
  /** False when the credit has run out, the month's limit is reached, or the account is on hold. */
  can_spend: boolean;
  /** What is left to spend this month under the account's limit; null for an account with no monthly limit. */
  balance_usd?: number | null;
  /** The account's credit left; null for an organisation's member, who does not read it. */
  credit_usd?: number | null;
  /** Why it cannot spend, in words a person reads. */
  reason?: string;
}

/** An API key, never its secret. */
export interface Key {
  id: string;
  object: "key";
  name: string;
  scopes: Scope[];
  /** The projects it is limited to; empty for every project of the account. */
  projects: string[];
  /** Its first and last characters, to recognise it by. */
  shown: string;
  state: KeyState;
  rate_per_minute?: number | null;
  created_at: string;
  expires_at?: string | null;
  last_used_at?: string | null;
  /** In an organisation, the person who made it, by their own account's id; null in a person's own account. */
  made_by?: string | null;
}

/** A key just made, with its secret: shown in this answer only. */
export interface KeyCreated extends Key {
  secret: string;
}

/**
 * The account and key a call is made with: the account it acts in, the person behind it - the one who made the key -
 * and their role there; in an organisation, their teams and their own limits there too.
 */
export interface Me {
  object: "me";
  account: Account;
  person?: Person;
  /** Their role in the account; `owner` in their own. */
  role?: Role;
  /**
   * In an organisation, for a member, the projects of their teams - empty for one in no team yet; null for any other
   * role, and in a person's own account.
   */
  teams?: string[] | null;
  /** In an organisation, the person's own limits there; null in their own account. */
  limits?: PersonLimits | null;
  /** What the call holds. */
  scopes?: Scope[];
  key: Key;
  standing: Standing;
  /** In an organisation, their level in each team they manage, by its project; empty otherwise. */
  levels?: Record<string, Level>;
}

/** An organisation as one of its people sees it. */
export interface Org {
  id: string;
  object: "org";
  name: string;
  /** Your role in it. */
  role: Role;
  /** You are its primary owner, the owner who transfers it. */
  primary_owner: boolean;
  created_at: string;
  /** How many people are in it. */
  people: number;
}

/** A line of the account's audit: what was done, to which thing, by whom, from where. */
export interface AuditEvent {
  id: string;
  object: "audit_event";
  at: string;
  /** Such as `machine.create`, `member.role` or `machine.terminal.open`. */
  action: string;
  /** What it was done to: a machine, a key, a person. */
  target: string;
  /**
   * Who did it, by their id and name; null for what Wheelmachine did by itself, and for Wheelmachine's staff.
   */
  person: { id: string; name: string } | null;
  /**
   * The key (`key_...`) or the sign-in (`ses_...`) it was done with; `system` for what Wheelmachine did by itself, `wheelmachine`
   * for Wheelmachine's staff.
   */
  by: string;
  /** Where it came from. */
  address: string;
  request_id: string;
  detail: Record<string, string>;
}

export interface Project {
  id: string;
  object: "project";
  name: string;
  created_at: string;
}

export interface GpuRef {
  /** An `id` from `client.gpus()`. */
  id: string;
  model?: string;
  count?: number;
  memory_gb?: number;
}

export interface Size {
  cpus: number;
  memory_gb: number;
  disk_gb?: number;
}

export interface Location {
  city?: string;
  /** ISO 3166-1 alpha-2. */
  country?: string;
}

/** What a machine may reach on the internet. */
export interface NetworkPolicy {
  /** `"open"` (the whole internet), `"closed"` (nothing), or `"allowed"` (only `allow`). */
  internet: "open" | "closed" | "allowed";
  /**
   * For `"allowed"`: domain names (exact, or a leading `*.` for every name under it) for HTTP and HTTPS, and IPv4/IPv6
   * ranges (CIDRs) for other ports. Empty for `"open"` and `"closed"`.
   */
  allow: string[];
}

export interface Machine {
  id: string;
  object: "machine";
  name: string;
  project: string;
  /** In an organisation, the person who made it, by their own account's id; null in a person's own account. */
  made_by?: string | null;
  /** In an organisation, whose it is: the person who holds it, with their role there; null in a person's own account. */
  held_by?: HeldBy | null;
  /**
   * What you may do with it: `full` - you hold it and use it all - or `watch`: in an organisation, its owners and admins
   * and its team's managers see a member's machine and watch its screen, and never use it.
   */
  access?: "full" | "watch";
  state: MachineState;
  gpu?: GpuRef | null;
  size?: Size | null;
  /** Where it runs now. */
  location?: Location | null;
  /** Its CPUs, 2, 4, 8 or 16; null for a GPU machine, whose size comes with its GPU. */
  cpus?: number | null;
  /** Its memory, 2 GB for each CPU; null for a GPU machine. */
  memory_gb?: number | null;
  /** True: it pauses by itself once it is idle (nothing reached it for `idle_after_s`, its programs quiet as long). */
  sleep_when_idle: boolean;
  /** How long it must be idle before it pauses by itself; null when it never does. */
  idle_after_s?: number | null;
  labels: Record<string, string>;
  /** The names of the account's secrets its commands get as environment variables; never their values. */
  secrets?: string[];
  /**
   * The names of the account's AI keys - its secrets on every machine - that reach it: its commands get each as a
   * placeholder, and the key is added on the way out, only to the key's hosts.
   */
  ai_keys?: string[];
  /** What it may reach on the internet. */
  network: NetworkPolicy;
  /** Its files in storage are encrypted. */
  files_sealed: boolean;
  /** All the time it has run, which is what is charged. */
  running_seconds?: number;
  disk_bytes?: number;
  /** CPUs it used, about a minute's average, when running. */
  cpu_cores?: number;
  memory_bytes?: number;
  created_at: string;
  last_used_at?: string | null;
  /** How long making it ready took, when this answer made it ready (create, start, fork). */
  ready_ms?: number;
  /** Why it never came up, in plain words, when its `state` is `failed`. */
  failure?: string;
  /** Why it is paused, while a limit holds it so: the limit's words as you are told them, and the limit. */
  pause_reason?: PauseReason | null;
  /** It is being handed off, or cleared for the person it was forked for: every way into it is refused meanwhile. */
  clearing?: boolean;
  /** Where it came from, when another person's machine was its start: a fork of theirs, or handed off by them. */
  origin?: Origin | null;
  /**
   * In an organisation, who handed it off to the person who holds it: they watch it, and take it back whenever they choose
   * (`machines.takeBack`); its holder gives it back to them (`machines.giveBack`). Null when nobody did.
   */
  given_by?: PersonName | null;
}

/** Where a machine came from, when another person's machine was its start. */
export interface Origin {
  /** `fork`: a fork of another person's machine; `hand_off`: handed off by them. */
  kind: "fork" | "hand_off";
  /** The person whose machine it was. */
  from: PersonName;
  /** The name the machine it came from had then. */
  machine_name: string;
  at: string;
}

/**
 * A request's state: `waiting` on someone's OK (`waiting_on` says whose); `approved` and being carried out; `done`;
 * `failed` (`failure` says why); `declined`; `expired` after a day; `taken_back` by the one who asked.
 */
export type RequestState = "waiting" | "approved" | "done" | "failed" | "declined" | "expired" | "taken_back";

/**
 * A request, in an organisation, to fork a machine for someone or to hand it off to them: given by its holder (or one
 * above them), or asked for by the person who would get it - or a machine handed off, back with its giver, or a member's
 * fork of a machine they hold (`machines.fork`), made once it is approved.
 */
export interface MachineRequest {
  id: string;
  object: "request";
  kind: "fork" | "hand_off";
  /**
   * `give`: by the machine's holder or one above them - and a member's fork of their own machine, its `holder` and
   * `receiver` both them; `ask`: by the one who would get it; `back`: a machine handed off, back with the one who handed
   * it off - taken back by them (`asked_by` is the `receiver`), or given back by its holder.
   */
  way: "give" | "ask" | "back";
  /** The machine, by its name when it was picked and by its id to those who reach it; null while an ask waits for its holder's pick. */
  machine: { id: string | null; name: string } | null;
  holder: PersonName;
  /** Who gets the machine, or the fork. */
  receiver: PersonName;
  asked_by: PersonName;
  /** The machine's team, by its project. */
  from_team: string | null;
  /** The team it goes into, when it is another. */
  to_team: string | null;
  gpu: boolean;
  why: string;
  state: RequestState;
  /**
   * Whose OK it waits on - the person who made the key (`asker`, by name: one given or asked with a key, as every call of this library
   * is, which they approve signed in to the dashboard), its holder's (by name), a manager's (for a fork, a full
   * manager's, an owner's or an admin's) or an admin's; null once it waits no more.
   */
  waiting_on: { who: "asker" | "holder" | "manager" | "admin"; name: string | null } | null;
  decided_by: PersonName | null;
  decision_why: string;
  /** The fork, by its id, once it is made. */
  result: string | null;
  /**
   * A fork from one of the machine's backups: that backup's id; null for a fork of the machine as it is, and for a
   * hand-off.
   */
  backup: string | null;
  /** A fork from a backup asked at another size: its CPUs; null for the machine's own size. */
  cpus: number | null;
  /** Why it could not be carried out. */
  failure: string | null;
  /** The machine started again on the way, its programs ended. */
  restarted: boolean;
  /** Wheelmachine keys found in it as it was cleared, and revoked. */
  keys_revoked: number;
  created_at: string;
  expires_at: string;
  done_at: string | null;
}

/**
 * A team's rules, in an organisation: those set, else the defaults - hand-offs and forks `ask`, GPUs and open internet
 * on, no budget.
 */
export interface TeamRules {
  object: "team_rules";
  project: string;
  /** Whether its machines are handed off freely, with a manager's OK, or not at all. */
  hand_offs: RuleWay;
  /**
   * Whether its members fork its machines - every fork, of their own machine or for another person, waiting for the OK
   * of a full manager of the team, an owner or an admin (`ask`) - or not at all (`off`). Never `free`.
   */
  forks: "ask" | "off";
  /** Whether GPU machines are made, forked or started in it. */
  gpus: boolean;
  /** Whether its machines may have open internet; one open already stays open until changed. */
  open_internet: boolean;
  /** What its machines may come to in a month, in dollars; null for no budget. */
  month_budget_usd: number | null;
  /** What its machines came to this month: shown to its managers, owners, admins and billing; null to its members. */
  spent_usd: number | null;
  /** Its machines whose internet is open; null as `spent_usd` is. */
  open_machines: number | null;
  /** `any`, until a team chooses its models. */
  models: string;
  set_by: PersonName | null;
  set_at: string | null;
  /**
   * The largest machine it holds, in CPUs: a bigger one is not made, forked, moved or handed into it, nor started in
   * it. Null for no rule.
   */
  max_machine_vcpus: number | null;
  /** The longest its machines stay up idle before they sleep, in seconds. Null for no rule. */
  idle_max_seconds: number | null;
  /**
   * Whether its machines' ports may be opened to the whole internet (`on`), only shared by a five-minute link
   * (`links_only`), or neither (`off`).
   */
  public_ports: "on" | "links_only" | "off";
}

/**
 * A backup: a machine's files as they were when it was taken. `machines.fork(backup.machine, { backup })` makes a new
 * machine from it, at the machine's size or another.
 */
export interface Backup {
  id: string;
  object: "backup";
  machine: string;
  note?: string;
  created_at: string;
}

/** What a command wrote and how it ended. */
export interface ExecResult {
  exit_code: number;
  /** Left out when `binary_output` was asked for: then the bytes are in `stdout_base64`. */
  stdout?: string;
  stdout_base64?: string;
  stderr: string;
  duration_ms: number;
  /** True when the output was longer than 8 MiB and was cut there. */
  truncated: boolean;
}

/**
 * What a machine used, a point a step. A point a minute is kept for up to the last day: `kept_since` is how far back
 * that goes (null: nothing kept).
 */
export interface MachineMetrics {
  object: "metrics";
  machine: string;
  step_s: number;
  /** What `cpu_percent` is a share of. */
  cpus: number | null;
  kept_since: string | null;
  points: MetricsPoint[];
}

/** One step of a machine: null for a number that could not be read, and for the rates of a step that covers none. */
export interface MetricsPoint {
  at: string;
  /** Its CPUs' use, a share of its own size, 0 to 100. */
  cpu_percent: number | null;
  /** The most memory it held in the step. */
  memory_bytes: number | null;
  memory_limit_bytes: number | null;
  disk_read_bps: number | null;
  disk_write_bps: number | null;
  net_rx_bps: number | null;
  net_tx_bps: number | null;
}

/**
 * A secret as the API shows it: its name, the machines given it and how it is kept - never its value, which is not
 * shown again.
 */
export interface Secret {
  object: "secret";
  name: string;
  /** The machines that name it, that the key in use can see. */
  machines: string[];
  /** Set when it is kept outside the machines; null for an ordinary secret, whose value reaches them. */
  outside: Outside | null;
  /**
   * On every machine of the account that may send it somewhere, not only on those that name it: an AI key is, unless
   * it was set with `every_machine: false`.
   */
  every_machine: boolean;
  created_at: string;
  /** When its value was last set. */
  updated_at: string;
}

/**
 * How a secret kept outside the machines is sent - never its value, which never enters a machine: the machine holds a
 * placeholder, and the value is added on the way out, only to `hosts`.
 */
export interface Outside {
  /** The preset it follows (`client.secrets.presets()`), an AI key's; null for a policy of its own. */
  preset: string | null;
  hosts: string[];
  /** The first of `headers`. */
  header: string;
  format: string;
  /** Every request header its value goes in. */
  headers: HeaderRule[];
  /** Query parameters whose value may carry it. */
  query: string[];
}

/** A request header a secret kept outside the machines goes in, and how it is written there: `{value}` once. */
export interface HeaderRule {
  name: string;
  format: string;
}

/**
 * A model provider's key as the API knows it by its names: set under one of `names`, a secret is kept outside the
 * machines, on every one of them, and sent only to `hosts`.
 */
export interface SecretPreset {
  object: "secret_preset";
  id: string;
  /** The provider, as people know it. */
  title: string;
  names: string[];
  hosts: string[];
  headers: HeaderRule[];
  query: string[];
}

/** A policy of its own, for a secret kept outside the machines. */
export interface OutsideSet {
  hosts: string[];
  header: string;
  /** How the value is written, with `{value}` once; `{value}` by default. */
  format?: string;
}

/** How a secret is kept, beside its value; each left out is the API's default for the name. */
export interface SecretSetOptions extends RequestOptions {
  /**
   * `true`: the name's preset (an AI key). `false`: an ordinary secret, its value in the machines given it, for a tool
   * that needs the real value. An object: a policy of its own. Left out: the name's preset when it has one.
   */
  outside?: boolean | OutsideSet;
  /** `false`: only on the machines given it. Left out: true for an AI key, false for any other secret. */
  every_machine?: boolean;
}

/** A port of a machine open at a web address of its own; HTTP, WebSockets and streams pass through. */
export interface Port {
  object: "port";
  machine: string;
  port: number;
  /**
   * True: anyone may reach the address. False: only a call with an API key that can see the machine, and a browser
   * that opened a link made for it; anyone else is answered 401.
   */
  public: boolean;
  /** `https://<port>-<the machine's id without m_>.run.wheelmachine.ai`. */
  url: string;
  created_at: string;
}

/** A link just made, that lets one browser into a private address for a while: shown in this answer only. */
export interface PortLink {
  object: "port_link";
  machine: string;
  port: number;
  /** Opens once, within five minutes. Shown in this answer only. */
  url: string;
  /** When the link stops opening, if it was not opened. */
  expires_at: string;
  /** How long the browser that opens it may reach the address (seconds). */
  access_s: number;
}

/**
 * `web`: a page at the machine's private web address for its port; `service`: reached from inside the machine alone;
 * `agent`: an agent with a session of its own in the machine's terminal, running its `command`; `terminal`: an agent run
 * in its terminal by typing its `command`.
 */
export type AppKind = "web" | "service" | "agent" | "terminal";

/**
 * An app in a machine, as the machine says it is now. `link` (with `expires_at` and `access_s`) comes in the answer
 * to turning a web app on and to `link()`; `token` in the answer to turning on an app that has one of its own. Each
 * is shown in that answer only: keep them out of logs.
 */
export interface App {
  object: "app";
  machine: string;
  name: string;
  title: string;
  kind: AppKind;
  /** Whether it is turned on; null for a terminal agent, which runs while it is typed. */
  on: boolean | null;
  /** Whether it runs now - for an agent, whether its session does; null for a terminal agent. */
  running: boolean | null;
  /** Where it listens inside the machine, when that is said. */
  port: number | null;
  /** A web app's own page on its address. */
  path: string | null;
  /** A web app's private address: `https://<port>-<the machine's id without m_>.run.wheelmachine.ai`. */
  url: string | null;
  /** What an agent's session runs, or what to type in the machine's terminal to run a terminal agent. */
  command: string | null;
  /**
   * For an agent: whether its session, which runs, began with other AI keys than the machine has now. Turned off and on
   * again, its next session begins with them. Left out when the machine does not say it.
   */
  keys_changed?: boolean;
  /**
   * For an agent, and a web app with an agent of its own: the providers whose AI keys it can use, in the order it takes
   * them. Left out when the machine does not say them.
   */
  suppliers?: string[];
  /** With `suppliers`: what it last started on, provider/model; null when it has not started yet, or picks its own model. */
  model?: string | null;
  /** With `suppliers`: who chose what it last started on. */
  model_by?: "chosen" | "team" | "default" | "agent" | null;
  /** With `suppliers`: whether it runs on another model than the one chosen for it now. */
  model_changed?: boolean;
  /** A link that lets one browser into the app's address once, within five minutes, landing on its page. */
  link?: string;
  /** When `link` stops opening, if it was not opened. */
  expires_at?: string;
  /** How long the browser that opens `link` may reach the address (seconds). */
  access_s?: number;
  /** The app's own token, which its page asks for: in the answer to turning it on alone. */
  token?: string;
}

/** A place a machine can be asked to run near. */
/** A code host connected to the account: a GitHub App installation, or a GitLab group. */
export interface CodeConnection {
  object: "code_connection";
  id: string;
  provider: "github" | "gitlab";
  host: string;
  name: string;
  suspended: boolean;
  added_at: string;
}

/** A repository a team is given: to `read`, or to `write` (push) too. */
export interface CodeGrant {
  object: "code_grant";
  id: string;
  project: string;
  connection: string;
  host: string;
  repo: string;
  access: "read" | "write";
  granted_at: string;
}

/** The account's code hosts, and the repositories given to the teams the caller is in. */
export interface Code {
  object: "code";
  github: boolean;
  connections: CodeConnection[];
  grants: CodeGrant[];
}

/** A folder given to a team (a project, in a person's own account): to `view`, or to `edit`. */
export interface FolderGrant {
  object: "folder_grant";
  folder: string;
  folder_name: string;
  project: string;
  access: "view" | "edit";
  granted_at: string;
}

/** A team folder, or a person's own (My files), as you may use it: `size` sees how much it keeps, alone. */
export interface SharedFolder {
  object: "shared_folder";
  id: string;
  kind: "team" | "own";
  name: string;
  person: { id: string; name: string } | null;
  /** A person's own: when they left the organisation; it goes 30 days after (`goes_at`). */
  left_at: string | null;
  goes_at: string | null;
  access: "edit" | "view" | "size";
  teams: FolderGrant[];
  /** What it keeps, its Trash included. */
  bytes: number;
  changed_at: string | null;
  made_at: string;
}

/** The folders you see - your own first - and, to those who are told, what the account keeps in them and may. */
export interface FolderList {
  object: "list";
  /** Whether folders are set up here. */
  on: boolean;
  data: SharedFolder[];
  used_bytes?: number;
  cap_bytes?: number;
  file_most_bytes: number;
}

/** A file or a folder in a folder. */
export interface FolderEntry {
  name: string;
  type: "file" | "folder";
  size: number;
  changed_at: string;
  /** Who changed it last - from one of their machines too. */
  changed_by: { id: string; name: string } | null;
}

/** One page of what is in one folder of a folder: folders first, then files, each by name. */
export interface FolderListing {
  object: "shared_listing";
  folder: string;
  path: string;
  entries: FolderEntry[];
  /** Where the next page starts, as `after`; null after the last. */
  next: string | null;
}

/** A page of a folder's listing: at most `limit` entries (1 to 1000), from the `next` of the page before. */
export interface FolderListParams {
  after?: string | null;
  limit?: number;
}

/** What one deletion put in a folder's Trash: put back from there until `goes_at`. */
export interface FolderTrash {
  object: "shared_trash";
  id: string;
  folder: string;
  path: string;
  type: "file" | "folder";
  bytes: number;
  deleted_at: string;
  deleted_by: { id: string; name: string } | null;
  goes_at: string;
}

export interface Region {
  id: string;
  city: string;
  country: string;
}

export interface GpuOffer {
  id: string;
  model: string;
  count: number;
  memory_gb: number;
  cpus?: number;
  ram_gb?: number;
  disk_gb?: number;
  location?: Location;
  price_per_hour_usd: number;
  /** Can be interrupted at any time; cheaper for it. */
  interruptible?: boolean;
  available: boolean;
}

export interface GpuList {
  object: "list";
  /** False while GPU machines are not yet open to every account; the list shows what is coming. */
  for_sale: boolean;
  data: GpuOffer[];
}

export interface UsageLine {
  machine: string;
  /** The machine's name. */
  name?: string;
  project?: string;
  /** In an organisation, the person who made the machine; null in a person's own account. */
  made_by?: string | null;
  /** In an organisation, whose the machine is; null in a person's own account. */
  held_by?: HeldBy | null;
  running_seconds: number;
  usd: number;
}

/** Each machine's running time and cost in a month. */
export interface Usage {
  object: "usage";
  month: string;
  total_usd: number;
  machines: UsageLine[];
  /** What the account's folders came to in the month: to those who watch its money, null to anyone else. */
  folders_usd?: number | null;
}

/**
 * What an entry of the credit is: `welcome` the $5 of free credit your own account gets; `machine_time` a
 * machine's running time of a day (UTC); `folders` what the account's folders kept a day; `purchase` credit bought,
 * paid through Paddle (and a disputed payment that came back); `refund` credit refunded through Paddle, or
 * disputed with a bank; `staff_credit` and `staff_debit` credit Wheelmachine's staff put in or took back.
 */
export type CreditEntryKind = "welcome" | "machine_time" | "folders" | "purchase" | "refund" | "staff_credit" | "staff_debit";

/** Credit put in (`amount_usd` above zero) or taken (below). */
export interface CreditEntry {
  id: string;
  object: "credit_entry";
  kind: CreditEntryKind;
  amount_usd: number;
  /** What it is, in words a person reads. */
  reason: string;
  /** The machine whose running time it is; null for any other entry. */
  machine: string | null;
  /** That machine's name, a deleted one's too. */
  machine_name: string | null;
  at: string;
  /** When it last changed: a day's running time grows until the day ends; no other entry changes. */
  updated_at: string;
}

/**
 * The account's credit - prepaid usage credit in dollars, for Wheelmachine alone - and a page of its entries, the
 * newest first. At zero or below the account cannot spend: its machines are paused, and none starts.
 */
export interface CreditBalance {
  object: "credit";
  /** The account it is: your own, or the organisation's. */
  account: string;
  balance_usd: number;
  empty: boolean;
  /**
   * Whether credit can be bought here (`client.credit.buy`), in which amounts - dollars, one at a time - and what
   * Paddle.js is opened with in a browser: Paddle's environment and its client-side token, made to be given to
   * browsers. Closed, no amounts and no Paddle.
   */
  buy: { open: boolean; amounts_usd: number[]; paddle: { environment: "sandbox" | "live"; client_token: string } | null };
  /**
   * Whether its use is taken now: `on`; `from` an account made before billing began, not charged before `from` (null
   * until its notice has gone out); `off` not charged at all.
   */
  charging?: { state: "on" | "from" | "off"; from: string | null };
  /** Below this the credit is running low, and those who buy it are e-mailed once. */
  low_usd?: number;
  /**
   * The free credit your own account is still to get, once you link a GitHub account at least `github_min_days` old
   * on the dashboard's Code page (`too_new_until`: the one linked is old enough then); null when there is none to get.
   */
  free_credit?: { usd: number; needs: "github"; github_min_days: number; too_new_until: string | null } | null;
  data: CreditEntry[];
  /** Where the page after this one begins, as `after`; null for the last. */
  next: string | null;
}

/**
 * A checkout started to buy credit. Nothing is charged until it is paid in Paddle's checkout - `checkout.url` opens it
 * on the dashboard's Billing page, for a person signed in there - and `amount_usd` of credit is there once Paddle says
 * the payment is complete.
 */
export interface CreditPurchase {
  object: "credit_purchase";
  id: string;
  /** The account the credit is for. */
  account: string;
  amount_usd: number;
  checkout: {
    provider: "paddle";
    environment: "sandbox" | "live";
    client_token: string;
    /** Paddle's id for the purchase's transaction, or null while it has none: a purchase can have one before it is paid. */
    transaction_id: string | null;
    /** Paddle's price of this purchase's amount, and the four amounts' prices. */
    price_id: string;
    prices: { amount_usd: number; price_id: string }[];
    /** What Paddle's checkout is opened with, so that its payment names this purchase. */
    custom_data: Record<string, string>;
    url: string;
  };
  created_at: string;
}

/** A page of the credit's entries: how many (1 to 200; 50 when left out). */
export interface CreditParams {
  limit?: number;
}

/** Every limit for the month that applies to the account, as you may see it, and how near each is. */
export interface UsageLimits {
  object: "usage_limits";
  month: string;
  /** When limits for the month start again: the first of the next month, 00:00 UTC. */
  resets_at: string;
  alert_percents: number[];
  account: {
    name: string;
    kind: "person" | "org";
    /** The account's own limit; null for none, or when you are not shown it. */
    month_cap_usd: number | null;
    /** The lower of its own and Wheelmachine's: the one that applies. */
    in_force_usd: number | null;
    in_force: "account" | "wheelmachine" | "";
    spent_usd: number | null;
    percent: number | null;
    limited: boolean;
    set_by: { id: string; name: string } | null;
    set_at: string | null;
    wheelmachine: { month_cap_usd: number | null; max_machines: number; max_vcpus: number | null };
    changed_by: string[];
    you_change: boolean;
  };
  teams: Array<{
    id: string; name: string; month_budget_usd: number | null; spent_usd: number | null; percent: number | null; yours: boolean;
    changed_by: string[]; you_change: boolean;
  }>;
  people: Array<{
    person: { id: string; name: string }; role: string; month_cap_usd: number | null; spent_usd: number | null; percent: number | null;
    max_machines: number | null; max_vcpus: number | null; gpus: boolean; changed_by: string[]; you_change: boolean;
  }>;
  yours: Array<{ scope: LimitHit["scope"]; name: string; limit_usd: number | null; spent_usd: number | null; percent: number | null; reached: boolean }>;
  passed: Array<{ scope: "account" | "team" | "person"; name: string; percent: number; at: string }>;
}

/** A page of a list, as the API gives it; `next` goes in `after` for the page after. */
export interface Page<T> {
  object: "list";
  data: T[];
  next?: string | null;
}

// --- what calls take ---------------------------------------------------------------------------------------------

export interface MachineCreateParams {
  /** For people; ids are made by the API. */
  name?: string;
  /** Its project; the account's first project when left out. */
  project?: string;
  /** A region `id` from `client.regions()` to run near. */
  region?: string;
  /** A GPU `id` from `client.gpus()`. Needs the `cloud:gpus` scope. */
  gpu?: string;
  /**
   * Its CPUs, 2, 4 or 8 (2), with 2 GB of memory each; 16 is not offered yet. Not with `gpu`: its size comes
   * with it.
   */
  cpus?: 2 | 4 | 8;
  labels?: Record<string, string>;
  /**
   * Names of the account's secrets every command run in it gets as environment variables (`client.secrets`). Giving
   * them needs a key with the `cloud:secrets:write` scope, of the whole account.
   */
  secrets?: string[];
  /**
   * What it may reach on the internet: `{ internet: "open" | "closed" | "allowed", allow: [...] }`.
   * Left out, the machine is open. `allow` is empty for open and closed.
   */
  network?: NetworkPolicy;
  /**
   * True (the default): it pauses by itself once it is idle - nothing has reached it for `idle_after_s` and its
   * programs have been quiet as long, so a job left running keeps it going. False: it never pauses by itself, and runs
   * (and is charged) until it is paused or stopped.
   */
  sleep_when_idle?: boolean;
  /** How long it must be idle before it pauses by itself (seconds, 60 to 86400; 300). */
  idle_after_s?: number;
  /** How long to wait for it to be running before answering with it `starting` (seconds, 0 to 600; 60). */
  wait_s?: number;
}

export interface MachineListParams {
  project?: string;
  state?: MachineState;
  /** Machines holding every one of these labels. */
  labels?: Record<string, string>;
  /** How many come in each page (1 to 200; 50). */
  limit?: number;
}

export interface MachineUpdateParams {
  name?: string;
  /** Labels merge: a label given `null` is taken off. */
  labels?: Record<string, string | null>;
  /** Every secret it is to get, by name: the list takes the place of the one it had, and `[]` takes them all away. */
  secrets?: string[];
  /**
   * Its internet policy, said whole: it takes the place of the one it had. A running machine's policy changes at
   * once, without a restart.
   */
  network?: NetworkPolicy;
}

export interface MachineMetricsParams {
  /** From when: a Date, or how long ago as the API writes it (`"6h"`, `"15m"`, `"1d"`); the last hour, a day at most. */
  since?: string | Date;
  /** How long each point covers, in seconds: whole minutes from 60 to 3600 (60). */
  step?: number;
}

export interface MachineStartParams {
  wait_s?: number;
}

export interface MachineForkParams {
  name?: string;
  labels?: Record<string, string>;
  /**
   * One of its backups to make the fork from, starting afresh from its files: a `Backup`, its id, or `"latest"` for its
   * newest. Left out, the fork is of the machine as it is now, at its size.
   */
  backup?: string | Backup;
  /**
   * Only with `backup`: the fork's CPUs, 2, 4 or 8, with 2 GB of memory each - the machine's own when left
   * out. Not for a GPU machine, whose size comes with its GPU.
   */
  cpus?: 2 | 4 | 8;
  wait_s?: number;
}

/** @deprecated `MachineForkParams`, by the name it had before. */
export type MachineCopyParams = MachineForkParams;

/** Whom a machine is forked for, or handed off to, in an organisation. */
export interface GiveParams {
  /** The teammate: their id, e-mail address or GitHub login. */
  to: string;
  /** The team of theirs it goes into, when it is another than the machine's. */
  team?: string;
  why?: string;
}

/** Where a machine handed off goes back to the one who handed it off. */
export interface BackParams {
  /** The team of theirs it goes into, when it should be another than where it is or was handed off from. */
  team?: string;
}

/** An ask of a teammate for a fork of one of their machines, or for one of them: they pick which as they approve. */
export interface RequestAskParams {
  kind: "fork" | "hand_off";
  /** The teammate whose machine: their id, e-mail address or GitHub login. */
  of: string;
  /** The team of yours it goes into, when it is another than the machine's. */
  team?: string;
  why?: string;
}

export interface RequestListParams {
  /** True: those still waiting or being carried out alone. */
  open?: boolean;
  /** Those of one machine: its own, and the forks made of it. */
  machine?: string;
  limit?: number;
}

export interface MachineBackupParams {
  /** At most 200 characters. */
  note?: string;
}

export interface BackupListParams {
  /** How many at most (1 to 200; 100). */
  limit?: number;
}

export interface MachineWaitParams {
  /** Seconds to wait for the state (60). Waits longer than one call can hold are asked for again. */
  timeout_s?: number;
}

/** Give `command` (the words, no shell) or `shell` (a line for `sh -lc`), not both. */
export interface ExecParams {
  command?: string[];
  shell?: string;
  env?: Record<string, string>;
  /** Where it starts; the work folder when left out. */
  cwd?: string;
  user?: "user" | "root";
  /** Give stdout as base64 in `stdout_base64`. */
  binary_output?: boolean;
}

export interface PortOpenParams {
  /**
   * True: anyone may reach the address. False, or left out: only calls with an API key that can see the machine,
   * and browsers that opened a link made for it. A port open already is made public or private as this says.
   */
  public?: boolean;
}

export interface PortLinkParams {
  /** How long the browser that opens the link may reach the address (seconds, 60 to 3600; 1800). */
  access_s?: number;
  /** Where on the address the link lands: a path beginning with `/`, a query may follow it; `/` when left out. */
  path?: string;
}

export interface AppLinkParams {
  /** How long the browser that opens the link may reach the app's address (seconds, 60 to 3600; 1800). */
  access_s?: number;
}

export interface ProjectCreateParams {
  name: string;
}

export interface ProjectUpdateParams {
  name: string;
}

export interface ProjectListParams {
  limit?: number;
}

export interface KeyCreateParams {
  name: string;
  scopes: Scope[];
  /** Projects to limit it to; every project of the account when left out. */
  projects?: string[];
  /** When it stops working: an RFC 3339 time, or a Date. */
  expires_at?: string | Date;
  rate_per_minute?: number;
}

export interface UsageParams {
  /** `YYYY-MM`, or a Date in that month (UTC); this month when left out. */
  month?: string | Date;
  project?: string;
}

/** What the audit is narrowed by; each may be left out. */
export interface AuditQueryParams {
  /** One person's events, by their id. */
  person?: string;
  /** The events whose action begins with this, such as `"machine."` or `"member."`. */
  action?: string;
  /** From this time on: a Date, or an RFC 3339 time. */
  since?: string | Date;
  /** Before this time: a Date, or an RFC 3339 time. */
  until?: string | Date;
}

export interface AuditListParams extends AuditQueryParams {
  /** How many come in each page (1 to 200; 50). */
  limit?: number;
}

export interface AuditExportParams extends AuditQueryParams {
  /** `"csv"` (the default): every field quoted, and one a spreadsheet would run begun with `'`; or `"json"`. */
  format?: "csv" | "json";
}

// --- what the library adds ----------------------------------------------------------------------------------------

/** The key's rate as an answer gave it (the `RateLimit` and `RateLimit-Policy` headers). */
export interface RateLimit {
  /** Calls a minute the key may make. */
  limit: number | null;
  windowSeconds: number | null;
  /** Calls that could be made at once now. */
  remaining: number | null;
  /** Seconds until the whole allowance is back. */
  resetSeconds: number | null;
}

/** How an answer came. */
export interface ResponseInfo {
  status: number;
  /** This call's id: give it when you ask us about a call. */
  requestId: string | null;
  /** True when the API gave a create call's first answer again (the same Idempotency-Key). */
  replayed: boolean;
  rateLimit: RateLimit | null;
}

/** An object a call answered with, with how that answer came in `lastResponse` (not enumerable, so not in JSON). */
export type WithResponse<T> = T & { readonly lastResponse: ResponseInfo };

export interface RequestOptions {
  /** Milliseconds this call may take; calls the API holds by design get the time they need on top. */
  timeout?: number;
  maxRetries?: number;
  /** Stops the call, and any retry of it, when it aborts. */
  signal?: AbortSignal;
}

export interface CreateOptions extends RequestOptions {
  /** The key that makes retries of this create safe; one is made for each call when left out. */
  idempotencyKey?: string;
}

/** A key's own options: a key with full access (`cloud:*`) carries the code we e-mail its person. */
export interface KeyCreateOptions extends CreateOptions {
  /** The 6-digit code `client.confirmCodes.send` had e-mailed for it. */
  confirmCode?: string;
}

/** What a confirm code is for: a risky step, and what of it the code is bound to - as an error's `confirm` says it. */
export interface ConfirmAsk {
  step: "close_account" | "delete_org" | "hand_over" | "make_owner" | "full_access_key" | "key_making_key";
  org?: string;
  to?: string;
  account?: string;
}

/** A confirm code on its way: what it is for, when it stops working, and the address it went to, in part. */
export interface ConfirmCodeSent extends ConfirmAsk {
  object: "confirm_code";
  expires_at: string;
  sent_to: string;
}

export interface ReadFileOptions extends RequestOptions {
  /** A local path to stream the file into (written whole or not at all), or a WritableStream. */
  to?: string | WritableStream<Uint8Array>;
}

export interface ExportOptions extends RequestOptions {
  /**
   * A local path to stream the file into (written whole or not at all; a new one readable by you only), or a
   * WritableStream.
   */
  to?: string | WritableStream<Uint8Array>;
}

export interface WriteFileOptions extends RequestOptions {
  /** The length of a stream: needed for a ReadableStream or an async iterable, whose length is not known. */
  contentLength?: number;
}

/** A model one of the account's AI keys can use, as its provider lists it; `id` is its provider and the provider's own id for it. */
export interface Model {
  object: "model";
  id: string;
  supplier: string;
  model: string;
  name: string;
  created_at: string | null;
  /** When its provider says it is to be retired. */
  retiring_at: string | null;
  /** The agents of the account's machines that can use its provider's models. */
  agents: string[];
  /** What it costs, as the provider that bills for it lists it; null where that is not known. */
  price: ModelPrice | null;
  /** Its score on LMArena's text leaderboard; null where the board has none for it. */
  intelligence: ModelIntelligence | null;
}

/**
 * A model's list price: a million tokens in and a million out, in US dollars, as the provider that bills for it
 * (`source`) lists it at `url`, checked on `as_of` (a day). It is paid to that provider with the account's own key:
 * Wheelmachine charges nothing for a model.
 */
export interface ModelPrice {
  input_per_million_usd: number;
  output_per_million_usd: number;
  source: string;
  url: string;
  as_of: string;
}

/**
 * A model's score on LMArena's text leaderboard: the best it has there at any effort, as a whole number, higher
 * better - the board (`source`) at `url`, its `license`, and the day it was published (`as_of`).
 */
export interface ModelIntelligence {
  score: number;
  source: string;
  url: string;
  license: string;
  as_of: string;
}

/** A provider of the account's AI keys, and how its list stands; `detail` says it in words when it is not `listed`. */
export interface ModelSupplier {
  object: "model_supplier";
  id: string;
  title: string;
  /** The name of the AI key its list is read with. */
  key: string;
  whose: "account" | "organisation";
  state: "listed" | "refused" | "busy" | "unreachable" | "unlisted";
  listed_at: string | null;
  detail: string | null;
}

/** The models of the account's AI keys, and their providers. */
export interface ModelList {
  object: "list";
  data: Model[];
  suppliers: ModelSupplier[];
  /** The list of the models the caller may use that applies to them - its models alone are in `data` then; null when none does. */
  allowed: AllowedModels | null;
}

/** What a list of allowed models is kept for: the whole organisation, a team, a role or one person. */
export type AllowedLevel = "organisation" | "team" | "role" | "person";

/**
 * A list of the models an organisation's people may use, as its owners and admins keep it - for the whole organisation,
 * a team, a role (a person's level in the team: `member`, `approver`, `manager` or `full`) or one person - by `id` and
 * by `name`; its `models` in order, the first the one an agent starts on where its person chose none of them. A person's
 * choices are limited to the most specific list they have: theirs, their role's, their team's, the organisation's.
 */
export interface AllowedModels {
  object: "allowed_models";
  level: AllowedLevel;
  id: string;
  name: string;
  models: string[];
  set_by: PersonName | null;
  set_at: string;
}

/** Which list `client.models.allow()` sets, or `allowAny()` takes away: one of the four. */
export type AllowedAt = { organisation: true } | { team: string } | { role: "member" | "approver" | "manager" | "full" } | { person: string };

/** How an agent stands, in a word. */
export type AgentState = "chosen" | "default" | "own" | "no_key" | "needs_key" | "cannot_use" | "retired" | "not_allowed";

/** An agent of the account's machines, and the model it starts on. */
export interface Agent {
  object: "agent";
  name: string;
  title: string;
  /** The providers it can use, in the order it takes them, each with the model it starts on with that key (null: its own). */
  suppliers: { id: string; title: string; default: string | null }[];
  /**
   * The model chosen for it - for it, or for every agent where it can use that one, or by the list of the models you may
   * use (`allowed`: its first the agent can use, where you chose none it holds).
   */
  model: string | null;
  chosen_by: "you" | "every_agent" | "allowed" | null;
  /** What it starts on with your keys. */
  starts_on: string | null;
  state: AgentState;
  /** How it stands, in a sentence, when it is not as chosen. */
  note: string | null;
}

/** The agents of the account's machines, the one model chosen for every agent, and the list of the models you may use. */
export interface AgentList {
  object: "list";
  data: Agent[];
  every_agent: { model: string; chosen_by: "you" } | null;
  /** The list of the models you may use that applies to your choices in the team; null when none does. */
  allowed: AllowedModels | null;
}

/** What `client.models.list()` narrows to, and asks again of the providers. */
export interface ModelListParams {
  /** Only the models of the providers this agent can use. */
  agent?: string;
  /** Read each list again from its provider, when it was read more than a minute ago. */
  refresh?: boolean;
  /** In an organisation, the team, by its project's id, whose list of allowed models applies to you; left out, your one team. */
  project?: string;
}

/** In an organisation, the team, by its project's id, the choices are for; left out, the caller's one team. */
export interface TeamParams {
  project?: string;
}
