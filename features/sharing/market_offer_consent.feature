# STATUS: APPROVED 2026-10-11 (founder ruling: confirm before offering on the market)
# BUILD STATUS: BUILT (TestShareMarketConsentBDD)
#
# `roger share` with no model named auto-detects a local server and used to put that server's
# first model on the public market without asking. That model can be a local proxy to the
# operator's own paid account, which would let anyone on the market spend that account's quota.
#
# founder ruling 2026-10-11: confirm before offering on the market. Auto-detection stays, but
# nothing auto-picked goes on the public market without a yes. A named model (or the one the
# setup wizard saved) needs no prompt. LAN mode, a standalone Tower as the broker, and a private
# band never reach the public market, so they are not asked.
#
# Ground truth: cmd/rogerai/main.go cmdShare (the auto-detect pick and the go-live through
# agentStart), cmd/rogerai/marketconsent.go (the confirmation), cmd/rogerai/stationoffer.go
# (what counts as an account proxy and as roger-served).

Feature: Nothing auto-detected goes on the public market without a yes

  # founder ruling 2026-10-11: confirm before offering on the market
  Scenario: An auto-picked model is shown and needs a yes
    Given a local server on this machine has "qwen3-8b" on air
    When the operator runs "roger share" at a terminal and answers "y"
    Then roger showed "qwen3-8b" and the server it comes from before asking
    And it asked "put it on the public market? [y/N]"
    And "qwen3-8b" went on air

  # founder ruling 2026-10-11: confirm before offering on the market
  Scenario: Enter means no, and nothing goes on air
    Given a local server on this machine has "qwen3-8b" on air
    When the operator runs "roger share" at a terminal and answers ""
    Then nothing went on air
    And roger said "nothing went on the market"

  # founder ruling 2026-10-11: confirm before offering on the market
  Scenario: An account proxy is named as one, and no stays the default
    Given a local proxy to hosted accounts on this machine has "claude" on air
    When the operator runs "roger share" at a terminal and answers ""
    Then roger said "this is a proxy to your claude account: anyone on the market would use your quota"
    And nothing went on air

  # founder ruling 2026-10-11: confirm before offering on the market
  Scenario: A roger-served model is labeled as one
    Given roger serves "smollm2-135m-instruct" on this machine
    When the operator runs "roger share" at a terminal and answers "y"
    Then roger said "served by roger on this machine"
    And "smollm2-135m-instruct" went on air

  # founder ruling 2026-10-11: confirm before offering on the market
  Scenario: Without a terminal, nothing auto-detected goes on the market
    Given a local server on this machine has "qwen3-8b" on air
    When the operator runs "roger share" with no terminal
    Then nothing went on air
    And roger said "name the model to share it: roger share <model>"

  # founder ruling 2026-10-11: confirm before offering on the market
  Scenario: A named model goes on air with no prompt
    Given a local server on this machine has "qwen3-8b" on air
    When the operator runs "roger share qwen3-8b" with no terminal
    Then "qwen3-8b" went on air
    And roger did not ask anything

  # founder ruling 2026-10-11: confirm before offering on the market
  Scenario: The model the setup wizard saved counts as named
    Given a local server on this machine has "qwen3-8b" on air
    And the setup wizard saved "qwen3-8b" as the model to share
    When the operator runs "roger share" with no terminal
    Then "qwen3-8b" went on air
    And roger did not ask anything

  # founder ruling 2026-10-11: confirm before offering on the market
  Scenario: A private band is not the public market and is not asked
    Given a local server on this machine has "qwen3-8b" on air
    When the operator runs "roger share --private" with no terminal
    Then roger did not ask anything
