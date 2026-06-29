# ONBOARDING: first-run setup and the earn-path account hub. A new user can use rogerai with
# zero config (sensible defaults), is gently onboarded on first run, and — only when they want to
# EARN — links a GitHub account and completes Stripe Connect (KYC) so payouts can flow. Consuming
# and free sharing never require any of this.
#
# GROUND TRUTH:
#   cmd/rogerai/onboard.go: maybeOnboard(cfg) runs the first-run flow; cmdOnboard re-runs setup.
#   cmd/rogerai/main.go: payoutOnboard wires the operator into the payout/Connect flow.
#   cmd/rogerai-broker/payouts.go: connectOnboard(...) starts/links Stripe Connect for an account.
#
# Enforced by: cmd/rogerai onboard tests + the broker connect tests. (Doc spec; convertible.)

Feature: Onboarding — first run + the earn-path account hub

  Scenario: First run onboards, but the app still works with defaults
    Given a brand-new install with no saved config
    When the user starts rogerai
    Then a first-run setup is offered
    And declining still leaves a working app on sensible defaults (broker, proxy addr)

  Scenario: Consuming needs no account
    Given an un-logged-in user with a funded wallet (or free models)
    Then they can browse, tune in, chat, and run the agent with no GitHub login

  Scenario: Free sharing needs no login
    Given a user who shares a model FREE
    Then they can go on air without logging in

  Scenario: Earning requires linking a GitHub account
    Given an operator who wants to EARN from sharing
    When they go to cash out
    Then they must link a GitHub account first (the payout identity)

  Scenario: Payout requires Stripe Connect onboarding (KYC)
    Given a linked operator with payable earnings
    When they request a payout before completing Connect
    Then they are routed through connectOnboard (Stripe Connect KYC) first
    And payouts only flow once Connect is complete

  Scenario: Re-running setup updates config without wiping earnings/identity
    Given an existing user
    When they run `roger onboard` again
    Then broker/user/preferences can be reconfigured
    And their linked account + earnings are untouched
