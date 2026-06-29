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
# STATUS (P4 executable-spec pass): kept PROSE-ONLY on purpose - no faked godog harness.
# Its executable CORE already runs as REAL specs elsewhere (cross-referenced, not duplicated):
#   - Free sharing needs no login / earning requires a GitHub-linked owner (scenarios 3-4)
#     -> features/sharing/on_air.feature (executable) + features/pricing/price_ceiling.feature.
#   - Payout requires Stripe Connect KYC before money moves (scenario 5)
#     -> features/money/payouts.feature "KYC gate" (executable; payouts_bdd_test.go), driving
#        the REAL payoutsRequest Connect gate + connectOnboard.
# The remaining scenarios are the FIRST-RUN CLI WIZARD (scenario 1 "setup is offered" and
# scenario 6 "re-run Keep/Modify/Reset"), which are gated on an interactive TTY (onboard.go
# interactive()=isatty + huh forms). Driving them needs a CLI/PTY harness that does not exist
# yet; the non-interactive seams (maybeOnboard no-ops off a TTY -> app launches on defaults;
# cmdOnboard --free/--earn scripting) are unit-tested in cmd/rogerai/onboard_cov_test.go.
# FLAGGED for a future CLI-harness pass rather than faked here.
#
# Enforced by: the cross-referenced executable specs above + cmd/rogerai onboard_cov_test.go.

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
