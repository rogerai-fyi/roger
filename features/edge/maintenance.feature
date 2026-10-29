# ROGER EDGE - MAINTENANCE: acting on a unit from another machine, under the owner's two locks.
#
# STATUS: APPROVED 2026-09-28 (founder: "approved"); built the night of 2026-09-27. Not godog-bound: pinned by Go tests -
# internal/edge/maintain_test.go (the capability, the task, the face), cmd/rogerai/edgemaintain_test.go
# (the switch, the runner, `roger edge maintain`). The founder approved it on 2026-09-28,
# with the switch still OFF on every machine until an owner turns it on there.
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
# WHAT IT CANNOT DO. It cannot run a command, a script or a shell. The only processes it touches are
# rogers the owner already marked persistent ON THAT MACHINE, by the names they gave them. It cannot
# reach a name that is not persistent, and it cannot touch the roger serving the request. Rebooting
# and shutting a machine down are NOT here and are refused by name, so nothing pretends they exist.
#
# THE ACTIONS, as of 2026-09-28: START a persistent agent that is down, STOP one that is running,
# RESTART one (stop, wait for it to leave the household, start), and LOGS, the redacted log tail
# (features/edge/logs.feature). Every one of them goes through the two locks above.
#
# HOW A STOP IS SAFE. Signalling a process by number is how a pid gets reused under you, so a stop
# only signals a pid when ALL of these hold: the household registration that names it is FRESH (its
# heartbeat is inside the reap window, so the process it describes is the process still beating), the
# pid is not this process (a roger cannot kill the face answering the request), and on Linux the
# process's own command line still names a roger. It sends SIGTERM and never SIGKILL: a roger that
# will not leave is reported, not forced. The stop waits a bounded time for the registration to go,
# and says plainly whether it went.
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
    And the answer WAITS briefly and says whether it really joined the Edge, not merely that it was
      launched - a spawned agent that cannot stand up its face exits at once, and the owner hears it
      from the action they asked for
    And when it does not join, the answer says where to look: that machine's log
    And "shed"'s history carries a maintenance event naming the caller and the action

  @edge
  Scenario: on a machine running several rogers, address the one you mean
    Given a machine running two rogers, "roger" and "roger-3"
    And the node's recorded face is whichever of them last observed itself
    When desk asks "shed" (the machine) to stop "roger-3"
    Then the request can land on "roger-3" itself, and is refused saying why and what to do
    When desk asks "shed/roger" to stop "roger-3"
    Then it reaches THAT roger's own face and the stop is carried out
    And each roger records its own face port, so the address is the node's host and that port
    And the certificate is the NODE's, so the pin is unchanged and nothing new is trusted
    When desk addresses a roger the node does not run
    Then it is refused, naming the command that lists them
    When desk addresses a roger that reports no face of its own, as an older one does
    Then it is refused, saying to ask the node instead - it is never silently sent to the machine

  @edge
  Scenario: a spawned agent gets its own face, and is never left as a zombie
    Given this machine runs with a pinned face port
    When an agent is spawned here
    Then it does NOT inherit that pinned port, because two rogers cannot bind one face and the child
      would exit at once reporting nothing
    And the child is waited for, so a child that exits is not left a zombie for the life of this roger
    And a child that dies within seconds is logged, because the owner was told it had launched

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

  # ---- stopping and restarting ----------------------------------------------

  @edge
  Scenario: stopping a running agent
    Given "watcher" is running on "shed" and its registration is fresh
    When desk asks "shed" to stop "watcher"
    Then it is sent SIGTERM, never SIGKILL
    And the answer says it was asked to stop, and whether it had gone by the time the answer was sent
    And "shed"'s history carries the action and who asked
    When it is asked to stop something that is not running
    Then nothing is signalled, and the answer says it was not running - that is not an error

  @edge
  Scenario: a stop never signals a process it cannot vouch for
    Given a registration whose heartbeat is older than the reap window
    When desk asks to stop that agent
    Then nothing is signalled, and the answer says it is not running - a process that stopped beating
      reads as gone here, exactly as the fleet draws it DARK, and no pid nobody can vouch for is
      ever signalled
    Given a registration that names THIS process's own pid
    When desk asks to stop it
    Then it is refused: a roger cannot kill the face that is answering
    Given a registration naming a pid whose command line is not a roger
    When desk asks to stop it
    Then nothing is signalled, and the refusal says the pid is not a roger any more
    And a registration with no pid at all (written by an older roger) is refused the same way

  @edge
  Scenario: restarting is a stop then a start, and says which half did what
    Given "watcher" is running on "shed"
    When desk asks "shed" to restart "watcher"
    Then it is stopped, and once its registration goes a fresh roger is launched for it
    And the answer names both halves
    When the roger has not gone by the time the answer is due
    Then it is NOT started on top of itself, and the answer says so rather than reporting a restart
      that did not happen - asking again in a moment works
    When the stop half is refused
    Then nothing is launched, and the refusal is the stop's own reason
    When it is asked to restart something that is not running
    Then it is simply started, and the answer says so

  # ---- what is NOT here -----------------------------------------------------

  @edge
  Scenario: reboot and shutdown are refused by name until they are built
    When desk asks "shed" to reboot or shut down
    Then each is refused by name, saying it is not built yet
    And no partial action is taken
