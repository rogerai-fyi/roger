# ROGER EDGE - SWITCHING WHAT A MEMBER SERVES, FROM ACROSS THE EDGE.
#
# STATUS: APPROVED by the founder 2026-09-30; fetch-by-exact-id ruling 2026-10-01.
# BUILD STATUS (2026-10-01, wt/edge-limoni): BUILT - CLI (`roger model ... --on <node>`), TUI (M on a
# member), console (serve on a member card); live-proven over this machine's own face with a real
# grant. "A member that goes dark mid-fetch" is shown as an interrupted fetch from its last report;
# there is no separate authority-side record of a fetch beyond that.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-29: "lets say we have roger installed on jetson nano or nx or something
# and we want it to serve a different model, we should be able to support that entire flow and
# switch remotely through the edge, and roger should be able to do it as well independently of the
# edge if required".
#
# features/serving/local_models.feature gives a single machine the whole flow: browse, fit, fetch,
# serve. This file is the other half of the founder's sentence - doing it to a machine you are not
# sitting at. A Jetson in a cupboard is exactly the case: it has no screen, and walking to it with
# a keyboard to change a model is the thing an Edge exists to prevent.
#
# =============================================================================================
# WHY THIS IS THE MOST DANGEROUS THING ON THE EDGE, AND WHAT THAT BUYS IT
# =============================================================================================
#
# Everything else the Edge can do to a member is bounded by what is already there. `maintain` can
# start, stop or restart an agent that the owner already installed. `actuate` licenses a node that
# already has a door to open it.
#
# THIS IS DIFFERENT. It tells a machine to FETCH SOMETHING FROM THE INTERNET AND RUN IT. That is
# the first Edge operation whose blast radius is not bounded by the target's existing state, and
# saying so plainly is the point of this section. So it carries every gate the fabric has, and two
# of its own:
#
#   1. THE TARGET'S OWN SWITCH. Exactly as maintenance works: nothing reaches a member that its
#      owner did not turn on AT THAT MACHINE. A member that never enabled it refuses, and the
#      refusal is the same whether or not the caller holds a grant.
#   2. AN AUTHORITY-SIGNED GRANT, scoped to serve-control and to that node, held by the caller.
#   3. THE MODEL IS NAMED, NEVER ADDRESSED. The wire carries a catalogue entry's ID. It does not
#      carry a URL, a path, a runtime, or a command. A member resolves that id against ITS OWN
#      catalogue and fetches from the source it already trusts. There is no request shape that can
#      make a member fetch from somewhere the caller chose - that is the difference between a fleet
#      manager and a botnet, and it is enforced by the protocol having nowhere to put a URL.
#   4. THE TARGET DECIDES IF IT FITS. Fitness is judged ON THE TARGET, with the target's own
#      measurements. The caller's opinion about a Jetson's VRAM is not evidence.
#
#      This one is also forced by what the fabric carries, which is worth knowing before anyone
#      proposes showing a fit verdict in the fleet view. A node's rich hardware picture
#      (detect.LocalHW, with VRAMTotalMiB) is LOCAL-ONLY BY DESIGN and pinned by a test: the
#      network gets a four-value privacy bucket, because the supply side must not be believed
#      about its own capability - a self-declared hardware field was once found moving edge
#      placement by a factor of two. store.EdgeResources carries a GPU name and a used fraction
#      and no VRAM total at all. So the caller CANNOT compute the target's fit even if it wanted
#      to, and the honest design and the private one are the same design. Showing a fit verdict in
#      the fleet view would mean adding a field to EdgeResources, and that privacy rationale would
#      have to be revisited first for the owner-to-their-own-machine case.
#
# A member is always allowed to say no, and every no says why.
#
# GROUND TRUTH: features/serving/local_models.feature (the flow being triggered), features/edge/
# maintenance.feature (the switch + grant pattern this copies deliberately rather than inventing),
# features/edge/control.feature (grants), features/edge/serving_probe.feature (how the new band is
# proven), internal/edge/maintain.go (the shape of a gated member action).
#
# Tags: @edge (the fabric rule), @cli, @tui, @security.

Feature: The owner changes what a member serves without going to it, and nothing else can

  # =========================================================================
  # 1. THE GATES
  # =========================================================================

  @security
  Scenario: a member that has not opened its serving to the Edge refuses
    Given a member whose owner never turned on remote serving control there
    When another node asks it to serve a different model
    Then it refuses
    And the refusal names the switch that would allow it, and where to turn it on
    And nothing was fetched and nothing changed

  @security
  Scenario: the switch alone is not enough without a grant
    Given a member with remote serving control turned on
    And a caller holding no grant for it
    When the caller asks it to serve a different model
    Then it refuses
    And nothing was fetched

  @security
  Scenario: a grant for another node does not carry over
    Given a caller holding a serve-control grant for node A
    When it asks node B to serve a different model
    Then B refuses
    And the refusal does not reveal whether A would have accepted

  @security
  Scenario: the wire carries a model id, and has nowhere to put a URL
    Given a caller with the switch and a valid grant
    When it sends a request naming a download URL, a file path or a runtime
    Then the request is refused as malformed
    And the member fetched nothing
    And this is true even though the caller was fully authorised

  @security
  # FOUNDER RULING 2026-10-01: a member MAY fetch a model it does not have, when it is named by an
  # exact index id ("owner/repo:QUANT"). The id is resolved against the member's OWN configured
  # source - a lookup, not a search - and the member still judges fit with its own measurements
  # and still never takes a URL, a path or a runtime from the wire.
  Scenario: a model the member does not have is fetched only when named by an exact index id
    Given a caller with the switch and a valid grant
    When it names "owner/repo:QUANT" for a model this member does not have
    Then the member resolves that id against its own source, judges it, fetches it and serves it
    When it names anything else this member does not have - a bare name, a partial id, free text
    Then the member refuses, saying it does not know that model
    And it does not search anywhere for it

  @security
  Scenario: the confirmation records who asked and what they asked for
    Given a member that accepted a remote model change
    Then its history records the caller, the model id and when
    And that record is visible on the node's own page

  # =========================================================================
  # 2. THE FLOW
  # =========================================================================

  @edge
  Scenario: the owner switches a Jetson's model from the fleet screen
    Given a Jetson member with remote serving control on, and a grant held here
    When the owner picks a model for it
    Then the member judges whether it fits ITS hardware
    And it fetches the model if it does not have it
    And it serves it, and the new band is proven before anything routes to it
    And the owner sees the progress without leaving the screen

  @edge
  Scenario: the target's own fitness verdict decides, not the caller's
    Given a member with less memory than a model needs
    When the owner asks it to serve that model
    Then it refuses with its OWN arithmetic: what it has against what the model needs
    And the refusal is the same whether or not the caller thought it would fit

  @edge
  Scenario: a fetch on a member is visible while it happens
    Given a member fetching a model it was asked to serve
    When the owner looks at the fleet
    Then that member shows it is fetching, with the progress
    And it still says which model it is serving NOW, because it has not switched yet

  @edge
  Scenario: the old model keeps serving until the new one answers
    Given a member serving one model
    When it is asked to serve another and the new one fails to start
    Then the old one is still serving
    And the failure is reported to the owner with the reason

  @edge
  Scenario: a member that goes dark mid-fetch is not left half-switched
    Given a member fetching a model
    When it goes dark before it finishes
    Then the fleet shows the fetch as interrupted, not as done
    And when it returns it is serving whatever it was serving before
    And the partial file is resumed or discarded, never served

  # =========================================================================
  # 3. INDEPENDENCE - the Edge is an option, not a dependency
  # =========================================================================

  @cli
  Scenario: a machine with no Edge at all still does the whole flow
    Given a machine that has never enrolled on any Edge
    When its owner browses, fetches and serves a model
    Then all of it works
    And nothing asks for an authority, a grant or a network beyond the model fetch

  @cli
  Scenario: the same command works at the machine and across the Edge
    Given a member reachable on the Edge
    Then the command to change its model differs from the local one only by naming the node
    And the local form needs no grant, because it is the owner's own machine at their own keyboard
