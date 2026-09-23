# ROGER EDGE - FLEET HEALTH: telemetry and alerts, the private way.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-21, from a real r/raspberryDIY thread ("What do you use to monitor
# multiple Raspberry Pis?"). People run a handful of Pis and want ONE simple, honest answer to
# "are my Pis healthy?" without standing up Prometheus + Grafana + Ansible, or Nagios, or Zabbix,
# or a scripts -> MQTT -> pushover chain. They asked for exactly five things:
#   - a one-command install on each Pi,
#   - CPU temperature, RAM, disk space, uptime, and SD-card health,
#   - a notification (Discord / Telegram / email) when a device goes offline or looks wrong,
#   - open-source and self-hosted,
#   - no full monitoring stack to tend.
# One commenter named the real fear: exception-only alerting can DIE and then never tell you
# anything. The answer to that fear is a POSITIVE-PRESENCE model, which the Edge already has.
#
# WHAT THIS IS
#
# Roger Edge is already the private device fabric: nodes discovered on the LAN, enrolled into one
# owner-scoped fleet under one certificate root, present-or-DARK by heartbeat, described with real
# capabilities, all able to form with NO internet. This spec adds the missing layer on top of that
# plumbing - health TELEMETRY and threshold ALERTS - and nothing else. It reuses:
#   - the node/instance report (features/edge/instances.feature, internal/edge describe) to carry
#     metrics, so a member that already runs `roger` reports its health with no extra agent (the
#     one-command install the thread wanted is "already a member"),
#   - the heartbeat/presence system (features/edge/topology_view.feature: a node that stops being
#     heard goes DARK and is KEPT) to carry OFFLINE,
#   - the signed per-node receipt model (protocol.UsageReceipt / VerifyNode) so an alert names a
#     node authentically and cannot be forged by a peer.
#
# THE DEAD-MAN'S-SWITCH, SOLVED BY DESIGN. Offline is judged by the AUTHORITY (the always-on
# machine the owner designated, or Core), not by the node that died. So a Pi that loses power
# still produces its "offline" alert - the authority notices the missing heartbeat and fires it.
# Exception-only silence is impossible for offline: presence is watched continuously, from a
# machine that is by definition still up.
#
# HONESTY (the product's rule). No fabricated metrics, ever. A node that does not report a metric
# shows NOTHING for it - never a 0, never a guess. A stale reading is labelled with its age. An
# alert says which node, which metric, the real value and the threshold it crossed.
#
# PRIVACY. Metrics never leave the Edge unless the owner configures an external sink. On an
# airgapped Edge the whole thing works with no internet: the fleet view IS the dashboard, and a
# LAN sink (a webhook to a box on the same network) needs no cloud. External sinks (Discord,
# Telegram, SMTP) are the owner's explicit choice; their tokens live in config/env and are NEVER
# printed, logged, or put in a receipt.
#
# GROUND TRUTH: internal/edge/instance.go (the report a node publishes), internal/edge/describe
# and server.go (the /edge/describe face), internal/edge/status.go (SelfStatus), internal/edge/
# fleet + store.EdgeNode (presence, last_seen), internal/protocol (UsageReceipt, VerifyNode),
# cmd/rogerai/edge.go (the CLI), features/edge/instances.feature + topology_view.feature + mode.
#
# Tags: @edge runs in internal/edge (the report shapes, the threshold engine, the presence->offline
# rule); @cli in cmd/rogerai (the `roger edge health` / `roger edge alert` commands over an
# isolated config dir); @alert covers the sink delivery (a fake sink records what would be sent -
# no real Discord/Telegram/SMTP is contacted in a test).

Feature: A fleet's health is visible and alerts fire on real thresholds, privately, with offline judged by the always-on authority so a dead node still reports

  # =========================================================================
  # 1. WHAT A NODE REPORTS
  # =========================================================================

  @edge
  Scenario: a member reports the health facts the thread asked for
    Given a member node running roger on a Raspberry Pi
    When it publishes its health report
    Then the report carries CPU temperature, CPU load, memory used and total, and uptime
    And it carries each mounted disk's used and total
    And it carries SD-card wear where the platform exposes it
    And every value is a real reading, with the moment it was taken

  @edge
  Scenario: a metric the platform does not expose is absent, never zero
    Given a member whose board exposes no CPU-temperature sensor
    When it publishes its health report
    Then the report has no CPU-temperature field at all
    And nothing downstream shows 0 degrees for it

  @edge
  Scenario: health rides the same report as capabilities, so no new agent is needed
    Given a member that already describes its capabilities to the fleet
    When it adds health
    Then health travels on the same describe/heartbeat it already sends
    And a fleet reader needs no second connection to see it

  @edge
  Scenario: a stale reading is kept but labelled with its age
    Given a member last heard from 90 seconds ago
    When the fleet reads its health
    Then the last-known readings are shown
    And they are marked as 90 seconds old, never presented as current

  # =========================================================================
  # 2. THE FLEET READS HEALTH (feeds the patch bay bars and the console)
  # =========================================================================

  @edge
  Scenario: the fleet exposes each node's health for the surfaces to draw
    Given a fleet of three members reporting health
    When the fleet health is read once
    Then each node carries its latest readings and their age
    And a node that reports nothing carries no readings, not zeros
    And the read is a single snapshot, like every other Edge read

  @edge
  Scenario: a resource is a level with a real denominator or it is not shown
    Given a node reporting 8 GB of 32 GB memory used
    When the fleet health is read
    Then memory is a level of a real total, drawable as a bar
    Given a node reporting a memory figure with no total
    When the fleet health is read
    Then memory is shown as a plain figure, not a bar, because a bar would imply a denominator it does not have

  # =========================================================================
  # 3. THRESHOLDS - the owner's rules, simple by default
  # =========================================================================

  @edge
  Scenario: sensible default thresholds exist so it works out of the box
    Given an owner who set no rules
    Then a default rule warns on CPU temperature over a hot threshold
    And a default rule warns on any disk over nearly full
    And a default rule warns when a node has been offline past the liveness window
    And the defaults are documented and can be turned off

  @edge
  Scenario: a rule is a metric, a comparison and a value, per node or fleet-wide
    Given the owner sets a rule that CPU temperature over 80 degrees is a warning
    And a rule that disk over 90 percent is a warning, on every node
    And a rule for one node "nas" that disk over 75 percent is a warning
    Then the node "nas" uses the stricter 75 percent rule
    And every other node uses the fleet 90 percent rule

  @edge
  Scenario: a rule fires when crossed and clears when it recovers, once each
    Given a rule that CPU temperature over 80 degrees is a warning
    When "pi" reports 82 degrees
    Then the rule is firing for "pi"
    When "pi" reports 78 degrees
    Then the rule has cleared for "pi"
    And each crossing produced exactly one state change, never a stream while it stays over

  @edge
  Scenario: a flapping metric does not spam, it holds until it settles
    Given a rule with a small recovery margin
    When a metric wobbles across the threshold repeatedly within seconds
    Then it does not fire and clear on every wobble
    And it settles to one state after the wobble stops

  # =========================================================================
  # 4. OFFLINE - the dead-man's-switch, judged by the authority
  # =========================================================================

  @edge
  Scenario: offline is decided by the always-on authority, not the node that died
    Given an authority that is up and a member "pi" that stops sending heartbeats
    When the liveness window passes with no heartbeat from "pi"
    Then the authority marks "pi" offline
    And it does so without any message from "pi", because a dead node cannot send one

  @edge
  Scenario: coming back online clears the offline state once
    Given "pi" is marked offline
    When "pi" is heard from again
    Then it is marked online
    And the recovery is one state change, not a repeat of the whole history

  @edge
  Scenario: an offline alert names how long it has been dark
    Given "pi" went dark and the alert is prepared
    Then the alert says "pi" is offline and for how long
    And it names the last time "pi" was heard from

  # =========================================================================
  # 5. ALERTS AND SINKS - private by default, external only on purpose
  # =========================================================================

  @alert
  Scenario: an alert carries the node, the metric, the real value and the threshold
    Given a firing CPU-temperature rule for "pi" at 82 degrees against 80
    When the alert is formed
    Then it names "pi", the metric, 82 degrees, and the 80 threshold it crossed
    And it says whether it is firing or clearing

  @alert
  Scenario: with no sink configured, alerts are visible in the fleet, sent nowhere
    Given no external sink is configured
    When a rule fires
    Then the alert shows on the fleet surfaces and in the alert log
    And nothing is sent off the machine

  @alert
  Scenario Outline: a configured sink receives the alert, and only a configured one
    Given a <sink> sink is configured
    When a rule fires
    Then the alert is delivered to the <sink> sink
    And no other sink is contacted

    Examples:
      | sink     |
      | discord  |
      | telegram |
      | email    |
      | webhook  |

  @alert
  Scenario: a sink's secret is never printed, logged, or put in a receipt
    Given a Discord webhook configured with a secret URL
    When an alert is delivered and the delivery is logged
    Then the log names the sink and the outcome
    And the secret URL appears nowhere in the log, the output, or any receipt

  @alert
  Scenario: a sink that fails to deliver is retried and never loses the alert silently
    Given a webhook sink that is unreachable
    When a rule fires
    Then delivery is retried with a bounded backoff
    And the alert stays in the log as undelivered until it succeeds or the owner is told it could not be sent

  @alert
  Scenario: a LAN webhook needs no internet, so an airgapped Edge still alerts
    Given an Edge with no internet and a webhook sink on the same LAN
    When a rule fires
    Then the alert reaches the LAN sink
    And no external network was used

  @alert
  Scenario: the owner can send a test alert to prove a sink works
    Given a configured sink
    When the owner sends a test alert
    Then a clearly-marked test message reaches the sink
    And it changes no rule state and raises no real alarm

  # =========================================================================
  # 6. THE CLI
  # =========================================================================

  @cli
  Scenario: roger edge health shows the fleet's health at a glance
    When they run "roger edge health"
    Then it lists each node with its temperature, memory, disk, uptime and presence
    And a node reporting a metric shows it, and one that does not shows a dash, never a zero
    And an offline node is marked offline with how long

  @cli
  Scenario: roger edge health <node> shows one node in full, with reading ages
    Given a member "jetson" reporting health
    When they run "roger edge health jetson"
    Then it shows every metric "jetson" reports, each with how old the reading is
    And it shows which rules are firing for it, if any

  @cli
  Scenario: roger edge alert lists the rules and their current state
    When they run "roger edge alert"
    Then it lists every rule, its scope, and whether it is firing
    And it names where alerts are being sent, without printing any secret

  @cli
  Scenario: roger edge alert set adds a rule, and it takes effect on the next read
    When they run "roger edge alert set cpu_temp gt 80 --scope fleet"
    Then the rule is stored
    And the next health read evaluates it

  @cli
  Scenario: roger edge alert test sends a test to a sink by name
    Given a configured "discord" sink
    When they run "roger edge alert test discord"
    Then a test alert is delivered to that sink
    And the command says it was sent, without printing the sink's secret

  @cli
  Scenario: health honours --json for scripts, with the same honesty
    When they run "roger edge health --json"
    Then it is valid JSON of the fleet's health
    And an absent metric is absent from the JSON, not present as zero

  # =========================================================================
  # 7. THE RASPBERRY PI SPECIFICS THE THREAD NAMED
  # =========================================================================

  @edge
  Scenario: SD-card wear is reported where the Pi exposes it, and omitted where it does not
    Given a Pi whose kernel exposes SD-card lifetime
    When it reports health
    Then SD-card wear is carried as a real percentage
    Given a Pi whose kernel exposes no such figure
    When it reports health
    Then SD-card wear is absent, and a default SD rule simply does not apply to it

  @edge
  Scenario: a throttled or undervolted Pi is surfaced, because it is the classic Pi failure
    Given a Pi reporting an undervoltage or throttling flag
    When the fleet reads its health
    Then the flag is shown on the node
    And a default rule can warn on sustained throttling
