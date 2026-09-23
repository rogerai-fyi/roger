# ROGER EDGE - THE MAP AT SCALE: how [3] draws 3, 30, or 300 nodes (PROPOSED).
#
# STATUS: PROPOSED, awaiting founder approval. NOT IMPLEMENTED. This is the design for the founder's
# 2026-09-21 direction:
#
#   "We need to adjust the TUI when we have say 10-100 or more agents and TUIs, so as we grow we
#    might have to adjust the TUI design and how we represent or switch between representations of
#    the ASCII. Make sure we continue iterating and making beautiful, useful TUIs."
#
# THE PROBLEM: the card topology (edge_map_view.go) is right for a handful of nodes - a machine and
# the few rogers on it - but 18-wide cards do not scale to 100. The multi-node graph already
# degrades to a LIST past edgeMaxGraphNodes (topology_view.feature); this spec makes the SET-UP
# network map do the same, deliberately, with representations chosen by size, and a way to switch.
#
# THE REPRESENTATIONS (each keeps the invariants: never draw a link not seen; the selected node
# glows and moves with the arrows; every line fits the width; a dark node is kept):
#
#   1. MAP   (default, <= ~12 nodes): the card topology we have - machine card, branch, node cards.
#   2. GRID  (~13-60 nodes): compact one-line cells in a wrapped grid, glyph + short name + a state
#            dot, grouped under their machine. The selected cell glows.
#   3. LIST  (> ~60, or when chosen): one node per row - glyph, name, machine, job, model, state -
#            sortable and filterable, the densest legible form. This is the graph's existing list,
#            extended with the job/model columns from jobs.feature.
#
# The owner can SWITCH representation by hand (a key), and the map picks a sensible default by
# count. Groups collapse: a machine with many agents can fold to "machine (12 agents)" and expand.
#
# HONESTY, as everywhere: a representation change never changes what is true, only how it is drawn;
# a count shown ("showing 60 of 214") is the real count; a folded group says how many it hides; a
# node dropped from a too-large draw is never silently gone - the list always reaches it.
#
# GROUND TRUTH once approved: internal/tui/edge_map_view.go (the card map), the graph list in
# edge_view.go (edgeMaxGraphNodes, edgeListRows) as the model to extend, features/edge/
# topology_view.feature (the graph-degrades-to-list rule this mirrors), jobs.feature (the job/model
# columns), features/edge/patchbay.feature (the emblems).
#
# Tags: @tui (the representations + switching), @edge (only the counts/grouping it reads).

Feature: The Edge map draws a handful or hundreds of nodes, choosing and switching representation by size

  @tui
  Scenario: a handful of nodes draws as the card map
    Given a set-up machine with a machine and three rogers on it
    When the Edge screen renders
    Then it draws the card topology - the machine card, a branch, the node cards
    And the selected card glows and moves with the arrow keys

  @tui
  Scenario: a few dozen nodes draws as a compact grid
    Given an Edge of forty agents across four machines
    When the Edge screen renders
    Then it draws a grid of one-line cells, grouped under each machine
    And each cell shows the glyph, a short name, and a state dot
    And the selected cell glows and the arrows move across the grid
    And no line exceeds the terminal width

  @tui
  Scenario: hundreds of nodes draws as a dense list that says how many
    Given an Edge of three hundred nodes
    When the Edge screen renders
    Then it draws a list - glyph, name, machine, job, model, state per row
    And it says how many nodes it is showing out of how many
    And every node is reachable by scrolling the list, none silently dropped

  @tui
  Scenario: the owner switches representation by hand
    Given a set-up machine drawn as the card map
    When the owner presses the switch-view key
    Then the same nodes redraw as the grid, then the list, then back to the map
    And the selected node stays selected across the switch

  @tui
  Scenario: the default representation follows the node count, and is never a broken picture
    Given an Edge that grows from three to sixty to three hundred nodes
    When each size renders
    Then three nodes default to the map, sixty to the grid, three hundred to the list
    And no size produces a truncated card or a link drawn to a node not shown

  @tui
  Scenario: a machine with many agents folds and expands
    Given a machine running twenty agents
    When the Edge screen renders
    Then that machine folds to a group that says it holds twenty agents
    When the owner expands it
    Then its agents are drawn, and the selected one still glows

  @tui
  Scenario: a dark node is kept in every representation
    Given a dark persistent agent among the nodes
    When the map, the grid, and the list each render
    Then the dark node is drawn dark and kept in all three, never dropped for being offline
