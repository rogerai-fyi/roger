# RogerAI site design system

The living guide to how the site's look is built, so a theme or component change
lands on every page from one place. Read this before adding a page or a component.
The guard tests in `test/design-system.test.mjs` enforce the rules below; each rule
there has a written exception list, and every entry in it is a known debt.

## Component index

Search this table before you build anything. `components.css` unless noted; "JS"
is the module that wakes on the hook (every page loads it via `site-js.html`).

| Component | Class / hook | JS | Used on |
|---|---|---|---|
| Section, section head | `.section`, `.section__head`, `.sectionno` (base) | - | every marketing page |
| Tinted band / inset band | `.band`, `.band--inset` | - | homepage, research pages |
| Action row (lead row) | `.research-actions.research-actions--lead`, `.research-button` (every one solid, no variants) | press | 15 pages, every action row |
| Callout | `.man-note` (`--live`, `--ember`) | - | manual, keys, hardware, 2 articles |
| Install pill + copy tick | `install-box.html` partial, `.install`, `.install__box` (`--lg`) | `site.js` | homepage, app, Tower, articles |
| Inline / block copy command | `.copy-code` (`--block`) | `site.js` `[data-copy-target]` | Pricing, Integrations |
| Framed instrument panel | `.install[data-frame="panel"]` | - | homepage FIG. 1 |
| Ink panel | `.tone-zone[data-tone="ink"]` | - | 10 pages |
| Tinted panel | `.tint-panel`, `.tint-panel__tag` | - | 42 pages (cards, notes, sign-offs, account panel) |
| Wave Spectrum scale | `.spectrum` + `.wave-bar` | `scrub.js` `[data-scrub]` | Company (homepage: `.home-spectrum` + `.wave-bar`) |
| Wave bar | `.wave-bar` (in a tier `<li>`) | - | Company, homepage |
| Step path | `.steps` | - | Careers |
| Page index | `.page-index` | - | FAQ, legal pages |
| TOC tuner | `.toc-tuner` | `tuner.js` `[data-tuner]` | 16 pages |
| Scrubber | `[data-scrub]` -> `.scrub` | `scrub.js` | homepage, Company, Pricing |
| Range twin | `[data-range-twin]` -> `.range-twin` | `range-twin.js` | Pricing |
| Reveal / scroll lift | `[data-reveal]` (base), `[data-lift]`, `data-lift-skip` | `site.js` | 9 pages opt in to the lift |
| Anchor hold | `[data-anchor-hold]` | `anchor-hold.js` | homepage |
| Figure | `.figure` (`--plate`, `--phone`, `--chart`) | - | the articles |
| Scroll box | `.scroll-box` (`--diagram`) | - | every wide table; the Tower, Pricing, Industrial and Wave family diagrams |
| Data table | `.data-table` | - | articles, manual, Integrations, Hardware, Wave family |
| Code block | `.code-block` | `site.js` (adds the copy button) | the manual, Integrations, 3 articles |
| Fold on a phone | `details[data-fold-narrow]` | `site.js` | Broadcasts |
| Directory | `.bands-hero`, `.bands-panel`, `.band-tag`, `.price-tier` | page scripts | Models, Voices, homepage market, App (hero title) |
| Rail, on-air mark | `rail.html`, `onair.html` partials | - | 34 / 37 pages |
| Photo credit | `.photo-credit` (research.css) | - | Industrial, Hardware |
| Touch hit area | `--hit`, `--hit-inset` (tokens); the `@media (pointer: coarse)` blocks (components.css documents the pattern) | - | every standalone control, sitewide |
| Print | `@media print` at the end of `components.css` + `tokens.css` | - | every page |

### One role, one pattern

Parallel work drew some roles two ways; each role has one pattern now, held by
`test/consistency.test.mjs` (which reads usage, where `design-system.test.mjs` reads
definitions).

| Role | The pattern | Was also |
|---|---|---|
| Action row (hero, section, closing) | the lead row: `.research-actions.research-actions--lead`, every action the same solid `.research-button` (no primary/secondary: a single highlighted first button read as "the current view", founder ruling) | underlined quiet text links (`--quiet`), a solid primary then outlined secondaries (`--primary`), the 404's own pill + link |
| Page hero | paper; the homepage cover is the one ink hero. The ink panel marks a page's moment (its instrument or live data), not its title | ink hero panels (Company, Careers, FAQ) |
| Hero title | landing page: `--t-display`; document (article, manual, legal, 404, confidential): `--t-h1` | the Broadcasts front door at h1 size |
| Contents tuner | right after the hero (and the hero's strip or figure) on any page with 4+ numbered sections | after an article's Quick Answer (broadcast 010) |
| Section head | `.sectionno` then the section's h2 (then the lede) | - |
| Command block | `.code-block` (copy tick, prompts not copied); the install line is the install-box partial; an inline command `.copy-code` | 45 bare `<pre>` in the manual and two on Integrations, each with its own grey box and no copy |
| Data table | `.data-table` (no box, hairline rows, a label-type header over a `--hairline-2` rule), in a `.scroll-box` when it can outgrow a phone | six header styles (filled, 2px ink rule, sentence case, three weights) and three scroll wrappers with and without a box |

To flip a pattern later, change the component, not the pages: the lead row's secondaries
are the one `.research-button` rule set (see its comment in components.css); a
landing hero becomes ink by wrapping it in `.tone-zone` and changing the one rule in
the consistency test.

Known and kept: the signed-in pages keep their own plate, forms and tables
(account-base.css); the Playbox is a tool, its hero is the deck; the legal pages carry
no running-head rail (a rail needs its own words, and this pass changes no copy).

### Before you add a component

1. Search the index above and `components.css` for the look you want, including its
   declarations: a panel is `--paper-2` on `--panel-r`, a label is the mono micro
   uppercase set. If it exists, use the class.
2. If it almost fits, extend it (a modifier, or a page-context placement such as
   `.page-x .tint-panel { padding: ... }`), do not fork it under a new name. Two
   classes with the same declarations fail `test/design-system.test.mjs`, and so does
   any second rule that paints the tinted panel ground.
3. Only when a second page needs it does a page pattern become a component: move it to
   `components.css`, add its contract below and a row above, and make the first page
   use it.
4. Colours and sizes are tokens; text uses `--ink-400` or stronger (all AA, see
   **Changing the theme**); motion is opt-in and has a print and reduced-motion state.

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
`anchor-hold.js`, `range-twin.js`. `site-js.html` ships all of them on every page, so **a page gets a
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
- Every text ink is AA (4.5:1) on `--paper`, `--paper-2` and the raised `--white`, light
  and dark and inside ink panels (`test/contrast.test.mjs` computes all of them). The
  tertiary `--ink-400` is the shared labels' ink (section labels, eyebrows, FIG.
  captions, dates); do not lift a label to `--ink-500` in a page sheet, the test fails
  on it. `--ink-300` is decoration only. The one scoped exception: the signed-in plate
  (`.card`), whose command wells sit on `--paper-3`.
- Print is black on white in every theme (the print tokens at the end of `tokens.css`).
- Mono + ONE red (`--live`): red is an indicator (needle, on-air dot, tuned state, focus),
  never a wash behind text. Red WORDS (a hovered link, a kicker, a step number, SVG
  labels) use `--live-text`, the same red a shade deeper: the beacon red is 4.31:1 on
  `--paper-2`, the text shade is AA on every ground (`test/contrast.test.mjs` checks it
  and fails on `color: var(--live)` or a red-filled SVG word).

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
`.research-actions` is the row; `.research-button` the button, every one alike. (Historical
name, kept so no markup had to change; a rename to a neutral name is a one-commit job for
the rollout.) Pressable: sinks 1px while held (none under reduced motion).

(The homepage's card links, `.company__links` with `.company__primary`, look similar
but are a different role: sentence-case links set in the card's own type with a drawn red
rule on hover, not the page's uppercase action row. They stay in home.css.)

Every action row on the site is the lead row (see **One role, one pattern**):
`.research-actions.research-actions--lead`, and every action in it is the same solid
`.research-button`: an `--ink-900` fill with a `--paper` mono label (17:1), which the tokens
turn into a paper fill with an ink label on the dark site and in an ink panel. There is no
primary: a single highlighted first button read as "the current view" (founder ruling).
Hover and focus fill `--live-text` under the paper label (5.5:1 light, 5.7:1 dark),
keyboard focus adds a 2px `--live` ring, a press takes the `--ink-700` fill (12.6:1) and
sinks 1px; no glow. On touch every button is at least 44px tall. The lead row keeps every
button at its own width on phones and wraps onto new lines with a 12px gap, so it never
becomes a full-width stack. There is no outline or quiet link variant.

### Callout
`<div class="man-note">` with an optional `<span class="man-note__tag">`; `--live` and
`--ember` modifiers.

### Install / command pill, with the copy tick
```html
<!-- include: install-box.html id=installX cmd='curl <span class="tok">-fsSL</span> https://rogerai.fm/install.sh | sh' -->
<!-- args: size=lg (closing CTA size), oslock=linux (never swapped to PowerShell),
     oslock=any (a cross-platform command that is not the installer: never swapped),
     label="..." (defaults to "Copy install command to clipboard") -->
```
Wrap it in `<div class="install">` with an optional `<span class="fig">`,
`<p class="install__lead">` and `<div class="install__meta">`. site.js copies the
displayed `.install__code` of **every** `.install__box` (Windows visitors get the
PowerShell line unless `data-os-lock`: `linux` for a one-platform command such as the
Tower installer, `any` for a cross-platform command that is not the installer, such as
`roger use` on /models or `roger say` on /voices), marks it `.is-copied` for 1.6s (the icon becomes a
tick) and shows the toast. A hyphenated token wrapped in `<span class="tok">` never breaks.
No-JS: the command is plain selectable text. Pages with a text "copy" label instead of the
icon (the /models QSL card, /voices) use the same classes by hand.

### Copy control (anything else)
`<button data-copy-target>` copies its own `<code>`; `data-copy-target="#id"` copies that
element's text. Same tick class, same toast, delegated (works for controls added later).
The footer's upgrade commands use it; code blocks should.

An inline command inside prose is `copy-code`, the same control dressed as inline code:
```html
<button class="copy-code" type="button" data-copy-target aria-label="Copy roger topup 25"><code>roger topup 25</code></button>
```
A small two-sheet glyph (CSS, no icon file) sits after the command and becomes the tick
while `.is-copied`. The command never breaks inside itself. No-JS: selectable code text.
(Pricing's plates use it.) `copy-code--block` is the same control on a line of its own: it
fills its column and wraps at spaces (the Integrations desk's install lines).

### Framed instrument panel
`<div class="install" data-frame="panel">`: the one place a bezel and shadow are allowed,
for THE primary action of a page (homepage FIG. 1). Its `.fig` becomes the header strip
with the on-air dot.

### Ink panel
```html
<div class="tone-zone" data-tone="ink"> ...sections... </div><!-- /tone-zone -->
```
Inset from the page edge (the `--panel-mx` token: 64px outside the text column, never
closer than `--panel-inset`), radius `--panel-r`, still (nothing animates on the panel
itself). Everything inside re-themes via the scoped tokens. On a dark site the panel is
one step deeper than the page and carries a `--hairline-2` inset ring, so it keeps an edge.

### Tinted panel
```html
<div class="tint-panel"> ... </div>
```
`--paper-2` ground, radius `--panel-r`, no border, no rule: the calm replacement for a
hairline card with a heavy black top rule. Inside an ink panel it re-themes. Grids of
panels are placed by the page, and a page may set a panel's padding in its own context
(`.faq__group`, `.company__card`, `.dialwrap`); the ground itself is declared once, in
the one shared rule that also paints the tuner band and the figure plate. Note the panel
zeroes its first child's top margin and its last child's bottom margin: a child that
bleeds (a photo with negative margins) is placed with a two-class selector. (Named
`tint-panel`, not `panel`: the account sheet already uses `.panel` for its
hairline-divided sections.) A tinted panel that is a whole section is the inset band
(`.band--inset`).

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
a stack on phones. No-JS: the complete list. Each tier's bar is the shared
`<i class="wave-bar" aria-hidden="true">`, its stripe gap set by its position. The
homepage's `.home-spectrum` (home.css) is a different layout of the same data (ruled
cells, size / name / use on three lines, a touch gallery the scrubber drives) and
shares only the wave bar; moving it onto `.spectrum` would restyle every one of its
rules, so it stays its own.

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
2 to 20 stations; the count is read from the markup. Past 12 (a long document: the
manual) the scale gets finer on a desktop, and under 760px it becomes a plain two-column
contents list, number and name on every station, no needle. The band must carry `.wrap` (the
needle is laid out against its gutter). Keep the `toc-tuner__st` class on each link: a
classless link in an `<li>` gets the site's in-prose underline. CSS alone moves the needle
and the readout to the pointed or focused station; `tuner.js` adds roving focus (one tab
stop, arrows, Home/End) and rests the needle on the section in view
(`aria-current="location"`). The resting station keeps a red major tick while you point
elsewhere ("you are here"); a name comes into tune as it shows (its blur clears and its
tracking closes up; opacity flips at once). Never pinned. Drag and tap to tune (`tuner.js`): the scale strip (ticks, needle, numerals)
is the drag zone, `touch-action: none`, at least 56px tall on touch with a heavier needle
head as its grip; a press there moves the needle to it at once and a drag carries it,
whatever the angle, the readout naming the station under it. On TOUCH it is two steps
(founder: the page should not jump on its own): the first tap or drag only selects
(`.is-selected`: the needle rests on the station, the readout names it, and the hint "Tap
again to tune in" fades in under the scale, 250ms, instant under reduced motion; added by
the script on a coarse pointer only, `aria-hidden`); a second tap on that station, on the
readout or on the hint goes there. A tap or drag to another station moves the selection;
a reader's scroll, a tap outside the tuner or 6s of nothing clears it and the needle
returns to the section in view. A mouse click, a mouse drag's release, the keyboard and
assistive tech are one step, as before (only `pointerType` "touch" selects first). A
gesture that starts on the rest of the band scrolls the page (`touch-action: pan-y`). The
long-document list form (13+ stations under 760px) is plain tappable rows: full-width 44px
targets, no drag, no selection, no hint. The drag and the hint add no semantics; screen
readers still get the links and `aria-current`. No-JS: plain anchor links,
readout on the first station. Reduced motion: the needle jumps instead of swinging and
names appear already sharp. (`.tuner` is the /models
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
Items never fade. If the page lays the list out as a sideways gallery (it overflows its own
width, e.g. a phone snap gallery), scrubbing centres the tuned item and a swipe tunes the
item that comes to rest in the middle; the list must be `position: relative` (the items'
offsetParent). No-JS: the complete static list, no control.

### Range twin
```html
<input type="number" id="opHours" value="8" min="0" max="24" step="0.5" data-range-twin />
```
`range-twin.js` inserts `<input type="range" class="range-twin">` right after the field,
with its min, max and step, synced both ways; dragging it writes the field and fires the
field's own `input` event, so existing listeners (the Pricing calculator) need no change.
A hairline track filled in ink to the value, the red needle for a thumb; it spans its grid
row. The typed field stays the accessible control: the twin is out of the tab order and
`aria-hidden`, a pointer and touch convenience. Give a twin only to a bounded field whose
range reads well on a slider. No-JS: the field alone.

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

### Directory
`.bands-hero` (`__inner`, `__title`, `__sub`), `.bands-panel` (`__bar`, `__status` with
`is-live`/`is-off`/`is-quiet`, `__quiet`), and the chips `.band-tag` (`--free`, `--deal`,
`--ver`, `--seen`) and `.price-tier`. The live directories (Models, Voices) share the
shape, the homepage market its chips, the App page its hero title. Rows and columns are
each page's own.

### Touch hit area
On a touch screen (`pointer: coarse`) every standalone control is at least a `--hit`
(44px) target. A control whose look matters (a chip, a pill, an icon button, a lone text
link) keeps its drawn size: an invisible `::before` at `inset: var(--hit-inset)` (the token
does the math) grows its hit area, centred, and a `:where()` rule makes a static control
its containing block without overriding a page's own positioning. The sheet that owns the
control lists it in its own `@media (pointer: coarse)` block: `components.css` for the
components (where the pattern is documented), `base.css` for the chrome, each page sheet
for its own controls. The page index gets 44px rows, the contents tuner's stations grow
downward to 44px, and a form field is 44px tall (all in `components.css`).
`test/qa-polish2.test.mjs` holds the list of controls. Links inside running text (a sentence, a
callout, a spec plate) are exempt, as the target-size rule exempts them. A grown area must
never cover another control's drawn box (a tap aimed at the neighbour would open this
one), so controls packed close together do not grow: the homepage preset chips, card and
spectrum links, the Wave jobs chips, the Playbox spines and deck modes, the reel's mute,
the lean nav's mark and the code block's copy button (all 26px or more, inside the 24px
target-size minimum with spacing). `scripts/touch-overlap.py` (after a build) checks every
page at 390 and 320 with touch emulation. Also short of 44: a dial of 10+ contents
stations on a phone (32px pitch) and the scrubber's range track.

### Print
Every page prints whole: the print section at the end of `components.css` stops all
motion, sets every reveal at rest (`[data-reveal]`, the lift, the JS settle), leaves off
the nav, promo strip, rail, tuner, scrubber, range twins, toast, dialog and code copy
buttons, unclips code and tables, keeps figures and panels whole, prints gradient-cut
words (`.carrier`) as ink and follows an external prose link with its address. Colours:
black on white in every theme (the print tokens in `tokens.css`). A new component with a
screen-only control adds it to the hide list; `test/print.test.mjs` checks the rest.

### Rail and on-air mark
`<!-- include: rail.html head="RogerAI · Section" rev="Rev. 2026.09 · ((•))" orange=1 -->`
(the running head, `orange=1` adds the OC orange) and `<!-- include: onair.html -->` (the
`((•))` mark in eyebrows).

### Figure
```html
<figure class="figure figure--plate">          <!-- or --phone, --chart, or bare -->
  <img src="..." width="1280" height="560" alt="..." loading="lazy" />
  <figcaption class="fig mono">FIG. 1 - ...</figcaption>
</figure>
```
Promoted from the articles. The figure and its direct `img`/`video`/`svg` never outgrow
the column (a 1600px asset used to push a page sideways). `--plate` sets it on a calm
tinted inset panel (theme tokens: it recedes on the dark theme). `--phone` centres a
portrait screenshot at 440px. `--chart` is an inline SVG chart: heading ink (the chart
draws in `currentColor`), its drawing in `.scroll-box--diagram`; on a phone it keeps 820px,
where every article chart's smallest label reads at 11px or more, and scrolls inside that
box instead of shrinking its words (on a desktop the 640px article column holds it whole,
labels at 8.6-9.8px, accepted); the caption stays put. The caption is
the `.fig` label in its own ink (AA). SVG charts colour their red with `var(--live)` and their paper
with `var(--paper)` in their own `<style>`, never a literal, so they re-theme.

In the articles every figure is the plate (`figure figure--plate`, with `--chart`, `--phone`
or the page's own class for its content): one ground, one radius, one inset, the FIG.
caption inside under the media. A landscape lead (hero) still or loop is cropped to 16:9
(`broadcasts.css`; the two 1280x560 banners, Tower and VRAM, lose about a tenth of each side;
every other landscape lead, share-hero.png included, is drawn 16:9 or within 3% of it); a portrait phone lead keeps its shape; the headline video sits on the
plate with its label under the frame. No page draws its own frame round a figure (the
routing process figure and the economics chart and formula used to). Known and left: some
chart SVGs carry their own "FIG. n" title inside the drawing (an SVG edit), the economics
chart's caption is set in the prose face (its caption is a paragraph), and three articles
have no hero art (new art, not CSS).

### Scroll box
`<div class="scroll-box"><table>...</table></div>`: anything wider than a phone scrolls
sideways inside the box, never the page.

`scroll-box--diagram` holds an inline SVG diagram (the svg its direct child, the caption
outside it): the drawing keeps at least `--diagram-min` of width and scrolls on a narrower
column instead of shrinking its labels, with the code block's edge shade while there is
more to scroll (the same grouped rule; `--edge-ground` is the ground the shade ends on,
paper by default). The page sets `--diagram-min` in its own context so the figure's
smallest label renders at 11px or more: 11 x viewBox width / smallest label size
(`test/qa-polish2.test.mjs` checks the Tower, Pricing, Industrial and Wave family
figures). It prints at the page width.

### Code block
```html
<div class="code-block"><pre><span class="code-block__prompt" aria-hidden="true">$</span> roger share
<span class="code-block__comment"># a comment</span></pre></div>
```
A long line scrolls inside the block. `site.js` adds `.code-block__copy` (an icon
button using the shared `[data-copy-target]` copy tick) to every block; the copied text
leaves out `aria-hidden` parts, so the `$` prompts never paste. No-JS: the plain,
selectable `<pre>`, and no dead button.

### Tinted panel tag
```html
<aside class="tint-panel"><span class="tint-panel__tag">roger that</span> ... </aside>
```
The small mono label at the top of a tinted panel (see **Tinted panel**). The articles use
the panel for their sign-offs and the broadcasts telegram.

### Fold on a phone
`<details class="..." data-fold-narrow open><summary>...</summary>...</details>`: a
disclosure written open (no-JS readers and crawlers get all of it) that `site.js` folds on
a phone (<=640px), where it would push the page's main content down. The summary keeps
its own words and gains a quiet `+`/`-` mark. Used by the broadcasts telegram.

### Inset band
`<section class="section band band--inset">`: the tinted band as a rounded panel on the
ink panel's inset geometry (64px outside the text column, never closer than
`--panel-inset`), no top/bottom rules. Used by the research pages for their set-apart
sections instead of full-bleed ruled bands.

### The article kit (broadcasts.css)
Every `broadcasts-*.html` reads the same way: one measure (`--bc-measure`, 42rem), one
body size, the lead step, quiet section numbers (`.bc-sec__no`: muted mono, only the
section mark red), `.bc-answer` for a Quick Answer (prose face, 2px red rule), the FAQ
`<dl class="bc-faq">` (its `<dt><b>` mirrors the FAQPage JSON-LD, tested), a sign-off
`<aside class="tint-panel bc-signoff">` whose closing install command is the
install-box partial (`id=signoffInstall`), `.bc-table` for a data table in a
`.figure > .scroll-box`, and the shared `.figure`/`.code-block`. Long pieces carry the
toc-tuner above the body, one station per numbered `.bc-sec` (`id="sN"`). No inline
style and no colour literal in an article (guarded).

## Adding a page

1. `src/<page>.html`: `<!-- include: head.html title=... desc=... theme=external -->`,
   `<!-- include: nav.html variant=marketing ... -->`, content, `<!-- include: footer.html -->`,
   then `<!-- include: site-js.html -->` (`promo=0` to leave out the promo strip script).
   Every script is deferred, so they run in document order: a page script written before
   the include runs before site.js, one written after it runs after. Page scripts that need site.js to have run
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
  fix changes that page, so it is the rollout's call. (QA pass: `--s-7` is now a real
  step, 28px; the nav panel and scope-picker hovers use `--paper-2`, so they show; the
  privacy card heading uses `--t-body`. `--kept` and `--fill` remain, on dead rules.)
- **Behaviour copies.** Six scripts wrote to the clipboard. On /models and /voices the
  command was copied twice (site.js already binds every install pill, and the page script
  did it again). Those two copies are gone; keys, the private console and the Playbox keep
  their own affordance (listed debts). The footer's copy buttons were a hard-coded id list
  (now `data-copy-target`).

## Consolidation (after the section rollouts)

Five rollouts built in parallel on the shared layer and, working apart, re-invented some
of it. What was found and where it went:

- **The tinted panel, invented four times** (`.tint-panel` x3, `.inset-panel`), merged
  by hand into `.tint-panel`; then **nine more page rules** that re-painted its ground
  (homepage company cards, FAQ groups, careers roles, the legal notice, the account
  pages' panel, the /models dial, the routing simulator, the research onward links,
  developer steps and deployment cards) take the class now, and the tuner band and figure
  plate share its one ground rule. The inset geometry literal (`max(--panel-inset, ...)`)
  had four copies; it is the `--panel-mx` token everywhere.
- **The directory**: /voices re-declared the /models hero and panel verbatim and the
  homepage re-declared the chips; one component now (and /voices' FREE chip is finally
  styled). The App hero title was the same rule under another name.
- **The homepage bands** re-declared `.band--inset` as `main > .band`; they take the class.
- **The Wave Spectrum**: one `.wave-bar` for both ladders; the ladders stay two layouts
  (see **Wave Spectrum scale**).
- **research-labs.css** folded into `research.css` (every page on the shell is lifted);
  its dead `.research-card` rules went. Company and careers now get the inset
  distinction strip and the shell pages lose the hairline under the hero, as the
  research pages had.
- **Twin rules**, one grouped rule each: `.sectionno` / `.hero__eyebrow`, the manual's
  labels, and labels in keys, billing, metrics and payouts; the two photo credits are
  `.photo-credit`.
- **Contrast**, fixed once: the tertiary ink is AA; the local lifts (article labels,
  the research label list, Company kickers, figure captions) are gone.
- **Kept, with reasons** (listed in the guard): the signed-in chart and form kit
  (dashboard / metrics / billing / payouts / account: a shared account kit is the next
  step), two dialogs of one design (billing help, report a station), a step-flow figure
  in two articles, label type coincidences, and layout coincidences.

## Pages that do not fit the system yet

- **Account pages** (account, billing, payouts, usage, dashboard, console, keys, private,
  r, login, device, stations, legal): on the shared runtime now (deferred, like every page)
  and on real tokens, but still their own `account-base.css` surface: the plate (`.card`)
  is a raised `--white` face on an inset tinted panel (`.authwrap`), with its tertiary ink
  scoped to the secondary one in `tokens.css` so its labels hold AA. Their buttons
  (`.primary`, `.ghost`, `.gh`) and fields are account-base's own, not `.research-button`:
  full-width form controls, a different job. Page scripts keep their own affordances
  (keys, private copy).
  One pattern is shared across them: the **account state** (`.acct-state.tint-panel`,
  account-base.css), the look of every signed-out and empty state: the shared tinted
  ground placed in the plate, the card's mono section label (its `h2`), the page's own
  words, an optional command well, and the page's existing action as the lead row's
  solid button (keys and usage signed out; no stations; no dashboard traffic; Base Station
  signed out, whose Log in stays a link in its sentence). Dashboard, console and payouts
  send a signed-out visitor to /login, and /r writes its message from script, so they
  have no signed-out panel; /stations shows its error line when signed out (a logic
  change, not this pass).
- **Playbox** (playbox, wave-patch, wave-factory sheets): self-contained games with their
  own palettes; 310 of the remaining colour literals.
- **/models and /voices**: the text-label copy pill written by hand.
