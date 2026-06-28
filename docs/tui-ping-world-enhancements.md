# Ping World — beautification & animation proposal

> Source: enhancement-agent pass on commit `bb92df3` (the standalone `roger --ping`
> + the in-TUI `/ping`/`z` launch). Feeds a future "Ping World v2" build. Every item
> below preserves the pinned invariants and stays pure + seeded so each is spec-able
> over `worldBuffer` cells. Companion: `docs/tui-ping-world-design.md`.

Target: `internal/tui/pingworld.go`. Sprites: `internal/tui/ping.go`. Motion helpers
(`pulseWith`, `signalTowerAt`, `scanOffset`, `anim`, `quiet`): `internal/tui/tui.go`.

## Invariants every item must preserve (pinned by `pingworld_cov_test.go`)
- **ONE-RED**: only `•` (a live Ping eye) and the single `◉` may have `eye=true`.
  Reflections, moon, grass, aurora, ducklings, etc. are **always** ink (`eye=false`).
- **≥1 red `•` every frame** (`TestWorldBufferOneRedInvariant`) and **exactly one `◉`
  every frame** (`TestWorldHasOneOnAirStar`). → If the solo Ping ever *sleeps* (eye
  `-`), another Ping must keep an open `•`.
- **Pure + seeded**; degenerate size → `""`; 1×1 must not panic; **no line may exceed
  width** (keep all new art going through `blit`, which clips).

## Cross-cutting robustness finding (cheap; do alongside P0)
`compositeWorld` + `worldBuffer`'s `blit` render runes **raw** — the world never calls
`glyphs.Fold`, so the design doc's "degrades on legacy consoles" is **not wired**, and
`░▒▓ ◉ •` already mojibake on a non-UTF8 console. Fix: fold each run in `compositeWorld`
before `Render`, and extend `asciiFold` (`internal/glyphs/glyphs.go:114`) for the new
glyphs: `◐→(`, `◑→)`, `◯/○→o`, `░→.`, `▒→:`, `▓→#`, `≈∼∽≋→~`, `◦→o`. Then every motif
below is automatically legacy-safe. Effort: S.

---

## P0 — transforms calm, additive, no scene-direction risk

### P0-1 — Give the solo Ping a real behavior loop (biggest "loop → life" win)
Replace the mechanical `px=(frame/2)%…` pace with a pure, seeded act-loop mirroring
`idleScene` (ping.go): mostly **amble**, occasionally **stop** to idle/look-up, an
occasional short **run burst**, **transmit** toward the `◉`, and at deep night
**sit/sleep**. New pure `worldPingAct(frame,seed) → (frames, eye, x, y)`; make x the
integral of a per-window speed (loop windows `0..frame/W`, each window's speed =
`worldHash(win,seed)` → {stop 0, amble 1, run 2}). Reuse `pingIdleFrames` /
`pingWalkFrames` / `pingTxFrames`. Eye stays red `•`. Effort L.

### P0-2 — Depth-weighted 3-tier starfield (genuine parallax)
Bucket each star `tier := worldHash(i,9,seed)%3`: **far** (`. ˙ ' ,`, many, faint,
static twinkle), **mid** (`+ *`, fewer, slow drift), **near** (`o ✦`, rare, brightest,
faster drift). Pure reshaping of the existing star loop; all ink. Effort S.

### P0-3 — Day/night "breathing" + dawn/dusk horizon glow
A slow `dayNightPhase(frame)` (~2-3 min/cycle) modulates star count (day ≈ 10%),
twinkle brightness, and a glow band (2-3 rows above the rim get a rising `▁▂▃` ink
ramp at dawn/dusk). **Keep `◉` always painted** — only thin the *other* stars by day.
Glow is ink-only (no red sunrise). Effort M.

### P0-4 — The moon (a calm anchor that isn't the red star)
Small moon sprite at a hash-fixed sky spot, drifting ~1 cell/24 frames, phase tied to
`dayNightPhase`. Body ink (`stDim`), never red. Fold `◐◑→( )`. Effort S-M.

---

## P1 — richer scene

### P1-1 — Still pond with reflection (HEADLINE; Ping is a duck) — maybe promote to P0
Reserve the bottom ~3-5 rows as calm water: a `~^~^~` waterline, then a dimmed,
vertically-mirrored, ripple-broken reflection of sky+moon+Ping. `reflect(buf,waterTop)`
writes dim copies with **`eye=false` always** (the `◉` reflection is dim, not red —
reinforcing one-red), breaking ~1/4 cells with `~`. Effort L. Single biggest beauty
piece; P1 only because pond-vs-bare-horizon is a scene choice.

### P1-2 — Ringed planet disc, surface = the band's own equalizer
A Saturn-style disc low on the horizon whose surface is `signalTowerAt` shimmering,
rings echoing Ping's `(( • ))` — the planet *is* a giant Ping beacon breathing the
band's signal. All ink. Effort L. (Competes with the pond for the "hero" slot — pick one.)

### P1-3 — Distant mountain/dune ridgeline (cheap depth)
A second very-dim static `/\  /\/\` line between stars and rim. One `blit`. Effort S.

### P1-4 — Duckling trail behind Ping
Replace the lone baby `(•)` with 1-3 ducklings lagging the parent; eyes **dim** (not
red) so the one-red beat stays on the parent. Effort S-M.

### P1-5 — Transmit-to-star + cheer (the "story" beat)
When P0-1 rolls "transmit," Ping faces the `◉`, plays `pingTxFrames`, the `◉` does a
`pulseWith` breathe back, Ping cheers. Ties the red eye to the red star. Effort M.

### P1-6/7 — Aurora ribbon + swaying signal-grass
Aurora: slow dim `≈∼∽≋` ribbon near the top at dusk/night (fold→`~`). Grass:
asciiquarium-style `║▒` tufts swaying by per-tuft hash phase. Pure, ink-only. S each.

### P1-8 — Wanderer spawn/despawn chain (≤3, never crowded)
Hash-scheduled arrivals/departures so the cast varies run-to-run. Closed-form schedule
from `worldHash(window)`. Effort M.

---

## P2 — flourishes (rare, transient, one-red-safe & pure)
- **Comet/asteroid** `~~─o` slow crosser; **orbiter** `·) ◦ (·` on a long arc. S each.
- **Meteor shower**: 3-5 staggered streaks extending the shooting-star branch. S.
- **Ghost/static Ping**: reuse `pingStaticFrame`, a rare dim wanderer with a hollow
  `○` (not red) eye that fades — a melancholy "off-air" beat. M.
- **Constellation**: a few fixed-offset stars forming a shape (a dipper). S.
- **Cloud band** `⌒‿⌒‿⌒` drifting and dimming the stars it passes. M.
- **Shore ember**: one flickering dim `*` campfire (stays mono — no warm color). S.

---

## New ASCII motifs (adapted to Ping)

Duck-Ping at the waterline with rippled reflection (P1-1) — eye `•` red above, dim `·` below:
```
       ((  •  ))
        \(   )/
         │ R │
      ╭──────────────╮          ← near shore (the R O G E R · A I band)
   ~~~^~~~~~^~~~~~~^~~~~~^~~~    ← waterline (dim ripples)
         │ R │
        ·(   )·                    reflection: dim ink, eye = dim ·
       ((  ·  ))                   (NOT red), broken by ~ ripples
```

Duckling trail (P1-4) — parent keeps the red eye; ducklings dim:
```
   ((  •  ))     trailing →   >(·)_   >(·)_   >(·)_
    \(   )/                    ‾‾‾     ‾‾‾     ‾‾‾
```

Moon + reflection (P0-4 / P1-1) — all dim ink, never red:
```
    ,-,-.                 crescent moon (after Flump); + is a faint star
   /.( +.\
   \ {. */
    `-`-'
  . . . . . . . . . . .   waterline
   ~{ . *}~               reflection: dim, rippled
   /.( +.\
```

Distant ridge (P1-3) — one very-dim static line behind the rim:
```
         /\         /\/\                  /\
    /\  /  \   /\  /    \    /\    /\    /  \    /\
 __/  \/    \_/  \/      \__/  \__/  \__/    \__/  \__
```

Ping looking up at the moon (P0-1 attract beat):
```
   ((  •  ))    ◐
    \(   )/   ·
     │ R │
     ╰───╯
```

Ping sleeping at deep night (needs another open `•` on screen for the one-red test):
```
   ((  -  )) z
    \(   )/    Z
     │ R │
     ╰───╯
```

Aurora ribbon (P1-6), fold-safe to `~~~~`:
```
   ≈∼∽≋≈∼∽≋≈∼∽≋≈∼∽≋        dim, slow, near the top at dusk/night
```

---

## Suggested build order (spec-first, per CLAUDE.md)
fold-safety wiring → P0-2 (depth stars) → P0-3 (day/night) → P0-4 (moon) → P0-1
(act-loop) → decide hero **P1-1 pond vs P1-2 planet disc** → rest of P1 → P2 as time
allows. Each edge case ("sleep frame still has one red eye", "reflection never sets
`eye=true`", "exactly one `◉` after day-thinning") becomes a permanent regression
scenario beside `TestWorldBufferOneRedInvariant` / `TestWorldHasOneOnAirStar`.
