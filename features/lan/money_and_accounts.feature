# STATUS: PROPOSED 2026-10-06 (awaiting founder approval)
#
# LAN MODE - MONEY AND ACCOUNTS: nothing is billed, nothing pretends to be.
#
# Every LAN source is free: a LAN Tower answers X-Roger-Cost: 0 and X-Roger-Local: 1 and keeps
# bookkeeping-only receipts (features/tower/standalone_consumer_plane.feature); an Edge turn
# debits nothing and its receipt never enters the ledger (features/edge/local_inference.feature).
# Today the client still prints est. cost, the balance and "a hold pre-auths your session"
# against a Tower, because it never reads X-Roger-Local. That text is a false statement about
# money; this file removes it in LAN mode.
#
# Not an internet lockdown (founder ruling 2026-10-06): an account still exists and its own
# commands (`roger balance`, top-up, login) keep working. LAN mode simply never needs them.
#
# GROUND TRUTH: internal/client/client.go (Use: balanceOf, the plate at :1545-1619, the default
# price cap header), internal/tui/view_connect.go (confirm plate), internal/localplane
# (completion.go: X-Roger-Cost, X-Roger-Local), protocol.UsageReceipt.Local.
#
# Tags: @client, @cli, @tui, @console.

Feature: LAN mode moves no money, shows no money it does not move, and needs no login

  Background:
    Given LAN mode is on

  @cli
  Scenario: `roger --lan use` makes no balance call and prints no money text
    Given the LAN Tower at 192.168.1.20:8787 serves "llama-3.3-70b"
    When they run "roger --lan use llama-3.3-70b"
    Then no request is made to /balance
    And the plate does not contain "est.", "balance" or "hold"
    And the plate reads "free" and "local"

  @tui
  Scenario: the TUI confirm plate in LAN mode has no price, balance or hold
    Given the TUI is open in LAN mode
    When the owner selects a local band to tune
    Then the confirm plate shows the band, its source and "free"
    And it shows no balance, no estimated cost and no hold text

  @client
  Scenario: no price cap header is sent to a LAN source
    When a turn is served by a LAN Tower
    Then the request carries no market price-cap header

  @client
  Scenario: a LAN Tower's answer is read as local and free
    Given the LAN Tower at 192.168.1.20:8787 answers with "X-Roger-Cost: 0" and "X-Roger-Local: 1"
    When a turn is served by it
    Then the turn is recorded as local and free
    And the caller's proxy response carries "X-RogerAI-Cost: 0"
    # Regression: the client read neither header.

  @client
  Scenario: a LAN Tower answer missing the local marker is not trusted as free
    Given a configured LAN address answers a completion without "X-Roger-Local: 1"
    When the turn completes
    Then the turn is recorded with the warning "this LAN source did not say it is local"
    And the address is marked on the dial as "not a standalone Tower"
    And no later turn is sent to it until the owner re-adds it
    # Catches a misconfigured box at a LAN address that forwards to a paid service. It is a
    # misconfiguration guard, not a security boundary: under trust-the-LAN a hostile box can
    # send the header (see trust_and_honesty.feature).

  @client
  Scenario: an Edge turn in LAN mode debits nothing, exactly as without LAN mode
    Given the Edge member "jetson" serves "gpt-oss-20b"
    When a turn is served by "jetson"
    Then no hold was placed and no ledger row was written
    And the session carries a local receipt signed by "jetson"

  @client
  Scenario: LAN receipts never reach the broker
    Given turns were served by this machine, an Edge member and a LAN Tower
    Then no receipt from any of them was sent to the public broker
    And none appears in /generation or the account's usage

  @cli
  Scenario: LAN mode works with no login at all
    Given this machine has never logged in
    When they run "roger --lan use qwen3-8b" and this machine serves "qwen3-8b"
    Then the proxy opens and the turn is served
    And no login prompt is shown

  @cli
  Scenario: a logged-in user in LAN mode keeps their account untouched
    Given this machine is logged in with a funded account
    When they run "roger --lan use qwen3-8b" and send 10 turns
    Then the account's balance is unchanged
    And no hold was placed

  @cli
  Scenario: account commands still work in LAN mode, and say LAN turns are free
    Given this machine is logged in
    When they run "roger --lan balance"
    Then it shows the network balance as usual
    And it adds one line saying turns in LAN mode are free and do not touch this balance

  @tui
  Scenario: the header shows LAN in the place of the balance tag
    Given the TUI is open in LAN mode and logged in
    When the header renders
    Then it shows the LAN badge where the balance tag would be
    And the balance is still reachable from the account section
    # DECISION: hide the balance from the header in LAN mode. Recommendation: yes, the header
    # should state what this session costs (nothing), and the balance is one keystroke away.

  @console
  Scenario: the console's cost line in LAN mode says nothing is billed
    Given "roger --lan webui" is running
    When the console renders
    Then it does not say "settles on the broker"
    And it says "local, nothing billed"

  @client
  Scenario: spend limits never refuse a LAN turn
    Given the account's monthly cap is fully spent
    When a turn is served by a LAN source
    Then it is not refused for the cap
    # Caps bound market spend; LAN turns spend nothing.
