# RogerAI — TUI Beautification + `roger --ping` "Ping World" Screensaver
### A design proposal (DESIGN ONLY — no production Go in this doc)

Author: TUI UX/UI + ASCII-art pass · Date: 2026-06-27
Scope: `internal/tui/tui.go`, `internal/tui/ping.go`, `internal/tui/pingwalk.go`,
`cmd/rogerai/main.go` dispatch. Mascot: **Ping**, the `(( • ))` radio beacon.
Look law: **~95% monochrome ink + exactly ONE red glint** (the on-air beacon / Ping's eye).

---

## 0. Research log — what I looked at and what I drew from

All gathered with `~/ai/hermes-brains/bin/web-fetch` (browser daemon; `--ping` = `{"ok":true}`).

| Source | Mode | What I drew from it |
|---|---|---|
| **MacAsciiquarium** screenshot (habilis.net/macasciiquarium) | screenshot (read) | The whole layered-world recipe **seen live** in an 80×24 terminal: a **fixed landmark** (the castle, anchored bottom-right), **swaying seaweed** at the floor, **fish drifting horizontally** at many depths, **bubbles rising**, all calm. This is the structural template for "Ping wanders a planet while things drift." |
| **asciiquarium** source `cmatsuoka/asciiquarium` | text (read full 1319 lines) | Exact mechanics: `halfdelay(1)` ≈ **10 fps** input/animate cadence; fish speed `rand(2)+.25` = **0.25–2.25 cells/frame** with random direction (`*= -1`); seaweed sway `rand(.05)+.25`; bubbles rise at `-1` y; **`die_offscreen => 1`** auto-reap; a **depth hash** (shark 2, fish 3–20, seaweed 21, castle 22 — lower = nearer); and the killer trick: **random objects are chained one-at-a-time** via `death_cb => \&random_object` so exactly ONE special crosser is on screen at a time → the scene is never crowded. |
| **asciiart.eu/space/planets** (Joan G. Stark) | markdown (read) | Saturn-with-rings (`.::''MMMMM88&&&&''::.`), banded Earth globes, a full solar-system line. Source for the **planet sprite** (rings + banding) adapted to mono. |
| **asciiart.eu/space/stars** | markdown (read) | Star/twinkle glyph vocabulary (`*`, `+`, `.`, `'`, `,`, dipper constellations, `--==> * <==--`). Source for the **twinkle** glyph cycle. |
| **asciiart.eu ASCII Night Sky Generator** | screenshot (read) | **Validated the parallax model**: it buckets stars by glyph weight — **Dot `.` / Plus `+` / Star `*` / Circle `o` / Quote `'`** — at sparse density (80 small + 3 big over ~80×20), plus moon phases and drifting `~~+o` asteroids/comets. I map those buckets directly onto depth layers. |
| **asciiart.eu/space/aliens & /astronauts** | markdown (read) | Little-creature silhouettes + a lander/satellite (`,MMM`, `[_[_]`) → **other-Ping variants** and the orbiter sprite. |
| existing **`internal/tui/ping.go` / `pingwalk.go`** | code (read full) | The mascot is already a 5-line sprite with idle/wave/scan/look/headset/blink/tx/static/**walk** banks, a 3-line `cornerHead`, a deterministic desync hash `pingHash`, an `anim()`/`quiet` reduced-motion freeze, and a working `pingWalkModel` (90 ms tick, re-enters from the left, any key quits). **The screensaver reuses all of it.** |

Two screenshots I read live (and am citing): the **MacAsciiquarium** terminal capture (layered calm world) and the **Night Sky Generator** preview (glyph-weighted starfield). The asciiquarium README screenshot confirmed the controls vocabulary (p pause / r redraw / q quit).

---

## 1. Grounding: the design system already in the code

So both deliverables stay native, here's what `tui.go` already gives us (cited so the
proposal builds on it, not beside it):

- **Palette** (`tui.go:158-204`): `cRed` (#E0231C/#FF4438) is the *only* hue; everything
  else is the ink ramp `cInk / cBody / cDim / cInkBg`. Styles: `stBrand` (bold ink),
  `stDim` (labels/structure), `stKey` (load-bearing value), `stRed`/`stSelText` (the red
  glint), `stPresetOn`/`stRowSel` (reverse-video red selection).
- **Glyph set + ASCII fold** (`internal/glyphs`): rich set `◉ ○ ◆ ✓ (( • )) ▁▂▃▄▅▆▇█`,
  and a **fold map** for legacy consoles: `• → *`, `○ → o`, `◉ → @`, `◆ → #`, `✓ → +`,
  box-draw `│─╰╯╭╮ → | - +`, feet `╿╽ → |`, and the signal ramp **`▁▂▃▄▅▆▇█ → .:-=+*#@`**.
  *Design rule for all new art below: build from fold-safe glyphs so the screensaver
  degrades cleanly.*
- **One carrier beat** (`tui.go`): a single `tick()` at **160 ms** drives `m.frame++`;
  `slowTick()` at 5 s is the calm/idle beat; `anim(frame)` returns a **frozen** frame when
  `quiet` (NO_COLOR / non-TTY) or in compact (`frozenFrame = 1`) → reduced-motion is already
  a first-class concept.
- **Animation primitives to reuse**: `pulseWith()` (beacon arcs `(`→`((`→`(((`→`((` with
  a phosphor-decay eye `• ↔ ·`), `signalTowerAt()/scanOffset()` (a triangle-wave equalizer
  `▁▂▃▄▅▆▇`), `tintSignal()` (mono tower with a **red peak glint**), and `pingHash()`
  (deterministic SplitMix desync so motion never looks like a metronome **and stays unit-testable**).
- **Mascot tinting**: `tintEyeLine()` reddens the first eye glyph per line. *(Important
  compositor caveat for the world — see §B.5.)*

---

# DELIVERABLE A — TUI beautification

## A.1 The problem, named in the actual code

The browse/chat screens stack **chrome rows around a thin slice of content**. Counting the
real render order in `View()` (`tui.go:3630`):

```
ROW  SOURCE                              what it is
1    presetBar (tui.go:3501)             [0] AGENT [1] TUNE IN [2] SHARE [3] CONFIG [L] LOGIN [?] HELP
2    "\n\n" spacer                       blank
3    header() top line (tui.go:4407)     ▟█▙ R O G E R · A I  ((•))            TUNE IN [s]
4    header() rule                       ────────────────────────────────────────────
5    header() state line                 scanning… · 3 on air · ✓ @bownux $42.17  ([m] compact)
6    section heading (browseView)        ▌ THE BAND   240 models on air · sort strongest · OPEN MARKET
7    column header                       band            on air   range    signal  flags
8..  band rows                           «the actual content»
…    endpointPanel / onAirPanel          bordered boxes (when connected / sharing)
P    promptLine (tui.go:3716)            rog › press / to type a command · enter to tune in
F1   footer rule (tui.go:6727)           ────────────────────────────────────────────
F2   footer keys (giant per-mode switch) ↑↓ pick · enter tune in · i log · f filter · ~ freq · S sort · ←/→ section · s
F3   footer right                        ✓ @bownux $42.17
F4   status / updateLine                 (transient)
```

That's **~7 rows of chrome above** the list and **~4 below** — on an 80×24 terminal the band
list gets ~12 of 24 rows. Three specific smells:

1. **Duplicated footers / key dumps.** `footer()` is a long per-mode `switch` (≈10 modes ×
   wide+narrow strings), AND `chatView()` prints its *own* in-view key line
   (`tui.go:5790`: "enter sends · esc disconnects · tab peek · /help") that restates what the
   footer already says. `modalFooter` is a third footer path.
2. **Inline command dump.** Everything a user can type is taught at once — in the footer
   key-line, in the chat key-line, and the full `helpView()` (`tui.go:6892`) prints *start
   here* + *all commands* + *channel commands* + *glossary* + *signal factors* in one long
   scroll. Nothing is progressively disclosed.
3. **Header carries 3 rows** (lockup, rule, state) that say overlapping things (the section
   appears in the preset bar, the badge, AND the state line).

## A.2 The target: four zones + progressive disclosure

Collapse to **exactly four horizontal zones**, every screen, every mode. Nothing shown today
is removed — it's **relocated** (nav → number keys + a one-line breadcrumb; full command list
→ the `/help` palette; balance/status → the status bar + transient toasts).

```
┌──────────────────────────────────────────────────────────────────────────┐
│ ZONE 1  STATUS BAR      one line + a hairline. Identity, where-am-I, money. │
│ ZONE 2  STAGE           the hero. Scrollable transcript / band table.       │  ← gets all reclaimed rows
│ ZONE 3  INPUT           one labeled, always-live prompt line.               │
│ ZONE 4  HINT BAR        one contextual line: only the 3–4 keys that matter  │
│                         NOW + the door to everything else (`/`, `?`).       │
└──────────────────────────────────────────────────────────────────────────┘
```

Principle: **the status bar answers "who/where/how-much", the hint bar answers "what can I do
right now", and the palette (`/`) answers "what else exists".** Each fact lives in exactly one
zone (kills the header/badge/state triplication).

## A.3 BEFORE / AFTER — the BAND browser

**BEFORE** (today, ~11 chrome rows around the list):

```
  [0] AGENT  [1] TUNE IN  [2] SHARE  [3] CONFIG  [L] LOGIN  [?] HELP

  ▟█▙ R O G E R · A I  ((•))                                     TUNE IN [s]
  ────────────────────────────────────────────────────────────────────────
   scanning the band… · 3 on air · ✓ @bownux $42.17              ([m] compact)
  ▌ THE BAND   240 models on air · sort strongest · OPEN MARKET
  band                 on air     range        signal   flags
  gpt-oss-20b          ◉ 4        $0.20–0.40   ▅▆▇▆▅    ◆ ✓
  qwen2.5-coder-32b    ◉ 2        $0.30–0.55   ▄▅▆▅▄    ✓
  llama-3.3-70b        ○ 0        —            ▁▁▁▁▁
  …
  rog › press / to type a command  ·  enter to tune in
  ────────────────────────────────────────────────────────────────────────
  ↑↓ pick · enter tune in · i log · f filter · ~ freq · S sort · ←/→ section · s
                                                          ✓ @bownux  $42.17
```

**AFTER** (4 zones — same data, ~4 chrome rows, the beacon lives in the bar):

```
 (( • )) ROGER·AI   ‹ TUNE IN ›  ·  240 bands · 3 on air        ✓ @bownux · $42.17
 ──────────────────────────────────────────────────────────────────────────────
  THE BAND · strongest · open market                                  filter: —
                                                                                 
   band                on air   $/1M out    signal      ·                        
 › gpt-oss-20b         ◉ 4      0.20–0.40   ▅▆▇▆▅       ◆ ✓                       
   qwen2.5-coder-32b   ◉ 2      0.30–0.55   ▄▅▆▅▄         ✓                       
   llama-3.3-70b       ○ 0      —           ▁▁▁▁▁                                 
   mixtral-8x7b        ◉ 1      0.45        ▃▄▅▄▃         ✓                       
   …                                                                             
                                                                                 
 rog ›                                                                           
 ──────────────────────────────────────────────────────────────────────────────
 ↑↓ move · ⏎ tune in · / commands · ? help                       ‹ →  SHARE ›
```

What moved, nothing lost:
- **Preset bar → status-bar breadcrumb `‹ TUNE IN ›` + hint-bar `‹ → SHARE ›`.** The number
  keys `0/1/2/3/L/?` still jump (unchanged in `presetForKey`); `←/→` still cycle sections.
  The *full* labeled bank `[0] AGENT … [?] HELP` is one keystroke away in the `/help` palette.
  Saves rows 1–2.
- **header lockup + rule + state line (rows 3–5) → one status bar + one rule.** The pulsing
  `(( • ))` (the existing `onAirPulse`) sits at the bar's left as the one red glint; section,
  counts, login, and balance ride the same line (already proven by `compactHeader`,
  `tui.go:4303` — this is essentially "compact header, promoted to the default, with the
  preset nav folded in").
- **footer key dump (rows F2–F4) → one hint bar** carrying only the keys live *now*. `i log /
  ~ freq / S sort / f filter / s share` are **not deleted** — `f`, `~`, `S`, `i`, `s` still
  work, and they're listed in `/help` and surface contextually (e.g. while a row is selected,
  the hint can rotate in `i log · d disconnect`). Balance moved up to Zone 1; `status`/
  `updateLine` become a **transient toast** that overlays the hint bar for ~3 s then fades.

## A.4 BEFORE / AFTER — the CHANNEL (chat)

**BEFORE** (`chatView` prints a heading + its own key line, then the footer prints another):

```
  ▌ CHANNEL   ✓ nyx-gpt-oss-20b · gpt-oss-20b   cost $0.0123 · system set
  you ▸ explain the broker's recount cap
  them ◂ The broker caps an over-reporting node at settle, so …
  (( • )) Receiving…  3s (holding the channel)
  you › ▌
  enter sends  ·  esc disconnects (leave this channel)  ·  tab peek at the band  ·  /help
  ────────────────────────────────────────────────────────────────────────
  type to talk  ·  esc disconnect  ·  tab peek at the band  ·  /quit leaves channel  ·  ⌃c quit app
                                                          ✓ @bownux  $42.17
```

**AFTER** (the transcript is the hero; one prompt; one hint; the duplicate key line is gone):

```
 (( • )) ROGER·AI   ◆ on nyx · gpt-oss-20b · $0.30/1M       ✓ @bownux · $42.17
 ──────────────────────────────────────────────────────────────────────────────
  you ▸ explain the broker's recount cap                                         
                                                                                 
  Ping ◂ The broker caps an over-reporting node at settle, so a node that        
        claims more tokens than it served is billed only for the lesser —        
        the recount reconciles claimed vs. measured each finalize. ▁▂▃▄▅▆▇  ←in  
                                                                                 
  ((•)) receiving · 3s ·························································· hold 
                                                                                 
 you ›                                                                           
 ──────────────────────────────────────────────────────────────────────────────
 talk · ⏎ send · esc leave · tab peek · / commands                  cost $0.0123
```

Changes:
- **One key line, not two.** `chatView`'s inline `enter sends · esc disconnects …` line
  (`tui.go:5789-5790`) is deleted; the hint bar (Zone 4) is the single source. The verbose
  `/quit leaves channel · ⌃c quit app` distinctions go to `/help`.
- **Session cost** moves to the hint bar's right (one number, where the eye rests after
  reading keys); model/endpoint/account live in Zone 1.
- The **`▁▂▃▄▅▆▇` shimmer** trails the streaming line as the "message-in" cue (see A.6), and
  the relay line (`transmitLine`) keeps its elapsed read-out.

## A.5 Progressive disclosure: the `/` command palette

Replace the inline command dump with a **centered overlay** opened by `/` (type-to-filter) and
a calmer `?` help. This is the home for the *full* nav bank + every `/command` — discoverable,
not always-on.

```
            ┌─ commands ───────────────────────────── type to filter ─┐
            │  (( • ))   rog ›  re|                                     │
            │                                                          │
            │  › /search        re-scan the band            r          │
            │    /share         put your GPU on air         [2] / s     │
            │    /reconnect     re-tune the last channel                │
            │    /limits        per-model spend caps        [3]         │
            │    /login         link GitHub (to earn)       [L]         │
            │    /grant         private keys for bots/family            │
            │    /confidential  route only to TEE nodes     ◆           │
            │    ─ sections ───────────────────────────────────────    │
            │    [0] AGENT  [1] TUNE IN  [2] SHARE  [3] CONFIG  [?]     │
            │                                                          │
            │  ↑↓ choose · ⏎ run · esc close                           │
            └──────────────────────────────────────────────────────────┘
```

- Live fuzzy filter (reuses the existing `textinput`). Each row shows the command, a plain
  one-liner, and its **shortcut + the equivalent `roger <cmd>`** (so the palette also teaches
  the CLI, the way `helpView` does today).
- `?` opens the same content in a **read** layout (the current `helpView` body — start-here +
  glossary + signal factors — minus the command dump, which is now interactive here). So the
  glossary the founder wrote isn't lost; it's the "reference" tab of the same surface.
- The palette is the **one** place the giant per-mode `switch` collapses into: instead of the
  footer teaching 10 modes' keys, each mode publishes its 3–4 hot keys to Zone 4, and the
  palette is the exhaustive list.

## A.6 Tasteful animation + structural flourishes

All cheap, all reduced-motion-safe (freeze under `quiet`/compact via the existing `anim()`):

1. **Beacon pulse (have it).** `(( • ))` in Zone 1 keeps `onAirPulse`/`pulseWith` — the arcs
   breathe `1→2→3→2` and the eye phosphor-decays `• ↔ ·`. The single red heartbeat of the app.
2. **Signal shimmer (have it).** Band rows + the empty-band CTA keep `signalTowerAt` (a
   triangle wave whose amplitude = real in-flight load) with the red peak glint at `▇/█`.
   Reuse it as the **streaming "message-in" trail** in chat.
3. **Message-in (new, subtle).** When a transcript line is appended, reveal it over 2–3 frames:
   a **left-to-right wipe** using the signal ramp as a brightness gradient
   (`▁▂▃▄▅▆▇` → text), or simply render the newest line `stDim` for one frame then `stBody`
   (a 1-frame "ink settling"). No layout shift — it animates in place. Deterministic via a
   per-message frame stamp so tests can assert "frame N renders dim, N+2 renders bright".
4. **Selection carat slide.** The `›` row cursor (already `stSelText`/`selCarat`) can ease one
   cell on ↑↓ (render `›` then `·›` for one frame) — a 1-frame motion cue, NO_COLOR-safe.
5. **Hairline structure, not boxes.** Keep the single `────` rules between zones and the `▌`
   section tab; drop the bordered `endpointPanel`/`onAirPanel` *boxes* in favor of one-line
   status under Zone 1 (compact already does this — promote it). Fewer rectangles = calmer.
6. **Toast, don't stack.** `status`/`updateLine` overlay the right end of the hint bar for a
   few seconds (dim→fade) instead of permanently adding rows.

Net: BROWSE goes from ~12 content rows (of 24) to ~17; CHANNEL gives the transcript ~4 more
rows; the eye always lands on the one red beacon, then the hero, then the 3 keys that matter.

---

# DELIVERABLE B — `roger --ping`: the "Ping World" screensaver

> A full-screen, slow, relaxing, fully-animated little planet where **Ping** ambles and runs,
> other Pings drift in and wander off, stars twinkle and parallax, the odd shooting star or
> orbiter crosses, and the day/night sky turns — paced like the asciiquarium and the "lo-fi
> beats to relax to" streams. Standalone (`roger --ping`) **and** launchable from inside the
> TUI. The one red glint stays sacred: **only Ping's eye(s)** and **one "on-air" star** ever
> go red.

## B.1 Concept + pacing philosophy

Borrow asciiquarium's calm directly (cited from the live screenshot + source): a **fixed
landmark** (there, a castle; here, **the planet**), **slow horizontal drift** as the dominant
motion, **layered depth** for parallax, and **at most one "special" crosser at a time**
(chained by death callback). Map RogerAI's identity on top: the **starfield is the band** —
each star is a far-off station, and **one star pulses red = a station on air**, to which Ping
occasionally **transmits** (`pingTxFrames`) and gets a pulse back. The screensaver is the band,
seen from Ping's little world at night.

**Cadence target.** Base clock ~**120 ms (≈8 fps)** — a touch faster than the 160 ms TUI tick
for smoother drift, still slow. *Movement* is much slower than the clock (asciiquarium's trick:
high-ish frame rate, fractional speeds):

| Thing | Moves | = cells/frame | feel |
|---|---|---|---|
| Ping ambling | 1 cell / 5 frames | 0.2 | a slow stroll |
| Ping running (a burst) | 2 cells / frame | 2.0 | a happy dash |
| near stars (layer 2) | 1 cell / 16 frames | ~0.06 | barely creeping |
| mid stars (layer 1) | 1 cell / 32 frames | ~0.03 | almost still |
| far stars (layer 0) | static, **twinkle only** | 0 | depth by stillness |
| shooting star | 3–4 cells/frame | fast | a 1-second streak |
| orbiter / moon | 1 cell / 24 frames | ~0.04 | a long arc |

Everything "random" uses **`pingHash(frame, salt)`** (the existing deterministic desync), so
the world is reproducible (testable) yet never metronomic — exactly how `idleScene` already
picks Ping's acts.

## B.2 The scene — parallax layers (back → front)

Drawn as a **cell buffer** composited back-to-front (see §B.5), depth model lifted from
asciiquarium's `%depth` and the Night Sky Generator's glyph buckets:

```
LAYER 0  deep sky      twinkling faint stars  . ˙ ' ,        (static, twinkle only)   ← farthest
LAYER 1  mid sky       + and * stars, a moon ◐, the aurora ribbon (very slow drift)
LAYER 2  near sky      o / ✦ bright stars, ONE red on-air star ◉, an orbiter ·)       (slow drift)
LAYER 3  THE PLANET    the curved horizon Ping stands on; a ringed planet behind
LAYER 4  surface life  swaying signal-grass ║▒, a parked dish, a campfire spark
LAYER 5  PINGS         Ping + wandering other-Pings (the only red eyes)               ← nearest
LAYER 6  weather/FX    shooting stars, meteor shower, a drifting cloud band           (transient)
```

Parallax = layers 0/1/2 move at the speeds in §B.1; the planet (3/4) is fixed; Pings (5) walk
on the horizon; FX (6) cross and `die_offscreen`. Smaller + dimmer + slower = farther (the
Night Sky Generator's dot/plus/star/circle weighting made literal).

## B.3 Concrete ASCII art

> All built from **fold-safe glyphs** (`• ○ ◉ │ ─ ╰ ╯ ╭ ╮ ▁▂▃▄▅▆▇█` etc.) so the world
> degrades on legacy consoles via the existing `glyphs.Fold`. Body = ink (`stPingBody`/`stDim`);
> **the eye is the ONLY red** (`stPingEye`), exactly as `renderPing` already enforces.

### B.3.1 Ping — idle on the planet (REUSE `pingIdleFrames`, ping.go:66)

```
((  •  ))      ← antennae arcs + the one red eye
 \(   )/       ← arms
  │ R │        ← body, R faceplate
  ╰───╯        ← base
   ▔ ▔         ← feet
```
Plus the existing wave / scan / look / headset / blink / **transmit** banks for "attract" beats.

### B.3.2 Ping — RUNNING RIGHT (new; 4-frame stride, leans into the run, speed streak trails)

The mascot tips forward, the eye leads, arms pump, legs do contact→passing→contact; a `·`/`~`
streak behind reads as speed. (Mirror horizontally for **running left**.)

```
 frame A (reach)     frame B (pass)      frame C (reach)     frame D (pass)
   ((• ))~             (( •))              ((• ))~             (( •))
 ·\(   )╲            ··\(  )/            ·\(   )╲            ··\(  )/
    │R│                 │R│                 │R│                 │R│
    ╰─╯                 ╰─╯                 ╰─╯                 ╰─╯
    ╱ ╲                 ╿ ╿                 ╲ ╱                 ╿ ╿
```
- The eye `•` shifts to the leading edge of the head (`((• ))` vs `(( •))`) — `tintEyeLine`
  reddens it wherever it lands (already supported, ping.go:147).
- Legs: `╱ ╲` (stride open) ↔ `╿ ╿` (passing) ↔ `╲ ╱` (opposite stride) — extends the existing
  2-frame `pingWalkFrames` contact/passing into a 4-frame run.
- `·` / `··` / `~` to the left = motion lines; drop them under reduced motion.

### B.3.3 Ping — extra poses for the attract loop (new, small)

```
 look up at a star     sit / rest          sleep (night)        cheer (got a pulse)
   ((  •  ))            ((  •  ))            ((  -  )) z          \\(  O  )//
    \(   )/  *            \( _ )/             \(   )/  Z           (   )
     │ R │                │ R │               │ R │                │ R │
     ╰───╯               ╰─────╯             ╰───╯                ╰───╯
      ▔ ▔                 ▔▔▔▔▔               ▔ ▔                  ▔ ▔
```
- `sleep` = eye to `-` (blink frame) + drifting `z`/`Z` that rise like asciiquarium bubbles.
- `cheer` = arms up `\\ //`, eye wide `O` — played when Ping transmits and the red star pulses.

### B.3.4 Other-Ping variants (depth + variety; the asciiquarium "fish species")

Smaller variants read as **farther away** (parallax), reusing the 3-line `cornerHead`
(ping.go:287) and a 1-line "baby":

```
 a distant Ping (3-line cornerHead, REUSE)   a tall Ping        a "static" ghost Ping
        (( • ))                               ((  •  ))           .. ○ ..      ← hollow eye
         \( )/                                 \(   )/            \,   ,/        (lost signal,
         ╰─╯                                   │     │             │ R │         drifts slowly,
                                               │ R   │             ╰───╯         fades after a bit)
 a far speck (1-line):   (•)                   ╰─────╯              ▔ ▔
                                                ▔   ▔
```
- The **static/ghost Ping** reuses `pingStaticFrame` (ping.go:118) — a rare, slow, dim wanderer
  with a hollow `○` eye (NOT red) that fades out, a melancholy beat.
- A **baby Ping `(•)`** (the `beaconDot`) can trail a parent Ping like a duckling.
- Spawn variety by `pingHash` so the mix differs each run but is reproducible.

### B.3.5 The planet + horizon (new; Saturn-rings adapted to mono + the R faceplate)

Foreground horizon arc Ping walks along (spans the width, gently curved):

```
        Ping ambles along this rim →   ((•))
                                      ╱ \( )/ ╲
   ___________________╭─────────────────────────────────╮___________________
  ╱                    the planet surface — banded ink — ROGER · AI           ╲
 ╱  ▒▒░░  ·  ░░▒▒▒  ·  ▓▓▒▒░  ·  ░▒▒▓▓  ·  ▒░░  ·  ░░▒▒  ·  ▓▒▒░  ·  ░▒▒▓▓░  ·  ╲
```
(Surface banding from fold-safe ramp `░▒▓` ≈ use `:-=+*#` if a console can't render shades.)

A ringed planet sitting low on the horizon behind Ping (adapted from Joan Stark's Saturn):

```
            . · ˙ ✦ ˙ · .
        ·°                 °·
      ·      ▁▂▃▄▅▆▇▆▅▄▃▂▁      ·          ← the band: a slow signal-shimmer "surface"
    ,·      ▃▄▅▆▇███████▇▆▅▄      ·,        (REUSE signalTowerAt across the disc)
   (  ·    ▅▆▇█████████████▇▆▅    ·  )      ← rings = the (   ) brackets, the Ping motif writ large
    `·,     ▃▄▅▆▇███████▇▆▅▄     ,·`
      ·°      ▁▂▃▄▅▆▇▆▅▄▃▂▁      °·
        ·°                 °·
            ` · ˙ ✦ ˙ · `
```
- The planet's "surface" is the **equalizer shimmer** (`signalTowerAt`) — the world literally
  breathes the band's signal. The encircling `(   )` rings echo Ping's own `(( • ))` brackets:
  the planet is a giant Ping beacon. Tasteful, on-brand, and reuses code.

### B.3.6 Starfield glyph weights (from the Night Sky Generator)

```
 LAYER 0 far   . ˙ ' ,            many, dim (stDim), static, twinkle by glyph-swap
 LAYER 1 mid   +  *               fewer, ink, slow drift; twinkle cycle  · → + → * → +
 LAYER 2 near  o  ✦  ◐(moon)      rare, bright; one ◉ pulses RED = a station ON AIR
```
Twinkle = swap a star's glyph on a `pingHash(starID, frame/8)` schedule (no movement, just
shimmer) — the same desync trick `idleScene` uses for blinks.

### B.3.7 FX sprites (transient, `die_offscreen`)

```
 shooting star:  ╲                 comet/asteroid (from the night-sky gen):   ~~─o
                  ╲.                orbiter (Ping's satellite, REUSE beacon):  ·) ◦ (·
                   ╲·               aurora ribbon (slow, near top):  ≋∽≈∼≋∽≈∼≋∽≈
                    `.              cloud band (rare, drifts):       ⌒‿⌒‿⌒  (or  .-~-.)
```

### B.3.8 Full "world postcard" (≈80×20 — the static frame `quiet`/NO_COLOR prints)

```
  .  ˙   '       +        .      ˙   ✦        '    .      +         ˙    '   .   ◐
      '   .   +     ˙  .      ◉        '   .     +      ˙     .   '      +    .      '
   ˙    .       '      .   ✦    .      +    '   .      ˙   '    .      o     '   ˙  .
        +    .   '   .      ˙        '   .      +    .   '    ˙   .       '   .    +
                                  ((•))                                            
                                 ╱\( )/ ╲                  (•)                      
   ____________________╭────────────────────────────────────────╮________________ 
  ╱        ▒░  ·  ░▒▓  ·  ░▒▒ R O G E R · A I  ▒▒░  ·  ▓▒░  ·  ░▒        ▁▂▃▄▅▆▇    ╲
 ╱  signal-grass:  ║▒  ║▒    ║▒       ▒║   ║▒        ║▒    ▒║      a dish:  ┳        ╲
```
This is the one frame `PingWorld()` prints (centered) when not on a live color TTY.

## B.4 Animation + pacing spec

- **Clock.** A dedicated `worldTick` at ~120 ms (≈8 fps). One counter `frame`. (Standalone uses
  its own program; embedded reuses the TUI tick but advances the world every frame — see §B.5.)
- **Easing.** Ping's run uses the contact/passing stride for natural foot-fall (already the
  `pingWalkFrames` idea, extended to 4). Acceleration: when Ping decides to run, ramp speed
  `0.2 → 2.0` over ~6 frames (a tiny ease-in) then ease-out to a stop — `scanOffset`-style
  triangle easing, not a hard on/off.
- **Parallax.** Per-layer scroll speeds from §B.1; layers wrap (a star leaving the left
  re-enters the right with a fresh `pingHash` y). The planet/horizon never scroll (the anchor).
- **Other Pings — spawn cadence (asciiquarium chain).** Keep a small live set (cap **≤3**
  wanderers so it's never crowded — the asciiquarium "one crosser at a time, chained by
  death_cb" rule, relaxed to ≤3). A new Ping enters from a screen edge every
  `~rand(12–28 s)` (via `pingHash`), picks a random variant + direction + speed, ambles/runs
  across or mills near the horizon, then **`die_offscreen`** when it drifts off — which schedules
  the next arrival. The ghost/static Ping is a rarer roll that **fades** instead of walking off.
- **Attract rhythm (the idle "story").** Ping's solo behavior is a desynchronized act loop
  (exactly like `idleScene`): mostly **amble**, occasionally **run** a burst, **stop to look
  up** when a shooting star crosses, **transmit** (`pingTxFrames`) toward the red on-air star
  and **cheer** when it pulses back, **sit** to rest, and at deep night **sleep** (`z Z`
  rising). A wandering Ping passing close triggers a mutual **wave** (`pingWaveFrames`). No two
  beats land on the same frame (desync via `pingHash`), so it reads as life, not a loop.
- **Day/night cycle (slow, ~2–4 min full turn).** A phase `dayNightPhase(frame)` cycles
  **night → dawn → day → dusk**:
  - *night*: dark, full star density, the moon `◐` up, the red on-air star pulsing, Pings may sleep.
  - *dawn*: a horizon glow line brightens (`▁▂▃` ink ramp rising at the rim), stars thin to `·`,
    Pings wake (`blink` → stand).
  - *day*: few/no stars, the brightest horizon, Pings most active (more running).
  - *dusk*: glow recedes, stars return, an aurora ribbon is likelier.
  Keep it **ink-only** — the red stays reserved for eyes + the one on-air star (no red sunrise).
- **Weather flourishes (rare, transient).** A **shooting star** every ~20–40 s (a 3–4-frame
  `╲` streak); occasionally a **meteor shower** (3–5 staggered streaks); a slow **aurora
  ribbon** `≋∽≈∼` near the top at dusk/night; a rare drifting **cloud band**. Each is an FX
  entity that crosses and `die_offscreen`s — never more than one weather event at a time.
- **The one-red invariant, animated.** Across the whole world the red channel is used **only**
  for: (a) each Ping's live eye `•/O`, and (b) the single on-air star `◉` (a slow
  `pulseWith`-style breathe). The static/ghost Ping's `○` is hollow ink (not red), reinforcing
  "off air." This is the screensaver's signature and must be asserted by a test (see §B.7).

## B.5 Integration plan

### B.5.1 Factor a reusable `pingWorldModel` (mirrors `pingWalkModel`)

`pingwalk.go` already shows the pattern: a Bubble Tea model with a tick, a width/height, "any
key quits," and a `quiet` static path. Build `pingWorldModel` the same way, but make its
**View pure + seeded** (deterministic from `frame` + a seed via `pingHash`) so it can be both
standalone and embedded, and so frames are unit-testable.

- **State**: `w, h, frame, seed`, the layer scroll offsets, `[]pingActor` (each: variant, x, y,
  vx, state, phase), the FX queue, the spawn schedule, `dayNight` phase, `prevMode` (when
  embedded). All evolved by a pure `step(state) → state`.
- **Render**: a **cell buffer** `[][]cell{ r rune; eye bool }`, composited back→front per §B.2,
  then tinted once at the end: ink everywhere, **red only where `eye` is set** (or the on-air
  star). *This fixes the `tintEyeLine` "first eye per line only" limitation* — with multiple
  Pings on one row, a per-line first-match tint would redden only one eye; the cell buffer
  reddens each eye independently and centralizes the one-red rule.

### B.5.2 Standalone `roger --ping`

Today (`cmd/rogerai/main.go:453,521-522`) `ping` / `--ping` / `-ping` all route to
`tui.PingWalk()`. Proposal:

```
case "ping", "--ping", "-ping":
    return tui.PingWorld()        // the new screensaver
```

- `PingWorld()` mirrors `PingWalk()` (pingwalk.go:90): under `quiet` print the **static
  postcard** (§B.3.8) + a friendly radio line and return; else
  `runProgram(pingWorldModel{...}, tea.WithAltScreen())`.
- **Back-compat option (founder's call):** keep the beloved quick crossing by making the old
  walk the world's **intro** — Ping runs in from the left edge (the `pingWalkModel` entrance)
  and the world settles around it — so `roger ping` still "does the walk," now into a world.
  Alternatively keep bare `roger ping` = the 2-lap walk and make only `roger --ping` = the
  world. (I recommend folding the walk into the world's entrance; one delightful path.)

### B.5.3 In-TUI launch

Bubble Tea can't nest `tea.Program`s, so the in-TUI screensaver is a **mode**, not a sub-program:

- Add `modePingWorld` to the mode enum and a `world pingWorldModel` field on `model`.
- **Entry**: a `/ping` command (discoverable in the `/help` palette) **and** a direct key.
  Free, mnemonic key: **`z`** ("zen") — it's unused in the keymap (`0-3 L ? s m f ~ S F C O i c
  d q tab esc arrows j/k/l/h` are taken; `z` is open). `z` (or `/ping`/`/zen`) sets
  `m.prevMode = m.mode; m.mode = modePingWorld; m.world = newWorld(m.width, m.height)`.
- **Update**: while `modePingWorld`, route `tickMsg` into `m.world.step()` (reuse the existing
  160 ms tick; the world's slower movement comes from its cells-per-frame fractions, so 160 ms
  is fine and avoids a second clock), and route `WindowSizeMsg` to re-fit the world.
- **View**: when `modePingWorld`, `View()` returns `m.world.View()` **full-screen** (skip the
  status/hint zones entirely — a screensaver fills the frame).
- **Exit (any key, like `pingWalkModel`)**: any `tea.KeyMsg` → `m.mode = m.prevMode` and resume
  (a clean return to exactly where they were). `esc`/`q`/space all work; keystrokes are **not**
  forwarded to the underlying screen (the first key only wakes).
- **Optional attract auto-launch (founder opt-in, off by default):** track
  `m.lastKeyFrame`; if `modeBrowse` and idle for `screensaverIdle` (e.g. ~3 min) with no
  relay/turn in flight, drift into `modePingWorld`; the first key exits. Gate behind a config
  knob (e.g. `tui.screensaver_idle`) and the existing `quiet`/compact reduced-motion checks.

### B.5.4 Width/height responsiveness

- `WindowSizeMsg` sets `w,h`; **starfield density ∝ w·h** (Night-Sky-style: ~1 faint star per
  ~18 cells, far fewer bright). Ping walks the horizon row computed from `h`; off the right edge
  it re-enters from the left (the `pingWalkModel` wrap, pingwalk.go:48-53).
- **Planet scales**: wide → the full ringed disc (§B.3.5); medium → horizon + a small disc;
  **tiny (`w<48` or `h<14`)** → minimal fallback: one Ping + a sparse twinkle + a one-line
  horizon (never overflow, never garble).
- Centered with `lipgloss.Place` (already used by `pingPose`/`PlaceHorizontal`).

### B.5.5 Reduced motion / NO_COLOR / narrow

- `quiet` (NO_COLOR / non-TTY): print the **single static postcard** (§B.3.8) and exit —
  exactly `PingWalk`'s quiet contract (pingwalk.go:91-99). No cursor churn in a pipe.
- Embedded under compact: `/ping` is an **explicit opt-in to motion**, so it runs the live
  world; `q`/any key returns to the user's calm compact view. (We could offer a "calm world" —
  slower drift, no weather — but I'd keep one world and let `quiet` be the still path.)

## B.6 Why this stays unmistakably RogerAI

- **One red glint, everywhere.** Only Ping eyes + one on-air star are red; the planet, stars,
  other-Ping bodies, horizon, weather are all ink/dim. The screensaver passes the same
  "~95% mono + ONE red" law as the app.
- **The band, made literal.** Stars = stations; a red-pulsing star = on air; the planet's
  surface = the `signalTowerAt` equalizer; Ping transmits to the band and gets a pulse back.
  It's the product's worldview at rest.
- **All built on what exists.** Reuses `pingIdleFrames`, `pingWaveFrames`, `pingTxFrames`,
  `pingStaticFrame`, `cornerHead`, `pingWalkFrames`, `pingHash`, `anim/quiet`, `pulseWith`,
  `signalTowerAt`, `tintSignal`, `runProgram`, and the `pingWalkModel` shape.

## B.7 Build order (spec-first, per CLAUDE.md — design only here)

Because money/audit rules don't apply but the **one-red invariant** and reduced-motion
contracts do, keep render **pure + seeded** so godog/table tests can assert frames (mirroring
the deterministic `pingHash`/`idleScene` approach). Suggested spec surface to get approved
before any code:

- `features/screensaver/*.feature` (godog):
  - "the world renders within W×H and never overflows" (table over sizes incl. tiny + 1×1).
  - **"every painted red cell is a Ping eye or the single on-air star — nothing else"** (the
    invariant; scan the styled buffer).
  - "at most 3 wandering Pings exist at once; a departed Ping schedules the next."
  - "any key exits to the prior mode (embedded) / quits (standalone)."
  - "NO_COLOR / non-TTY prints exactly one static postcard frame, no escape churn."
  - "narrow/tiny terminals degrade to the minimal scene (one Ping + sparse stars)."
  - "reduced-motion (compact/quiet) freezes to a stable frame."
- unit tables (stdlib + testify): `starfield(seed,w,h)` determinism + density; `step()` parallax
  wrap; `spawnSchedule` chaining; `dayNightPhase(frame)` cycle; the cell-buffer compositor
  z-order; run-cycle frame selection.

---

## Appendix — sprite sheet quick-reference (copy-ready)

```
IDLE (reuse)        RUN-RIGHT A        RUN-RIGHT B        SLEEP              CHEER
((  •  ))             ((• ))~            (( •))            ((  -  )) z        \\(  O  )//
 \(   )/            ·\(   )╲           ··\(  )/            \(   )/  Z         (   )
  │ R │                │R│                │R│               │ R │             │ R │
  ╰───╯                ╰─╯                ╰─╯               ╰───╯             ╰───╯
   ▔ ▔                 ╱ ╲                ╿ ╿                ▔ ▔               ▔ ▔

FAR PING (3-line)   BABY   GHOST(static)   STARS                 FX
 (( • ))            (•)    .. ○ ..         far . ˙ ' ,           shoot ╲   comet ~~─o
  \( )/                    \,   ,/         mid +  *  (twinkle)   aurora ≋∽≈∼
  ╰─╯                       │ R │          near o ✦ ◐  ◉(red)    orbiter ·) ◦ (·
                           ╰───╯
                            ▔ ▔
```
Eye = the only red. Box-draw + bullets fold to `| - + * o @` on legacy consoles (`glyphs.Fold`).
```
