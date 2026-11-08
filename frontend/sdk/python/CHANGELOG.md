# Changelog

`wheelmachine`, the Wheelmachine library for Python. Each release is tagged `py-v<version>`.

## 0.1.3

- A machine's desktop, seen and worked: `client.machines.screenshot()` gives a picture of it (`ScreenPicture`, whose
  `.to_bytes()` is its file); `click()` and `scroll()` act at places read off a picture, and `type()` and `keys()` type
  and press keys; `use_screen()` does several of these in order, with moves, drags and waits, and can give a picture
  after. `machines.create(desktop=True)` turns the desktop on behind the answer, so that a first picture finds it ready.
- `use_screen()` and the calls made of it are never sent again once they may have reached the API, not even when refused
  for a moment, so a click or a word is never done twice.
- Characters that aren't on an English keyboard, such as accents, Russian, Chinese or emoji, can come out wrong when typed.
  Take a picture after typing them to check; a password box can't be checked this way.
- A command (`exec`) answered 503 is no longer sent again, unless the answer says it never reached its machine (`not_run`).
  It used to be sent up to four times in all, and it could run each time.
- The words on `ExecResult.truncated` say what it means: the output was too long, and was cut (past 8 MiB of a stream, or
  as little as 5 MiB) or left out when it could not be brought back, with `exit_code` still said. They said only that it
  was cut at 8 MiB.

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
