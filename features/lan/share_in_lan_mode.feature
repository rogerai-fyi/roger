# STATUS: APPROVED 2026-10-06 (founder)
#
# LAN MODE - SHARE: what "share" means when the market is filtered out.
#
# In LAN mode, sharing serves the LAN and nothing else: this machine's models go to LAN Towers it
# attaches to and to Edge members over the instance's own face. It never registers with the
# public broker, never heartbeats it, and never joins the relay fabric.
#
# Today only headless `roger share --broker http://<lan-ip>` detects a standalone Tower
# (isLocalTowerBroker + agent.ServeLocalTower). TUI SHARE and console SHARE do not, so the same
# owner gets a different share depending on which surface they used. LAN mode makes all three
# the same.
#
# GROUND TRUTH: cmd/rogerai/localshare.go, internal/agent/localtower.go (ServeLocalTower),
# internal/agent/agent.go (register, heartbeat, long-poll), cmd/rogerai/relayfabric.go,
# internal/tui (SHARE section), internal/webui (SHARE panel), the Edge face (edge/infer.go).
#
# Tags: @cli, @tui, @console, @agent (internal/agent).

Feature: Sharing in LAN mode serves the LAN Towers and the Edge, and never the market

  Background:
    Given LAN mode is on
    And this machine serves "qwen3-8b" with `roger model serve`

  @cli
  Scenario: headless share in LAN mode serves every configured LAN Tower
    Given lan_towers contains "192.168.1.20:8787" and that Tower has attached this machine's station key
    When they run "roger --lan share"
    Then the station polls "http://192.168.1.20:8787/local/poll"
    And no request is made to the public broker's /nodes/register or /nodes/heartbeat
    And the relay fabric is not joined

  @cli
  Scenario: share in LAN mode with no LAN Tower serves the Edge only, and says so
    Given lan_towers is empty and the broker setting is not local
    When they run "roger --lan share"
    Then the output says it is serving this Edge's members only
    And no request is made to the public broker

  @cli
  Scenario: a LAN Tower that has not attached this station is reported, not retried silently forever
    Given lan_towers contains "192.168.1.20:8787" and that Tower has not attached this station's key
    When they run "roger --lan share"
    Then the output names the Tower, says it has not attached this station, and shows the key to attach
    And polling of that Tower backs off

  @tui
  Scenario: TUI SHARE in LAN mode serves the same targets as headless share
    Given lan_towers contains "192.168.1.20:8787"
    When the owner turns SHARE on in the TUI
    Then the station polls the Tower at 192.168.1.20:8787
    And no request is made to the public broker's /nodes/register
    # Regression: TUI SHARE did not detect a standalone Tower.

  @console
  Scenario: console SHARE in LAN mode serves the same targets as headless share
    Given "roger --lan webui" is running and lan_towers contains "192.168.1.20:8787"
    When SHARE is turned on from the console
    Then the station polls the Tower at 192.168.1.20:8787
    And no request is made to the public broker's /nodes/register

  @tui
  Scenario: the SHARE section says where it is on air in LAN mode
    Given SHARE is on in LAN mode, serving the Tower at 192.168.1.20:8787 and the Edge
    When the SHARE section renders
    Then it reads "on air: LAN" and lists "tower: 192.168.1.20" and "edge"
    And it shows no earnings, price or payout text

  @cli
  Scenario: `roger --lan share --private` is refused
    When they run "roger --lan share --private"
    Then it exits non-zero, saying private bands are a network feature and LAN mode is on
    And nothing was registered anywhere

  @cli
  Scenario: `roger --lan share --price` is refused as meaningless on the LAN
    When they run "roger --lan share --price 0.5"
    Then it exits non-zero, saying LAN sharing is free and has no price
    # RULED 2026-10-06: refuse a price flag in LAN mode, because a
    # price the owner thinks is set but is not would mislead them.

  @agent
  Scenario: a share that was on the market when LAN mode started is not left registered
    Given the same machine was sharing on the market from another process
    When "roger --lan share" starts
    Then it refuses to start while the market share is live, naming it
    # One registrant per node id (the voice-station lesson): two sharers of one node id
    # black-hole jobs. Recommendation: refuse rather than silently take the node id over.
