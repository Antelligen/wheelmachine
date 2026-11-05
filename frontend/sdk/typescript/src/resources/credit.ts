/**
 * `client.credit`: the account's credit - prepaid usage credit in dollars, for Wheelmachine alone. Your own account
 * gets $5 of free credit once - at once if you signed up with GitHub, or when you link a GitHub account at least 30
 * days old - and a new organisation none; machines' running time is taken from it once a
 * minute, one entry a machine a day; at zero or below the account cannot spend, and its machines are paused until
 * credit is bought (`buy`). In an organisation the credit is the organisation's: read by its owners, admins and
 * billing, and bought by its owners and billing (PermissionDeniedError for anyone else, and for a key limited to some
 * projects).
 */

import { type Core, withResponse } from "../core.js";
import { PageIterator } from "../pagination.js";
import type { CreateOptions, CreditBalance, CreditEntry, CreditParams, CreditPurchase, RequestOptions, WithResponse } from "../types.js";
import { keyFor } from "./shared.js";

export class Credit {
  readonly #core: Core;

  /** @internal */
  constructor(core: Core) {
    this.#core = core;
  }

  /**
   * The credit: what is left, whether it has run out, whether credit can be bought here yet, and its latest `limit`
   * entries (1 to 200; 50 when left out), the newest first.
   */
  async get(params: CreditParams = {}, options: RequestOptions = {}): Promise<CreditBalance> {
    const query: Array<[string, string]> = params.limit !== undefined ? [["limit", String(params.limit)]] : [];
    const { data } = await this.#core.json<CreditBalance>({ method: "GET", path: "/v1/credit", query, options });
    return data as CreditBalance;
  }

  /** Every entry of the credit, the newest first, page after page as it is iterated; `limit` is how many a page. */
  entries(params: CreditParams = {}, options: RequestOptions = {}): PageIterator<CreditEntry> {
    const base: Array<[string, string]> = params.limit !== undefined ? [["limit", String(params.limit)]] : [];
    return new PageIterator<CreditEntry>(async (after) => {
      const query: Array<[string, string]> = after === null ? base : [...base, ["after", after]];
      const { data } = await this.#core.json<CreditBalance>({ method: "GET", path: "/v1/credit", query, options });
      return { items: data?.data ?? [], next: data?.next || null };
    });
  }

  /**
   * A checkout for `amountUsd` dollars of credit - 10, 25, 50 or 100 - started. Nothing is charged until it is paid in
   * Paddle's checkout: open `checkout.url` in a browser signed in to the dashboard, in the same account. Paddle adds
   * any sales tax or VAT and e-mails the receipt and tax invoice; the credit is there once Paddle says the payment is
   * complete. Where buying credit is not set up, the error's `code` is `not_open`. Like every create, a retry after a
   * lost connection never starts two.
   */
  async buy(amountUsd: number, options: CreateOptions = {}): Promise<WithResponse<CreditPurchase>> {
    const { data, info } = await this.#core.json<CreditPurchase>({
      method: "POST",
      path: "/v1/credit/purchases",
      json: { amount_usd: amountUsd },
      idempotencyKey: await keyFor(options),
      options,
    });
    return withResponse(data ?? ({} as CreditPurchase), info);
  }
}
