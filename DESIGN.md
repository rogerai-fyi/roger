# RogerAI design system

One visual language across **both** surfaces - the website (`web/`) and the CLI/TUI
(`internal/tui`). Minimal, white-forward, beautiful, quietly animated. If a change
to one surface isn't reflected here and in the other, it's not done.

## Metaphor
A **two-way radio for GPUs.** Providers go *on air*; users *tune in* to a *station*
(a model on a node) over a *channel*. **Signal = throughput.** A gold **◆ call-sign =
lineage-verified.** "Roger that" = connected. Lean on it everywhere, lightly.

## Color tokens (canonical - `web/styles/tokens.css` mirrors these; TUI lipgloss uses the same hex)
| Token | Hex | Use |
|---|---|---|
| volt  | `#5B5BFF` | brand, selection, primary action, "you"/receiver |
| live  | `#00C781` | on-air, health, signal bars |
| ember | `#FF8A3D` | money / cost / API key |
| gold  | `#E8B339` | lineage call-sign ◆ (and confidential ◆) |
| ink   | `#0B0D12` | text on light; base bg in the TUI |
- **Web = light/white** (ink on white, generous whitespace, one accent at a time).
- **TUI = dark terminal** but the *same* accent hues, so they read as siblings.
- One accent per view; never rainbow. Pure `#000`/`#fff` avoided in the web palette.

## Glyph kit (the ASCII/braille signature - identical in both surfaces)
- Wordmark: `▟█▙  R O G E R · A I`
- On-air pulse: `( • )` → `(( • ))` → `((( • )))` (animated)
- Signal bars: `▁▂▃▄▅▆▇█` (height ∝ measured tok/s)
- Lineage / confidential call-sign: `◆` (gold)
- Online / offline dot: `●` (live) / `○` (mist)
- Price range (cross-station, live): `min ~ max` ($/1M out); single station = a point, no `~`.
  Cheap end tinted `live`, high end `ember`. The 24h price track reuses the signal-bar
  ramp `▁▂▃▄▅▆▇█`; the hour-trend caret is `▴` up / `▾` down / `─` flat. Over a user's
  per-model max = `above limit` in `ember` (sorts last). Same kit on web and TUI.

## Motion
- **Quiet + slow.** Ambient, never demanding. 60fps, `requestAnimationFrame`, pause offscreen.
- Signature animations: the **blip-map** (stations rippling on a faint grid, token-beams to the
  receiver) on web; the **on-air pulse** + **live signal bars** on both; the **terminal replay**
  of the tune-in flow on web mirrors the real TUI.
- **Always** honor `prefers-reduced-motion` (web) / `NO_COLOR` + non-TTY (TUI) with a static fallback.

## Typography & voice
- Web: a clean sans (Inter/system) for prose, a mono (Geist Mono/JetBrains) for commands + the
  terminal. TUI: the terminal's font.
- Voice: terse, lowercase-leaning, radio-flavored ("on air", "tune in", "station", "roger that").
  Show, don't sell. The hero is the install command; the proof is the live demo.

## Coherence checklist (run when touching either surface)
- [ ] Same hues (table above), same glyph kit, same metaphor words.
- [ ] Minimal: lots of whitespace (web) / clean alignment (TUI); one accent per view.
- [ ] An animation present but quiet; reduced-motion/no-TTY fallback works.
- [ ] The endpoint+key + ◆ lineage motif appear the same way in both.
