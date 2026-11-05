/** What the resources share. */

import { uuid4 } from "../runtime.js";
import type { CreateOptions } from "../types.js";

/** An id, as one piece of a path. */
export function id(value: string, what: string): string {
  if (typeof value !== "string" || value === "") throw new TypeError(`${what} must be a non-empty string`);
  return encodeURIComponent(value);
}

/** The fields given: undefined means left out (null is kept: it means "take this off"). Nothing given: undefined. */
export function given<T extends object>(fields: T): Partial<T> | undefined {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(fields)) if (v !== undefined) out[k] = v;
  return Object.keys(out).length === 0 ? undefined : (out as Partial<T>);
}

/** A time as the API takes it: a Date (in UTC, to the second), or an RFC 3339 string as it is. */
export function rfc3339(value: string | Date, what: string): string {
  if (value instanceof Date) {
    if (Number.isNaN(value.getTime())) throw new TypeError(`${what} is not a valid Date`);
    return value.toISOString().replace(/\.\d{3}Z$/, "Z");
  }
  return value;
}

/** The Idempotency-Key for one create call: made once, and sent again with every retry of it. */
export async function keyFor(options: CreateOptions | undefined): Promise<string> {
  return options?.idempotencyKey || (await uuid4());
}
