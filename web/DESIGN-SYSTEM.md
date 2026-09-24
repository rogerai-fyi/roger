# RogerAI site design system

The living guide to how the site's look is built, so a theme or component change
lands on every page from one place. Read this before adding a page or a component.
The guard tests in `test/design-system.test.mjs` enforce the rules below; each rule
there has a written exception list, and every entry in it is a known debt.

## The layers

Every page is assembled from the same stack. Each layer may use the ones above it,
never the other way round.

| Layer | Where | Owns |
|---|---|---|
| Tokens | `src/styles/tokens.css` | every colour (light, dark, the ink-panel scope), type scale, spacing, radii, panel geometry, motion easings and durations, z-index, the Wave Spectrum palette. Custom properties only. |
| Base | `src/styles/base.css` | reset, typography, links (the in-prose underline), focus, the `[data-reveal]` primitive, typesetting primitives (`.wrap`, `.mono`, `.eyebrow`, `.sectionno`, `.fig`, `.inline`, `.tok`), and the site chrome (promo strip, nav, footer, toast, Let's talk dialog). |
| Components | `src/styles/components.css` | the shared components below, each with a markup contract. |
| Account base | `src/styles/account-base.css` | the signed-in surface (account pages only). |
| Page | `src/styles/<page>.css` | only what is true of one page (or one family: `research.css` is the shell of the research, company, pricing, FAQ and careers pages; `broadcasts.css` of the articles). |

The first three load on every page, in that order: `CSS_SHARED` in `build.mjs`. A
page's bundle is `CSS_BUNDLES[page]` in the same file, and `head.html`'s
`<!-- css-bundle -->` marker becomes one `<link>` per entry. Separate files rather than
one concatenated sheet on purpose: each is cached on its own content hash, so a page
sheet edit never invalidates the shared layers.

**HTML** comes from build-time partials (`src/_partials/`, the `<!-- include: -->`
system in `build.mjs`): `head.html`, `nav.html`, `footer.html`, `site-js.html` (the page
runtime), `rail.html`, `onair.html`, `install-box.html`, and the smaller brand pieces.

**JS**: `site.js` is the page runtime (theme, nav, reveal settle, the copy tick, OS
detection). Each shared behaviour beyond it is a small module that initializes itself
from a `data-` hook and does nothing on a page without one: `tuner.js`, `scrub.js`,
`anchor-hold.js`. `site-js.html` ships all of them on every page, so **a page gets a
behaviour by markup alone**.

## Changing the theme

- Colours, sizes, radii, motion: edit `tokens.css`. Nothing else holds a colour literal
  (the guard holds today's leftovers to a per-file budget that may only go down).
- Dark mode: `:root[data-theme="dark"]` in `tokens.css` (warm ink, never blue-black).
  `theme-init.js` / the inline head script set the attribute before first paint from the
  saved choice or the OS.
- The ink panel (`.tone-zone`) re-scopes the dark token set to a block, one step deeper
  on a dark site. Any component inside it re-themes for free. If you change an ink or
  ground token, `test/tone-zones.test.mjs` re-checks AA for every ink on every ground.
- `--ink-400` is AA only inside ink panels; on paper use `--ink-500` for anything that
  must be read. `--ink-300` is decoration only.
- Mono + ONE red (`--live`): red is an indicator (needle, on-air dot, tuned state, focus),
  never a wash behind text.

## Components

Markup contract, then behaviour. "No-JS" and "reduced motion" say what a visitor gets
without scripts or with `prefers-reduced-motion: reduce`.

### Section and section head
```html
<section class="section" id="x">            <!-- add .band for the tinted band -->
  <div class="wrap">
    <div class="section__head">              <!-- .section__head--left: left-set -->
      <span class="sectionno">§1 / LABEL</span>
      <h2>Claim</h2>
      <p>Lead.</p>
    </div>
    ...
```
`.sectionno` (base) is the mono label with its short red rule. Anchors land under the nav
where the page sets `section[id] { scroll-margin-top: 72px }` (home does).

### Buttons
`.research-actions` is the row; `.research-button` the button, `.research-button--primary`
the one primary. (Historical name, kept so no markup had to change; a rename to a neutral
name is a one-commit job for the rollout.) Pressable: sinks 1px while held
(none under reduced motion).

### Callout
`<div class="man-note">` with an optional `<span class="man-note__tag">`; `--live` and
`--ember` modifiers.

### Install / command pill, with the copy tick
```html
<!-- include: install-box.html id=installX cmd='curl <span class="tok">-fsSL</span> https://rogerai.fm/install.sh | sh' -->
<!-- args: size=lg (closing CTA size), oslock=linux (never swapped to PowerShell),
     label="..." (defaults to "Copy install command to clipboard") -->
```
Wrap it in `<div class="install">` with an optional `<span class="fig">`,
`<p class="install__lead">` and `<div class="install__meta">`. site.js copies the
displayed `.install__code` of **every** `.install__box` (Windows visitors get the
PowerShell line unless `data-os-lock`), marks it `.is-copied` for 1.6s (the icon becomes a
tick) and shows the toast. A hyphenated token wrapped in `<span class="tok">` never breaks.
No-JS: the command is plain selectable text. Pages with a text "copy" label instead of the
icon (the /models QSL card, /voices) use the same classes by hand.

### Copy control (anything else)
`<button data-copy-target>` copies its own `<code>`; `data-copy-target="#id"` copies that
element's text. Same tick class, same toast, delegated (works for controls added later).
The footer's upgrade commands use it; code blocks should.

### Framed instrument panel
`<div class="install" data-frame="panel">`: the one place a bezel and shadow are allowed,
for THE primary action of a page (homepage FIG. 1). Its `.fig` becomes the header strip
with the on-air dot.

### Ink panel
```html
<div class="tone-zone" data-tone="ink"> ...sections... </div><!-- /tone-zone -->
```
Inset from the page edge (64px outside the text column, never closer than
`--panel-inset`), radius `--panel-r`, still (nothing animates on the panel itself).
Everything inside re-themes via the scoped tokens.

### Tinted panel
```html
<div class="tint-panel"> ... </div>
```
`--paper-2` ground, radius `--panel-r`, no border, no rule: the calm replacement for a
hairline card with a heavy black top rule. Inside an ink panel it re-themes. Grids of
panels are placed by the page. (Named `tint-panel`, not `panel`: the account sheet already
uses `.panel` for its hairline-divided sections.) The tuner band and the homepage cards
still write the same two declarations by hand and can adopt the class.

### Wave Spectrum scale
```html
<ol class="spectrum" data-scrub aria-label="The Wave Spectrum, pico to exa">
  <li><i aria-hidden="true"></i><span><b>Wave Pico</b> what it watches ...</span></li> ...
</ol>
```
One station per tier, pico to exa (the words in one `<span>`, so a phone can set the bar
beside them); the bar over each is the wave, its stripes tightening
as the model grows (set by position, up to seven tiers, no inline style). With
`data-scrub` the scrubber tunes a tier (quiet ground, red wave). Four columns under
1080px, where the range control steps aside (pointing or tapping a tier still tunes it);
a stack on phones. No-JS: the complete list. The homepage's `.home-spectrum` (home.css)
draws the same picture and can adopt this class.

### Step path
```html
<ol class="steps"><li><b>Lead.</b> The step.</li> ...</ol>
```
A short numbered path (2 to 6 steps): a mono numeral and a tick per step on one hairline,
the first tick red. Left to right on wide screens, a vertical rail under 820px. The
numerals are a CSS counter over the `<ol>`, which already carries the order.

### Page index
```html
<nav class="page-index" aria-label="Sections">
  <ol><li><a class="page-index__link" href="#id">Heading, word for word</a></li> ...</ol>
</nav>
<!-- grouped: -->
<nav class="page-index" aria-label="Every question">
  <div class="page-index__group">
    <a class="page-index__head" href="#group">Group name</a>
    <ol><li><a class="page-index__link" href="#q-id">Question</a></li> ...</ol>
  </div> ...
</nav>
```
An in-flow index of a long page built only from its own headings or questions, in columns
so all of it fits one screen. Never pinned. Every link carries a class (a classless link
in an `<li>` gets the prose hairline). Used by the FAQ (every question) and the legal
pages (every section).

### TOC tuner
```html
<nav class="toc-tuner" data-tuner aria-label="Sections">
  <div class="wrap toc-tuner__band">
  <div class="toc-tuner__readout" aria-hidden="true"></div>
  <ol class="toc-tuner__scale">
    <li><a class="toc-tuner__st" href="#s1"><b>§1</b><span class="toc-tuner__name">NAME</span></a></li>
    ...
  </ol>
  <span class="toc-tuner__needle" aria-hidden="true"></span>
  </div>
</nav>
```
2 to 12 stations; the count is read from the markup. The band must carry `.wrap` (the
needle is laid out against its gutter). Keep the `toc-tuner__st` class on each link: a
classless link in an `<li>` gets the site's in-prose underline. CSS alone moves the needle
and the readout to the pointed or focused station; `tuner.js` adds roving focus (one tab
stop, arrows, Home/End) and rests the needle on the section in view
(`aria-current="location"`). Never pinned. No-JS: plain anchor links, readout on the first
station. Reduced motion: the needle jumps instead of swinging. (`.tuner` is the /models
search bar, a different thing.)

### Scrubber
```html
<ol class="my-list" data-scrub aria-label="What the control scrubs">
  <li><b>Item name</b> ...</li> ...
</ol>
```
`scrub.js` inserts `<input type="range" class="scrub">` above the list, labelled by the
list's `aria-label`, reading out each item's `<b>`. Dragging, arrowing or pointing tunes an
item: it gets `.is-tuned`, which the page styles (homepage: a quiet ground and a red wave).
Items never fade. No-JS: the complete static list, no control.

### Scroll lift and reveal
`[data-reveal]` on a block (base.css + site.js): visible without JS, settled by site.js;
never opacity 0. A page opts in to the scroll lift with a `[data-lift]` container (the
homepage's `<main>`): each outermost reveal block rises and sharpens as it enters, on a CSS
scroll timeline where supported, transform and blur only. Touch and tablets: one plain rise,
done by 85% of the viewport. `data-lift-skip` opts a block out. Reduced motion: nothing moves.

### Anchor hold
Mark any element `[data-anchor-hold]` on a page whose content grows above its anchors after
load (live data, media). A `#fragment` stays on its target until the reader scrolls, types
or touches, or 4s pass.

### Rail and on-air mark
`<!-- include: rail.html head="RogerAI · Section" rev="Rev. 2026.09 · ((•))" orange=1 -->`
(the running head, `orange=1` adds the OC orange) and `<!-- include: onair.html -->` (the
`((•))` mark in eyebrows).

### Figure and code block (next, owned by the article work)
Article figures (`.bc-figure`) and `<pre>` blocks are being fixed on a separate branch
(overflow, dark mode, inline styles). When that lands, promote the result from
`broadcasts.css` to `components.css` under the same class names, so non-article pages can
use them with no markup change. The copy hook for a code block is `data-copy-target`
(above).

## Adding a page

1. `src/<page>.html`: `<!-- include: head.html title=... desc=... theme=external -->`,
   `<!-- include: nav.html variant=marketing ... -->`, content, `<!-- include: footer.html -->`,
   then `<!-- include: site-js.html -->` (`sync=1` for the account pages' blocking form,
   `promo=0` to leave out the promo strip script). Page scripts that need site.js to have run
   go after the include.
2. Register its bundle in `CSS_BUNDLES` (`build.mjs`): `[...CSS_MARKETING, "<page>.css"]`
   or `[...CSS_ACCOUNT, ...]`. The build fails if you forget.
3. Build it from components first. Page CSS only for what no other page will need, with
   tokens for every value. If a second page needs it, it becomes a component.
4. `npm test`: the guards check the partials, the layers, the colours and the behaviours.

## Adding or changing a component

Define it in `components.css` (one section, a header comment with its contract), its
behaviour in a module that initializes from a `data-` hook and is added to
`site-js.html`, and its contract here. Page sheets may place a component in their own
context (`.page-x .install__box { margin-top: ... }`) but never restyle it bare; the guard
fails on a bare component selector outside `components.css`.

## Audit (2026-09, before this structure)

53 pages (48 full pages, 3 redirect or pointer stubs, 2 with their own minimal runtime),
35 stylesheets, 45 scripts, 10 partials.

- **Chrome was already shared** for head, nav and footer (every full page). What was not:
  the script tail, hand-copied on 48 pages in 5 variants; the running-head rail, verbatim
  on 34 pages; the `((•))` mark, on 37 eyebrows; the install pill with its icon, 4 copies.
  All four are partials now.
- **Components in the wrong sheet.** 48 CSS blocks were styled from more than one file.
  Most are legitimate placements; the real problems were: the ink panel, the framed
  panel, the press, the copy tick, the tuner, the scrubber and the scroll lift living in
  `home.css` (unreachable from any other page); the install pill, section head, buttons and
  callout mixed into `base.css`'s chrome; `.install__box--lg` defined only in `home.css`
  while the Tower page's markup asked for it; and `.tuner` naming two different things
  (the homepage TOC, the /models search bar).
- **Duplicated values.** The Wave Spectrum palette was declared in three sheets (now once,
  in tokens; the Playbox decks keep their own scoped copy on purpose). The market chips
  (`.band-tag`, `.price-tier`, `.sig`, `.sigbar`) are still defined in both `home.css`
  and `models.css`, and /voices re-declares the /models panel (left alone while /models
  is being fixed elsewhere; merge them into components afterwards).
- **Colour literals outside tokens**: 425 in 18 sheets (310 of them the Playbox games'
  own palettes), plus 72 in inline styles on 10 article pages. After moving the tier
  palette: 397 in 17 sheets, held by budget.
- **Tokens that do not exist**, silently falling back to the initial value: `--s-7` (the
  spacing scale has no 7; research, Tower), `--t-lg`, `--t-base`, `--hover-bg`, and two bar
  properties nothing sets (`--kept`, `--fill`). Stations, device and payouts reference
  `--rule`, `--beacon` and `--warn` with hard-coded fallbacks. Listed in the guard; each
  fix changes that page, so it is the rollout's call.
- **Behaviour copies.** Six scripts wrote to the clipboard. On /models and /voices the
  command was copied twice (site.js already binds every install pill, and the page script
  did it again). Those two copies are gone; keys, the private console and the Playbox keep
  their own affordance (listed debts). The footer's copy buttons were a hard-coded id list
  (now `data-copy-target`).

## Pages that do not fit the system yet

- **Account pages** (account, billing, payouts, usage, dashboard, console, keys, private,
  r, login, legal): their own `account-base.css` surface and blocking scripts; they share
  the tokens and chrome but none of the new components yet.
- **device.html, stations.html**: their own minimal runtime (no site.js), and hard-coded
  fallbacks of tokens that do not exist.
- **Playbox** (playbox, wave-patch, wave-factory sheets): self-contained games with their
  own palettes; 310 of the remaining colour literals.
- **Articles**: inline figure styles with light-only colours (dark-mode debt, being fixed
  separately), and the figure/code components still to be promoted.
- **/models and /voices**: the market chips and the directory panel duplicated between
  `home.css`, `models.css` and `voices.css`; the text-label copy pill written by hand.
- **The research shell** (`research.css`) restyles `.research-button` full width on phones
  for its pages only.
