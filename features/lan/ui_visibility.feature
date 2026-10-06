# STATUS: PROPOSED 2026-10-06 (awaiting founder approval)
#
# LAN MODE - YOU CAN ALWAYS SEE IT.
#
# The Edge mode file's rule applies: a label is a report, never a promise, and the setting
# somebody turns on to be sure must be visible everywhere they look (features/edge/mode.feature).
# LAN mode is visible on every surface for the whole session: the TUI header, the dial, the
# connect plate, every CLI command's first line, `roger status`, and the console top bar.
#
# STYLE: mono plus the one-red rule (web/DESIGN-SYSTEM.md, tui-look-and-feel). The LAN badge is
# NOT red: red is the brand dot and "on air". The badge is a mono inverse tag, distinguishable
# without colour.
#
# Two words must not blur: the Edge's ROOT badge (LOCAL ROOT / CORE) and its route preference
# badge (LOCAL-ONLY) already exist. LAN is a third thing, the band filter. When LAN mode is on,
# the Edge screen shows LAN in place of the preference badge, because LAN implies local-only.
#
# GROUND TRUTH: internal/tui/tui.go (header), internal/tui/view.go (browseView),
# internal/tui/view_connect.go (confirmView, connectingView, endpointPanel), internal/tui/
# edge_view.go, internal/edge/status.go (RootBadge, PreferBadge), internal/webui (console.html
# topbar), web/DESIGN-SYSTEM.md.
#
# Tags: @tui, @cli, @console, @edge.

Feature: LAN mode is visible on every surface, in words, without relying on colour

  Background:
    Given LAN mode is on

  @tui
  Scenario Outline: the header carries the LAN badge at every width and in the compact windowshade
    When the TUI renders at <width> columns <shade>
    Then the header contains the text "LAN"

    Examples:
      | width | shade              |
      | 120   |                    |
      | 80    |                    |
      | 60    |                    |
      | 100   | in the windowshade |

  @tui
  Scenario: the badge is distinguishable with NO_COLOR
    Given NO_COLOR is set
    When the header renders
    Then the LAN badge is readable as text alone

  @tui
  Scenario: the badge is not red
    When the header renders with colour
    Then the LAN badge is not drawn in the brand red
    And the brand red dot is unchanged

  @tui
  Scenario: the "on air" count counts only local bands
    Given 3 local bands and 40 market bands are on air
    When the header renders
    Then it reads "3 on air"

  @tui
  Scenario: the dial's title says it is the LAN dial
    When the dial renders
    Then its title names LAN

  @tui
  Scenario: the connecting and connected plates name the source
    Given the owner tuned "llama-3.3-70b" from the LAN Tower at 192.168.1.20:8787
    When the connected plate renders
    Then it names "tower: 192.168.1.20" and "LAN"

  @tui
  Scenario: the Edge screen shows LAN instead of the preference badge
    Given edge.prefer is "local"
    When the Edge screen renders
    Then the header shows the root badge and "LAN"
    And it does not show "LOCAL" as the preference

  @cli
  Scenario Outline: every CLI command's output names LAN mode once
    When they run "<command>" in a terminal
    Then the output names LAN mode once

    Examples:
      | command                     |
      | roger --lan use qwen3-8b    |
      | roger --lan search          |
      | roger --lan share           |
      | roger --lan status          |

  @cli
  Scenario: machine-readable output never mixes the note into stdout
    When they run "roger --lan search --json"
    Then stdout is valid JSON
    And the LAN note, if any, is on stderr

  @console
  Scenario: the console top bar carries the LAN badge
    Given "roger --lan webui" is running
    When the console renders
    Then the top bar contains "LAN"
    And it passes the design-system checks and the phone-width check

  @tui
  Scenario: a session's route for a LAN Tower turn reads lan, distinct from local and market
    Given a turn served by the LAN Tower at 192.168.1.20:8787
    When the Edge sessions view renders
    Then that row's route reads "lan"
    # DECISION: a third route value. features/edge/mode.feature defines route local ("stayed on
    # hardware the owner owns") and market. A LAN Tower may not be the owner's (an office Tower),
    # so calling it local would overstate it. Recommendation: add "lan" as a third route.
