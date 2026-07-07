# AGENT [0] DESK ENTRY REDESIGN — the band-table flag badges (design R4/R5).
#
# The band table's flags cell (internal/tui/tui.go: bandBadge / plainBandBadge) gains two
# capability marks alongside the existing ✓ verified / ◆ confidential / FREE / above-limit:
#
#   ⌁   agent-ready  — the model's window fits a coding agent (>=16k, operatorCtxFloor).
#   ⌁~  agent-ready, INFERRED from the window (R5: today it is ALWAYS inferred; a later
#       broker tool-call probe can flip the ~ off). No probe ships yet, so ~ is on.
#   ◪   vision       — the station DECLARED the "vision" capability (never inferred).
#
# HONESTY (R4/R5): a band claims a capability ONLY from real signal. Agent-ready is read
# from the representative window (bandCtx); vision is read from offer.Capabilities, which
# is DECODE-ONLY on this side. An ABSENT capabilities set claims NOTHING — no "text-only"
# badge is ever fabricated. A KNOWN-small window (0 < ctx < 16k) is not agent-ready.
#
# The marks register in internal/glyphs with ASCII folds (⌁ -> %, so ⌁~ -> %~; ◪ -> [v])
# so a legacy Windows console / ROGERAI_ASCII stays legible, and a dim legend line under
# the table explains the two non-self-describing glyphs. NO_COLOR strips the SGR only.

Feature: Band-table capability badges (agent-ready + vision)
  The band table tells a coding agent, at a glance, which bands fit its window and
  which see images — from real signal only, never a fabricated claim.

  # ── agent-ready, inferred from the window (R5) ───────────────────────────────

  Scenario: A 16k+ window earns the inferred agent-ready mark
    Given a band "big" whose representative window is 32768
    Then the band badge shows the agent-ready mark
    And the agent-ready mark carries the inferred "~" suffix

  Scenario: Exactly at the 16k floor is agent-ready
    Given a band "floor" whose representative window is 16384
    Then the band badge shows the agent-ready mark

  Scenario: A known-small window is NOT agent-ready
    Given a band "small" whose representative window is 8192
    Then the band badge does not show the agent-ready mark

  Scenario: An unknown window claims no agent-readiness (no fabrication)
    Given a band "unknown" whose representative window is 0
    Then the band badge does not show the agent-ready mark
    And the band badge does not say "text-only"

  # ── vision, declared only ────────────────────────────────────────────────────

  Scenario: A station that declares "vision" earns the vision mark
    Given a band "seer" with a station declaring capabilities "vision"
    Then the band badge shows the vision mark

  Scenario: Absent capabilities claim nothing
    Given a band "plain" with a station declaring no capabilities
    Then the band badge does not show the vision mark
    And the band badge does not say "text-only"

  Scenario: A capability the station did not declare is never shown
    Given a band "audio-only" with a station declaring capabilities "audio"
    Then the band badge does not show the vision mark

  # ── the two together, and alongside the existing flags ───────────────────────

  Scenario: Agent-ready and vision render together
    Given a band "multimodal-big" whose representative window is 65536
    And that band has a station declaring capabilities "vision"
    Then the band badge shows the agent-ready mark
    And the band badge shows the vision mark

  Scenario: The marks sit alongside FREE without displacing it
    Given a free band "free-big" whose representative window is 32768
    Then the band badge shows the agent-ready mark
    And the band badge shows "FREE"

  # ── the legend ───────────────────────────────────────────────────────────────

  Scenario: The legend explains the two non-self-describing glyphs
    Then the badge legend names the agent-ready mark as "agent-ready"
    And the badge legend names the inferred suffix as "inferred"
    And the badge legend names the vision mark as "vision"

  # ── the ASCII fold + NO_COLOR ────────────────────────────────────────────────

  Scenario: ASCII fold — agent-ready folds to %~, vision to [v]
    Given ROGERAI_ASCII is set
    And a band "ascii-big" whose representative window is 32768
    And that band has a station declaring capabilities "vision"
    Then the band badge contains "%~"
    And the band badge contains "[v]"
    And the band badge does not contain "⌁"

  Scenario: NO_COLOR — the plain badge carries the marks without ANSI
    Given a band "nocolor-big" whose representative window is 32768
    And that band has a station declaring capabilities "vision"
    Then the plain band badge shows the agent-ready mark
    And the plain band badge shows the vision mark
    And the plain band badge carries no ANSI color
