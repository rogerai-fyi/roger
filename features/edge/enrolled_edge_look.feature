# STATUS: APPROVED 2026-10-13 (founder, D1-D6 as recommended)
#
# THE ENROLLED [3] EDGE IN THE HEALTH/FLEET LOOK: the follow-up slice ruled on 2026-10-11 (R1 in
# features/edge/empty_edge_simple.feature): the welcome card and the empty [3] EDGE wear the look
# now, and the enrolled view follows. Outline: "Follow-up: enrolled [3] look" in
# docs/welcome-and-empty-edge-mockups.md.
#
# Mockups rendered with the real internal/look palette inside the TUI's own View() chrome, from the
# NEARBY golden fixture (authority "shed", member "garage" through a relay, two nearby devices):
# docs/enrolled-edge-mockups.md, docs/enrolled-edge-100x30.png, docs/enrolled-edge-80x24.png,
# docs/enrolled-edge-60x24.png, docs/enrolled-edge-80x24-shade.png, and today's frames for
# comparison: docs/enrolled-edge-today-100x30.png, docs/enrolled-edge-today-80x24.png.
#
# The proposed 100x30 frame, NO_COLOR (the TUI's preset bar, brand and footer stay above and below):
#
#   [*] ROGER . EDGE   ● ON AIR
#   > YOUR EDGE · LOCAL ROOT · shed · prefers local
#   THIS MACHINE  shed · enrolled
#   AUTHORITY     shed (this machine) · enrolling a new node needs no network beyond this LAN
#   DISCOVERY     @O scanning this network every 30s · last pass 0s ago
#
#   YOUR EDGE NETWORK  1 unit on your Edge  ·  2 devices nearby, not on your Edge yet
#   | ◉ shed
#   | YOU ARE HERE · authority · LOCAL
#   | join here  http://192.168.1.5:8791
#     │  no rogers running here yet
#     ┊  ON THE EDGE
#     | ▣ garage                          ┊ through a relay
#     | MACHINE · HEARD 20s
#   NEARBY  i invite · a approve
#   > gentle-ibex-14  mobile · seen 12s ago · key 8eda6e1a92c9  invited · waiting
#     deskside        host · last seen 20m ago · fp abababababab
#   WHAT YOU CAN DO  on  ▯ gentle-ibex-14 · PHONE · nearby, not on your Edge yet
#   [i] Invite ●  an invite only this device can use, as a QR and a line
#   [a] Approve ●  let it onto your Edge: a phone by claim, a roger when it asks
#   ↑↓←→ move   ⏎ open   tab buttons   v list   r re-read   esc back
#
# WHY: today the enrolled screen opens with a 4-row arcade cabinet and draws every card and button
# in a border, so the frame is far taller than a 30-row terminal. The height backstop keeps the
# bottom rows, and the owner never sees the title, the counts or this machine's own card at
# 100x30 or 80x24 (see the "today" frames). The look the founder approved for Health and Fleet
# (2026-09-26) and for the empty state (2026-10-11) says the same things in about half the rows.
#
# GROUND TRUTH (read 2026-10-13 at 915f5039): internal/tui/edge_view.go (edgeView: the cabinet,
# edgeNetworkMap, edgeActionBar, edgeColorSegments, edgeFactLabelStyle, edgeHints),
# internal/tui/edge_map_view.go (edgeMapTopology, edgeCardBand, edgeNetCardBig, edgeNetCard,
# edgeDropLine, edgeWireLegend, edgeMapList), internal/tui/edge_setup_view.go (edgeCabinet),
# internal/tui/edge_empty_simple.go and edge_nearby.go (the look as the TUI paints it: lookStyle,
# lookKeyCap, lookActionBar), internal/look (palette, Rail, eye, Fold, KeyCap, Eyebrow),
# internal/tui/view.go (View, finishFrame: the height backstop keeps the bottom rows),
# internal/tui/view_compact.go (the windowshade), internal/edge/status.go (MachineLine,
# AuthorityLines, DiscoveryLine, Scan, RootBadge, PreferBadge).
#
# SCOPE: only the drawing of the enrolled [3] EDGE main screen. OUT OF SCOPE, unchanged: the
# empty state (empty_edge_simple.feature), the setup wizard and its cabinet (onboard.feature),
# the node detail pages opened with enter, the NEARBY invite panel, the console's Edge view,
# `roger edge`, and every key, selection rule, action, session and map rule.
#
# DECISIONS for the founder (each with a recommendation):
#
# DECISION D1. The map's tree. Recommendation: LEFT-ANCHORED, the trunk dropping from under the
#   hub's glyph, the way Health and Fleet read down their left rail. Alternative: keep today's
#   centred hub and centred bands with rails swapped in for borders (same geometry, more empty
#   columns on either side).
# DECISION D2. The purpose sentence "the authority controls your machines, agents & devices" beside
#   YOUR EDGE NETWORK. Recommendation: DROP it; the row carries the tally instead, and what an Edge
#   is is said by the empty state and the welcome card. agents.feature:238 stays met by the
#   YOUR EDGE NETWORK heading, which is what its runner checks. Alternative: keep it as a second
#   muted row at 100 columns and up (one more row on every frame).
# DECISION D3. A terminal too SHORT for the map. Recommendation: the rows below give way in a fixed
#   order and, if the map still cannot fit, the map gives way to the list with "short pane: the
#   list" on the YOUR EDGE NETWORK row, the same honesty rule the narrow pane already follows.
#   At 80x24 with the full header this draws the list; the windowshade draws the map. A frame
#   with no measured height (tests, pipes) draws everything. Alternative: always draw the map and
#   let the backstop clip the top (today's behaviour, which hides the title and the hub).
# DECISION D4. The ON AIR lamp. Recommendation: STEADY red while enrolled, never breathing: motion
#   on [3] comes only from real events (topology_view.feature, patchbay.feature §7), and a lamp
#   that breathes on a still fleet would be the one exception. Alternative: breathe Live/Lamp
#   like the Health brand band.
# DECISION D5. The buttons. Recommendation: one row per button (key cap, label, the lit dot, its
#   explanation in muted beside it); when rows are short the explanations go and the caps sit on
#   one row. The keyboard-focused button carries the red rail and nothing else does. Alternative:
#   caps on one row always, the focused button's explanation on a row under them.
# DECISION D6. The colour rule. Recommendation: as the scenarios below say, the Health/Fleet
#   palette with the [3] exception kept ONLY where colour already repeats a glyph: the wire tints
#   and the kind glyph tints. The faceplate's multi-hue badge words (edgeColorSegments) and the
#   fact labels' lamp hues (edgeFactLabelStyle) go to ink and muted. Alternative: keep the badge
#   words tinted (root red, local green, market amber) on the title row.
#
# SUPERSEDED BY THIS SPEC (approved 2026-10-13; applied in place, line-neutral, in patchbay.feature;
# the topology_view.feature addition is appended at the end of that file as its own scenario):
#
#   features/edge/patchbay.feature:322 "the board sits in the same arcade cabinet as the wizard"
#     -> RETIRE for the main screen. Proposed text:
#        Scenario: the board wears the Health/Fleet brand band, and the wizard keeps its cabinet
#          When the patch bay renders at 100 columns with colour
#          Then under the TUI's own brand, "▟•▙ ROGER · EDGE" is a muted eyebrow row with the ON AIR lamp
#          And the setup wizard still wears the marquee cabinet
#          And under NO_COLOR the brand row folds through glyphs.Fold, still legible
#   features/edge/patchbay.feature:328 "the header is a faceplate, not a label line"
#     -> Proposed text:
#        Scenario: the title row carries the mode, the counts and the route mix
#          Given an Edge rooted at the designated machine "shed" with 3 nodes and 5 instances
#          When the patch bay renders at 100 columns with colour
#          Then the title row reads YOUR EDGE, the root and preference badges, and the route mix, in ink with muted separators
#          And the same words read under NO_COLOR
#   features/edge/patchbay.feature:354 "the cabinet transmits only while something is really happening"
#     -> Proposed text:
#        Scenario: the screen moves only while something is really happening
#          Given a fleet with a turn in flight to "jetson"
#          When the frames render
#          Then the wire to "jetson" pulses while traffic moves
#          And on a still fleet nothing on the screen moves, the ON AIR lamp included
#   features/edge/topology_view.feature:149 "the screen is correct at every width it will meet"
#     -> the 80 and 60 rows stand for a terminal tall enough to hold the map; add (proposed):
#        And on a terminal too short to hold the map, the layout is "list" and says "short pane: the list"
#   features/edge/jobs.feature:162-171 (the "DISCOVERED band" comment and steps) already describe
#     the pre-NEARBY map; unchanged by this spec, noted only because it names the ON THE EDGE band,
#     which stays.
#   features/edge/mode.feature:77, :145, :271 (badge in the header, route mix, two labels) are MET
#     unchanged by the title row; listed so the reviewer checks the runner's faceplateLine still
#     finds the ROOT/CORE line (internal/tui/edge_mode_bdd_test.go:100 keys off content).
#
#   Golden and text pins of the old cabinet (tests, rewritten in the build, not edited here):
#     internal/tui/testdata/nearby_enrolled_100.golden, nearby_enrolled_80.golden,
#       nearby_enrolled_60.golden: regenerated with UPDATE_GOLDEN=1 and reviewed; the new goldens
#       come from View() at 100x30, 80x24 and 60x24 (see §8).
#     internal/tui/edge_map_readable_test.go:261 pins "press the letter, or ↓/Tab then ←→"
#       -> proposed: pins the action bar's "tab buttons" key cap.
#     internal/tui/edge_network_test.go:89 pins "the authority controls your" (D2)
#       -> proposed: pins "YOUR EDGE NETWORK", "◉ desk", "no rogers running here", "WHAT YOU CAN DO".
#     internal/tui/edge_map_wires_test.go:94 finds the member wire 3 lines under "ON THE EDGE"
#       -> proposed: finds it on the ON THE EDGE row itself (the drop and the heading share a row).
#     internal/tui/edge_setup_render_test.go (the wizard cabinet) is NOT affected.
#
# No em dashes and no provider names in this file by house rule.

@tui
Feature: The enrolled [3] EDGE is drawn in the Health/Fleet look, and nothing it does changes
  As the owner of an Edge
  I want the [3] EDGE screen to read like Health and Fleet and to fit my terminal
  So that I see my Edge, its map and what I can do without the top of the screen scrolling away

  Background:
    Given this machine "shed" is the local authority of its Edge and is enrolled
    And "shed" is reachable at "http://192.168.1.5:8791"
    And the member "garage" is a host reached through a relay, heard 20 seconds ago
    And two devices are nearby: the phone "gentle-ibex-14", invited, and the host "deskside", seen 20 minutes ago
    And discovery passes every 30 seconds and the last pass was just now

  # =====================================================================================
  # 1. What each region becomes
  # =====================================================================================

  Scenario: the cabinet is gone and the brand band sits under the TUI's own brand
    When the owner opens [3] EDGE at 100x30
    Then the preset bar and the big ROGER·AI brand are on screen
    And under them "▟•▙ ROGER · EDGE" is a muted eyebrow row
    And no double-ruled marquee, "◉ YOUR EDGE ◉", spectrum shoulders or "· · · ON AIR · · ·" is drawn

  Scenario: the ON AIR lamp is lit only on an enrolled machine
    When the owner opens [3] EDGE at 100x30
    Then the brand band ends with a red "● ON AIR" lamp
    # DECISION D4
    And the lamp is steady from frame to frame on a still fleet

  Scenario: an authority that is not itself enrolled shows the lamp off
    Given this machine is the authority but its own enrollment has not completed
    When the owner opens [3] EDGE at 100x30
    Then the brand band ends with a muted "○ off air"

  Scenario: the title row carries the mode badge and the route mix
    Given 3 sessions went local and 1 went to the market
    When the owner opens [3] EDGE at 100x30
    Then the row under the brand band reads "YOUR EDGE · LOCAL ROOT · shed · prefers local · 3 local · 1 market"
    And it leads with the red rail and "YOUR EDGE" in bold paper
    And the root badge and the preference badge are separate words, never merged into one

  Scenario: under LAN mode the title row names LAN, not a stored preference
    Given the TUI runs with --lan
    When the owner opens [3] EDGE at 100x30
    Then the title row reads "YOUR EDGE · LOCAL ROOT · shed · LAN"

  Scenario: the facts are eyebrow and ink rows, one row each
    When the owner opens [3] EDGE at 100x30
    Then three rows follow the title: THIS MACHINE, AUTHORITY and DISCOVERY
    And each label is a muted eyebrow in a 14-column field and each value is ink
    And THIS MACHINE reads "shed · enrolled"
    And AUTHORITY reads the first line of the authority fact, "shed (this machine) · enrolling a new node needs no network beyond this LAN"
    And DISCOVERY leads with the magic eye and reads "scanning this network every 30s · last pass 0s ago"

  Scenario: a fact too long for its row ends in an ellipsis, never a cut word
    When the owner opens [3] EDGE at 80x30
    Then the AUTHORITY row ends with "…" and no line exceeds 80 columns

  Scenario: the full authority fact is still one keypress away
    When the owner selects "shed" and presses enter
    Then the detail page shows every authority line, as today

  Scenario: the map heading carries the tally
    When the owner opens [3] EDGE at 100x30
    Then a row reads "YOUR EDGE NETWORK  1 unit on your Edge  ·  2 devices nearby, not on your Edge yet"
    And "YOUR EDGE NETWORK" is a muted eyebrow
    # DECISION D2
    And the sentence "the authority controls your machines, agents & devices" is not drawn

  Scenario: a tally clause that does not fit is left out whole
    When the owner opens [3] EDGE at 80x24 in the windowshade
    Then the map heading reads "YOUR EDGE NETWORK  1 unit on your Edge"
    And no clause is cut in half

  Scenario: the hub card is a rail card with the same three lines
    When the owner opens [3] EDGE at 100x30
    Then the hub card reads "◉ shed", "YOU ARE HERE · authority · LOCAL" and "join here  http://192.168.1.5:8791"
    And each of its rows starts with a rail and none is drawn inside a border
    And the join address is ink, not amber

  Scenario Outline: a card's rail says whether it is selected
    Given the selection is on <card>
    When the owner opens [3] EDGE at 100x30
    Then <card>'s rail is red and its name is bold paper
    And every other card's rail is dim and its name is ink

    Examples:
      | card     |
      | "shed"   |
      | "garage" |

  Scenario: a member card keeps its words and loses its border
    When the owner opens [3] EDGE at 100x30
    Then the "garage" card reads "▣ garage" and "MACHINE · HEARD 20s" behind a rail
    And no card on the screen is drawn with ╭, ╮, ╰, ╯, ╔ or ╗

  # DECISION D1
  Scenario: the tree hangs from the hub's left edge
    When the owner opens [3] EDGE at 100x30
    Then the trunk drops from the column under the hub's glyph
    And "no rogers running here yet" sits on the trunk's first row
    And the drop to "garage" and the "ON THE EDGE" eyebrow share the next row
    And the "garage" card hangs directly under its drop

  Scenario: the wires keep their glyphs, tints and pulses
    When the owner opens [3] EDGE at 100x30
    Then the drop to "garage" is "┊" in the dial tint, because it is reached through a relay
    And a LAN-direct member's drop is "│" in the signal tint and a member not heard from is "╎" dim
    And a heartbeat arriving from a member lights its own drop red for that frame, as today

  Scenario: the wire legend rides beside the card it explains
    When the owner opens [3] EDGE at 100x30
    Then "┊ through a relay" is on the "garage" card's first row, after the card
    And no legend is drawn when every wire on screen is LAN-direct

  Scenario: rogers on this machine and a relay's own members draw as rail cards in the same tree
    Given two rogers run on "shed", one of them an agent, and "garage" relays a member "attic"
    When the owner opens [3] EDGE at 120x40
    Then the rogers hang from the trunk as rail cards with "‹ this one" on the roger being looked at, in red
    And "attic" hangs one level down, under "garage", on its own drop
    And every card keeps today's words: kind, hardware when it fits, via, state word and age, alert count

  Scenario: a dark unit's card is dim and keeps its place
    Given "garage" has gone dark
    When the owner opens [3] EDGE at 100x30
    Then the "garage" card is still drawn, its glyph and words dim, its drop "╎"

  Scenario: NEARBY is unchanged
    When the owner opens [3] EDGE at 100x30
    Then the NEARBY rows are byte for byte the rows empty_edge_simple.feature §7 draws at this width

  Scenario: WHAT YOU CAN DO names the selected unit and draws its buttons as key caps
    When the owner opens [3] EDGE at 100x30
    Then a row reads "WHAT YOU CAN DO  on  ▯ gentle-ibex-14 · PHONE · nearby, not on your Edge yet" with the eyebrow muted
    # DECISION D5
    And the buttons follow one per row: the key as a paper cap, the label in paper, the red dot when lit, the explanation muted
    And no button is drawn inside a border

  Scenario: the focused button carries the red rail
    When the owner presses tab and then right
    Then the "Approve" row carries the red rail and the "Invite" row does not
    And the action bar reads "←→ choose   ⏎ run   ↑ back to map"

  Scenario: the rename, bind and serve-model prompts keep their words in the look
    When the owner starts a rename on "shed"
    Then the prompt row is a muted eyebrow "RENAME THIS ROGER" with "⏎ apply · esc cancel"
    And the input row leads with the red "▶" and a red cursor, as today

  Scenario: the foot is a key-cap action bar
    When the owner opens [3] EDGE at 100x30
    Then the last body row is the action bar "↑↓←→ move   ⏎ open   tab buttons   v list   r re-read   esc back"
    And its keys are bold paper and its labels muted
    And the centred hint "↑↓←→ move · ⏎ open · ↓/Tab for buttons · v map/list" is not drawn
    And the TUI footer below is unchanged

  # =====================================================================================
  # 2. The colour rule
  # =====================================================================================

  # DECISION D6
  Scenario: the screen wears the Health/Fleet palette and the [3] exception only where a glyph already speaks
    When the owner opens [3] EDGE at 100x30 with colour
    Then every colour in the body is one of look.Palette or a wire or kind tint the screen already used
    And eyebrows are muted, names and values ink, the selected name and key caps paper
    And red is used only for: the title rail, the selected rail, the ON AIR lamp, a lit dot, a pulse, the invited state and "‹ this one"
    And the title row's words are ink with muted separators, not one hue per badge
    And the fact labels are muted, not lamp hues

  Scenario: amber is not used on the enrolled screen
    When the owner opens [3] EDGE at 100x30 with colour
    Then no span is drawn in the glow colour

  # =====================================================================================
  # 3. Widths and heights
  # =====================================================================================

  Scenario Outline: the screen fits every width it will meet
    When the owner opens [3] EDGE at <size>
    Then no line exceeds <cols> columns
    And the map is drawn as <layout>
    And the brand band and the title row are on screen

    Examples:
      | size   | cols | layout                                         |
      | 100x30 | 100  | the tree                                       |
      | 80x40  | 80   | the tree                                       |
      | 80x24  | 80   | the list, with "short pane: the list"          |
      | 60x24  | 60   | the list, with "narrow pane: the list"         |

  # DECISION D3
  Scenario: rows give way in a fixed order when the frame is short
    When the owner opens [3] EDGE at a height that cannot hold every row
    Then rows give way in this order until the frame fits:
      | order | what gives way                                                       |
      | 1     | the blank rows, the lowest first                                     |
      | 2     | each button's explanation (the caps move onto one row)               |
      | 3     | the THIS MACHINE and AUTHORITY rows (DISCOVERY stays)                |
      | 4     | the key-cap action bar (the TUI footer still names the keys)         |
      | 5     | the map, which becomes the list, and the order starts over           |
    And the brand band, the title row, DISCOVERY, the map heading, NEARBY and WHAT YOU CAN DO never give way

  Scenario: at 100x30 the whole screen fits under the TUI's chrome
    When the owner opens [3] EDGE at 100x30
    Then the frame is exactly 30 rows
    And it shows the preset bar, the brand, the brand band, the title, three facts, the tree, NEARBY, WHAT YOU CAN DO with explained buttons, the action bar and the footer

  Scenario: the height backstop never has to clip the [3] screen
    When the owner opens [3] EDGE at 100x30, 80x24 and 60x24
    Then the body plus the chrome is never taller than the terminal before finishFrame runs

  Scenario: a frame with no measured height draws every row
    Given no window size has been received
    When the Edge screen renders at 100 columns
    Then every row of every region is drawn and the map is the tree

  Scenario: v still switches the map and the list, whatever the height
    Given the map is drawn as the tree at 100x30
    When the owner presses v
    Then the map is drawn as the list, with no "short pane" note
    And pressing v again draws the tree

  Scenario: the list rows use rails for the selection mark
    When the owner opens [3] EDGE at 60x24
    Then each unit row starts with a rail, red for the selected unit and dim otherwise
    And the "▸ " marker is not drawn
    And a state too long for its row ends in "…"

  Scenario: the action bar shortens at narrow widths
    When the owner opens [3] EDGE at 60x24
    Then the action bar reads "↑↓ move   ⏎ open   v list   esc back"

  # =====================================================================================
  # 4. The windowshade
  # =====================================================================================

  Scenario: in the windowshade the brand band folds into the title row
    Given the compact windowshade mode is on
    When the owner opens [3] EDGE at 80x24
    Then the compact header names [3] EDGE
    And the first body row reads "YOUR EDGE · LOCAL ROOT · shed · prefers local" followed by the "● ON AIR" lamp
    And no separate "▟•▙ ROGER · EDGE" row is drawn

  Scenario: the windowshade keeps the tree at 80x24
    Given the compact windowshade mode is on
    When the owner opens [3] EDGE at 80x24
    Then the map is drawn as the tree
    And no line exceeds 80 columns

  Scenario: m toggles the windowshade without moving the selection
    Given the selection is on "garage"
    When the owner presses m twice
    Then the selection is still on "garage"

  # =====================================================================================
  # 5. NO_COLOR
  # =====================================================================================

  Scenario: under NO_COLOR every word is still there and the glyphs fold
    Given NO_COLOR is set
    When the owner opens [3] EDGE at 100x30
    Then the frame carries no ANSI escape
    And "▟•▙" folds to "[*]", the magic eye folds through glyphs.Fold, and key caps read "[i]" and "[a]"
    And the selected rail reads ">" and an unselected rail reads "|"
    And the wires still read "│" direct, "┊" through a relay and "╎" not heard from
    And every word of the colour frame is present

  Scenario: under NO_COLOR the selection is still told without colour
    Given NO_COLOR is set
    And the selection is on "garage"
    When the owner opens [3] EDGE at 100x30
    Then only the "garage" card's rows start with ">"
    And the "shed" card's rows start with "|"

  Scenario: under the ASCII glyph set the screen reads in ASCII alone
    Given the ASCII glyph set is in force
    When the owner opens [3] EDGE at 100x30
    Then every glyph on the screen is one glyphs.Fold or the Edge ASCII fold folds

  # =====================================================================================
  # 6. Nothing it does changes
  # =====================================================================================

  Scenario Outline: every key does what it did before the restyle
    Given the selection is on <selection>
    When the owner presses <key>
    Then the effect is <effect>, the same as before the restyle

    Examples:
      | selection          | key       | effect                                                     |
      | "shed"             | right     | the selection moves to the next unit                       |
      | "shed"             | enter     | the detail page for "shed" opens                           |
      | "garage"           | enter     | the detail page for "garage" opens                         |
      | "gentle-ibex-14"   | i         | the invite panel for "gentle-ibex-14" opens                |
      | "deskside"         | a         | the approve flow for "deskside" starts                     |
      | "shed"             | tab       | the first button takes the keyboard focus                  |
      | "shed"             | v         | the map and the list switch                                |
      | "shed"             | r         | the Edge is re-read                                        |
      | "shed"             | esc       | the screen goes back                                       |
      | "shed"             | m         | the windowshade toggles                                    |

  Scenario: the buttons offered for a selection are the same set as before
    When the owner moves the selection across every unit on the screen
    Then for each unit the buttons offered, their keys, labels, lit state and order equal edgeActions() before the restyle

  Scenario: the restyle changes no state, no session and no map rule
    When the owner opens [3] EDGE and moves through every unit
    Then the selection order, the held selection across a fleet change, the map's partition into hub, members and nearby, and the session block are the same as before the restyle

  Scenario: the empty state, the wizard and the detail pages are untouched
    When the owner opens the empty state, the setup wizard and a node's detail page
    Then each renders byte for byte as before the restyle
    And the setup wizard still wears its marquee cabinet

  Scenario: an Edge that cannot be read still says so, above the map
    Given the fleet cannot be read
    When the owner opens [3] EDGE at 100x30
    Then the row under the facts reads "the Edge could not be read: <reason>" in the one red
    And the screen does not claim this machine is the only node

  # =====================================================================================
  # 7. Motion
  # =====================================================================================

  Scenario: a still fleet draws identical frames
    Given no traffic, no heartbeat and no pass completes
    When two frames render a second apart
    Then they are byte for byte the same

  Scenario: traffic moves only the wire it is on
    Given a turn in flight to "garage"
    When the frames render
    Then only the drop to "garage" changes between frames

  # =====================================================================================
  # 8. Goldens
  # =====================================================================================

  Scenario: golden frames pin the look
    When the enrolled [3] EDGE is rendered by the TUI's View() at 100x30, 80x24 and 60x24, and at 80x24 in the windowshade
    Then each frame contains every key and word the region rules above name, and no line overflows
    And each matches its committed golden file: enrolled_edge_100x30, enrolled_edge_80x24, enrolled_edge_60x24 and enrolled_edge_80x24_shade
    And the old nearby_enrolled_100, _80 and _60 goldens are replaced by these
