# ROGER EDGE - INSTANCES: a running `roger` is a member of the Edge.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-19: "i want to be able to register roger on the edge, and now it's
# part of the edge network ... say on this pc i have 2 or 3 roger instances, each with a
# different name, but same node".
#
# Sections 1 to 12 of the design modelled a node as a MACHINE, and stopped there. But a machine
# does not serve a model or run an agent: a PROCESS does. The tree already half-knows this and
# the halves disagree - `internal/edge` calls a machine a node, `internal/node` calls
# `<station>-<model>` a node id, and `internal/onair` already arbitrates between two roger
# processes on ONE machine with a comment that reads "THE LOCK IS WHY MULTIPLE INSTANCES ARE
# SAFE". Multiple instances per machine is not a new idea being introduced here; it is an
# existing situation that the Edge cannot see, cannot name and cannot route to. The voice-station
# work logged it as a defect: a TUI booth and a headless `roger share` on one machine fighting
# over one id, with jobs black-holed.
#
# THE MODEL THIS SPEC FIXES
#
#   NODE      a machine.                one per machine.   a certificate under the Edge's root.
#   INSTANCE  a running `roger` on it.  several per node.  a name the owner chooses.
#   STATION   an instance broadcasting one model.          the existing `<station>-<model>` id.
#
# CAPABILITIES BELONG TO INSTANCES. A node's capabilities are the union of its instances'. That
# is the correction the whole thing turns on: "this Jetson can serve" is shorthand for "a roger
# on this Jetson is serving", and when that process exits the machine can no longer serve - which
# today the fleet has no way to notice.
#
# AN INSTANCE IS INSIDE ITS NODE'S TRUST BOUNDARY and gets no certificate of its own. This is
# deliberate: it keeps the enrolment ceremony at one per machine. An instance shares the node's
# key because it IS the same machine under the same owner - anything that could forge an instance
# could already read the node's private half and be the node. The node's LAN face reports its
# instances over its own certificate, and that report is the only account of them anybody
# believes: the same rule `describe` already follows for capabilities.
#
# REGISTRATION IS THEREFORE NOT A CEREMONY. A roger starting on an enrolled node registers
# itself and deregisters when it exits. Joining the Edge stays a per-machine act; being on it is
# per-process and automatic.
#
# GROUND TRUTH: internal/edge/node.go (Capability, Kind, NewNode), internal/edge/server.go
# (Describe, DescribePath - the face that must now report instances), internal/edge/fleet.go,
# internal/edge/mirror.go (the per-process file pattern this reuses, already proven by the
# session mirror), internal/store/edgenode.go (EdgeNode, EdgeCap), internal/node/controller.go
# (the station id and the on-air lock), cmd/rogerai/edgehost.go (the host that runs the face).
#
# Tags pick the surface: @edge in internal/edge, @cli in cmd/rogerai, @tui in internal/tui,
# @console in internal/webui. Real fleets over real stores; no mocks.

Feature: A running roger registers itself on its node's Edge, several to a machine, and the fleet routes to the process rather than the box

  # =========================================================================
  # 1. AN INSTANCE JOINS BY RUNNING
  # =========================================================================

  @edge
  Scenario: a roger starting on an enrolled node is a member, with no ceremony
    Given an enrolled node "workshop"
    When a roger starts on it
    Then it is registered as an instance of "workshop"
    And it carries a name, a start time and the capabilities it offers
    And nothing was enrolled, no certificate was issued and no network was needed

  @edge
  Scenario: a roger on a machine that has not enrolled is not a member
    Given a machine that has not enrolled
    When a roger starts on it
    Then it registers no instance
    And the Edge lists no member for that machine
    And the reason given names enrolment, not a failure

  @edge
  Scenario: several instances run on one node and the node stays one node
    Given an enrolled node "workshop"
    When rogers named "desk", "share" and "lab" start on it
    Then the Edge lists ONE node "workshop"
    And that node carries exactly the instances "desk", "share" and "lab"
    And each instance is addressable as "workshop/<its name>"

  @edge
  Scenario: an instance that exits stops being a member, and its node does not
    Given an enrolled node "workshop" running the instances "desk" and "share"
    When the instance "share" exits
    Then the Edge lists the node "workshop" still
    And its instances are exactly "desk"
    And nothing about the node's enrolment changed

  @edge
  Scenario: an instance killed without exiting is reaped, not drawn forever
    Given an enrolled node "workshop" running the instance "share"
    And that instance's process is gone without deregistering
    When the fleet is read past the liveness window
    Then "share" is not listed as an instance
    And the node "workshop" is untouched

  # =========================================================================
  # 2. CAPABILITIES BELONG TO THE PROCESS
  # =========================================================================

  @edge
  Scenario: a node's capabilities are the union of its instances'
    Given an enrolled node "workshop"
    And an instance "share" offering serve
    And an instance "desk" offering operate
    When the fleet is read
    Then the node "workshop" offers serve and operate
    And each capability names the instance that provides it

  @edge
  Scenario: a capability is gone when the instance providing it is gone
    Given an enrolled node "workshop"
    And an instance "share" offering serve
    And an instance "desk" offering operate
    When the instance "share" exits
    Then the node "workshop" no longer offers serve
    And it still offers operate
    And nothing routes serve to that node

  @edge
  Scenario: an instance's claimed capability is CLAIMED until it is verified, exactly as a node's is
    Given an enrolled node "workshop"
    And an instance "share" claiming serve
    When the fleet is read
    Then serve on "share" is CLAIMED
    And it names how it will be verified
    And nothing routes on it

  @edge
  Scenario: verifying a capability verifies it on the instance, not on the whole machine
    Given an enrolled node "workshop"
    And an instance "share" claiming serve and an instance "lab" claiming serve
    When the serving probe passes against "share" only
    Then serve on "share" is VERIFIED
    And serve on "lab" is still CLAIMED

  @edge
  Scenario: actuate on an instance keeps the owner's confirmation
    Given an enrolled node "gate" running an instance "gate-ctl" declaring actuate
    When the fleet is read
    Then actuate on "gate-ctl" is PENDING CONFIRMATION
    And it is never probed
    And the owner's confirmation is what changes it

  # =========================================================================
  # 3. NAMES
  # =========================================================================

  @edge
  Scenario: an instance has a default name the owner can change
    Given an enrolled node "workshop"
    When a roger starts on it with no name chosen
    Then it is given a default name derived from this node, not a random one
    And the owner can rename it
    And the new name is what every surface shows

  @edge
  Scenario Outline: a name that cannot be an address is refused
    Given an enrolled node "workshop"
    When a roger tries to register as "<name>"
    Then it is refused with a reason naming what is wrong
    And no instance by that name exists

    Examples:
      | name           |
      |                |
      | with space     |
      | slash/inside   |
      | ../escape      |
      | UPPER          |
      | a-very-long-name-that-goes-well-past-any-label-a-network-will-carry |

  @edge
  Scenario: two instances on one node cannot share a name
    Given an enrolled node "workshop" running the instance "desk"
    When another roger on "workshop" tries to register as "desk"
    Then it is refused
    And the running "desk" is untouched
    And the refusal says the name is taken on this node

  @edge
  Scenario: the same instance name on two different nodes is not a conflict
    Given an enrolled node "workshop" running the instance "desk"
    And an enrolled node "bench"
    When a roger on "bench" registers as "desk"
    Then both are members
    And each is addressed as "<its node>/desk"
    And a bare "desk" is ambiguous and says so, listing both

  @edge
  Scenario: a bare name addresses an instance when it is unique on the Edge
    Given an enrolled node "workshop" running the instance "lab"
    And no other instance named "lab" on this Edge
    Then "lab" addresses it
    And "workshop/lab" addresses the same instance

  @edge
  Scenario: an instance cannot name itself onto another node
    Given an enrolled node "workshop"
    When a roger on "workshop" tries to register as "bench/desk"
    Then it is refused
    And no instance is recorded against "bench"

  # =========================================================================
  # 4. THE NODE'S FACE REPORTS ITS INSTANCES
  # =========================================================================

  @edge
  Scenario: describe reports the instances, over the node's own certificate
    Given an enrolled node "workshop" running the instances "desk" and "share"
    When a peer dials its LAN face and describes it
    Then the answer carries the node's id, kind and account as today
    And it carries the instances "desk" and "share" with their capabilities
    And the answer arrived over the certificate the advertisement committed to

  @edge
  Scenario: the advertisement does not carry the instance list
    Given an enrolled node "workshop" running three instances
    When it advertises on the LAN
    Then the advertisement carries what it carries today and no instance list
    And the instances are learned only from a dialled, verified describe

  @edge
  Scenario: what a peer records is what describe said, never what was advertised
    Given a peer advertising capabilities that its describe does not report
    When it is dialled and verified
    Then the instances and capabilities recorded are describe's
    And the advertised claim is not stored

  @edge
  Scenario: a peer reporting an absurd number of instances is bounded, not believed
    Given a peer whose describe reports 500 instances
    When it is dialled and verified
    Then at most the bound is recorded
    And the fleet says the list was truncated rather than silently dropping it

  @edge
  Scenario: an instance list from another account is never merged
    Given a peer on this LAN enrolled to a different account
    When it is dialled
    Then it is not a member
    And none of its instances appear anywhere on this Edge

  # =========================================================================
  # 5. THE COLLISION THAT ALREADY EXISTS
  # =========================================================================

  @edge
  Scenario: two instances on one machine broadcasting different models both work and are both drawn
    Given an enrolled node "workshop"
    And an instance "share" broadcasting "gpt-oss-120b"
    And an instance "lab" broadcasting "qwen-3.8-27b"
    Then both are members of the Edge
    And the node "workshop" offers both bands
    And each band names the instance broadcasting it

  @edge
  Scenario: two instances claiming the same station keep the existing lock, and the Edge says who holds it
    Given an enrolled node "workshop"
    And an instance "share" broadcasting "gpt-oss-120b"
    When an instance "lab" tries to broadcast "gpt-oss-120b"
    Then the existing on-air lock refuses the second, exactly as it does today
    And the Edge shows the band held by "share"
    And "lab" is drawn as a member that is not serving that band
    And nothing is black-holed

  # =========================================================================
  # 6. THE SURFACES
  # =========================================================================

  @tui
  Scenario: the Edge screen draws instances under their node
    Given a fleet with a node "workshop" running "desk" and "share"
    When the Edge screen renders at 100 columns
    Then "workshop" is drawn as one node
    And "desk" and "share" are drawn under it, indented, with their own marks
    And the headline counts nodes and instances separately

  @tui
  Scenario: this instance is named on its own screen
    Given this machine is enrolled as "workshop" and this roger is the instance "desk"
    When the Edge screen renders at 100 columns
    Then self is drawn as "workshop/desk"
    And it is marked as this instance, distinctly from the other instances on this node

  @tui
  Scenario: a node with many instances stays legible
    Given a fleet with a node "workshop" running 12 instances
    When the Edge screen renders at 80 columns
    Then no line exceeds 80 columns
    And the node says how many of how many instances it is showing

  @cli
  Scenario: the CLI lists instances under their node
    Given a fleet with a node "workshop" running "desk" and "share"
    When they run "roger edge list"
    Then "workshop" is listed once as a node
    And "desk" and "share" are listed under it with their capabilities

  @cli
  Scenario: the CLI describes one instance
    Given a fleet with a node "workshop" running "desk"
    When they run "roger edge describe workshop/desk"
    Then it prints the instance's name, node, capabilities, start time and what it is serving
    And it does not pretend the instance has a certificate of its own

  @cli
  Scenario: renaming this instance is a command
    Given this machine is enrolled and running one instance
    When they run "roger edge name . bench-desk"
    Then this instance is renamed to "bench-desk"
    And every surface shows the new name
    And the node's name is untouched

  @console
  Scenario: the console draws instances under their node and in the detail
    When the Edge data is read
    Then each node carries its instances with their names and capabilities
    And the panel draws them under the node, and the detail lists them

  # =========================================================================
  # 7. THE TRUST BOUNDARY IS WRITTEN DOWN
  # =========================================================================

  @edge
  Scenario: an instance holds no certificate and issues nothing
    Given an enrolled node "workshop" running the instance "desk"
    Then the instance has no certificate of its own
    And nothing about it can be verified except through the node's face
    And the node's enrolment is what vouches for it

  @edge
  Scenario: a forgotten node takes its instances with it
    Given an enrolled node "workshop" running the instances "desk" and "share"
    When the owner forgets "workshop"
    Then neither instance is a member
    And the node's certificate is revoked exactly as today
