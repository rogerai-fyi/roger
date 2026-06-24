# RogerAI - Claude ramp notes

RogerAI is a peer-to-peer marketplace + CLI/TUI to discover hobbyist home-GPU LLMs and pay per
token. Metaphor: **"two-way radio for GPUs"** - operators go ON AIR, you TUNE IN to a channel.
LIVE: `rogerai.fyi` (site) + `broker.rogerai.fyi` (API), both on DigitalOcean. Go monorepo,
public on GitHub as `rogerai-fyi/roger`, **PolyForm Perimeter 1.0.0** (source-available, noncompete). Latest release **v4.3.0**
(the v4 line = web-parity CLI/TUI redesign + the `[0] AGENT` harness + the public Ping concierge).

This file is the fast-ramp map. Internal working notes are in `docs-internal/` (gitignored).

## Architecture (three parts, one binary)

- **Broker** `cmd/rogerai-broker/` - the ONLY public component. OpenAI-compatible relay + registry
  + wallet + metering, content-blind (stores token counts + signed receipts, never prompts).
  Files split by concern:
  - `main.go` wiring; `tunnel.go` relay/poll/stream/pick/credit-hold; `market.go` /discover,/market;
    `dashboards.go` + `account.go` /balance,/me,/earnings,/account; `billing.go` Stripe top-ups +
    `charge.dispute.created`; `payouts.go` Connect payouts; `httputil.go` helpers.
  - Serves `cmd/rogerai-broker/openapi.yaml` at `/openapi.yaml`.
- **Node-agent** `internal/agent/` - provider side (`rogerai share`). **Dials OUT**: N outbound
  long-poll workers pull jobs, serve against the local upstream, sign a lineage receipt, post it
  back. No inbound ports, no tunnel (AI-Horde pattern). NOT Cloudflare/Tailscale.
- **Client / TUI** `internal/client/`, `internal/tui/`, `cmd/rogerai/` - one binary: consume
  (`search`/`use`/`balance`) or share. The TUI is a Bubble Tea "radio" experience.
  - **Transparent failover:** `internal/client/failover.go` - on node failure, re-select an
    alternative that satisfies EVERY `Criteria` constraint (model/confidential/min-tps/max-price);
    never a silent downgrade.
- **Agent harness** `internal/harness/` - the `[0] AGENT` tool-use loop embedded in the TUI
  (`internal/tui/agent.go`). `loop.go` runs the tool-call loop (MaxSteps-bounded, degrade-to-chat
  if the channel's model can't tool-call); `tools.go` is a SMALL bounded set - `read_file` /
  `list_dir` / `web_fetch` auto-run, `write_file` / `run_shell` are confirm-gated (y/N), all
  sandboxed to the cwd via `resolveInRoot` (absolute paths and `../` escapes rejected); `persona.go`
  loads the `dj.md` persona from `~/.config/rogerai/dj.md` (written on first run, user-editable);
  `broker.go` is the completer (relay through the broker). NO persistent memory (session-only),
  per-turn answer budget 4096, tool-output previews. The agent runs on the resolved model: the
  open channel, else the LAST band tuned this session (`lastConnected`), else the `/model` picker -
  never a stale config default.
- Other packages: `internal/store` (swappable `Store`: `Mem` + `Postgres`/pgx, append-only ledger),
  `internal/detect` (build-tagged HW + local-LLM discovery), `internal/protocol` (hash-chained
  dual-signed receipts), `internal/tokenizer` + `cmd/tokenizer-sidecar` (L1 token re-count),
  `internal/update` (self-updater).

Request flow: `discover -> pick cheapest match -> pre-auth credit HOLD -> relay to node ->
node serves + signs receipt -> broker verifies + co-signs -> capture cost (Finalize) ->
70% owner / 30% broker`. Streaming (SSE) piped node -> /agent/stream -> client.

## Versioning

`const Version` in `cmd/rogerai/main.go` AND `helpVersion` in `internal/tui/tui.go` - bump BOTH.
On a release, also update `web/src/manual.html` (the `data-cli-version` spans + add a changelog row), run `node web/build.mjs`, and `git add -f web/dist` - the smoke gate fails if the built manual does not mention the current `const Version`.
**Push-verify step (works - keep doing it):** the tag-time bump CAN lose the push race if you tag
before the bump commit lands. Verified 2026-06-24: tags v4.2.2..v4.3.0 all carry the correct
`Version`/`helpVersion` - the verify-by-refetch step is what keeps them in sync. After cutting,
re-fetch and confirm the bump landed on `origin/main` BEFORE you `git tag`.
Beware the grep trap: a `git log` / `branch -r` line reading `main ->` (a remote symref) is NOT
`HEAD -> main` (your tip pushed) - match the wrong one and a failed push looks like a success.

## Build / test / release

```
GOTOOLCHAIN=local go build ./... && go test ./...   # local dev
make build        # bin/rogerai + bin/rogerai-broker
make demo         # broker + node + request, end to end (needs a local OpenAI endpoint)
make site         # web build (below)
```
**Release gate:** run `make smoke` (must be green) before every `git tag`. It runs build + vet +
gofmt + the regression suite, builds + serves `web/dist`, asserts every page returns 200, and
crawls every internal `<a href>` to catch the clean-URL-404 class of bug; prints `SMOKE: PASS/FAIL`
and exits non-zero on any failure. `make smoke-live` adds production checks (site + broker
`/health` + a credentialed-CORS preflight). Script: `scripts/smoke.sh`. The same link crawl also
runs under `go test ./...` via the self-contained `test/smoke/` package.

Release: `git tag vX.Y.Z && git push --tags` -> `.github/workflows/release.yml` cross-compiles 6
targets (linux/macos/windows x amd64/arm64, `CGO_ENABLED=0` static) + `checksums.txt`.
After tagging, **re-fetch and verify the version bump landed** (see the Versioning push-verify gotcha).
Install: `curl rogerai.fyi/install.sh | sh` (POSIX) / `irm rogerai.fyi/install.ps1 | iex` (Windows);
`rogerai upgrade` self-updates.

## Web is single-sourced (build step)

`web/src/*.html` are the source pages; shared chrome lives in `web/src/_partials/`
(head/brand/nav/footer) pulled via `<!-- include: nav.html variant=marketing -->` markers
(`{{var}}` + `{{#if}}`/`{{#unless}}` gates). `node web/build.mjs` (or `make site`) resolves
includes -> `web/dist/`. **`web/dist/` is committed and is what DO serves** (DO uses the Go
buildpack, no node at deploy). Workflow: edit `web/src`, run the build, commit `web/dist`.
Dependency-light (Node ESM + `node:fs`, no npm install).
**DO serves NO clean URLs** - every page is `.html`; all internal links and the broker redirect
envs (`ROGERAI_DASHBOARD_URL`/`LOGIN_URL`/`CONSOLE_URL`) MUST be `.html` paths.

## Pages: Stations vs Models, + the Ping concierge

- **Stations = nodes (on-air GPUs); Models = the LLMs.** `/bands.html` was renamed to
  **`/models.html`** (`/bands.html` redirects; the nav is a single "Models" link). `web/src/models.html`
  + `web/src/js/bands.js`. The Models page is **REAL-DATA-ONLY**: no demo/fake bands, an honest
  empty state, a localStorage grayed-out history of stations seen, and an on-air/offline filter.
  It hosts the interactive mouse-driven tuning dial (moved off the homepage).
- **Homepage** has a small auto-animated **dial teaser** (`web/src/js/teaser.js`) + a multi-demo
  **tape-deck player** (auto-play, auto-advancing playlist) whose `rogerai` demo opens on the
  SHARE / provider-detection flow.
- **Ping concierge** (`web/src/js/ping-chat.js`): an always-on side ticker + a draggable/closable
  type-in popup that POSTs to the broker `POST /concierge` (`cmd/rogerai-broker/concierge.go`).
  Fallback chain = dogfood a free on-air station -> Groq `llama-3.3-70b-versatile` (`GROQ_API_KEY`)
  -> a canned "off air" reply (never an error). Bounded: small persona, per-IP rate limit
  (`ROGERAI_CONCIERGE_RPM`/`_BURST`, default 6), a global daily message cap, and a STOPGAP keyword
  precheck (`unsafeTerms`). **This is the FIRST public unauthenticated LLM surface** - the real
  content-filter P0 (a `MODERATION_URL` / Llama Guard hook on the relay) is being wired now; the
  keyword precheck is only a stopgap.

## Design system - "The Live Operating Manual"

Specs: `docs-internal/design/direction-foundation.md` (type/color/layout/copy) +
`direction-signature.md` (motion/assets/Ping persona).

- **~95% monochrome + ONE red beacon.** Live red `#E0231C` (light) / `#FF4438` (dark); red is a
  signal, never a surface (no filled brand-color buttons). Retired the old indigo "volt" `#B14BFF`.
  Warm neutrals: paper `#FBFBFA` / ink `#15140F` (light), paper `#0E0D0B` / ink `#F3F1EA` (dark).
- **Type:** Space Grotesk (display/UI) + JetBrains Mono (numbers/commands/labels, tabular-nums +
  slashed-zero). Body prose never in mono; numbers/labels/commands always in mono.
- **Layout:** flat, hairline-defined (no shadows except the terminal demo); left-bound document
  grid on a fixed spine (`.rail` running head + `§`-markers + `FIG.` labels); square corners.
- **Signature motifs:** command-as-hero (the install command is the primary element); a **tuning
  dial** hero (frequency strip locks onto the strongest band from `/market`); the **/market signal
  field** (instrument panel driven by the broker `/market` endpoint, `signal` 0-100 towers);
  the **beacon** (state-driven ON-AIR light); **Ping** persona (one-eyed `(( • ))` state machine:
  standing by / listening / on air / no carrier); an oscilloscope transcript on the demo; a
  **colophon** footer.
- **Motion discipline:** single shared rAF; animate only transform/opacity; full
  `prefers-reduced-motion` fallbacks; no new runtime dependency (CSS/SVG/Canvas only); page usable
  with JS/network off.
- **All account/operational pages are now restyled** onto the manual (auth.css moved off the old
  indigo); the colophon footer is right-aligned and footer/nav are grouped.

## Billing / payouts

- **Stripe** SDK-free (raw API + stdlib HMAC), inert until `STRIPE_SECRET_KEY` set. Broker handles
  `checkout.session.completed` (credit wallet) + `charge.dispute.created`. See `docs-internal/STRIPE.md`.
- **Config = PER-ENV** (no `_PROD_`/flag): the broker env holds the LIVE values in
  `STRIPE_SECRET_KEY` / `STRIPE_WEBHOOK_SECRET`; the local `.env` holds test. `ROGERAI_REQUIRE_LIVE=1`
  is an opt-in **fail-closed guard** (refuse to start in test mode). The broker logs `[LIVE]` vs
  `[test]` at startup (detects the `sk_live` prefix). **Currently [test mode].**
- **Production switch (operator):** set the broker's `STRIPE_SECRET_KEY` = `sk_live`,
  `STRIPE_WEBHOOK_SECRET` = the live `whsec`, subscribe `charge.dispute.created` on the webhook
  (`broker.rogerai.fyi/billing/webhook`), set `ROGERAI_REQUIRE_LIVE=1`, enable Connect, redeploy.
  Validate the live flow with the `web-fetch` CLI. Fintech-lawyer gate before real money.
- **Payout policy = OPTION A:** 90-day HOLD, NO separate reserve (`ROGERAI_PAYOUT_RESERVE` default
  0), $25 MIN, MONTHLY, Connect-KYC gated. Payout rail is transfer-SAFE (`payouts.go`: debit-first
  store txn -> transfer exact amount -> settle/fail rollback). Append-only `rogerai.ledger`;
  `DeriveBalance` re-derives the wallet to catch counter drift. Design: `docs-internal/ACCOUNT-PAYOUTS-DESIGN.md`.

## Identity / wallet

One wallet per GitHub account (`walletOf`); anon = free-only, no balance (`SeedOnce`). Consumers
sign every broker request with an Ed25519 user key (`~/.config/rogerai/user.key`); the broker
derives id from the pubkey (`X-Roger-User` is not trusted). Owners use `rogerai login`
(GitHub device flow). OAuth callback = `https://broker.rogerai.fyi/auth/github/callback` (the
BROKER exchanges the code, not the static site). Design: `docs-internal/AUTH-DESIGN.md`.

## Security / launch P0s

3 of 4 launch-gating P0s CLOSED: **auth** (Ed25519 + GitHub OAuth), **double-spend** (credit
Hold -> Finalize/ReleaseHold, atomic `WHERE balance>=amount`), **node-register** (Ed25519
challenge). **TWO launch gates remain:** the **content-filter P0** (CSAM/illegal pre-dispatch
screen via a `MODERATION_URL` / Llama Guard hook on the relay - IN PROGRESS, now urgent because the
Ping concierge is a live public unauthenticated LLM surface) and the **Stripe production switch**.
Privacy: broker is content-blind; users pseudonymized per-(user,node). Tracked in
`docs-internal/{ROADMAP,SUGGESTIONS}.md`.

## Deploy (DigitalOcean App Platform)

Broker + static site are DO apps in the **RogerAI** DO project; `git push origin main`
auto-redeploys both. doctl authed via `DOCTL_API_TOKEN` in `.env`; repo/env changes via
`doctl apps update --spec` (DO GitHub app must be installed on the org). Broker app id
`681cb8bc-d413-4477-8913-3b2eb980681b`; web app id `ae8ca67b-c3a2-4353-842a-e12f4eb6e97b`.
Postgres reuses `db-halo-main`, least-priv `rogerai` schema (`rogerai.*` tables); broker uses
`DATABASE_URL`, admin SQL via `DATABASE_ADMIN_URL`. `BROKER_PRIVATE_KEY` (hex ed25519 seed) must
be set in prod or receipts won't verify across restarts. See `docs-internal/DEPLOY.md`.

## Conventions / gotchas (Claude sessions)

- Commits authored `bownux <bownux@users.noreply.github.com>`, **NO Claude co-author trailer**.
- The pre-push **`claude-audit` hook frequently hangs >180s** -> validate, then
  `git push --no-verify`. It serializes pushes; run ONE pushing agent at a time.
- Worktree-isolated subagents on **disjoint file domains**; push-once after rebase. Ensure the
  shell CWD is inside the repo before launching them. Never `git reset --hard` the main tree while
  an agent holds unpushed commits.
- `docs-internal/` is gitignored - design/platform/review notes live there.
- Visual/login-walled validation: the `web-fetch` CLI (Playwright Chromium daemon).
- No em/en dashes in any RogerAI text (use "-").

## Pending queue

**Launch gates:** content-filter moderation P0 (in progress) · Stripe production (operator sets the
live env values). **Next-up:** MCP support + connections-as-profiles for the `[0] AGENT` harness
(the rest of the "harness" vision beyond the shipped agent) · an endpoint-handoff for external
harnesses · any account-page polish still trailing the redesign.
