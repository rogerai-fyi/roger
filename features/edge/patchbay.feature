# ROGER EDGE - THE PATCH BAY: the [3] EDGE screen as a living, colourful instrument.
#
# WHY THIS EXISTS
#
# FOUNDER RULINGS 2026-09-19/20: "more ux friendly and novel on the ascii animation generation of
# the graph like a game or something" and "beautiful and super simple and easy to use like a
# video game it should be colorful on that 3 tab for edge".
#
# So the Edge screen becomes the SECOND deliberate exception to the product's mono-plus-one-red
# rule (Ping World is the first). Colour is allowed on [3] because the screen is an instrument
# and colour is how an instrument says state at a glance. Everywhere else the rule stands.
#
# THE METAPHOR IS THE PRODUCT'S OWN. Every other surface talks like a radio station: bands,
# stations, tuning in, the desk, the mic, patching a guest through. The Edge screen is the room
# those words come from - a PATCH BAY with signal meters:
#
#   - THE DESK on the left is this instance.
#   - EVERY NODE IS A STRIP: name, kind, root/route badges, capability marks, resource bars.
#   - INSTANCES HANG UNDER THEIR NODE, indented, each with its own marks, so "2 or 3 rogers on
#     this PC" is a thing you can see.
#   - THE WIRE from a node to the desk keeps its earned texture (solid LAN-direct, dashed through
#     a relay drawn as its own strip, broken when dark) and gains a VU METER: a needle that jumps
#     on real traffic and decays.
#   - TRAFFIC IS A PACKET travelling the wire with a short trail, arriving with a small burst.
#   - DISCOVERY IS A SWEEP along the rail that reveals candidates at the bottom edge.
#   - FOCUS re-centres the board on the selected node and dims the rest.
#
# WHAT CARRIES FORWARD FROM topology_view.feature, UNCHANGED, because those are rules and not
# layout: never draw a link you have not seen; a dark node is kept; the animation is driven by
# real events only; correct at every width; legible under NO_COLOR and in a pipe; selection is
# sticky across fleet changes; a fleet too large to draw becomes a list that says so. This spec
# SUPERSEDES that spec's layout and glyph scenarios by founder ruling, and says which.
#
# THE ONE RULE COLOUR MUST OBEY: colour is never the ONLY carrier of a state. Every colour has a
# glyph or a texture beside it, so NO_COLOR, a pipe and a colour-blind owner read the same board.
#
# GROUND TRUTH: internal/tui/edge.go and edge_view.go (the screen this replaces), internal/tui/
# pingworld.go (the existing colour exception and its NO_COLOR-safe tinting), internal/tui/
# style.go (the palette rules), internal/edge/view.go (Arrange, GroupSessions - the shared rules
# the board still draws from), features/edge/topology_view.feature (superseded in part),
# features/edge/instances.feature, features/edge/mode.feature.
#
# Step definitions render the real model at fixed widths and assert on the plain text, and on
# the ANSI where a scenario is about colour. No mocks; a real fleet, real instances.

Feature: The Edge screen is a patch bay: colourful, alive on real events only, legible without colour, and simple enough to read in one glance

  Background:
    Given a TUI with a fleet of nodes and instances

  # =========================================================================
  # 1. THE BOARD
  # =========================================================================

  Scenario: this instance is the desk, on the left
    When the patch bay renders at 100 columns
    Then this instance is drawn as the desk, named "node/instance"
    And every node's wire runs to it

  Scenario: every node is a strip, and its instances hang under it
    Given a node "workshop" running "desk" and "share"
    When the patch bay renders at 100 columns
    Then "workshop" is one strip
    And "desk" and "share" are drawn under it, indented, each with its own marks
    And the strip says how many instances it has

  Scenario: a strip carries the node's facts in one line
    Given a node "jetson" of kind host, serving "qwen-3.8-27b", reached LAN-direct
    When the patch bay renders at 100 columns
    Then its strip carries the name, the kind, its capability marks, the band it serves and its wire
    And nothing on the strip needs a second key press to understand

  Scenario: the header is the mode badge and the count
    Given an Edge rooted at the designated machine "shed" with 3 nodes and 5 instances
    When the patch bay renders at 100 columns
    Then the header reads the root LOCAL, 3 nodes and 5 instances
    And it carries the route mix of the live sessions

  Scenario: a relay is its own strip and the nodes behind it hang under it
    Given "bench" reached only through the relay "shed"
    When the patch bay renders at 100 columns
    Then "shed" is a strip with a relay mark
    And "bench" hangs under "shed" on a dashed wire
    And the hop is visible, never implied

  Scenario: the empty board is the status, not a blank
    Given this instance is the only member
    When the patch bay renders at 100 columns
    Then the desk is drawn alone
    And the three facts and both ways to add a node are drawn, as the empty screen already does

  # =========================================================================
  # 2. COLOUR, AND WHAT IT MAY AND MAY NOT DO
  # =========================================================================

  Scenario: the board is in colour
    When the patch bay renders at 100 columns with colour
    Then the frame carries more than one hue
    And the palette is the board's own, not the product's mono ramp

  Scenario Outline: each state has a colour AND a glyph
    Given a node in the state <state>
    When the patch bay renders at 100 columns with colour
    Then its strip is tinted <hue>
    And its strip carries the glyph <glyph>
    When the same frame renders under NO_COLOR
    Then the glyph <glyph> alone still says <state>

    Examples:
      | state       | hue    | glyph |
      | live        | green  | ●     |
      | dark        | grey   | ○     |
      | candidate   | amber  | ◌     |
      | this desk   | white  | ▣     |
      | needs re-verify | red | ⚠    |

  Scenario Outline: each wire texture has a colour AND a pattern
    Given a node reached <how>
    When the patch bay renders at 100 columns with colour
    Then its wire is drawn <pattern> and tinted <hue>
    And under NO_COLOR the pattern alone says <how>

    Examples:
      | how              | pattern | hue   |
      | LAN-direct       | solid   | green |
      | through a relay  | dashed  | blue  |
      | dark             | broken  | grey  |

  Scenario: the route is coloured on every session, and worded too
    Given a session that went local and one that went to the market
    When the patch bay renders at 100 columns with colour
    Then the local session is tinted green and reads local
    And the market session is tinted amber and reads market

  Scenario: red is for one thing on this board
    When the patch bay renders at 100 columns with colour
    Then red appears only on a refusal, a node needing re-verification, or the selection cursor
    And never as decoration

  Scenario: NO_COLOR and a pipe get the whole board in glyphs
    Given NO_COLOR is set
    When the patch bay renders at 100 columns
    Then the frame carries no ANSI escapes
    And every strip, wire, needle and session is distinguishable by text alone

  # =========================================================================
  # 3. ALIVE ON REAL EVENTS ONLY
  # =========================================================================

  Scenario: a heartbeat sends a packet down the wire
    Given a node "bench" that is heard from
    When two frames render between heartbeats
    Then a packet glyph advances along "bench"'s wire toward the desk
    And it leaves a short trail that fades over the next frames

  Scenario: a session sends a packet the other way and the needle jumps
    Given a turn dispatched to "jetson/serve"
    When the next frames render
    Then a packet travels from the desk to "jetson"
    And "jetson"'s VU needle jumps and then decays over the following frames

  Scenario: a busy wire reads busy
    Given 12 sessions to "jetson" in the last ten seconds and 1 to "bench"
    When the patch bay renders
    Then "jetson"'s needle is drawn high and "bench"'s low
    And the counts are on the wires, never as new strips

  Scenario: a still fleet is a still board
    Given no heartbeat and no session for a while
    When several frames render
    Then nothing moves between them
    And every needle is at rest

  Scenario: nothing moves on a bare timer
    Given no heartbeat and no session
    When the animation tick fires 50 times
    Then no packet appeared, no needle moved and no strip changed

  Scenario: discovery is a sweep that reveals candidates
    Given a discovery pass that finds two candidates
    When the frames during the pass render
    Then a sweep glyph travels the rail once
    And the candidates appear at the bottom edge as the sweep passes
    And they are marked candidate, outside the fleet, with no wire to the desk

  Scenario: a node going dark is a wire that breaks in front of you
    Given a node "bench" whose heartbeat ages out mid-session
    When the frames render across the moment it goes dark
    Then its wire changes texture from solid to broken
    And its strip dims and keeps its place
    And it is never removed

  Scenario: animation stops when the screen is not shown and resumes on return
    Given the patch bay is left for another screen
    Then no frames are rendered for it
    When the owner returns to it
    Then it re-reads the fleet and resumes from real events

  Scenario: prefers-reduced-motion and the compact mode freeze the motion, not the information
    Given the compact mode is on
    When the patch bay renders
    Then packets and needles are drawn at their current state without travelling
    And every fact is still on the board

  # =========================================================================
  # 4. SIMPLE TO USE
  # =========================================================================

  Scenario: the whole screen is driven by five keys, and they are on the screen
    When the patch bay renders at 100 columns
    Then the hint line names: up and down to select, enter for detail, a to adopt, r to re-read, esc to leave
    And nothing else is needed to use it

  Scenario: selecting a strip focuses the board
    Given a fleet with four nodes
    When the owner selects "jetson"
    Then "jetson"'s strip is highlighted with the cursor
    And the other strips dim
    And "jetson"'s instances and bands expand under it

  Scenario: selection is sticky across fleet changes
    Given "jetson" is selected
    When a node arrives, another goes dark and a third is forgotten
    Then "jetson" is still the selection

  Scenario: enter opens the detail beside the board, not over it
    Given "jetson" is selected
    When the owner presses enter
    Then the detail opens as a side panel with the node's facts, its instances and its recent sessions
    And the board stays visible and alive beside it

  Scenario: a candidate is adopted in one key, and the board shows it move
    Given a candidate "new-pi5" at the bottom edge, selected
    When the owner presses a
    Then "new-pi5" is enrolled
    And over the next frames its strip rises from the candidate edge into the fleet and gains a wire

  Scenario: a first-time owner is told the one thing to do next, on the board
    Given this machine is not enrolled
    When the patch bay renders
    Then the board says, in one line, that this machine is not enrolled and names the command
    And the rest of the board is not drawn as if it were

  # =========================================================================
  # 5. RESOURCES ARE ON THE STRIP
  # =========================================================================

  Scenario: a node that reports its resources shows them as bars
    Given a node "jetson" reporting a GPU with 60% memory used and 8 GB RAM of 32 GB
    When the patch bay renders at 100 columns
    Then its strip carries a GPU bar and a RAM bar with those levels
    And the bars are tinted by level and marked by fill glyph, not by colour alone

  Scenario: a node that reports no resources shows none, never zeros
    Given a node "pi" that reports no resource facts
    When the patch bay renders
    Then its strip carries no resource bars
    And it does not show 0% anywhere

  # =========================================================================
  # 6. THE SCREEN HOLDS UP
  # =========================================================================

  Scenario Outline: the board is correct at every width it will meet
    Given a fleet of 6 nodes with 9 instances
    And a terminal <cols> columns wide
    When the patch bay renders
    Then no line exceeds <cols> columns
    And the layout is "<layout>"

    Examples:
      | cols | layout  |
      | 200  | board   |
      | 120  | board   |
      | 100  | board   |
      | 80   | board   |
      | 60   | compact |
      | 40   | list    |

  Scenario: a fleet too large to draw becomes a list that says so
    Given 40 nodes
    When the patch bay renders at 100 columns
    Then the layout is "list"
    And it says how many of how many it shows
    And the needles and route badges are still on the rows

  Scenario: a long name never breaks a strip
    Given a node named with 60 characters
    When the patch bay renders at 80 columns
    Then its strip is clipped with an ellipsis and every column still aligns
    And the selected strip marquees the full name, as the tables already do

  Scenario: the board never wedges on a fleet that changes while it renders
    Given a fleet that changes between the snapshot and the draw
    When the patch bay renders
    Then the frame is internally consistent
    And no packet is drawn on a wire the frame does not contain

  # =========================================================================
  # 7. WHAT THIS SUPERSEDES
  # =========================================================================

  Scenario: the rules of the old screen still hold on the new one
    Then a link is never drawn that has not been seen
    And a dark node is kept
    And motion comes only from real events
    And the screen is measured, never guessed, at every width
    And it is legible under NO_COLOR
    And these are the scenarios carried forward from topology_view.feature, and the row-and-wire layout scenarios there are retired by this spec
