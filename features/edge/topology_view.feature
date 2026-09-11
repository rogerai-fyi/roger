# ROGER EDGE - THE EDGE SCREEN ("what is on my Edge, and how is it connected?").
#
# WHY THIS EXISTS
#
# FOUNDER RULING 2026-09-11: the TUI gains "[3] Edge" and CONFIG moves to "[4]", and the Edge
# screen draws "a graphical animated ascii graph representing how your Edge is connected".
#
# The screen answers ONE question at a glance, and the animation is not decoration: a heartbeat
# that pulses along the edge it arrived on is the difference between "I believe that Pi is alive"
# and "I can see that Pi is alive". A still graph of a fleet is a diagram; a moving one is an
# instrument.
#
# THE RULE THE WHOLE SCREEN OBEYS
#
# IT NEVER DRAWS A LINK IT HAS NOT SEEN. A fleet view that guesses is worse than no fleet view,
# because the owner will act on it. So: a solid edge means a LAN-direct connection this instance
# verified; a dim edge means a path through a relay, with the relay drawn as its own node so the
# hop is visible rather than implied; and a node whose heartbeat aged out is drawn DARK and kept,
# never dropped, because a fleet that silently loses members hides exactly the failure the owner
# needs to see.
#
# TERMINAL HONESTY. The repo has been bitten before by a panel that assumed its width. This
# screen is measured, never guessed: it is correct at 80 columns, it degrades to a list rather
# than a broken drawing when the graph cannot fit, and it renders with no ANSI under NO_COLOR.
#
# GROUND TRUTH: internal/tui/tui.go (the mode enum and the numbered screens), the existing
# k9s-style SHARE table for row/selection idiom, internal/tui/helpers.go (the measured-geometry
# rules learned on the CONFIG edit box), features/edge/discovery.feature (where nodes and their
# transports come from), features/edge/node_identity.feature (capability marks).
#
# Step definitions render the real model at fixed widths and assert on the plain text, the way
# the existing TUI suites do. No mocks; the fleet is a real in-memory fleet.

Feature: The Edge screen draws the owner's fleet, animates its liveness, and never claims a link it has not seen

  Background:
    Given a TUI with a fleet of nodes

  # =========================================================================
  # 1. THE SLOT
  # =========================================================================

  Scenario: Edge is [3] and config becomes [4]
    When the main screen is rendered
    Then "[3]" names Edge
    And "[4]" names CONFIG
    And pressing "3" opens the Edge screen
    And pressing "4" opens the spend-limit config screen that "3" used to open

  Scenario: every hint that said [3] CONFIG now says [4] CONFIG
    When any screen renders a hint naming the config screen
    Then it says "[4]"
    And no rendered hint anywhere still says "[3] CONFIG"

  Scenario: the Edge screen is reachable and leavable like the other numbered screens
    When the Edge screen is open
    Then "esc" returns to where the user came from
    And "q" does what it does on every other numbered screen

  # =========================================================================
  # 2. THE GRAPH
  # =========================================================================

  Scenario: this instance is drawn as the centre of the graph
    When the Edge screen renders
    Then this instance appears as a node marked as self
    And every other node is drawn in relation to it

  Scenario: a LAN-direct peer is a solid edge, a relayed peer is a dim edge through the relay
    Given a node "bench-pi" verified LAN-direct
    And a node "cabinet-jetson" reachable only through relay "tower-1"
    When the Edge screen renders
    Then "bench-pi" is joined to self by a solid edge
    And "cabinet-jetson" is joined to "tower-1", and "tower-1" to self, by dim edges
    And "tower-1" is drawn as a node, so the hop is visible

  Scenario: a node reachable both ways is drawn once, on its preferred transport
    Given a node reachable LAN-direct and through a relay
    Then it appears exactly once
    And its edge is the solid LAN one
    And its detail still lists both transports

  Scenario: capability marks say what a node can do
    Given nodes declaring serve, classify, sense, actuate and relay
    When the Edge screen renders
    Then each node carries a distinct mark per declared capability
    And a CLAIMED but unverified capability is visually distinct from a VERIFIED one

  Scenario: a dark node is drawn dark and kept
    Given a node whose last heartbeat aged out
    When the Edge screen renders
    Then it is drawn DARK with its last-seen age
    And it is still in the graph
    And its edge is drawn as broken rather than solid

  Scenario: a candidate is drawn outside the fleet
    Given an unenrolled peer discovered on the LAN
    When the Edge screen renders
    Then it appears in a CANDIDATES area, visually separate from the fleet
    And it has no edge to self, because nothing is connected yet
    And the screen says how to adopt it

  Scenario: an empty Edge explains itself instead of drawing an empty box
    Given a fleet with no nodes and no candidates
    When the Edge screen renders
    Then it says this machine is the only node on the Edge
    And it names the one action that would add another

  # =========================================================================
  # 3. THE ANIMATION
  # =========================================================================

  Scenario: a heartbeat pulses along the edge it arrived on
    Given a node sending heartbeats
    When two frames are rendered between heartbeats
    Then the pulse advances along that node's edge
    And no other node's edge changes

  Scenario: the animation is driven by real events, never by a timer alone
    Given a fleet where no node has sent anything for a minute
    When frames are rendered
    Then no pulse moves on any edge
    And the graph is still, because a moving graph must mean traffic

  Scenario: a relayed node's pulse travels through the relay node
    Given a node reachable only through a relay
    When its heartbeat arrives
    Then the pulse travels node to relay to self, in that order

  Scenario: animation stops when the screen is not visible
    When the user leaves the Edge screen
    Then no further animation frames are requested
    And returning to the screen resumes from the current state, not a replay

  Scenario: the graph is legible under NO_COLOR and in a pipe
    Given NO_COLOR is set
    When the Edge screen renders
    Then the output contains no ANSI escapes
    And solid, dim and broken edges remain distinguishable by glyph alone

  # =========================================================================
  # 4. GEOMETRY - measured, never guessed
  # =========================================================================

  Scenario Outline: the screen is correct at every width it will meet
    Given a terminal <cols> columns wide
    When the Edge screen renders
    Then no line exceeds <cols> columns
    And the layout is "<layout>"

    Examples:
      | cols | layout            |
      | 200  | graph             |
      | 120  | graph             |
      | 100  | graph             |
      | 80   | graph             |
      | 60   | compact graph     |
      | 40   | list              |

  Scenario: a fleet too large to draw becomes a list rather than a broken picture
    Given a fleet of 200 nodes
    When the Edge screen renders at 100 columns
    Then the layout is a list
    And it says how many nodes it is showing out of how many
    And no line is truncated mid-glyph

  Scenario: a long node name never breaks the drawing
    Given a node whose name is the maximum allowed length
    When the Edge screen renders at 80 columns
    Then the name is elided with a marker rather than overflowing
    And the graph's box alignment is preserved

  Scenario: the screen never wedges on a fleet that changes while it renders
    Given nodes appearing and disappearing during a render
    Then the frame drawn is internally consistent
    And no frame shows an edge to a node it does not draw

  # =========================================================================
  # 5. SELECTION AND DETAIL
  # =========================================================================

  Scenario: a node can be selected and described without leaving the screen
    When the user moves the selection and opens a node
    Then its detail shows id, name, kind, capabilities with verification state, transports in preference order, last seen, and the pinned fingerprint for a LAN peer
    And no secret is shown

  Scenario: the detail of a relayed node names the relay it is reached through
    Given a node reachable only through relay "tower-1"
    When its detail is opened
    Then it names "tower-1" as the path

  Scenario: selection survives a fleet change
    Given a selected node
    When another node goes dark
    Then the same node is still selected
