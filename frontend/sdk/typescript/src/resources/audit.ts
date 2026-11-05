/**
 * `client.audit`: what was done in the account, by whom - the person, and the key or sign-in they did it with - to which
 * thing, and from where. Read with a key that holds every scope (`cloud:*`), and in an organisation by its owners and
 * admins alone (PermissionDeniedError for anyone else).
 */

import type { Core } from "../core.js";
import { saveTo, writeTo } from "../files.js";
import { PageIterator } from "../pagination.js";
import type {
  AuditEvent,
  AuditExportParams,
  AuditListParams,
  AuditQueryParams,
  ExportOptions,
  Page,
  RequestOptions,
} from "../types.js";
import { rfc3339 } from "./shared.js";

/** An export of the audit may be held this long (seconds) before its first byte: a year of it is read whole first. */
const HOLDS_EXPORT = 120;

/** What the audit is narrowed by, as the API takes it: times in RFC 3339. */
function narrowed(params: AuditQueryParams): Array<[string, string]> {
  const query: Array<[string, string]> = [];
  if (params.person !== undefined) query.push(["person", params.person]);
  if (params.action !== undefined) query.push(["action", params.action]);
  if (params.since !== undefined) query.push(["since", rfc3339(params.since, "since")]);
  if (params.until !== undefined) query.push(["until", rfc3339(params.until, "until")]);
  return query;
}

export class Audit {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * Every event, the newest first, page after page as it is iterated: `person` is one person's id, `action` what the
   * actions begin with (`"machine."`, `"member."`), `since` and `until` times (a Date, or RFC 3339), `until` itself
   * left out; `limit` is how many come in each page (1 to 200).
   */
  list(params: AuditListParams = {}, options: RequestOptions = {}): PageIterator<AuditEvent> {
    const base = narrowed(params);
    if (params.limit !== undefined) base.push(["limit", String(params.limit)]);
    return new PageIterator<AuditEvent>(async (after) => {
      const query: Array<[string, string]> = after === null ? base : [...base, ["after", after]];
      const { data } = await this.#core.json<Page<AuditEvent>>({ method: "GET", path: "/v1/audit", query, options });
      return { items: data?.data ?? [], next: data?.next || null };
    });
  }

  /**
   * The audit as a file, whole: `"csv"` (every field quoted, and one a spreadsheet would run begun with `'`, so that it
   * is shown and never run) or `"json"` (an array of events, the newest first). It covers a year at most - the last one
   * when `since` is left out - and 100,000 events at most; asking for more is refused (BadRequestError) in the API's
   * own words. Gives the file's bytes, or with `to` streams it into that local path (written whole or not at all; a new
   * one readable by you only, as it names people and where they were) or WritableStream, and gives how many bytes came.
   */
  export(params?: AuditExportParams, options?: RequestOptions & { to?: undefined }): Promise<Uint8Array>;
  export(params: AuditExportParams, options: RequestOptions & { to: string | WritableStream<Uint8Array> }): Promise<number>;
  export(params?: AuditExportParams, options?: ExportOptions): Promise<Uint8Array | number>;
  async export(params: AuditExportParams = {}, options: ExportOptions = {}): Promise<Uint8Array | number> {
    const format = params.format ?? "csv";
    if (format !== "csv" && format !== "json") throw new TypeError('format is "csv" or "json"');
    const { to, ...rest } = options;
    const answer = await this.#core.send({
      method: "GET",
      path: "/v1/audit/export",
      query: [["format", format], ...narrowed(params)],
      accept: format === "csv" ? "text/csv" : "application/json",
      timeout: this.#core.held(HOLDS_EXPORT),
      options: rest,
      stream: to !== undefined,
    });
    if (to === undefined) return answer.bytes;
    const idle = rest.timeout ?? this.#core.timeout;
    try {
      return typeof to === "string"
        ? await saveTo(answer.response, to, idle, this.#core.baseURL, 0o600)
        : await writeTo(answer.response, to, idle, this.#core.baseURL);
    } finally {
      answer.finish();
    }
  }
}
