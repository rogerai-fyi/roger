# ROGER EDGE - CONTROL: agents and devices call each other, under a grant, and every refusal is
# specified before any feature.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-19: "the raspberry pi agent can use or communicate to the agents and
# ask for inference or use its agents in some way".
#
# The vocabulary anticipated this. `operate` is defined as "runs an agent that can act on other
# nodes" and its verification method has read "declared, and gated by grant at use time" since the
# node record was written. The endpoint was reserved, not forgotten: server.go says describe is
# "the one endpoint the LAN face exposes at this stage ... being asked to DO something is
# protocol.feature and control.feature". This is control.feature.
#
# THE FOUR ARROWS, one fabric:
#   escalate   device -> agent    a camera cannot name what it sees and hands the reading up (built)
#   infer      anything -> model  the Pi asks the Jetson's band (features/edge/local_inference.feature)
#   delegate   agent -> agent     the laptop's agent hands a job to the workstation's agent
#   act        agent -> device    an agent asks a board to read, or to act, under a grant
#
# The claim worth making is that these are NOT four mechanisms: one address (node/instance), one
# trust root (the Edge's certificate), one authorization object (a grant), one evidence shape (a
# receipt), one view (a session on the graph).
#
# THE GRANT. store.Grant already carries Nodes, Models, Free, ExpiresAt, Revoked, rate limits, caps
# and a Self flag documented as "owner's own boxes/agents; always $0". On the Edge the record gains
# capabilities and is SIGNED BY THE EDGE'S AUTHORITY, so any member verifies it with the public
# root exactly as it verifies a certificate - no broker round trip, which is what an airgapped
# Edge requires. The authority is the designated machine on a locally-rooted Edge and Core on a
# Core-rooted one. The owner issues a grant; the authority signs it; a member checks it.
#
# THE RULES THAT KEEP A FABRIC FROM BECOMING A BOTNET, stated before the features:
#   1. Delegation does not launder permission. A task runs under the grant it carries AND the
#      receiver's own rules, intersected.
#   2. Actuate keeps its owner confirmation. A delegated task is not a second way in.
#   3. No transitive delegation unless the grant says so.
#   4. Every task carries its origin and a hop count; a member refuses a task whose chain names it.
#   5. Every invocation is receipted and drawn, refusals included.
#   6. A locally-rooted Edge contacts Core for nothing, control included.
#
# GROUND TRUTH: internal/edge/node.go (Operate, Actuate, VerificationMethod), internal/store/
# grant.go (Grant, the record this extends), internal/edge/server.go (the face, gaining invoke),
# internal/edge/fleet.go (AuthorizeInvoke - already refuses on capability and node scope),
# internal/harness (the loop a delegated task runs in), internal/edge/session.go (Record).
#
# Tags: @edge (the grant, the face, the refusals), @harness (running a delegated task), @tui, @cli.

Feature: A member can ask another member to act, only under a grant the authority signed, and every refusal is drawn

  # =========================================================================
  # 1. THE GRANT
  # =========================================================================

  @edge
  Scenario: the owner issues an Edge grant and the authority signs it
    Given a locally-rooted Edge with the authority "shed"
    When the owner issues a grant allowing "laptop/desk" to operate "workstation/agent" for 7 days
    Then the grant names the caller, the callee, the capability and the expiry
    And it is signed by the authority
    And any member verifies it with the public root alone

  @edge
  Scenario: a grant is scoped to capabilities as well as nodes
    Given a grant allowing "laptop/desk" to operate "workstation/agent"
    Then it does not allow "laptop/desk" to actuate anything
    And it does not allow "laptop/desk" to operate "bench/agent"

  @edge
  Scenario Outline: a grant that is not valid is refused before anything runs
    Given a grant that is <defect>
    When "laptop/desk" invokes "workstation/agent" with it
    Then it is refused, naming <reason>
    And nothing ran

    Examples:
      | defect                                   | reason                          |
      | signed by a key that is not the authority | the signature                   |
      | expired                                   | the expiry                      |
      | revoked                                   | the revocation                  |
      | for a different caller                    | the caller                      |
      | for a different callee                    | the callee                      |
      | for a different capability                | the capability                  |
      | for a different account                   | the account                     |

  @edge
  Scenario: revocation reaches every member without a broker
    Given a grant in use on a locally-rooted Edge
    When the owner revokes it
    Then the next invoke under it is refused by the callee
    And the refusal names the revocation
    And Core was not contacted

  @edge
  Scenario: a Self grant on the Edge is always free and says so
    Given an Edge grant issued by the owner to the owner's own instances
    Then its price is zero
    And no invocation under it can debit a wallet

  # =========================================================================
  # 2. DELEGATE: AGENT TO AGENT
  # =========================================================================

  @edge
  Scenario: an agent delegates a task to another agent under a grant
    Given "laptop/desk" and "workstation/agent" on one Edge, "workstation/agent" declaring operate
    And a grant allowing "laptop/desk" to operate "workstation/agent"
    When "laptop/desk" invokes "workstation/agent" with a task
    Then "workstation/agent" runs the task in its own harness, with its own tools and its own permissions
    And the result returns to "laptop/desk"
    And a session is drawn from "laptop/desk" to "workstation/agent", attributed to "laptop/desk"

  @harness
  Scenario: a delegated task runs under the receiver's rules, not the caller's
    Given "workstation/agent" refuses writes outside its workdir
    When a delegated task tries to write outside it
    Then the write is refused exactly as it would be for a local turn
    And the refusal is in the result the caller gets, not hidden

  @harness
  Scenario: a delegated task cannot exceed the grant's budget
    Given a grant with a budget of $0.10 on the market
    When a delegated task would spend past it
    Then the task is stopped at the budget, with what it did so far returned
    And the caller sees the budget as the reason

  @edge
  Scenario: delegation does not launder permission
    Given "laptop/desk" may operate "workstation/agent" but may not actuate "gate/ctl"
    When a delegated task on "workstation/agent" tries to actuate "gate/ctl"
    Then it is refused
    And the refusal names that the originating grant does not cover actuate
    And "gate/ctl" did nothing

  @edge
  Scenario: no transitive delegation unless the grant says so
    Given a grant allowing "laptop/desk" to operate "workstation/agent"
    When a task on "workstation/agent" tries to delegate onward to "bench/agent"
    Then it is refused
    And the refusal names that the grant does not allow onward delegation

  @edge
  Scenario: a grant may allow one hop of onward delegation, and no more
    Given a grant allowing "laptop/desk" to operate "workstation/agent" with one onward hop
    When "workstation/agent" delegates to "bench/agent"
    Then it runs
    When "bench/agent" tries to delegate onward
    Then it is refused

  @edge
  Scenario: a task that would loop back to a member in its chain is refused
    Given a task originating at "laptop/desk", delegated to "workstation/agent"
    When "workstation/agent" tries to delegate it to "laptop/desk"
    Then "laptop/desk" refuses it
    And the refusal names the loop

  @edge
  Scenario: a task carries its origin and every hop, and each is on the receipt
    Given a task delegated "laptop/desk" to "workstation/agent" to "bench/agent"
    Then the receipt on "bench/agent" names the origin and both hops in order
    And the Edge draws the path in that order

  # =========================================================================
  # 3. ACT: AGENT TO DEVICE
  # =========================================================================

  @edge
  Scenario: an agent reads a sensor under a grant
    Given "gate/cam" declaring sense, VERIFIED, and a grant allowing "laptop/desk" to sense it
    When "laptop/desk" asks "gate/cam" for a reading
    Then it receives a well-formed sample
    And a session is drawn from "laptop/desk" to "gate/cam"

  @edge
  Scenario: an agent may not actuate without the owner's standing confirmation on the device
    Given "gate/ctl" declaring actuate, PENDING CONFIRMATION
    And a grant allowing "laptop/desk" to actuate "gate/ctl"
    When "laptop/desk" asks "gate/ctl" to act
    Then it is refused
    And the refusal names that the owner has not confirmed actuate on that device
    And "gate/ctl" did nothing

  @edge
  Scenario: with confirmation and a grant, an actuation runs and is receipted with who asked
    Given "gate/ctl" declaring actuate, confirmed by the owner
    And a grant allowing "laptop/desk" to actuate "gate/ctl"
    When "laptop/desk" asks "gate/ctl" to act
    Then it acts
    And the receipt names "laptop/desk", the grant and the moment
    And the session is drawn as an actuation, distinctly from an inference

  @edge
  Scenario: a device refuses a command that is not on its contract
    Given "gate/ctl" whose actuate contract allows "open" and "close"
    When "laptop/desk" asks it to "unlock"
    Then it is refused
    And the refusal names the contract

  # =========================================================================
  # 4. THE WIRE AND THE REFUSALS
  # =========================================================================

  @edge
  Scenario: invoke is served at the callee's LAN face, mutually authenticated
    Given "workstation/agent" with its LAN face up
    When a dial arrives with no client certificate, or one under another root, or one of another account
    Then each is refused before the task is read

  @edge
  Scenario: a callee that does not declare the capability refuses, whatever the grant says
    Given a grant allowing "laptop/desk" to operate "bench/serve"
    And "bench/serve" does not declare operate
    When "laptop/desk" invokes it
    Then it is refused, naming the missing capability

  @edge
  Scenario: every refusal is receipted and drawn
    Given any refused invoke
    Then a receipt exists for the refusal, naming the reason
    And the Edge draws it as a refused session on both ends

  @edge
  Scenario: an invoke is bounded in time and size
    Given "workstation/agent" with its LAN face up
    When a task body exceeds the cap, or a task runs past its time bound
    Then it is refused or stopped, with what was done returned
    And the callee is never left holding a task nobody is waiting for

  @edge
  Scenario: a locally-rooted Edge contacts Core for nothing, control included
    Given a locally-rooted Edge
    When grants are issued, verified and revoked, and tasks are delegated
    Then no path reached Core
    And that is proven by what the code links, as the local authority already proves

  # =========================================================================
  # 5. THE SURFACES
  # =========================================================================

  @tui
  Scenario: a delegation is drawn as a session between two instances, in order
    Given a task delegated "laptop/desk" to "workstation/agent"
    When the Edge screen renders
    Then a pulse travels from "laptop/desk" to "workstation/agent"
    And the session row reads delegate, names both, and its outcome

  @tui
  Scenario: a refused invoke is drawn as a refusal, never as nothing
    Given an invoke refused for a missing grant
    When the Edge screen renders
    Then the session row reads REFUSED and names the missing grant

  @cli
  Scenario: `roger edge grant` issues, lists and revokes
    When they run "roger edge grant laptop/desk operate workstation/agent --for 7d"
    Then a grant is issued and its id printed
    When they run "roger edge grant"
    Then it lists the grant with caller, callee, capability and expiry
    When they run "roger edge grant revoke <id>"
    Then the grant is revoked

  @cli
  Scenario: `roger edge invoke` is the CLI's arrow
    Given a grant allowing this instance to sense "gate/cam"
    When they run "roger edge invoke gate/cam read"
    Then it prints the reading
    And the session appears in "roger edge sessions"
