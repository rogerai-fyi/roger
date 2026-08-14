# Engraved plate library (theme-adaptive mask plates)

Every asset here is a pair of ALPHA PLATES, not a raster illustration: `*-ink.png`
carries the black engraving as an alpha mask, `*-spot.png` carries only the red
accent (empty on assets with no accent, kept for pattern parity). Colors come from
theme tokens at render time via the two-layer `mask-image` CSS pattern — see
`.wm-masthead` / `.wm-masthead__ink` / `.wm-masthead__spot` in
`web/src/styles/wave-patch.css` for the reference implementation. Do not `<img>`
these; they are transparent black PNGs and will look like smudges.

Pipeline: Flux (flux1-dev-fp8, FluxGuidance 3.5, euler/simple, 24 steps, cfg 1.0)
→ PIL plate derivation (ink = (232−lum)/232·255·1.25 − spot; spot = redness·2.2
where redness = R−max(G,B) > 28, MedianFilter 3). Spot plates must stay < 16 KB
(pinned by test).

| Asset | Depicts | Dims | Ink / spot size | Seed | Prompt sha256[:12] | Suggested use |
|---|---|---|---|---|---|---|
| `mesh-console-*` | 1950s radio patch console, red lamp + cable (masthead) | 1408×480 | 132 KB / 3.1 KB | 42 | (pre-existing) | Playbox masthead (already wired) |
| `pump-vignette-*` | Industrial centrifugal pump w/ twin gauges, no red | 640×448 | 100 KB / 1.2 KB (empty) | 42 | `f0bfaaea25ca` | Template-card art or section marker for plant/process templates |
| `robot-arm-*` | Small industrial robot arm on pedestal, no red | 640×448 | 53 KB / 1.2 KB (empty) | 1337 | `efbe6a0676ce` | Template-card art for robotics/actuation templates |
| `gearbox-cutaway-*` | Gearbox cutaway, sectioned housing + helical gear, no red | 640×448 | 131 KB / 1.2 KB (empty) | 42 | `fecdcad803d5` | Template-card art for drivetrain/mechanical templates (tonally the heaviest plate — use on roomy cards) |
| `tape-reader-*` | Paper-tape reader feeding punched tape into a small radio; red lamp on the radio | 768×448 | 87 KB / 1.8 KB | 7 | `7ec73decc69b` | The "paste your plant's bytes in" intake/paste vignette |
| `mast-ladder-*` | Four lattice masts, tiny → tall, linked by wires; red beacon on the tallest | 1408×480 | 59 KB / 2.9 KB | 1337 | `06445fe7b9b6` | Tier-ladder plate (pico → nano → micro → core) for pricing/broadcast-article headers |
| `sensor-gauge-*` | Round-dial process sensor on a transmitter box, no red | 190×312 | 33 KB / 0.3 KB (empty) | 42 | `p-sensor.txt` sha256 n/a - prompt archived in session scratchpad | The signal bench's sensor-wall cards (wired) |
| `radio-reader-*` | Compact 1950s tabletop radio, red lamp on the VU meter | 580×380 | 131 KB / 1.4 KB | 42 | (cropped from plate-reader raw; knob-label microtext zapped) | The signal bench's READER radio (wired) |
| `radio-pocket-*` | Pocket transistor radio (palm-size set), no red | 370×310 | 59 KB / 0.5 KB (empty) | 42 | (cropped from plate-pocket raw; faceplate pseudo-text + signature zapped) | The attach menu's pico/edge-scale model icon (wired) |
| `radio-senior-*` | Tall rack-mounted radio console, three stacked panels, no red | 512×640 | 147 KB / 1.3 KB (empty) | 42 | (from plate-senior raw) | The signal bench's SENIOR rack (wired) |

Notes:
- Raw Flux outputs live in the ComfyUI output dir (`plate-*` prefixes); prompt
  texts are hashed above for reproducibility (checkpoint flux1-dev-fp8, seed listed).
- Corner registration marks, engraver microtext captions, and tiny pseudo-text
  labels were erased at plate-derivation time (border/bottom erase + targeted
  zap/fill rects), so the plates are cleaner than the raws.
- One red accent maximum per plate is a brand rule; the tape-reader's lamp sits
  on the receiving radio (the "bytes arrived" light).
- Integration is deliberately not done here: each plate needs its own container
  with the correct `aspect-ratio` and the two-layer mask pattern from
  `wave-patch.css` before it appears anywhere.
