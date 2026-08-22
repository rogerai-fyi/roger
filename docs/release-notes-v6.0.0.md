# v6.0.0 — self-hosted relays, and a module path that matches

334 commits since v5.7.1. The major is not ceremony: the Go module path changes, which
is a breaking change for anyone importing this module, and Go requires the version in the
path to say so.

## Breaking

- **The module path is now `rogerai.fm/roger/v6`.** Installing from source becomes
  `go install rogerai.fm/roger/v6/cmd/rogerai@latest`. Go only considers tags whose major
  matches the path's suffix, so the suffix has to move with the tag or `@latest` silently
  resolves an older major.
- **`rogerai.fm/roger/v5` keeps working and is expected to.** The v5 tags carry a `go.mod`
  naming that path, so anyone who has not migrated keeps resolving from them. The v5
  `go-import` document is still served, the edge rule still covers `/roger/v5`, and a test
  now fails if either is removed — that break is invisible from inside this repository and
  lands only on somebody else's machine.

Nothing else is breaking. No API change, no database migration, no configuration change.

## Self-hosted relays

`roger-tower` is a relay an operator runs themselves, in one of two modes.

- **Standalone** needs no account and makes no outbound connection: it mints its own local
  network and trust root, admits one local operator by a one-time invitation code, accepts
  Station attachments, and routes between them. Local routing is free and locally
  accounted, and the CLI says so on every receipt rather than implying earnings.
- **Joined** holds a link to Roger Core and hosts the sealed data plane, so a Tower can
  carry other people's traffic and be paid for it.

A data directory is initialised as ONE mode for life. Nothing is copied between them,
because an identity, trust root or Station registry must never cross that boundary.

Two commented example configurations ship in `packaging/tower/`. They are tested: the real
loader parses them, and every key in them — **including the commented-out ones** — is
checked against the fields the code declares, so an example cannot rot into advertising a
setting that no longer exists.

`make build` now builds `roger-tower`. It always shipped in the release pipeline; it was
the local developer build that never produced it.

## Security

The relay path was hardened repeatedly while it was being built. The findings worth naming:

- **A Station's assertion key had to be proved, not merely asserted.** Self-attach took the
  key out of the request body, and the request was signed with the caller's ACCOUNT key —
  which proves who is asking and nothing about whether the key they are handing over is
  theirs. Anyone who learned a Station's public key could bind it to a Station of their own
  and deny its owner service indefinitely. Attach now carries a possession proof bound to
  the caller, the timestamp, the body digest and the network.
- **Station identifiers are derived from the assertion key**, so they cannot be squatted by
  construction.
- **The hub epoch was chosen by the caller rather than the hub.**
- **Hub connections are pinned to the certificate's public key**, distributed by Core over
  the link it already authenticates, with TLS 1.3 as the floor. A volunteer's home relay
  has no domain and no publicly-trusted certificate; requiring one would have excluded the
  operators the programme exists for.
- **A refused self-attach could not spend its own invitation**, so repeated refusals could
  lock an account out.
- **Seven days unseen retired a Station permanently**; it now sleeps.
- **A Station's failure was charged to its Tower.** Attribution now follows fault.
- **The public report endpoint wrote to an unbounded table** over an unauthenticated,
  unrated surface. Reports are now retained against a derived horizon, child-safety reports
  get the statutory period, and the endpoint draws on the tighter anonymous rate limiter it
  should always have used.

## Console

The browser console gains settings, a chat landing, and `roger webui` to open it directly.

## Operations

- `/openapi.yaml` and `/version` are now pinned to each other by a test. They are both
  public claims about which build is running, served by the same process, and nothing had
  been keeping them in step.
- Two release-specific version literals were retired from the test suite. Both passed for
  the release they were written for and failed every subsequent bump, naming an old release
  instead of anything wrong with the change in front of you.

## Deployment note

The Cloudflare vanity-import rule must be re-applied so `/roger/v6` is covered. Order
matters: the site has to ship `/roger/v6/` first, or the redirect points at a 404. The v5
path stays in the rule.
