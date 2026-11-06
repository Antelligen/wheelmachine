# Changelog

`wheelmachine`, the Wheelmachine library for Python. Each release is tagged `py-v<version>`.

## 0.1.2

- An account counted in pounds or euros reads its balance, its credit and its prices in that currency.
- `credit.balance`, `credit.currency` and each entry's `amount` are new. `balance_usd` and `amount_usd` still work, and
  are deprecated.
- `client.credit.set_currency()` changes the account's currency, until your first purchase or until we change it for
  you. `client.credit.buy(25)` now buys 25 of that currency.
- `closed` now says what still works, and the words are plainer.

## 0.1.1

First release.

- The whole Wheelmachine API from Python, method for method, with retries that never make two of anything.
- Python 3.9 or later, with the standard library alone.
