# TECH - what the RogerAI site is built with, and why

The brief: a **super-minimal, white, no-build static site** for a CLI tool - a
curl-install hero, an animated terminal demo, and a quiet animated "radio map"
background. Deployable as-is to **Cloudflare Pages**.

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

```
web/
├─ index.html          One page. Hero (curl box) · terminal demo · what-it-is ·
│                       how-it-works · monetize · CTA · footer.
├─ install.sh          Real POSIX installer piped from the hero curl.
├─ styles/
│  ├─ tokens.css        Design tokens (colors, type, spacing, motion) - source of truth.
│  └─ site.css          All layout & components. Consumes tokens.css.
├─ js/
│  ├─ radiomap.js       Canvas2D blip-map background (grid · blips · ripples · beams).
│  ├─ terminal.js       Hand-built TUI replay (run → browse → tune in → endpoint+key).
│  └─ site.js           Nav state, scroll-reveal, copy-on-click, OS hint, earn sparkline.
├─ TECH.md              This file.
└─ README.md            Overview + run/deploy.
```

No dependencies are bundled. The only external request is Google Fonts
(Inter + Geist Mono), which **degrades gracefully** to system UI/mono stacks
offline. Every icon and animation is hand-built (CSS/SVG/Canvas) - no emoji,
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

## Run it locally (no build)

```sh
cd web
python3 -m http.server 5173
# → http://localhost:5173
```

Or just open `index.html` directly (fonts/canvas behave best when served).

## Deploy to Cloudflare Pages

This is a pure static site - **no build command**.

**Dashboard (Git-connected):**
1. Cloudflare dashboard → **Workers & Pages → Create → Pages → Connect to Git**.
2. Pick the repo.
3. **Framework preset:** *None*. **Build command:** *(leave empty)*.
   **Build output directory:** `web`.
4. Deploy. Add the custom domain `rogerai.fyi` under **Custom domains**.

**Wrangler (direct upload), from the repo root:**
```sh
npx wrangler pages deploy web --project-name rogerai
```

Because `install.sh` lives in `web/`, it is served at
`https://rogerai.fyi/install.sh` - exactly what the hero `curl` pipes to.
(Cloudflare serves it as `text/x-sh`/`application/octet-stream`; `curl … | sh`
doesn't care about the content type.)
