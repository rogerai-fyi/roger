# STATUS: APPROVED 2026-10-07 (founder)
# BUILD STATUS: BUILT. Every scenario runs: cmd/rogerai lan_edgeonly_bdd_test.go, internal/tui, internal/webui and internal/edge runners.
#
# LAN MODE - EDGE-ONLY: the strict subset for sensitive work. Only your Edge members and this
# machine; no LAN Tower is ever shown or dialed.
#
# WHY THIS EXISTS
#
# RULED 2026-10-06 (trust_and_honesty.feature, the manual scenario): "offer `roger --lan
# --edge-only` (Edge members and this machine, no Towers) for sensitive work. It is the strict
# subset that needs no trust in the LAN." FOUNDER RULING 2026-10-06: "spec and build". Towers are
# "trust the LAN" (plain http, unverified); Edge members are verified by certificate and this
# machine is this machine, so edge-only needs no trust in anything else on the network.
#
# THE ONE RULE: edge-only is decided once at start, from the same precedence as LAN mode (flag,
# then environment, then config), and every surface reads that one answer. Under edge-only a
# Tower is absent everywhere: not on the dial, not as a candidate, not in the ladder, not as a
# share target.
#
# GROUND TRUTH: internal/lanmode (Decide, Mode, Source), cmd/rogerai/main.go (flag parsing,
# config, `roger status`, `roger lan`), internal/client/lan_relay.go (relayLAN rungs,
# LANTowers), client.LANDiscover, internal/tui (header badge, dial), internal/webui (top bar),
# internal/agent StartLAN (share targets).
#
# Tags: @cli, @client, @tui, @console.

Feature: `roger --lan --edge-only` uses only Edge members and this machine, never a Tower

  # =========================================================================
  # 1. TURNING IT ON, AND PRECEDENCE
  # =========================================================================

  @cli
  Scenario: --edge-only implies LAN mode
    When they run "roger --edge-only use qwen3-8b"
    Then the command runs in LAN mode, edge-only
    # RULED 2026-10-07 (founder approved the recommendation): --edge-only implies --lan rather than being an error without it. Recommend
    # implies: the flag has no meaning outside LAN mode, and requiring both adds a way to get
    # it wrong. `roger status` still reports both.

  @cli
  Scenario Outline: edge-only is set by flag, environment or config
    Given <setup>
    When they run "roger use qwen3-8b"
    Then the command runs in LAN mode, edge-only, from source "<source>"

    Examples:
      | setup                                            | source |
      | the command line carries --edge-only             | flag   |
      | ROGER_LAN_EDGE_ONLY is set to "1"                | env    |
      | config "mode" is "lan-edge-only"                 | config |
    # RULED 2026-10-07 (founder approved the recommendation): config value "lan-edge-only" (one key, three values: default, lan,
    # lan-edge-only) rather than a second key. Recommend yes: one key cannot contradict itself.

  @cli
  Scenario: the flag beats the environment, the environment beats config
    Given config "mode" is "lan-edge-only"
    And ROGER_LAN_EDGE_ONLY is set to "0"
    When they run "roger use qwen3-8b"
    Then the command runs in LAN mode, not edge-only, from source "env"

  @cli
  Scenario: --lan on the command line turns edge-only off for that command
    Given config "mode" is "lan-edge-only"
    When they run "roger --lan use qwen3-8b"
    Then the command runs in LAN mode, not edge-only, from source "flag"

  @cli
  Scenario: --no-lan turns both off
    Given config "mode" is "lan-edge-only"
    When they run "roger --no-lan use qwen3-8b"
    Then the command runs in the default mode

  @cli
  Scenario: --edge-only with --no-lan is a conflict
    When they run "roger --edge-only --no-lan use qwen3-8b"
    Then it exits non-zero saying --edge-only and --no-lan conflict

  @cli
  Scenario Outline: an unrecognised ROGER_LAN_EDGE_ONLY value is an error, never a guess
    Given ROGER_LAN_EDGE_ONLY is set to "<value>"
    When they run "roger status"
    Then it exits non-zero naming ROGER_LAN_EDGE_ONLY and the accepted values

    Examples:
      | value  |
      | maybe  |
      | 2      |

  @cli
  Scenario: `roger config set mode lan-edge-only` makes it sticky, and `default` clears it
    When they run "roger config set mode lan-edge-only"
    And then "roger status"
    Then it reports LAN mode, edge-only, from config
    When they run "roger config set mode default"
    Then the next "roger status" reports the default mode

  @cli
  Scenario: `roger status` reports edge-only and what that excludes
    Given edge-only is on
    When they run "roger status"
    Then it reports "LAN mode: on (edge-only)" and its source
    And it says LAN Towers are not used
    And `roger status --json` carries "lan": true and "edge_only": true

  # =========================================================================
  # 2. TOWERS ARE ABSENT EVERYWHERE
  # =========================================================================

  @client
  Scenario: a configured LAN Tower is never dialed under edge-only
    Given edge-only is on
    And the LAN Tower at 192.168.1.20:8787 is in lan_towers and serves "qwen3-8b"
    And nothing else local serves "qwen3-8b"
    When a turn is dispatched for "qwen3-8b"
    Then no request of any kind was made to 192.168.1.20:8787
    And it is refused, saying edge-only is on and LAN Towers are not used

  @client
  Scenario: the broker setting pointing at a local Tower is not used under edge-only
    Given edge-only is on
    And the broker setting is a standalone Tower at 192.168.1.20:8787
    When the LAN dial is read
    Then no request was made to 192.168.1.20:8787

  @client
  Scenario: an advertised Tower is not even offered as a candidate under edge-only
    Given edge-only is on
    And a Tower at 192.168.1.30:8787 advertises itself on the LAN
    When the LAN dial is read
    Then no candidate line for 192.168.1.30:8787 is shown

  @client
  Scenario: the dial under edge-only shows Edge members and this machine only
    Given edge-only is on
    And this machine serves "qwen3-8b", the Edge member "jetson" serves "llama-3.3-70b"
    And the LAN Tower at 192.168.1.20:8787 serves "gpt-oss-20b"
    When the LAN dial is read
    Then it lists "qwen3-8b" from this machine and "llama-3.3-70b" from "jetson"
    And it does not list "gpt-oss-20b"

  @cli
  Scenario: `roger use <tower-address>` is refused under edge-only
    Given edge-only is on
    When they run "roger use 192.168.1.20:8787 qwen3-8b"
    Then it is refused, saying edge-only is on and Towers are not used
    And no request was made to 192.168.1.20:8787

  @cli
  Scenario: `roger lan add` still works under edge-only, and says the Tower will not be used now
    Given edge-only is on
    When they run "roger lan add 192.168.1.20:8787"
    Then the Tower is added to lan_towers
    And the output says it will be used in LAN mode but not while edge-only is on

  @cli
  Scenario: `roger lan list` under edge-only marks every Tower as not in use
    Given edge-only is on and lan_towers has 192.168.1.20:8787
    When they run "roger lan list"
    Then 192.168.1.20:8787 is listed as "not used (edge-only)"
    And no request was made to it

  @client
  Scenario Outline: every front end honors edge-only
    Given edge-only is on and the only source of "qwen3-8b" is a LAN Tower
    When a turn for "qwen3-8b" is sent from "<path>"
    Then it is refused and the Tower was never contacted

    Examples:
      | path              |
      | roger use         |
      | the TUI tune      |
      | TUI chat          |
      | the agent         |
      | a guest operator  |
      | console chat      |
      | roger say         |

  # =========================================================================
  # 3. SHARE UNDER EDGE-ONLY
  # =========================================================================

  @cli
  Scenario: share under edge-only serves Edge members only
    Given edge-only is on and lan_towers has 192.168.1.20:8787
    When they run "roger share"
    Then it serves this Edge's members
    And it never polls 192.168.1.20:8787
    And it says it is serving Edge members only because edge-only is on

  @tui @console
  Scenario: TUI and console SHARE under edge-only serve Edge members only
    Given edge-only is on and lan_towers has 192.168.1.20:8787
    When SHARE goes on air from the TUI or the console
    Then it reads "on air: LAN · edge" with no Tower named
    And 192.168.1.20:8787 is never polled

  # =========================================================================
  # 4. VISIBILITY
  # =========================================================================

  @tui
  Scenario: the TUI header badge reads LAN EDGE-ONLY
    Given edge-only is on
    When the TUI opens
    Then its header carries "LAN EDGE-ONLY" in place of the balance tag
    And the badge is drawn in the same ink as the LAN badge, never the brand red
    And it reads correctly with NO_COLOR

  @tui
  Scenario: the TUI dial title reads LAN EDGE-ONLY DIAL
    Given edge-only is on
    When the dial is shown
    Then its title reads "LAN EDGE-ONLY DIAL"

  @console
  Scenario: the console top bar shows LAN EDGE-ONLY
    Given "roger --edge-only webui" is running
    When the console loads
    Then its top bar shows "LAN EDGE-ONLY"
    And the state snapshot carries "lan": true and "edge_only": true

  @cli
  Scenario: the one-line LAN note names edge-only
    Given edge-only is on
    When they run "roger use qwen3-8b"
    Then stderr names LAN mode, edge-only, once

  @cli
  Scenario: the plain-http trust note is not shown under edge-only
    Given edge-only is on and the trust note has never been shown
    When they run "roger use qwen3-8b"
    Then the plain-http Tower trust note is not shown
    And it is still shown the first time a turn goes to a LAN Tower with edge-only off

  # =========================================================================
  # 5. ADVERSARIAL
  # =========================================================================

  @client
  Scenario: an Edge member answering from a Tower's address is still only used as a member
    Given edge-only is on
    And the enrolled Edge member "jetson" lives at 192.168.1.20
    And a standalone Tower also listens at 192.168.1.20:8787
    When the LAN dial is read
    Then "jetson" is reached over mutual TLS on its Edge face
    And no request was made to 192.168.1.20:8787

  @client
  Scenario: an Edge advertisement that fails verification is not used under edge-only
    Given edge-only is on
    And a device advertises itself as the Edge member "jetson" without a valid certificate
    When the LAN dial is read
    Then the advertisement is refused for its typed reason
    And nothing is dialed on its behalf

  @client
  Scenario: edge-only never falls back to a Tower, even when every Edge source fails
    Given edge-only is on
    And the Edge member "jetson" and a LAN Tower both serve "qwen3-8b"
    And "jetson" fails
    When a turn for "qwen3-8b" is dispatched
    Then it is refused, naming "jetson" and how it failed
    And the Tower was never contacted
    And no request was made to the public broker

  # founder ruling 2026-10-07 (edge-only covers streaming): edge-only's rule holds for a
  # streamed request too; a LAN Tower that can stream is still never asked.
  @client
  Scenario: a streamed turn under edge-only never reaches a Tower
    Given edge-only is on
    And the Edge member "jetson" and a LAN Tower both serve "qwen3-8b"
    And "jetson" fails
    When a streamed turn for "qwen3-8b" is dispatched
    Then it is refused, naming "jetson" and how it failed
    And the Tower was never contacted
    And no request was made to the public broker
