# TECH - what the RogerAI site is built with, and why

The brief: a **super-minimal, white, no-build static site** for a CLI tool - a
curl-install hero, an animated terminal demo, and a quiet animated "radio map"
background. Served as plain files from a static origin behind an edge proxy (see the
Deploy section + `EDGE.md`).

## 2026-10: SvelteKit, prerendered

The decision below stood for a year and the site it produced is the one this build still
emits. What changed in October 2026 is how the pages are WRITTEN: 56 hand-written HTML
files, each pulling chrome from include markers, became routes of one SvelteKit app, with
the chrome as components, the head as one function, the broker client in one module and
the data-file facts behind one resolver. Every signed-in page is a Svelte island (its card a
component tree under `app/lib/<page>/`, its behavior a vitest file), the nine scripts
every page ran ship as one module with `fmt.js` (`app/runtime.js`, built to `js/runtime.js`),
and a page's own scripts as one more (`app/pages/<page>.js`). The
output is the same static tree (every route prerendered, nothing hydrates through
SvelteKit), on the same host, under the same edge CSP, inside the same weight budgets. The plan and
the spike numbers are in the private planning notes. The rest of this file is the original decision, kept because the
runtime story (Canvas2D, no animation libraries, self-hosted fonts) is unchanged.

## The decision: hand-written vanilla, no framework, no bundler

After surveying how modern animated/CLI sites get their motion, the lightest
tech that fits a no-build static site is **plain HTML + CSS + a little vanilla
JS**, with **Canvas2D** for the one continuous background animation. No React,
no GSAP, no Three.js, no Lottie, no build step.

### What I looked at and what I took

| Reference | What they do | What we borrowed / why we skipped |
|---|---|---|
| **bun.sh / starship.rs / deno** | CLI sites whose hero **is the install command** in a copy-on-click box; everything static. | Copied the pattern exactly: the `curl … \| sh` box is the first and primary CTA. These sites are essentially static HTML - confirms no framework is needed. |
| **Linear / Vercel / Stripe** | Restrained type, generous whitespace, one accent, scroll-reveals, subtle blur navs. | Took the *restraint* and the IntersectionObserver scroll-reveal + sticky-blur nav. Their heavy WebGL/video is overkill here. |
| **GSAP + ScrollTrigger** | Best-in-class scroll choreography. | **Skipped** - a ~50KB dep for what a 30-line `IntersectionObserver` + CSS transitions do for our needs. |
| **Lottie / rive.app** | Vector animation runtimes. | **Skipped** - pulls a player lib + JSON assets; our motion is simple enough for CSS keyframes + one Canvas loop. |
| **Three.js / WebGL** | GPU particle fields, 3D. | **Skipped** - too heavy and too "loud" for a quiet white background. Canvas2D is plenty for soft blips + ripples + beams and is far lighter. |
| **asciinema** | Records/replays real terminal sessions as a player. | Took the *idea* (a terminal replay) but **hand-built** it in JS - no player dependency, full control of color/pacing, and it's tiny. |
| **TUI craft** (the project's TUI design) | Signal bars `▁▂▃▄▅▆▇█`, `◉ ◌ ○`, gold `◆` lineage, the endpoint+key panel. | The terminal demo replays the real TUI flow using the project's canonical TUI glyphs. |

### Why Canvas2D (not WebGL) for the background
The radio-map is **quiet by design**: a faint dotted grid, low-opacity station
blips that breathe and occasionally ripple, and the odd token-beam to a receiver.
That's a handful of circles and strokes per frame - Canvas2D handles it at 60fps
with a tiny footprint, no shader/program setup, and trivially respects
`prefers-reduced-motion`. WebGL would add complexity and weight for no visible gain.

## Files

The tree is in [`README.md`](README.md) (Layout): the routes and components under `app/`,
the static tree under `src/` (`styles/` is `tokens.css` -> `base.css` -> `components.css` ->
a sheet per page; `js/` holds the scripts the bundles carry, with `terminal.js` the hand-built
TUI replay and `site.js` the nav state, scroll-reveal and copy-on-click), `build.mjs`, and the
guards under `test/` and `e2e/`. `src/install.sh` is the real POSIX installer the hero `curl`
pipes, copied to the output root.

No dependencies are bundled. The fonts (Space Grotesk + JetBrains Mono, SIL OFL)
are self-hosted in `src/assets/fonts` (`scripts/vendor-fonts.mjs`), so a page makes
no third-party request before first paint; they fall back to system UI/mono stacks. Every icon and animation is hand-built (CSS/SVG/Canvas) - no emoji,
no icon fonts, no JS libs.

## Performance & accessibility

- The background canvas uses `requestAnimationFrame`, a **DPR cap of 2**, pauses
  on `visibilitychange`, and pauses via `IntersectionObserver` when scrolled off.
- **`prefers-reduced-motion: reduce`** disables the canvas entirely (a static CSS
  gradient shows instead), freezes all CSS animations, renders the terminal demo
  in its final state (no typing), and turns off scroll-reveal transitions.
- The terminal demo is `role="img"` with an `aria-label` describing the flow, so
  screen readers get the gist without parsing animated text.
- Copy-on-click works with the async Clipboard API and falls back to `execCommand`.

## Run it locally

```sh
make site-serve      # node web/build.mjs, then http.server on http://localhost:5173
```

or, from `web/`, `node build.mjs && python3 -m http.server -d dist 5173`. The pages are
routes, so there is no file to open directly: the build writes `dist/`, and that is the site.

## Deploy

Two layers:

**Origin - a static host.** Merging to `main` deploys the site: the host runs the build in
`web/` (`npm install && npm run build`, on Node 22, the floor `package.json` sets) and serves
`dist/` as plain files, with `404.html` as the error page. Nothing else is asked of it, so
any static host will do. CI builds with a newer Node, so a change to the build has to pass
on Node 22 as well.

**Edge - a CDN/DNS proxy.** The site's hostnames (the apex and `www.`) sit behind it. The
origin does not read [`src/_headers`](src/_headers) or [`src/_redirects`](src/_redirects)
(Cloudflare-Pages/Netlify conventions), so the security headers and the `www -> apex`
redirect are applied at the edge as rules, with those two files as the source of truth for
the values. The exact rules + an apply script are in **[`EDGE.md`](EDGE.md)**.

Because `install.sh` is copied to the output root, it is served at
`https://rogerai.fm/install.sh` - exactly what the hero `curl` pipes to.
