# RogerAI - marketing site

The front door for **RogerAI**, *a two-way radio for GPUs*: a community of
local-AI enthusiasts who put their self-hosted LLMs **on air** so others can
**tune in** and pay per token. One `rogerai` CLI (an interactive TUI) does all
of it.

This is a vanilla HTML/CSS/JS site with a tiny dependency-free Node build
(`web/build.mjs` stitches the shared partials -> `web/dist/`). It's hosted on
**DigitalOcean App Platform** (origin) behind **Cloudflare** (CDN/DNS proxy) -
*not* Cloudflare Pages. See [`../.do/app.yaml`](../.do/app.yaml), the Deploy
section below, and **[`EDGE.md`](EDGE.md)** for the Cloudflare edge rules.

## What's on the page

1. **Hero - the install command first.** A copy-on-click box with
   `curl -fsSL https://rogerai.fm/install.sh | sh`. That's the primary CTA,
   the way bun.sh / starship / deno do it.
2. **Animated terminal demo.** A hand-built, asciinema-style replay of the radio
   TUI: run `rogerai` → browse stations by **signal bars** → **tune in** → get a
   local **OpenAI-compatible endpoint + key**.
3. **Blip-map background.** A quiet Canvas2D "radio map": stations on a faint grid
   blink and ripple as they go on air, with occasional beams to a receiver.
4. **The story, tightly.** On-air / tune-in · lineage receipts (gold call-sign
   ◆) · your-price/your-terms (incl. free & time-of-use) · privacy (only the
   model is ever bridged) · monetize idle silicon.

White, minimal, lots of whitespace; one accent (volt `#5B5BFF`) with live/ember/
gold used only as small signals. No emoji, no icon fonts, no JS frameworks.

## Run it (tiny build)

The chrome (nav, footer, head, brand) is defined ONCE in `web/src/_partials/`
and stitched into every page by a dependency-free Node script. Pages live in
`web/src/`; the static output lands in `web/dist/`.

```sh
make site            # node web/build.mjs -> web/dist/
make site-serve      # build + http.server on http://localhost:5173
```

Include syntax: `<!-- include: nav.html variant=marketing -->`; inside a partial,
args substitute as `{{name}}` and gate `{{#if k=v}}...{{/if}}` blocks. The build
fails loudly on any unresolved marker.

**How to change the logo:** edit **`web/src/_partials/brand.html`** - the single
source of the brand mark (the `[ ]` brackets + the live-red circle on-air beacon
+ the spanned `RogerAI` wordmark). Every page's nav and footer brand come from
it. Keep the matching circle beacon in `favicon.svg`, `logo.svg` and Ping's eye
(`ping.svg`) so the brand family stays in sync.

## Wave status (one source)

Every status label a page shows for a Wave tier (Pico to Exa, plus Roger Edge and Wave
Infinite) lives in `src/data/wave-status.json`: the tier's canonical `stage` (one of
`planned`, `base-selected`, `training`, `gated`, `on-air`, `released`, defined in `stages`),
`publicCheckpoint`, `onAirBand`, and a `labels` map of the exact text each page shows today.
The file is build input only and is not shipped.

- **Wired labels** (`"wired": true`) are written in the page as `{{wave:<tier>.<label>}}`,
  e.g. `{{wave:giga.research-models.chip}}`, and `build.mjs` fills them in
  (`scripts/wave-status.mjs`). Change the `text` in the data file and every page follows.
- **Stage words** (`{{wave-stage:<tier>}}`) render the tier's live `stage` as one short
  public word from `stageWords` (`on-air` -> `ON AIR`, `training` -> `TRAINING`, ...). Nothing
  is typed per page: change a tier's `stage` and every stage word follows. The homepage Labs
  plate reads its Pico and Nano status this way.
- **Hand-typed labels** (`"wired": false`) are prose, JSON-LD, the Playbox JS, and the
  research-models stage lines (`test/research-hardware.test.mjs` reads those from source).
  Edit the page and the data file together; `test/wave-status.test.mjs` fails if they drift.

**Changing a tier's stage.** Set `stage` (and `publicCheckpoint` / `onAirBand` if they move),
then update that tier's label texts. Every label text is classified into a stage in the
`IMPLIES` table of `test/wave-status.test.mjs`; a new wording must be added there.

**Founder ruling.** A tier whose labels imply different stages carries a bare
`"ruling": "pending"` flag; the ledger test works out which labels disagree from the labels
themselves and requires the conflicted tiers to be exactly the pending ones. When the founder
rules: set `stage`, rewrite the tier's labels so they all imply it, delete `ruling`, and remove
the tier from the pending list at the bottom of the test. The ledger only shrinks when the
pages actually agree.

## Capability status (one source)

Adapters, base skills and device rows (the "your device, now a sensor" blocks) read
`src/data/capabilities.json`: each entry's `stage` is one of `planned`, `lab`,
`release-candidate`, `available` (defined in `stages`, with the public words in
`stageLabels`). Pages write `{{cap:<id>.<name|stage|label|description>}}` and `build.mjs`
fills them in (`scripts/wave-status.mjs`). The file is build input only and is not shipped.
Flip a stage in the file and every lamp follows; `test/capabilities.test.mjs` checks every
`data-cap` element on every built page against it and sweeps the site for any adapter
claimed available, live or running ahead of the file. A lamp is red only for `available`.

## Layout (2026-10, the SvelteKit migration)

Every page of the site is a route of the SvelteKit app in `app/`, prerendered to a static
file. `build.mjs` writes the one `dist/` tree the host serves:

```
web/
  app/                      the SvelteKit app (SvelteKit 3, Svelte 5, Vite 8)
    app.html                the shell: the two CSP-hashed inline scripts, nothing else
    routes/<page>/+page.svelte   one route per page; routes/+page.svelte is the homepage
    routes/<post>/+page.md       a broadcast as Markdown, laid out by lib/ui/BroadcastPost.svelte
    lib/head.js             the per-page <head>: siteHead({ title, desc, og, css, scripts })
    lib/ui/*.svelte         the chrome, once: Nav, Footer, Rail, Brand, OnAir, InstallBox, ...
    lib/api.js              the one broker client (credentialed fetch, getJSON, logout, the login-gate rule)
    lib/format.js, lib/fmt.js   money, counts, dates, the rfmt tile action; fmt.js is generated from src/js/fmt.js
    lib/rollup.js           the /metrics/series rollups (per model, totals) the dashboard and metrics share
    lib/content.js          wave, model-card and capability facts from src/data, one resolver
    lib/island.js, lib/ui/Island.svelte   islands: the interactive parts, hydrated by Svelte alone
    lib/account/            boot.js (the first read: only a 401 is "signed out") and the account
                            islands: LoginApp, LinkApp (/r), DeviceApp, AccountApp, StationsApp, PayoutsApp
    lib/console/, lib/dashboard/, lib/keys/, lib/billing/, lib/private/, lib/usage/
                            one folder per signed-in page: its component tree + the pure modules
                            it renders from (charts.js, topup.js, spark.js, rc.js, series.js, ...)
    islands/<name>.js       one entry per island, bundled to dist/js/islands/<name>.js
    body-attrs.json         per-route <body> attributes (class="auth", data-onair)
  src/                      the static tree every page uses: js/ (the site runtime and the
                            content pages' classic scripts), styles/, assets/, data/ (build input), install.sh
  build.mjs                 the orchestrator: islands -> app -> finishing passes -> sitemap, llms.txt
  test/                     the guard suite (node:test) over dist/ and the sources; test/unit (vitest):
                            every island's behavior over a fake broker, one file per page
  e2e/                      browser flows (Playwright) over the built site
```

Rules the build enforces (test/dual-build.test.mjs): every route builds and no page is a
file in src/; no page hydrates through SvelteKit's client (no inline loader, no `_app`
chunk), so the edge CSP stays `script-src 'self'`; an island hydrates its own prerendered
markup, strictly; the app shell's inline scripts are byte-identical to the legacy head's
(their hashes are allow-listed at the edge); every built page is cache-busted, carries one
canonical, and ships no hydration comment outside an island.

Every signed-in page is an island (console, dashboard, stations, account, keys, billing,
payouts, metrics, Base Station, sign-in, the device approval and the /r link handler): the
route is a shell (head, nav, `<Island>`, footer), the card is a component tree under
`app/lib/<page>/`, and the page's behavior is a vitest file under `test/unit/`. The Go
scenarios that pin a page (features/auth/*.feature, `# test:` pointers) run those files
through `cmd/rogerai-broker/web_spec_test.go`. `e2e/islands.spec.js` proves each island
hydrates its prerendered markup in place in a real browser.

## Deploy

Two layers (this is **not** Cloudflare Pages):

**Host / origin - DigitalOcean App Platform.** A single app spec
(**[`../.do/app.yaml`](../.do/app.yaml)**) defines both the `broker` service and
the `site` static site; both `deploy_on_push: true` on `main`. At deploy time DO
runs the build (`build_command: node web/build.mjs`, `output_dir: web/dist`) and
serves the result. The hero `curl` resolves to
**[`src/install.sh`](src/install.sh)**, copied to the output root.

**Edge - Cloudflare (CDN/DNS proxy).** The apex and the `broker.` subdomain are
proxied by Cloudflare in front of the DO origin (`x-do-app-origin` + `server:
cloudflare` in the live headers). DO static hosting can't emit custom response
headers, so the security headers in [`src/_headers`](src/_headers) and the `www ->
apex` redirect in [`src/_redirects`](src/_redirects) are **not** applied by the
host - they're mirrored to the Cloudflare edge as Transform / Redirect Rules. The
exact rules + an apply script are in **[`EDGE.md`](EDGE.md)**.

The rationale for the tech choices is in **[`TECH.md`](TECH.md)**.

## install.sh

A real POSIX installer: detects OS/arch, downloads the matching
`rogerai-<os>-<arch>` asset from `github.com/rogerai-fyi/roger` releases into
`~/.local/bin`, and prints a PATH hint. If no release is published yet (or the
asset is missing for a platform), it degrades gracefully with a clear
"build from source" message instead of failing silently. Override the version
with `ROGERAI_VERSION=vX.Y.Z` or the dir with `ROGERAI_INSTALL_DIR=…`.

## Files

```
web/
├─ build.mjs           the build: partial includes, per-page CSS bundles, cache-busting
├─ src/
│  ├─ *.html           the pages
│  ├─ _partials/       shared HTML: head, nav, footer, site-js (the page runtime), ...
│  ├─ styles/          tokens.css -> base.css -> components.css -> each page's own sheet
│  └─ js/              site.js (the runtime) + self-initializing component modules
├─ test/               node --test suites (npm test), incl. the design-system guards
├─ DESIGN-SYSTEM.md    layers, tokens, components and their markup contracts
├─ TECH.md             tech choices + how to deploy
└─ README.md           this file
```

How the look is built (and how to add a page or change the theme):
**[`DESIGN-SYSTEM.md`](DESIGN-SYSTEM.md)**.
