# HANDOFF increment 3: Claude Code at the desk, as a CONTEXT-ONLY guest.
#
# WHY THIS IS NOW ALLOWED. claude was excluded from the registry (registry.go:52,
# features/operator/detection.feature:35) for one specific reason: it speaks Anthropic's
# /v1/messages wire, so a naive launch would silently fall back to the user's REAL
# Anthropic account - they would pay Anthropic while believing they were on the band.
#
# That objection is about REROUTING THE MODEL. This feature does not reroute anything. The
# value is CONTEXT TRANSFER: you were working locally, and you want the work handed to
# Claude Code, which runs on its own account by design. So the fix is not a /v1/messages
# shim - it is a wiring strategy that injects NOTHING, plus telling the truth on screen.
# The failure mode that got claude excluded (silent billing) becomes an informed choice.
#
# GROUND TRUTH:
#   internal/operator/registry.go:12-28 - the three existing strategies, all of which
#     inject a base URL, a session key and a model. This one is defined by injecting none.
#   internal/operator/materialize.go:83 Materialize(Guest, Session) (Launch, cleanup, err);
#     :273 Command(Launch, bin, workdir, parentEnv); ComposeEnv:252 (additions override).
#   internal/operator/brand.go:150 - the claude plate is already drawn, dormant.
#   internal/tui/operator.go:663 startOperatorHandoff (NeedsSetup bails here), :817
#     onOperatorExec (budget wiring, capsule write, exec), :1036 operatorPatchView.
#   `claude [options] [prompt]` starts an INTERACTIVE session with a positional prompt
#     (verified against Claude Code 2.1.220) - that is how the brief gets read.
#
# Enforced by: internal/operator/claude_guest_bdd_test.go. The DESK-side behaviour (what
# the prelaunch plate says, and that spend wiring is skipped) is specced and executed in
# features/handoff/claude_desk.feature, so each suite runs in the package that owns it.

@handoff @operator
Feature: Claude Code takes the mic with your context, on its own account

  Rule: claude is a registry guest with a context-only wiring strategy

    Scenario: the registry lists claude with the context-only strategy
      Then the registry contains a "claude" entry
      And its strategy is context-only
      And it is not marked as needing setup
      # NeedsSetup was the placeholder for exactly this row. It is now launchable.

    Scenario: the MVP guests are unchanged
      Then "opencode", "hermes" and "aider" keep their existing strategies
      # This adds a guest; it must not re-wire the three that work.

  Rule: a context-only launch injects no RogerAI credentials at all

    Scenario: no session key reaches the guest
      Given a session with a live base URL, session key and model
      When claude is materialized
      Then the launch environment carries no session key
      And it carries no broker base URL
      And it carries no model override
      # This is the whole safety argument, inverted from the other guests: they get wired
      # to the band, this one deliberately is not. If a credential ever appears here, the
      # guest could spend on the band while the user believes it is on their own account.

    Scenario: no config file is written for claude
      When claude is materialized
      Then no scratch config file is created
      # Nothing to configure: it runs exactly as the user's own `claude` would.

    Scenario: the launch still runs in the user's working directory
      When claude is materialized
      Then the child runs in the user's workdir, not in a scratch dir
      # Regression of the existing Command() contract - the guest works on real files.

  Rule: the launch hands over the brief

    Scenario: the argv seeds an interactive session pointed at the brief
      When claude is materialized for a workdir with a brief
      Then the argv carries a single opening prompt
      And that prompt names the brief file
      And no non-interactive flag is passed
      # -p would print and exit; the user wants to keep working.

    Scenario: with nothing to hand over, claude still launches clean
      Given a session with no recorded turns
      When claude is materialized
      Then no brief is referenced in the argv
      # An opening prompt pointing at a file that was never written is a worse start than
      # no prompt at all.

  Rule: a guest that is not installed is offered, not hidden

    Scenario: claude absent from PATH shows the install hint
      Given claude is not installed
      Then the desk offers it with its install hint
      # Same degrade-gracefully rule as every other guest (detection.feature).
