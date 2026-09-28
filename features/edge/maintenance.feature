# ROGER EDGE - MAINTENANCE: acting on a unit from another machine, under the owner's two locks.
#
# STATUS: PROPOSED 2026-09-27, built the same night. Not godog-bound: pinned by Go tests -
# internal/edge/maintain_test.go (the capability, the task, the face), cmd/rogerai/edgemaintain_test.go
# (the switch, the runner, `roger edge maintain`). NEEDS THE FOUNDER'S APPROVAL: it is a new action
# a REMOTE member can cause on this machine, and that is a security boundary.
#
# WHY. Monitoring without acting is half a fleet manager. The screens already say a unit's agent is
# offline - an owner could see it from the kitchen and still had to walk to the shed. Every fleet
# manager answers that with a remote action: restart the service, reboot the box. This is the first
# one, and the smallest that closes the loop: START an agent that the owner already marked
# persistent on that machine, and that is not running.
#
# THE TWO LOCKS. Nothing here is reachable unless the owner did BOTH of these, on two machines:
#   1. On the TARGET machine: `roger edge maintenance on`. Off by default. Without it the machine
#      declares no maintain capability and its face refuses every maintenance invoke, grant or not.
#   2. On the CALLING machine: hold a grant the authority signed, scoped to `maintain` AND to that
#      node. A grant for operate does not reach maintenance: running a turn and starting a process
#      are different powers, and one is never the other.
# On top of those: the caller is a member over mutual TLS, the grant's caller must match the
# certificate, a revoked certificate is refused at once, and every action is on the node's history.
#
# WHAT IT CANNOT DO. It cannot run a command, a script or a shell: the ONLY thing it starts is a
# roger the owner already marked persistent ON THAT MACHINE, by the name they gave it. It cannot
# start something that is already running, cannot reach a name that is not persistent, and cannot
# touch the roger serving the request. Rebooting, shutting down and stopping a running agent are
# NOT in this slice and are refused by name, so nothing pretends they exist.
#
# WHO MAY DECLARE IT. features/edge/node_identity.feature (approved) says a host may declare any
# capability and a mobile any but relay, so both may DECLARE maintain; a board may not. Declaring it
# buys nothing on its own: the machine still refuses every maintenance invoke unless its owner
# turned maintenance on THERE, and only a roger implements the action at all.
#
# GROUND TRUTH: internal/edge/maintain.go, internal/edge/node.go (the capability),
# internal/edge/invoke.go (the face), cmd/rogerai/edgemaintain.go, cmd/rogerai/edgerunner.go.

Feature: maintenance actions

  Background:
    Given an Edge whose authority issued certificates to "desk" and "shed"
    And "shed" has a persistent agent "watcher" that is not running

  # ---- the locks ------------------------------------------------------------

  @edge
  Scenario: maintenance is off until the owner turns it on, on that machine
    Given "shed" has never run `roger edge maintenance on`
    Then it declares no maintain capability
    And its face refuses a maintenance invoke, saying maintenance is off on that machine
    And a grant scoped to maintain does not change that

  @edge
  Scenario: a grant for operate does not reach maintenance
    Given maintenance is on at "shed"
    And desk holds a grant scoped to operate on "shed"
    When desk asks "shed" to start "watcher"
    Then it is refused: the grant does not carry maintain
    And nothing was started

  @edge
  Scenario: every other refusal the face already makes still applies
    Given maintenance is on at "shed"
    When a caller with no certificate asks
    Then it is refused 401
    When a member whose certificate was revoked asks
    Then it is refused, and the revocation is the reason
    When a member asks with a grant naming a different node
    Then it is refused
    When a member asks with an expired grant
    Then it is refused
    And a replayed request is refused by the nonce cache, exactly as an operate invoke is

  # ---- the action -----------------------------------------------------------

  @edge
  Scenario: starting a persistent agent that is down
    Given maintenance is on at "shed" and desk holds a maintain grant for it
    When desk asks "shed" to start "watcher"
    Then a roger is launched on "shed" as the agent "watcher"
    And the answer says what was started, by name, and that it returns on its next registration
    And "shed"'s history carries a maintenance event naming the caller and the action

  @edge
  Scenario: starting what is already running changes nothing, and says so
    Given "watcher" is running on "shed"
    When desk asks "shed" to start "watcher"
    Then nothing is launched, and the answer says it is already running
    And that is not an error: the owner asked for an end state, and it is already so

  @edge
  Scenario: the refusals of the action itself
    When desk asks "shed" to start "nosuch"
    Then it is refused, saying no persistent agent has that name
    When desk asks "shed" to start the roger that is serving the request
    Then it is refused: a process cannot restart itself through its own face
    When desk asks for an action that is not "start"
    Then it is refused by name, saying which actions exist
    When the task names no target
    Then it is refused, saying an action names the agent it acts on
    And a name that is not a valid instance name is refused before anything is looked up

  @edge
  Scenario: one maintenance action at a time
    Given a maintenance action is running on "shed"
    When a second arrives
    Then it is refused promptly rather than queued, and says to try again shortly

  # ---- the surfaces ---------------------------------------------------------

  @cli
  Scenario: `roger edge maintenance` and `roger edge maintain`
    When the owner runs `roger edge maintenance` on this machine
    Then it says whether maintenance is on here, and what that allows
    When the owner runs `roger edge maintenance on`
    Then this machine declares maintain from its next registration
    When the owner runs `roger edge maintain shed start watcher`
    Then the grant is found, the invoke goes over the pinned face, and the outcome is printed
    And with no grant it says which grant is missing and how to issue one

  @edge
  Scenario: the fleet screen says a unit can be maintained
    When a unit declares maintain
    Then its readout says maintenance is on there, and what it allows
    And a unit that does not declare it says nothing at all

  # ---- what is NOT here -----------------------------------------------------

  @edge
  Scenario: stop, restart and reboot are refused by name until they are built
    When desk asks "shed" to stop, restart, reboot or shut down
    Then each is refused by name, saying it is not built yet
    And no partial action is taken
