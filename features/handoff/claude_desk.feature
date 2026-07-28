# HANDOFF increment 3, desk half: what the PRELAUNCH PLATE says when the guest is Claude
# Code. Split from claude_guest.feature only so each suite runs in the package that owns the
# behaviour - the plate and the spend wiring live in internal/tui.
#
# The plate is where the honesty lives. Every other guest is wired to the tuned band, so its
# plate shows BASE URL and MODEL rows. For a context-only guest those rows would be a lie:
# it runs on the user's own Anthropic account, and RogerAI meters nothing. The failure mode
# that got claude excluded from the registry was SILENT billing; saying it plainly on screen
# is what turns that into an informed choice.
#
# GROUND TRUTH: internal/tui/operator.go:1036 operatorPatchView (header, brand block, the
#   three step() rows, BASE URL / MODEL rows, closer); :817 onOperatorExec (SetBudget /
#   ResetSpend / ResetCalls, the capsule write, then exec).
#
# Enforced by: internal/tui/handoff_desk_return_bdd_test.go

@handoff @operator
Feature: The desk tells the truth about a context-only guest

  Rule: the desk tells the truth about whose account this runs on

    Scenario: the prelaunch plate says it runs on the user's own account
      Given the user picks claude at the desk
      Then the plate says it runs on their own Anthropic account
      And it says RogerAI is not metering it
      And it does not show a band or a model row
      # The other guests' plate rows (BASE URL / MODEL) would be a lie here.

    Scenario: the plate says what IS being handed over
      Given the user picks claude at the desk with recorded turns
      Then the plate says the session context is going with it

    Scenario: after a clear, the plate does not claim context it no longer has
      Given the user cleared the agent and then picks claude at the desk
      Then the plate says there is nothing to hand over
      # The turn COUNTER is a lifetime sequence and never goes backwards; what travels is
      # the ring. Reading the counter would promise a guest context that was cleared.

    Scenario: spend wiring is skipped for a context-only guest
      Given the user picks claude at the desk
      Then no budget is set and no spend counter is armed for the handoff
      # There is nothing to meter. Arming the meter would display a spend that never moves.

  Rule: a guest that needs no band is not gated on one

    Scenario: the band floor does not disable a context-only guest
      Given the tuned band is below the 16k agent-ready floor
      Then claude is still selectable at the desk
      And the other guests are still gated
      # The 16k floor exists because a guest DRIVES THE BAND. This one does not touch it,
      # so "needs a 16k+ band" would be untrue copy blocking the headline use case:
      # working locally with no useful band and wanting Claude Code on it.

    Scenario: a context-only guest is exempt from the band-too-small refusal
      Given the tuned band is below the 16k agent-ready floor
      When the user picks claude
      Then the handoff is not refused for the band being too small

    Scenario: the exec stage does not re-gate a context-only guest on the band
      Given the tuned band is below the 16k agent-ready floor
      And the user confirmed the plate for claude
      When the handoff reaches the exec stage
      Then it is not aborted for the band
      # The desk gates and the exec gates are separate: the exec re-checks exist because a
      # band can drop during the staging beat. Exempting only the desk would let a guest
      # through the door and then kill it after the user had already said yes.

    Scenario: a remote viewer is told the same thing the plate says
      Given a context-only handoff after an earlier guest that did spend
      When the handoff is announced to the base station
      Then the announced spend is zero
      And no band is named in the announcement
      # The plate calls this handoff unmetered and shows no band. Reporting the live
      # accumulator and the tuned model would tell a remote viewer the opposite - and
      # would attribute a PREVIOUS guest's residual spend to it.
