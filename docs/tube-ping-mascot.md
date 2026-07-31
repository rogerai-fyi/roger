# Tube Ping mascot system

Tube Ping is RogerAI's rounded terminal-native mascot. It evolves the original
Ping without deleting it: Tube Ping owns the house identity and hero moments,
while classic Ping remains the compact ASCII fallback and keeps its established
animation repertoire.

## Forms

### Hero

The five-row hero is used for the short `z` title card and future launch/export
art:

```text
   ▄███████▄
(  █   •   █▓  )
   █  ROG  █▓
    ▀█▄▄▄█▀▒
     ▀   ▀
```

The same compact silhouette is used in the title card and AGENT corner so Tube
Ping never changes character between surfaces. The rounded face uses the
founder-approved pixel silhouette. The interior is SEVEN cells and everything -
cap, eye, wordmark, base, feet - centres on one axis. An earlier revision
narrowed it to six and pushed the eye one cell right as an "optical correction";
that forced the wordmark against the right wall, and it was withdrawn. Do not
reintroduce a per-glyph offset. `▓` is the middle
depth edge and `▒` is its lower shadow. Render each body plane as one contiguous
ANSI span: styling every block cell independently introduces visible seams in
some terminals and makes the same silhouette look fragmented.

### Walker

Ping World uses a scene-sized five-row Tube Ping with alternating feet. It moves
slowly across the planet rim on terminals at least 72 columns wide and 20 rows
tall. Classic Ping continues walking and performing its original seeded
behavior loop in the same scene. Smaller worlds omit Tube Ping cleanly.

### Compact station bug

Persistent TUI headers use `▟•▙▓`, which fits the former one-row radio-mark
budget. AGENT expands the same visual language into a stable three-row reactive
form for waiting, thinking, streaming, and tool activity. Narrow and ASCII
layouts fold to the existing compact carrier-eye fallback.

## Color roles

- `•` is the only saturated Roger-red cell in a Tube Ping.
- Face and wordmark use bright house ink.
- `▓` uses the middle body plane.
- `▒` uses the dim shadow plane.
- Shape and meaning must survive `NO_COLOR`; color is reinforcement only.

## Compatibility

`ROGERAI_ASCII`, missing Unicode block support, and undersized mascot regions
retain classic Ping. Existing classic frame banks and Ping World behavior are
compatibility contracts and must not be removed when Tube Ping evolves.

The canonical glyph rows live in `internal/tui/tube_ping.go`; screens should
consume those renderers instead of maintaining hand-copied variants.
