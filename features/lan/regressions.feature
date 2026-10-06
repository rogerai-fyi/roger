# STATUS: PROPOSED 2026-10-06 (awaiting founder approval)
#
# LAN MODE - REGRESSIONS FOR THE GAPS FOUND WHILE MAPPING IT.
#
# Found by the 2026-10-06 exploration of main and the Edge branch. Each is a defect today,
# independent of LAN mode, and each becomes a permanent scenario. Several are why "local-only"
# (features/edge/mode.feature, section 4) is not yet true by every path.
#
# Tags: @client, @tui, @agent, @console, @edge.

Feature: The paths that ignored local sources or local answers now honour them

  @tui
  Scenario: the TUI dial retries /discover signed on a 401, as the CLI does
    Given the broker setting is a standalone Tower that admits this machine's key
    And LAN mode is off
    When the TUI dial refreshes
    Then the first unsigned /discover gets 401
    And the dial retries once signed and lists the Tower's bands
    And it is not drawn as "0 on air"
    # internal/tui/tui.go fetchOffers had no retry; client.discover (failover.go) already does.

  @client
  Scenario: the client reads X-Roger-Local and X-Roger-Cost from a Tower answer
    Given the broker setting is a standalone Tower
    And LAN mode is off
    When a turn is served by it with "X-Roger-Cost: 0" and "X-Roger-Local: 1"
    Then the turn is recorded as local and free
    And no hold text was shown for it

  @tui
  Scenario: TUI chat honours edge.prefer local-only even without LAN mode
    Given LAN mode is off and edge.prefer is "local-only"
    And the Edge member "jetson" serves "gpt-oss-20b"
    When the owner sends a chat message on "gpt-oss-20b" in the TUI
    Then it is served by "jetson"
    And no request was made to the broker
    # ChatTurns bypassed the Edge ladder; features/edge/mode.feature section 4 requires this.

  @agent
  Scenario: an agent turn honours edge.prefer local-only even without LAN mode
    Given LAN mode is off and edge.prefer is "local-only"
    And the Edge member "jetson" serves "gpt-oss-20b"
    When the TUI agent takes a turn on "gpt-oss-20b"
    Then it is served by "jetson"
    And no request was made to the broker

  @console
  Scenario: console chat honours edge.prefer local-only even without LAN mode
    Given LAN mode is off and edge.prefer is "local-only"
    And the Edge member "jetson" serves "gpt-oss-20b"
    When a chat message is sent from the console on "gpt-oss-20b"
    Then it is served by "jetson"
    And no request was made to the broker

  @client
  Scenario: `roger use <node>` does not consult the market before the peer
    Given LAN mode is off
    And the Edge member "jetson" serves "gpt-oss-20b"
    And the market has no station for "gpt-oss-20b"
    When they run "roger use jetson"
    Then the proxy opens pinned to "jetson"
    And it does not print "no station on air"
    And no request was made to the broker's /discover before the peer was dialed

  @edge
  Scenario: the Edge face listener binds private interfaces only
    Given a machine with a public and a private interface
    When the Edge host starts its face listener
    Then it listens on the private interface and loopback only
    And a connection to the public interface's address is refused

  @edge
  Scenario: the Edge authority listener binds private interfaces only
    Given a machine with a public and a private interface that is a local Edge authority
    When the authority listener starts
    Then it listens on the private interface and loopback only

  @edge
  Scenario: an explicit bind override is still honoured and named
    Given the owner set the Edge face bind to an explicit address
    When the Edge host starts
    Then it listens on that address
    And `roger edge` output names the explicit bind

  @cli
  Scenario: TUI and console SHARE use a standalone Tower broker setting like headless share
    Given LAN mode is off
    And the broker setting is a standalone Tower at a local address
    When SHARE is turned on from the TUI
    Then the station polls the Tower's /local/poll
    And no /nodes/register is attempted against it
