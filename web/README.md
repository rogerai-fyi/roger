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
   `curl -fsSL https://rogerai.fyi/install.sh | sh`. That's the primary CTA,
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
├─ index.html          the page
├─ install.sh          POSIX installer (the hero curl target)
├─ styles/
│  ├─ tokens.css        design tokens (source of truth)
│  └─ site.css          layout & components
├─ js/
│  ├─ radiomap.js       Canvas2D blip-map background
│  ├─ terminal.js       hand-built TUI replay
│  └─ site.js           nav, reveals, copy-on-click, OS hint
├─ TECH.md             tech choices + how to deploy
└─ README.md           this file
```
