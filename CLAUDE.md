# RogerAI - Claude ramp notes

RogerAI is a peer-to-peer marketplace + CLI/TUI to discover hobbyist home-GPU LLMs and pay per
token. Metaphor: **"two-way radio for GPUs"** - operators go ON AIR, you TUNE IN to a channel.
LIVE: `rogerai.fyi` (site) + `broker.rogerai.fyi` (API), both on DigitalOcean. Go monorepo,
public on GitHub as `rogerai-fyi/roger`, **BSL-1.1** (-> Apache 2030-06-23). Latest release **v0.3.3**.

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
- Other packages: `internal/store` (swappable `Store`: `Mem` + `Postgres`/pgx, append-only ledger),
  `internal/detect` (build-tagged HW + local-LLM discovery), `internal/protocol` (hash-chained
  dual-signed receipts), `internal/tokenizer` + `cmd/tokenizer-sidecar` (L1 token re-count),
  `internal/update` (self-updater).

Request flow: `discover -> pick cheapest match -> pre-auth credit HOLD -> relay to node ->
node serves + signs receipt -> broker verifies + co-signs -> capture cost (Finalize) ->
70% owner / 30% broker`. Streaming (SSE) piped node -> /agent/stream -> client.

## Versioning

`const Version` in `cmd/rogerai/main.go` AND `helpVersion` in `internal/tui/tui.go` - bump BOTH.

## Build / test / release

```
GOTOOLCHAIN=local go build ./... && go test ./...   # local dev
make build        # bin/rogerai + bin/rogerai-broker
make demo         # broker + node + request, end to end (needs a local OpenAI endpoint)
make site         # web build (below)
```
Release: `git tag vX.Y.Z && git push --tags` -> `.github/workflows/release.yml` cross-compiles 6
targets (linux/macos/windows x amd64/arm64, `CGO_ENABLED=0` static) + `checksums.txt`.
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
- **Phase-2 (pending):** `auth.css` is still indigo/Inter on ~10 account pages (out of scope for
  this pass; homepage + manual are on-brand).

## Billing / payouts

- **Stripe** SDK-free (raw API + stdlib HMAC), inert until `STRIPE_SECRET_KEY` set. Broker handles
  `checkout.session.completed` (credit wallet) + `charge.dispute.created`. See `docs-internal/STRIPE.md`.
  Production: `sk_live` key, set `STRIPE_WEBHOOK_SECRET` (the broker reads this exact name), add
  `charge.dispute.created` to the webhook, enable Connect, redeploy. Fintech-lawyer gate before
  real money.
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
challenge). **Content-filter** (CSAM/illegal pre-dispatch screen, needs a moderation LLM e.g.
Llama Guard 3) is **DEFERRED - the only open P0**. Privacy: broker is content-blind; users
pseudonymized per-(user,node). Tracked in `docs-internal/{ROADMAP,SUGGESTIONS}.md`.

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

`/bands` discovery page (HuggingFace-style, our theme, no PII beyond callsign) · multi-demo player
(switchable + replay/pause) · "Image #21 into the binary" TUI polish (signal bars, `◉◆` markers,
staged scanning/locking/handshake/CHANNEL OPEN) · Phase-2 page-body redesign (auth.css) · content-
filter moderation · Stripe production · **v0.4.0 "harness" vision** (`dj.md` personas + tool-calling
+ MCP + connections-as-profiles, plus an endpoint-handoff for external harnesses).
