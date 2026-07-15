# TUI beautification backlog (beauty-director audit, 2026-07)

Round 1 (shipped in v5.3.6) fixed: HELP pager + content dedupe, masked API key,
stale cross-section status, the lying TUNE IN [s] badge, free-band "(~0 replies)",
confirm asked once (was four surfaces), aligned confirm key labels, "mode X" debug
badges, bands/models terminology, /perms + masthead chip, /model pick pinning,
voice-view em dashes, /grant help padding.

## Remaining P1 (clear wins)
- STATION LOG (i): empty cells collapse so values sit under the wrong headers;
  pad with "-" at fixed widths and widen the callsign column (tui.go bandDetailView).
- /? listing in CHANNEL clips mid-token at 140 cols; wrap or split by category.
- Narrow (80 cols): agent lines hard-clip mid-word (use truncVisible + narrow
  variants everywhere), footer wraps the balance, and the [0]..[?] section bar
  disappears entirely (keep a micro tab bar before dropping the brand).
- Selection glyph varies by view: > (band), ▸ (desk/picker), ▌ (limits). Pick one.
- SHARE: "ON AIR 0/5" unexplained; whisper "FREE ~bytes" and "▽ stt" need a legend
  or plainer copy ("FREE (per audio byte)"); "Squelch open…" wants a suffix
  ("… scanning for local models").

## Remaining P2 (polish)
- One glyph grammar app-wide for keys (suggest words: enter, esc, ctrl+y) - browse,
  picker and agent currently mix enter/⏎/⌃y.
- Mascot caption "come back" after an answer reads as an instruction; suggest
  "your turn" or "over".
- Answer blocks render raw markdown (| pipes |, **bold**); strip ** at minimum.
- /model picker flags vs AGENT header ⌁ marks computed from different sources.
- "⌁~ inferred" legend wording ("probably" reads friendlier).
- CLI name drift: standardize on `roger` everywhere in help copy.

## Boldest idea (bigger, worth a design pass)
Collapse the three hand-maintained hint surfaces (inline hint row, footer keys
line, status line) into ONE registry-driven two-line console per view: line 1 =
transient state, line 2 = the top actions for the current focus. Fixes the drift
class (stale status, conflicting esc meanings, duplication, overflow) instead of
instances, and gives narrow mode a principled priority order.
