A. CRITIQUE: why it reads as "old IBM" (each with the fix)
1. Every region is a full rounded box with a title in the frame (map, readout, levels, rules, confirm). Six equal-weight rectangles = a 3270 form. FIX: zero full-border boxes. Regions separate by whitespace and ONE vertical hairline; cards get a 1-cell colored left rail (▌) only.
2. Label:value ledger in the readout ("ROLE / MACHINE / AUTHORITY / TURNS / DISCOVERY") with an 11-col padded key column. That is a mainframe record display. FIX: a headline (name + kind + presence) in paper bold, then meters, then facts as prose lines; keys become dim lowercase eyebrows only where a value alone is ambiguous.
3. Health is a spreadsheet: a │-ruled 7-column table plus a second 5-column rules table, both with "-" cells. FIX: one meter row per node (temp, mem, disk as S-meter bars), readings as sparklines, rules as sentences with a state dot.
4. Flat color: 80% of cells are ink/muted/dim gray on black. The palette in palette.go is good but touches maybe 20 characters per screen. FIX: color is carried by shapes (rails, meters, dots, the brand rule), not by words; one bar of teal reads as color, one teal word does not.
5. No focal element. The eye lands on the border joint "╮╭" in the center. FIX: the hub card is the hero: 3 rows tall, red rail, a live S-meter carrier line; everything else is 1 row.
6. Header is a spaced-out text string ("R O G E R · E D G E") plus a fact line plus a tricolor rule: three rows of chrome that look like a print banner. FIX: one row: mark + brand + on-air dot at left, tabs center, clock/counts right; the red/ember/gold rule stays (it is the one thing on screen that already looks like RogerAI).
7. Redundancy: alerts appear four times (header, map sub-line, ⚠ glyph, readout ALERT rows); "2 health" appears in readout AND action bar. FIX: alerts once per node (a ▲n chip on the card) and once in full (the readout), actions only in the bar.
8. The confirm dialog uses widgets.Dialog, which hard-codes a red-to-NEON-MAGENTA gradient border (dialog.go:95, visible in the capture: 60 distinct #f?0?xx hexes). Off-brand and unfixable via Style. FIX: own dialog: Block, 1 red top rule, DrawShadow, two ghost buttons.
9. Action hints wear the same dim gray as the noise ("↑↓ move  p prefer  2 health  r re-read  q quit") and the key caps are not caps. FIX: key in paper bold on a 1-cell dark chip, verb in muted, 3 spaces between; keep 5 max.
10. Text overflows and clips at the pane edge in the readout ("RogGentooE", "enrolling a", "roger edge s"). FIX: wrap or elide with "…" (wrapWords exists; the box-plus-11-col-key layout is what steals the width).

B. DIRECTION: "THE RACK". The Edge is a 1950s station's equipment rack: every machine is a rack unit with its own VU meter, the
authority hub is the transmitter at the top with the carrier line breathing, adopted machines are patched-in units below,
discovered devices are faint units on the bench waiting to be racked. Unmistakably RogerAI: the ▟•▙ mark and ◉ ON AIR pulse from the
Bubble Tea TUI, the S-meter glyph language (▁▂▃▄▅▆▇█, S1..S9, green "+" overzone) already in smeter.go, ONE red as the selection
and alarm needle, and copy in the operator's voice (dark, carrier, patched in, off the rack). Modern: no boxes, a 1-cell colored
rail per card, whitespace as the grouping tool, uppercase dim eyebrows vs paper-bold names as the only two type weights, one hero
element per screen (hub card on FLEET, the selected node's meter stack on HEALTH), and motion limited to the carrier and the alarm.

C. FLEET SCREEN (130x40; rows 3, 9, 13, 16, 20-36 are intentionally empty air; readout width 58, rail 1)
 ▟•▙ ROGER · EDGE    ◉ ON AIR                     FLEET   health                                      11:32:07 · 2 units · ▲ 3
 ━━━━━━━━━━━━━━━━━━━━━━━━───────────────────────────────────────────────────────────────────────────────────────────────────────────

 ▌ ◉ RogGentooEdged                     THIS MACHINE   │  RogGentooEdged   the authority · LOCAL ROOT
 ▌   authority · local root · prefers local            │  transmitter of this Edge; it holds the root key and signs every member
 ▌   ▇▇▇▇▇▇▇▁▁ S7  online 14d  ▲ 2                     │
 ▌      ╰ ◆ roggentooedged     agent · grok-4.6        │  CARRIER   ▇▇▇▇▇▇▇▁▁ S7    heard 22s ago      turns go local first
                                                       │  TEMP      ▇▇▇▇▇▇▇▇▉ 95.5°C   ▲ over 85
 ON THE RACK  1 patched in                             │  MEMORY    ▇▇▇▇▇▇▁▁▁ 85 of 125 GB
                                                       │  DISK      ▇▇▇▇▇▇▇▇▎ 92%  /mnt/train   ▲ over 90
 ▌ ▣ gentle-ibex-14        phone   ▁▁▁▁▁▁▁▁▁ dark 1d ▲1│
                                                       │  ON AIR    grok-4.6
 ON THE BENCH  1 asking                                │  CAN DO    SERVE  CLASSIFY  sense·  actuate·
                                                       │
 ▌ ◈ pi-kitchen-02         board   ┈┈┈┈┈┈┈┈┈ found 40s │  DISCOVERY not scanning in this process · roger edge scan
                                                       │  JOIN      roger edge join 10.0.0.9:44121
                              (rows 17..36: empty air) │

  ↑↓ select   p prefer   a adopt   n rename   f forget   2 health   q quit                          ▸ turns go local first
Color roles (dark ground; the light-ground twins are the site tokens):
 live    #E0231C (#FF4438 on dark)  selection rail, ON AIR dot, alarm ▲, lit alarm meter cells, primary key caps
 ember   #FF8A3D  the hub's own rogers (◆), rule segment 2
 gold    #F2C14E  discovered/asking units, DISCOVERED eyebrow, rule segment 3
 teal    #3FB8AF  phones, models on air, the healthy carrier meter fill
 sky     #6FB7FF  host machines' rail and ▣
 violet  #A78BFA  boards' rail and ▣, CLASSIFY chip
 mint    #7ED9A6  online word, verified chips, the S9 "+" overzone
 paper   #F3F1EA  names, values, key caps   ink #D7D3C8 prose   muted #9A968B eyebrows, verbs   dim #555148 dark units, rules
 A card's rail (▌) is its kind color; the SELECTED card's rail turns live and its name goes paper-bold. Nothing else changes on select.
Typography: UPPERCASE only for eyebrows (ON THE RACK, CARRIER, TEMP) and VERIFIED chips; names Title-as-given in bold paper; prose
sentence case in ink; hints lowercase muted; numbers always with their unit; never a bold uppercase sentence.
Motion (6 fps is enough): the ◉ ON AIR dot breathes live→dim over 2 s; the hub carrier's frontier cell breathes ▆▇█ (smeter.go
pattern, count never changes); a discovered unit's ◈ blinks 1 s on / 1 s off in gold; an alarm ▲ does NOT blink (it is a fact);
the toast slides in at the bar's right for 6 s. Never moves: names, rails, meter counts, layout, the rule.
Empty state (no members, none discovered): the hub card alone, then one paper line "nothing else on the rack yet" and two dim
lines: "on another machine:  roger edge join 10.0.0.9:44121" and "or scan the LAN:  roger edge scan".

D. HEALTH SCREEN (130x40; the fleet strip on top, the selected unit's instrument stack below, rules as sentences)
 ▟•▙ ROGER · EDGE    ◉ ON AIR                     fleet   HEALTH                                      11:32:07 · 2 units · ▲ 3
 ━━━━━━━━━━━━━━━━━━━━━━━━───────────────────────────────────────────────────────────────────────────────────────────────────────────
 UNIT                        TEMP             MEMORY           DISK             CARRIER
 ▌ ◉ RogGentooEdged   host   ▇▇▇▇▇▇▇▇▉ 95.5°  ▇▇▇▇▇▇▁▁▁ 68%   ▇▇▇▇▇▇▇▇▎ 92%   ▇▇▇▇▇▇▇▁▁ 14d up      ▲ 2
 ▌ ▣ gentle-ibex-14   phone  ┈┈┈┈┈┈┈┈┈ no reading           ┈┈┈┈┈┈┈┈┈        ▁▁▁▁▁▁▁▁▁ dark 1d       ▲ 1

 RogGentooEdged   host · online · readings 21s old
 TEMP    95.5°C  ▂▃▃▄▅▆▇▇█████ last 60 reads            ▲ cpu_temp over 85 for 14 min
 LOAD    13.04   ▁▂▂▃▃▄▄▃▅▆▅▆▇
 MEMORY  85.1 of 125 GB   ▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▁▁▁▁▁▁▁▁▁▁▁▁▁ 68%
 DISK    /mnt/train        ▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▎▁▁ 92%   ▲ over 90
         /mnt/fast         ▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▁▁▁▁▁▁ 86%
         /                 ▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 64%
 GPU     NVIDIA RTX PRO 4500 Blackwell   ▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ 43% memory

 RULES   ● cpu_temp over 85   fleet   firing on RogGentooEdged          alerts stay on this machine (no sink configured)
         ● disk over 90       fleet   firing on RogGentooEdged
         ● offline over 5m    fleet   firing on gentle-ibex-14
  ↑↓ unit   r re-read   1 fleet   q quit
Meters are 9-cell S-meters in the strip (teal fill; live fill from the cell where a rule's threshold sits, so 95.5°C shows 8 teal
cells and a red ▉) and wide LineGauge-style bars in the stack. Sparklines (widgets.Sparkline over the last 60 reads, teal, red
above threshold) replace "age" text; "no reading" is a dim ┈ bar, never "-" or 0. An alert reads as a sentence at the end of its
own meter row in live: "▲ cpu_temp over 85 for 14 min". A firing rule is a live ● dot, a quiet one a dim ○. Under 100 columns the
strip drops CARRIER, the stack keeps TEMP/MEMORY/DISK and folds GPU/LOAD.

E. SHARED COMPONENTS (build once in internal/fleetview, reuse on both screens)
 1. Brand band: one row: span("▟•▙", live) bold("ROGER", paper) span(" · ", live) bold("EDGE", paper), breathing ◉ ON AIR, tabs
    (active paper-bold with a live "▔" underline row, inactive muted lowercase), right-aligned clock/counts; under it the rule:
    ━ (heavy) in live for the first 24 cells then ─ dim. BigText was tested mentally and rejected: 8x8 VGA glyphs ARE the IBM look.
 2. Station card (unit): rail ▌ + glyph + name(22) + kind + 9-cell meter + presence + ▲n; 1 row, 3 for the hub; selected = live rail.
 3. Meter: an S-meter func (units 0..9, over bool, alarmFrom int, breathing bool) returning spans; wraps smeter.go's ramp; also a
    wide variant on Gauge{WholeCells:false} for the stack. One place decides teal/live/dim.
 4. Chip: VERIFIED = bold paper on the cap color; claimed = lowercase cap color + a trailing "·"; ▲n alarm chip = live bold.
 5. Confirm: own Block (no Dialog): 1 live ━ top rule, title paper-bold, one ink sentence, [y forget] live-bold and [n keep] muted,
    DrawShadow(2,1). Same builder serves rename (a TextInput row instead of the sentence).
 6. Toast: reuse the header-row swap but move it to the action bar's right end ("▸ turns go local first" mint / live), 6 s, no box.

F. BUILD ORDER (biggest visual jump first)
 1. Remove every Block border; add rails and the hairline column; kill the key:value readout for the hub/member headline (M).
 2. Header to one row + heavy/dim rule; tabs as underline (S).
 3. S-meter component + carrier line on the hub card and member cards (M).
 4. Health: strip of station cards with meters replaces the table; stack of wide meters replaces the record readout (L).
 5. Own confirm/rename dialog replacing widgets.Dialog (kills the magenta) (S).
 6. Sparklines from a ring buffer of the last 60 snapshots per node (M, needs a small history in Health).
 7. Chips, alarm sentences, rules-as-sentences with ● dots (S).
 8. Motion pass: breathing dot + frontier cell, gold blink, toast slide; honor ModeReducedMotion (S).
 9. 100-column degradation: drop CARRIER, fold GPU/LOAD, readout to 44 cols (S).
