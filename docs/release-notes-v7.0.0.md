# v7.0.0 - one program, a private network you can join with one line, and phones on the Edge

Draft for review. Not tagged. The version strings (internal/buildinfo, openapi.yaml, the manual's changelog) are not bumped yet: the smoke gate ties the buildinfo fallback to web/src/manual.html, which is frozen for the SvelteKit migration. Release builds are stamped at link time, so a tagged binary still reports 7.0.0. This release lands after the routing pull requests on main; the
routing work is listed here because this branch carries it too, and it ships to users in this
release.

Why a major version: `roger-tower` and `roger-tower-local` stop being programs of their own, and
the Edge authority gains a TLS listener and a new presence version. Old names and old wire forms
keep working for one release (details below). And the Go module path moves to
`rogerai.fm/roger/v7`, which is a breaking change for anyone importing the module.

## The module path is `rogerai.fm/roger/v7`

Go only considers tags whose major version matches the module path's suffix, so a v7 tag needs
a /v7 path. Installing from source becomes `go install rogerai.fm/roger/v7/cmd/rogerai@latest`.
`rogerai.fm/roger/v6` and `rogerai.fm/roger/v5` keep resolving from their own tags: their
go-import pages stay served and the edge rule still covers them. Most people install with
`install.sh` or a package manager and see no change.

## One program: `roger tower`

`roger` is the only program you download. A Tower operator runs `roger tower ...` with the same
commands and flags `roger-tower` had (`init`, `config`, `doctor`, `ready`, `status`, `invite`,
`admit`, `attach`, `stations`, `route`, `serve`, `login`, `logout`, `register`, `earnings`,
`probe`, `station`, `drain`, `resume`, `revoke`, `version`). `roger tower help` lists them.

`roger tower serve` serves what the data directory's mode says. A standalone directory runs the
consumer plane `roger-tower-local` used to run, now also reading `--config` and a configured
durable store. A joined directory runs the link and the sealed hub. A flag that belongs to the
other mode is refused with exit 2. A standalone Tower now runs on macOS and Windows; joined
Towers stay on Linux.

For one release `roger-tower` and `roger-tower-local` still ship on Linux as small stand-ins.
Each prints one line saying it is now `roger tower` and hands the process to `roger`, so units
and scripts that use the old names keep working. The next release removes them.

The promise that a standalone Tower cannot reach Roger Core now rests on two checked facts: the
standalone serve package imports no Core code (a dependency test enforces it), and in standalone
mode the process refuses every non-local connection and refuses to start if a Core client was
built.

## Joining, in one line

`roger join <invite>` is the one command for joining an Edge or a Tower. The owner runs
`roger edge invite` or `roger tower invite` and gets one line to paste, also drawn as a QR code in
the terminal. An invite is single use, expires after 15 minutes, names the address and the key
of what it joins, and has a guess budget. A spent or expired invite says so.

The second way is request and approve: `roger join <address>` asks, both screens show the same
phrase built over both keys, and the owner presses y after comparing. The owner can answer from
the terminal, the TUI or the console.

A Tower's identity is its pinned key over TLS. An upgraded Station pins its Tower on its first
TLS poll. `roger lan add` asks before pinning, and a Tower the owner declined to pin is never
pinned on first use. `roger edge enroll --invite` and `roger lan join` remain as old names for
one release.

## LAN mode

`roger --lan` limits the dial to Towers on your LAN, your Edge members and this machine. It is a
filter on where turns go, not an internet lockdown. In LAN mode chat, the agent, `roger use`,
voice and streaming all stay on the LAN, answers carry `X-RogerAI-Cost: 0`, and the TUI says LAN
mode in words and drops money text. `roger --edge-only` narrows it to Edge members and this
machine, never a Tower. `roger lan add|rm|list` manages the Towers, and `roger status` says what
the broker is still used for.

## Phones on the Edge

These changes are for the Roger app's Edge tab. The App Store version of the app does not have
an Edge tab yet, so nothing changes for it.

- The Edge authority also serves its paths over TLS, under a server certificate the Edge root
  issues (ServerAuth only, 24 hours, reissued at half its life or when the LAN addresses change,
  never written to disk). The plain listener keeps enrollment, claim, root, revocations, pairing
  and presence, and names the TLS port on every answer.
- A phone can use the Edge's models through its authority, signing each request with its member
  key. The authority relays the turn to the member that serves the band over its own pinned
  mutual TLS, with an attestation only the authority can make. A phone request is checked from
  its headers before it takes a slot or a byte of body.
- Presence v3 carries a power reading for any kind of node (battery, charging, source, low power,
  minutes left). Four default alert rules use it. v1 and v2 presence keep working.
- An invite bound to a device is bound to its key, and an allowed key gets no free guesses at an
  invite. A joiner's `checks_root` counts only when it signs it.
- The replay cache keeps nonces per node, with a quota, so one member cannot crowd out the rest.

## Routing

A request can say how it wants to be routed: model lists with fallbacks, provider order within a
rung, price caps, a minimum speed, a first-token ceiling, regions, and model classes such as
`@class/medium` (chat models of 7B to 24B). The same rules apply on the broker, in `roger use`
and on the LAN ladder, and malformed or duplicate-key routing objects are refused everywhere.
API keys can be minted, limited and deleted; an Idempotency-Key is held once charged so a retry
is never charged twice. Request-path state such as cooldowns, limits and idempotency claims lives
in the shared store, so two broker instances agree.

## Local models

`roger model get`, `serve` and `rm` resolve a short name by path, hub reference, disk, what is
served nearby, then the hub. Two quantisations of one repository on disk are picked by the
quantisation rule. `serve --adapter` serves a LoRA beside its base. Output is four lines by
default, with the full account behind `--verbose`, and one fit verdict.

## Also

- Edge instance names stop climbing on restart.
- Each mDNS socket hears only the interface it joined.
- The enrolled Edge screen ([3] EDGE) uses the shared look.
- On Windows the config lock and the owner admin channel use native locking and an owner-only
  named pipe.
- Every join screen says what happened and gives one next step.

## Upgrading

Upgrade the broker before relying on the new routing fields from a client. Run `roger upgrade` on
each machine. An Edge authority on v7 still accepts the enrollment, claim and presence (v1 and v2)
forms that earlier builds send, over the same plain listener. Upgrade the Edge authority before
its members, so v3 presence and the phone paths have somewhere to go.

## Deployment note

The site must serve `/roger/v7/` (its go-import page) and the Cloudflare vanity-import rule must
cover `/roger/v7` BEFORE the v7.0.0 tag is pushed, or the first `go install .../v7/...` resolves
nothing. Ship the page from main first (branch wt/vanity-v7), re-apply the rule with
`node web/scripts/cf-edge.mjs --apply`, confirm `--check` reports no drift for `/roger/v7`, then tag.
