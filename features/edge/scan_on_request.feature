# STATUS: APPROVED 2026-10-07 (founder ruling)
#
# ROGER EDGE - A MACHINE OUTSIDE AN EDGE SCANS ONLY ON REQUEST.
#
# WHY THIS EXISTS
#
# Broadcast 014 (web/src/broadcasts-sharing-your-gpu-is-safe.html) promises that sharing a model
# opens no inbound port: "Your machine never listens. It only ever asks." Discovery browses the
# LAN over mDNS, which binds a UDP socket on 224.0.0.251:5353 to hear replies. Until this ruling a
# fresh install browsed in the background whenever the TUI, `roger resume` or `roger webui` ran,
# even on a machine that had never set up an Edge.
#
# FOUNDER RULING 2026-10-07: a machine that has NOT joined an Edge (no member certificate) and is
# not an Edge's authority (no root held) scans the LAN only on request:
#   - while the owner is on the [3] EDGE screen,
#   - while the console's Edge view is open (it polls; each read is a short lease),
#   - for the run of `roger edge scan` (and the setup that runs through those surfaces).
# The scan stops when they leave. It never browses in the background. LAN mode's candidate
# discovery (Towers advertised with `roger tower serve --advertise`) follows the same rule, with
# the LAN dial open (or a LAN command run) as the request. A machine that IS in an Edge, or is
# its authority, keeps its current behavior: it browses and advertises as before.
#
# ONE DECISION. cmd/rogerai/edgescan.go edgeMayBrowse is the only place the question "may this
# process browse now" is answered; the Edge host and the LAN dial both ask it.
#
# "BINDS NO SOCKET" IS MEASURED, NOT ASSUMED: the steps read the test process's own open sockets
# (Linux: /proc/self/fd joined to /proc/net/{tcp,tcp6,udp,udp6}) before and after, and the
# mutation check reverts the gate and watches the step fail.
#
# Tags: @cli runs in cmd/rogerai (the real Edge host, the real LAN dial, real sockets); @tui in
# internal/tui (the real model's screen changes driving the hook); @console in internal/webui.

Feature: A machine that has not joined an Edge scans the LAN only while someone asked it to

  Background:
    Given a fresh install that has not joined an Edge and holds no Edge root

  # =========================================================================
  # 1. NOTHING IN THE BACKGROUND
  # =========================================================================

  @cli
  Scenario: a fresh install with the TUI open on another screen binds no socket at all
    Given the TUI is open on the TUNE IN screen
    When the Edge host has run several passes
    Then this process has bound no new socket, TCP or UDP
    And in particular nothing is bound on UDP port 5353

  @cli
  Scenario: the console open on another view binds no socket either
    Given the web console is open and nobody is on its Edge view
    When the Edge host has run several passes
    Then this process has bound no new socket, TCP or UDP

  # =========================================================================
  # 2. ON REQUEST, AND ONLY WHILE
  # =========================================================================

  @cli
  Scenario: opening [3] starts the scan and leaving it stops it
    Given the TUI is open on the TUNE IN screen
    When the owner opens the [3] EDGE screen
    Then this process browses the LAN on UDP port 5353
    When the owner leaves the [3] EDGE screen
    Then within a few seconds nothing is bound on UDP port 5353

  @tui
  Scenario: the TUI asks for a scan exactly while the [3] EDGE screen is showing
    Given the TUI is running with a scan hook
    When the owner presses 3
    Then the scan was asked for once
    When the owner presses 1
    Then the scan request was ended

  @cli
  Scenario: the console Edge view scans while it is open and stops after it closes
    Given the web console is open
    When the console's Edge view reads the Edge data
    Then this process browses the LAN on UDP port 5353
    When the console's Edge view stops reading
    Then within a few seconds nothing is bound on UDP port 5353

  @console
  Scenario: each read of the console's Edge view renews the scan request
    Given a console wired to a scan lease
    When the Edge data is read
    Then the scan lease was renewed

  @cli
  Scenario: roger edge scan scans for its run and leaves nothing bound
    When the owner runs "roger edge scan"
    Then it browsed the LAN during the run
    And after it exits nothing is bound on UDP port 5353

  # =========================================================================
  # 3. WHAT THE SCREEN SAYS
  # =========================================================================

  # superseded 2026-10-11 by founder ruling (welcome card / simple empty Edge)
  # now covered by features/edge/empty_edge_simple.feature section 2 (tagged @superseded so the old runner skips it)
  @cli @superseded
  Scenario: the DISCOVERY fact says the scan runs only while the view is open
    When the owner opens the [3] EDGE screen
    Then the scan line reads "scanning this network while this screen is open (every 30s)" with the nearby count under it

  # =========================================================================
  # 4. LAN MODE FOLLOWS THE SAME RULE
  # =========================================================================

  @cli
  Scenario: the LAN dial refreshed in the background does not browse for advertised Towers
    Given LAN mode is on
    And no LAN view is open
    When the LAN dial refreshes
    Then no browse for advertised Towers was made

  @cli
  Scenario: the LAN dial open on request browses for advertised Towers
    Given LAN mode is on
    And the LAN dial is open
    When the LAN dial refreshes
    Then one browse for advertised Towers was made

  @cli
  Scenario: a LAN command is its own request
    Given LAN mode is on
    When the owner runs "roger --lan search"
    Then one browse for advertised Towers was made

  # =========================================================================
  # 5. A MACHINE IN AN EDGE IS UNCHANGED
  # =========================================================================

  @cli
  Scenario: an enrolled machine keeps browsing in the background
    Given this machine has joined an Edge
    And the TUI is open on the TUNE IN screen
    When the Edge host has run several passes
    Then this process browses the LAN on UDP port 5353

  @cli
  Scenario: an enrolled machine's LAN dial still browses for advertised Towers in the background
    Given this machine has joined an Edge
    And LAN mode is on
    And no LAN view is open
    When the LAN dial refreshes
    Then one browse for advertised Towers was made

  @cli
  Scenario: discovery switched off stays off even on request
    Given ROGERAI_EDGE_DISCOVERY is "0"
    When the owner opens the [3] EDGE screen
    Then nothing is bound on UDP port 5353
