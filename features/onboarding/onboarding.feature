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
# STATUS (P6): the feature stays PROSE under godog (no faked harness), but its behavior is
# now EXECUTABLE - the money/trust CORE as REAL specs elsewhere, and the wizard's DECISIONS
# via extracted pure funcs (below); only the literal huh-form rendering awaits a PTY pass.
# The executable CORE runs elsewhere (cross-referenced, not duplicated):
#   - Free sharing needs no login / earning requires a GitHub-linked owner (scenarios 3-4)
#     -> features/sharing/on_air.feature (executable) + features/pricing/price_ceiling.feature.
#   - Payout requires Stripe Connect KYC before money moves (scenario 5)
#     -> features/money/payouts.feature "KYC gate" (executable; payouts_bdd_test.go), driving
#        the REAL payoutsRequest Connect gate + connectOnboard.
# Scenarios 1 ("first-run setup is offered") and 6 ("re-run Keep/Modify/Reset") are the
# FIRST-RUN CLI WIZARD, rendered with interactive huh forms behind a TTY gate (onboard.go
# interactive()=isatty). The huh-form RENDERING still needs a PTY, but the DECISIONS those
# forms drive are extracted into pure funcs and table-tested (no PTY) in
# cmd/rogerai/onboard_decision_test.go:
#   - applyRerunChoice  <- scenario 6: Keep / Modify / Reset, plus the INVARIANT that a
#     re-run touches ONLY the local share config - the linked GitHub identity (cfg.User),
#     saved prices, and station survive, and earnings (broker-side, never in cfg) can't move.
#   - applyIntent       <- scenario 1's consume-vs-share decision; consume marks onboarded
#     and launches on defaults (the "declining still leaves a working app" half).
# The non-interactive seams (maybeOnboard no-ops off a TTY -> app launches on defaults;
# cmdOnboard --free/--earn scripting; finishShare free/earn) are covered in
# cmd/rogerai onboard_cov_test.go + seams_cov_test.go.
#
# Enforced by: the cross-referenced executable specs above + the extracted-decision tests
# (onboard_decision_test.go) + cmd/rogerai onboard_cov_test.go / seams_cov_test.go.

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
