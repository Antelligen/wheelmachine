# Changelog

`wheelmachine`, the Wheelmachine library for TypeScript and JavaScript. Each release is tagged `ts-v<version>`.

## 0.1.5

- Deploy is open: `client.deployments` puts an app on the internet from a repository of your connections, a public
  repository's https address or an archive of files (`sendFiles()`), on a machine of its own at a public address, and
  follows, reads, changes, redeploys and deletes it. Its calls no longer say they are not open yet.
- `client.machines.repositories()` says which repositories a machine reaches, to read or to push too, and whether a push
  will go, and `client.machines.openPullRequest()` opens a pull request from a branch: on GitHub at once, under the name
  of the person who holds the machine, and on GitLab its `run` is the line of git to run in the machine's clone, which
  opens a merge request as it pushes. Only the person who holds the machine may open one, a retry never opens two, and
  neither call merges, approves or closes anything. Their types are `MachineRepositories`, `PullRequestParams` and
  `PullRequest`.
- A machine pauses no sooner than 30 minutes idle, and one set to less now waits 30 minutes: `machines.create()`'s
  `idle_after_s` is 1800 when left out, where it was 300, and less is refused.
- A machine whose internet is `"closed"` now has none, in or out, and no name lookups: a public port of it answers only
  as a private one. Whatever a machine's internet, its team's repositories and folders still work where given.
  `NetworkPolicy.internet` says so.

## 0.1.4

- A machine's desktop, seen and worked: `client.machines.screenshot()` gives a picture of it (`pictureBytes()` gives its
  file); `click()` and `scroll()` act at places read off a picture, and `type()` and `keys()` type and press keys;
  `useScreen()` does several of these in order, with moves, drags and waits, and can give a picture after.
  `machines.create({ desktop: true })` turns the desktop on behind the answer, so that a first picture finds it ready.
- `useScreen()` and the calls made of it are never sent again once they may have reached the API, not even when refused
  for a moment, so a click or a word is never done twice.
- Characters that aren't on an English keyboard, such as accents, Russian, Chinese or emoji, can come out wrong when typed.
  Take a picture after typing them to check; a password box can't be checked this way.
- A command (`exec`) answered 503 is no longer sent again, unless the answer says it never reached its machine (`not_run`).
  It used to be sent up to four times in all, and it could run each time.
- The words on `ExecResult.truncated` say what it means: the output was too long, and was cut (past 8 MiB of a stream, or
  as little as 5 MiB) or left out when it could not be brought back, with `exit_code` still said. They said only that it
  was cut at 8 MiB.

## 0.1.3

- An account counted in pounds or euros reads its balance, its credit and its prices in that currency.
- `credit.balance`, `credit.currency` and each entry's `amount` are new. `balance_usd` and `amount_usd` still work, and
  are deprecated.
- `client.credit.setCurrency()` changes the account's currency, until your first purchase or until we change it for
  you. `client.credit.buy(25)` now buys 25 of that currency.
- `closed` now says what still works, and the words are plainer.

## 0.1.2

First release.

- The whole Wheelmachine API from TypeScript, method for method, with retries that never make two of anything.
- ES modules for Node 18 or later, calling the API with the runtime's own `fetch`; no dependencies.
