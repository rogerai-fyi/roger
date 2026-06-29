# APPEALS + RECOURSE: a struck/banned/held operator is never permanently stuck on an automated
# decision. They can file a SELF-SERVE appeal; the founder sees a review queue; and a reviewed
# admin action can clear a recount hold and (with forgive) wipe strikes + lift the ban. Recount
# holds also expire on their own after a window, so a transient hold never traps earnings forever.
#
# GROUND TRUTH (cmd/rogerai-broker/recourse.go):
#   ownerStrikes() — the operator's own strikes + evidence + node-ban status (self-visibility).
#   ownerAppeal() — file a self-serve appeal (GET lists the caller's appeals/status).
#   adminAppeals() — the founder's open-appeal review queue (admin-gated).
#   adminUnhold(account, node, forgive) — clear a recount hold; forgive also wipes strikes +
#     lifts the ban (reviewed action; the /admin/unhold control).
#   recountHoldSweep / recountHoldSweepOnce — holds older than recountHoldDays expire automatically.
#
# Enforced by: cmd/rogerai-broker/recourse_test.go.

Feature: Appeals + recourse

  Scenario: An operator can see their own strikes + status
    Given operator "op-1" has strikes and a node ban
    When they read their own strikes
    Then they see the strike count, evidence, and node-ban status (transparency)

  Scenario: A struck or banned operator can file a self-serve appeal
    Given "op-1" is banned (or struck)
    When they file an appeal with a reason
    Then the appeal is recorded and visible to the operator as pending

  Scenario: The founder sees open appeals in a review queue
    Given one or more appeals are pending
    When the founder reads the admin appeals queue
    Then the open appeals are listed for review (admin-gated)

  Scenario: A reviewed unhold clears a recount hold so lots promote again
    Given account "op-1" is under a recount hold
    When the founder issues an admin unhold for "op-1"
    Then the hold is cleared and the operator's earning lots promote on the next sweep

  Scenario: Forgive clears the hold AND wipes strikes AND lifts the ban
    Given "op-1" is banned and under a hold
    When the founder unholds with forgive
    Then the hold clears, the strikes are wiped, and the ban is lifted (full reinstatement)

  Scenario: A recount hold expires automatically after the window
    Given an account placed under a recount hold
    When more than recountHoldDays pass
    Then the periodic sweep expires the hold (a transient hold never traps earnings forever)

  Scenario: The admin recourse controls are founder-gated
    Given a non-admin caller
    When they hit the admin appeals queue or the unhold control
    Then they are refused (these are super-admin-only, like the rest of /admin)
