# ROGER EDGE - SESSION ATTRIBUTION FOR `roger use` AND GUEST OPERATORS.
#
# WHY THIS EXISTS
#
# features/edge/sessions.feature (approved) says every consumer opens a session of the same
# shape: `roger use`, the TUI's own agent, a guest operator, the web console, a board. Two of
# those were wired (the TUI's turns, the console's chat) and two were deliberately left for
# their own spec: `roger use` and the guest operators. Both are served by the SAME local proxy
# (internal/client), which already forwards the broker's receipt header and discards it. The
# seam is one callback wide; what needed deciding was the ATTRIBUTION - who the caller was -
# and how a session opened in one process reaches the Edge screen in another.
#
# THE RULINGS THIS SPEC MAKES
#
# 1. The proxy hands each relayed response's receipt to ONE optional callback. It never blocks
#    the reply, never invents a receipt, and hands back a refusal's void receipt as-is, so a
#    refused turn is drawn REFUSED and never as served.
#
# 2. Attribution is decided by the surface that owns the proxy, never guessed by the proxy:
#    - the TUI's endpoint attributes a turn to the GUEST'S NAME while a guest holds the mic
#      (from the exec until the return to the desk), and to `roger use` otherwise, because an
#      external program pointed at the TUI's endpoint is exactly what `roger use` serves;
#    - the `roger use` process attributes every turn to `roger use`.
#    The attribution is what the session says; the receipt is still what it is derived from.
#
# 3. A session opened in one process reaches every Edge view on the machine through a MIRROR:
#    each process that records sessions writes its own live list to one private file under the
#    config dir (edge-sessions/<pid>.json); a viewer merges its siblings of the SAME account.
#    Nothing is shared in memory, nothing is locked, nobody writes another process's file. A
#    mirror carries what the view draws and never the receipt bodies. Mirrored sessions fade on
#    the same 90-second life, a corrupt file is ignored, and a dead process's file is removed
#    by the first reader that finds every session in it faded.
#
# GROUND TRUTH: internal/client (relayWithFailover, copyRelayResponse, ProxyOptions,
# ProxyOptionsHolder, Use), internal/tui/operator.go (onOperatorExec / onOperatorDone - the
# mic), internal/tui/tui.go (liveProxyOpts), internal/edge/session.go (Sessions, Traffic,
# SessionLife), cmd/rogerai/main.go (cmdUse), features/edge/sessions.feature.
#
# Tags: @client runs in internal/client (a real proxy over a stub broker), @edge in
# internal/edge (two real ledgers over one temp dir), @tui in internal/tui (the real model,
# the real proxy options), @cli in cmd/rogerai (the real `roger use` wiring).

Feature: Every turn through the local proxy is a session, attributed to whoever really opened it, visible from every Edge view on the machine

  # =========================================================================
  # 1. THE PROXY HANDS BACK THE RECEIPT
  # =========================================================================

  @client
  Scenario: a relayed turn hands its receipt to the callback, once
    Given a local proxy with a receipt callback over a broker that receipts every turn
    When a program relays one turn through the proxy
    Then the callback receives exactly one receipt
    And it names the request id, the band and the station the broker named
    And the reply reached the program unchanged

  @client
  Scenario: a response with no receipt hands back nothing
    Given a local proxy with a receipt callback over a broker that sends no receipt header
    When a program relays one turn through the proxy
    Then the callback is not called
    And the reply still reached the program

  @client
  Scenario: a refusal's void receipt is handed back as a refusal
    Given a local proxy with a receipt callback over a broker that refuses with a voided receipt
    When a program relays one turn through the proxy
    Then the callback receives the voided receipt with its reason
    And the program saw the refusal status

  @client
  Scenario: a proxy with no callback relays exactly as before
    Given a local proxy with no receipt callback
    When a program relays one turn through the proxy
    Then the reply reached the program unchanged

  @client
  Scenario: `roger use` passes the receipt callback into the endpoint it opens
    Given `roger use` opened with a receipt callback, confirmed
    When a program relays one turn through that endpoint
    Then the callback receives the receipt

  # =========================================================================
  # 2. WHO OPENED IT
  # =========================================================================

  @tui
  Scenario: a turn through the TUI's endpoint with no guest is a `roger use` session
    Given a TUI with a tuned channel and no guest
    When the endpoint relays a turn that returns a receipt
    Then the Edge records a session attributed to "roger use"
    And it names the band and the station from the receipt

  @tui
  Scenario: a turn while a guest holds the mic is the guest's session
    Given a TUI with a tuned channel and the guest "opencode" patched through
    When the endpoint relays a turn that returns a receipt
    Then the Edge records a session attributed to "opencode"

  @tui
  Scenario: when the guest returns, the endpoint's turns are `roger use` again
    Given a TUI with a tuned channel and the guest "opencode" patched through
    And the guest has returned to the desk
    When the endpoint relays a turn that returns a receipt
    Then the Edge records a session attributed to "roger use"

  @tui
  Scenario: a re-tune keeps the attribution wired
    Given a TUI with a tuned channel and no guest
    And the channel is re-tuned to another band
    When the endpoint relays a turn that returns a receipt
    Then the Edge records a session attributed to "roger use"
    And it names the new band

  @tui
  Scenario: a TUI with no session ledger wired records nothing and relays fine
    Given a TUI with a tuned channel and no Edge sessions wired
    When the endpoint relays a turn that returns a receipt
    Then no session is recorded
    And the reply reached the program

  @cli
  Scenario: `roger use` records its turns as `roger use` sessions on this machine's Edge
    Given the `roger use` recorder for this machine
    When it is handed a receipt for "gpt-oss-120b" served by "house-or-1"
    Then this machine's session mirror holds one session attributed to "roger use", band "gpt-oss-120b", station "house-or-1"

  @cli
  Scenario: a receipt with no request id records nothing
    Given the `roger use` recorder for this machine
    When it is handed a receipt with no request id
    Then this machine's session mirror holds no session

  # =========================================================================
  # 3. ONE MACHINE, MANY PROCESSES, ONE VIEW
  # =========================================================================

  @edge
  Scenario: a session recorded in one process is visible to another of the same account
    Given two session ledgers for account "acct-1" mirrored under one directory
    When the first records a receipted turn
    Then the second lists that session
    And the first lists it once, not twice

  @edge
  Scenario: another account's mirror is never merged
    Given a ledger for "acct-1" and a ledger for "acct-2" mirrored under one directory
    When the "acct-2" ledger records a receipted turn
    Then the "acct-1" ledger lists no session
    And nothing about it is inferable from the "acct-1" ledger

  @edge
  Scenario: a mirrored session fades on the same life as a local one
    Given two session ledgers for account "acct-1" mirrored under one directory
    When the first records a receipted turn
    And 91 seconds pass
    Then the second lists no session

  @edge
  Scenario: a mirror carries what the view draws, never the receipt bodies
    Given two session ledgers for account "acct-1" mirrored under one directory
    When the first records a receipted turn with prices and token counts
    Then its mirror file carries the session's attribution, band, station, outcome and time
    And the mirror file carries no receipt, no price and no token count

  @edge
  Scenario: a corrupt mirror file is ignored, and the view still draws
    Given two session ledgers for account "acct-1" mirrored under one directory
    And a corrupt file in the mirror directory
    When the first records a receipted turn
    Then the second lists that session
    And nothing panicked

  @edge
  Scenario: a viewer never writes another process's file
    Given two session ledgers for account "acct-1" mirrored under one directory
    When the first records a receipted turn
    And the second reads its sessions many times
    Then the first's mirror file is byte-for-byte unchanged
    And the second has written no file of its own, having recorded nothing

  @edge
  Scenario: a dead process's file is removed once everything in it has faded
    Given two session ledgers for account "acct-1" mirrored under one directory
    When the first records a receipted turn and its process is gone
    And 91 seconds pass
    And the second reads its sessions
    Then the first's mirror file is gone

  @edge
  Scenario: a mirror file that is still live is never removed by a reader
    Given two session ledgers for account "acct-1" mirrored under one directory
    When the first records a receipted turn and its process is gone
    And 30 seconds pass
    And the second reads its sessions
    Then the first's mirror file is still there

  @edge
  Scenario: the mirror is private to the owner
    Given two session ledgers for account "acct-1" mirrored under one directory
    When the first records a receipted turn
    Then the mirror directory is readable by the owner only
    And the mirror file is readable by the owner only

  @edge
  Scenario: a ledger with no mirror directory behaves exactly as before
    Given a session ledger with no mirror
    When it records a receipted turn
    Then it lists that session
    And no file was written anywhere

  @edge
  Scenario: a mirror that cannot be written never refuses the session
    Given a session ledger mirrored under a directory that cannot be written
    When it records a receipted turn
    Then it lists that session
    And the write failure is reported once, not on every turn
