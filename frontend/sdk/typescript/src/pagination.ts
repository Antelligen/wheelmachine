/** Lists that page by themselves. */

/** Fetches one page: given the `next` of the page before (null for the first), its items and its own `next`. */
export type PageFetcher<T> = (after: string | null) => Promise<{ items: T[]; next: string | null }>;

/**
 * Every item of a list, page after page: `for await (const machine of client.machines.list()) { ... }`. The next
 * page is asked for only when the items so far are used up, so breaking out early asks for no more than it needs.
 * Each `for await` starts again from the first page.
 */
export class PageIterator<T> implements AsyncIterable<T> {
  readonly #fetch: PageFetcher<T>;

  /** @internal */
  constructor(fetch: PageFetcher<T>) {
    this.#fetch = fetch;
  }

  async *[Symbol.asyncIterator](): AsyncIterator<T> {
    let after: string | null = null;
    for (;;) {
      const page: { items: T[]; next: string | null } = await this.#fetch(after);
      yield* page.items;
      // a `next` that does not move on would page for ever
      if (page.next === null || page.next === after) return;
      after = page.next;
    }
  }
}
