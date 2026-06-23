# RogerAI - marketing site

The front door for **RogerAI**, *a two-way radio for GPUs*: a community of
local-AI enthusiasts who put their self-hosted LLMs **on air** so others can
**tune in** and pay per token. One `rogerai` CLI (an interactive TUI) does all
of it.

This is a **static, no-build** site - vanilla HTML/CSS/JS, deployable as-is to
Cloudflare Pages (output dir = `web/`).

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

## Run it (no build)

```sh
cd web
python3 -m http.server 5173
# → http://localhost:5173
```

## Deploy

Cloudflare Pages, **no build command**, output directory `web`. Full steps and
the rationale for the tech choices are in **[`TECH.md`](TECH.md)**. The hero
`curl` resolves to **[`install.sh`](install.sh)** served from this same dir.

## install.sh

A real POSIX installer: detects OS/arch, downloads the matching
`rogerai-<os>-<arch>` asset from `github.com/bownux/rogerai` releases into
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
