# ROGER EDGE - THE EMPTY EDGE ("I pressed 3 and I have set nothing up. Now what?").
#
# WHY THIS EXISTS
#
# FOUNDER QUESTION 2026-09-13: "what happens when going to tab 3 for EDGE screen and i haven't
# set anything up". The honest answer, measured on the built binary, was a screen that said
# "gentle-mongoose-93 is the only node on the Edge" and "run RogerAI on another machine on this
# network" - and said EXACTLY the same thing when the Edge host had failed to start at all.
#
# That screen is true and useless. A fresh machine has three facts that decide what the owner
# should do next, and the screen knows none of them:
#
#   THIS MACHINE  - is it enrolled (does it have a name on its Edge), or not yet?
#   AUTHORITY     - what roots this Edge: Core (needs a login and one online moment per node) or
#                   a designated machine (forms with no internet at all) - and is it THIS one?
#   DISCOVERY     - is this machine scanning its network, how often, when did it last look, and
#                   what did it find?
#
# And there are TWO ways to add a node, not one: a machine on this LAN appears as a CANDIDATE
# that the owner adopts, OR a machine anywhere enrolls against the same authority. The screen
# named only the first.
#
# THE RULE: an empty Edge is not a blank; it is a status. The screen says what is true about
# this machine, and names the next command for each fact that is not yet what the owner wants.
# It NEVER says "the only node" when the Edge host did not start - that is not an empty fleet,
# it is an unread one, and the two get different sentences.
#
# ALL THREE SURFACES say the same three facts: the TUI's [3] EDGE, the console's EDGE tab, and
# `roger edge` with nothing set up. One status, read once, handed to each.
#
# GROUND TRUTH: cmd/rogerai/edgehost.go (what the host already knows: enrolledName, the
# discovery engine, each pass's report), cmd/rogerai/edgeenroll.go (edgeShowAuthority - the
# authority wording the CLI already prints), internal/edgeauth (Descriptor.Names /
# NetworkLine), internal/tui/edge_view.go (edgeEmptyView), internal/webui/assets (the panel's
# edge-empty block), features/edge/topology_view.feature (the empty-Edge scenario this extends
# without changing: "the only node" and "ADD A NODE" stay).
#
# Tags pick the surface: @tui runs in internal/tui, @console in internal/webui, @cli in
# cmd/rogerai. Each suite drives its real surface with a real status; the status itself is a
# plain record the host fills from the same files the CLI reads.

Feature: An empty Edge tells the owner what is true about this machine and what to do next

  # =========================================================================
  # 1. THE THREE FACTS
  # =========================================================================

  @tui
  Scenario: a fresh machine sees the three facts and a next command for each
    Given a machine that is not enrolled, not logged in, rooted at Core, scanning its network
    When the Edge screen renders at 100 columns
    Then it still says this machine is the only node on the Edge
    And under THIS MACHINE it says "not enrolled" and names "roger edge enroll <name>"
    And under AUTHORITY it says Core and that enrolling a new node needs the network
    And it names "roger edge authority local <name>" as the way to form an Edge with no internet
    And under DISCOVERY it says it is scanning this network and how often

  @tui
  Scenario: an enrolled machine is named, and the enroll hint goes away
    Given a machine enrolled as "workshop" with no other node
    When the Edge screen renders at 100 columns
    Then under THIS MACHINE it says "workshop" and "enrolled"
    And it does not name "roger edge enroll" as a next step

  @tui
  Scenario: a local authority is named, and this machine says when it is the one
    Given a machine that is the local authority "shed" with 2 machines allowed
    When the Edge screen renders at 100 columns
    Then under AUTHORITY it names "shed" and says enrolling needs no network beyond this LAN
    And it says this machine IS the authority and that 2 machines may enroll against it
    And it names "roger edge authority allow <user key>" as the way to admit another

  @tui
  Scenario: a machine rooted at Core with no login says what a login is for
    Given a machine that is not logged in and rooted at Core
    When the Edge screen renders at 100 columns
    Then it says a login is needed to enroll under Core, and names "roger login"

  @tui
  Scenario: discovery reports its last pass
    Given discovery last ran 12 seconds ago and nothing answered
    When the Edge screen renders at 100 columns
    Then under DISCOVERY it says the last pass was 12s ago and nothing answered

  @tui
  Scenario: discovery reports what it found
    Given discovery last ran 5 seconds ago and saw 2 candidates
    When the Edge screen renders at 100 columns
    Then under DISCOVERY it says 2 candidates were seen

  @tui
  Scenario: discovery that is switched off says so, and names the switch
    Given discovery is off
    When the Edge screen renders at 100 columns
    Then under DISCOVERY it says "off" and names ROGERAI_EDGE_DISCOVERY
    And it does not claim to be scanning

  @tui
  Scenario: discovery that has not run yet does not invent a pass
    Given discovery is on and has not completed a pass
    When the Edge screen renders at 100 columns
    Then under DISCOVERY it says it is scanning and that no pass has completed yet
    And no age is shown for a pass that never happened

  # =========================================================================
  # 2. TWO WAYS TO ADD A NODE
  # =========================================================================

  @tui
  Scenario: both ways to add a node are named
    Given a machine that is not enrolled, not logged in, rooted at Core, scanning its network
    When the Edge screen renders at 100 columns
    Then under ADD A NODE it names running RogerAI on another machine on this network, adopted with a
    And it names enrolling another machine against this Edge's authority

  @tui
  Scenario: the enroll-against hint carries this machine's address when it is the authority
    Given a machine that is the local authority "shed" reachable at "http://192.168.1.10:8791"
    When the Edge screen renders at 100 columns
    Then it names "roger edge enroll <name> --authority http://192.168.1.10:8791"

  # =========================================================================
  # 3. AN UNREAD EDGE IS NOT AN EMPTY ONE
  # =========================================================================

  @tui
  Scenario: an Edge host that did not start is never drawn as "the only node"
    Given the Edge host did not start this run
    When the Edge screen renders at 100 columns
    Then it does not say this machine is the only node
    And it says the Edge host did not start this run and that the log says why
    And it names "roger edge list" as the way to read the last-known Edge

  @tui
  Scenario: a status that could not be read is shown as unread, never guessed
    Given the machine's Edge record cannot be read
    When the Edge screen renders at 100 columns
    Then it says the status could not be read, with the reason
    And it does not claim any authority or enrollment

  # =========================================================================
  # 4. THE SCREEN HOLDS UP
  # =========================================================================

  @tui
  Scenario Outline: the empty screen is correct at every width it will meet
    Given a machine that is the local authority "shed" reachable at "http://192.168.1.10:8791"
    And a terminal <cols> columns wide
    When the Edge screen renders
    Then no line exceeds <cols> columns
    And the three facts are still present

    Examples:
      | cols |
      | 120  |
      | 100  |
      | 80   |
      | 60   |
      | 40   |

  @tui
  Scenario: the empty screen is legible under NO_COLOR
    Given NO_COLOR is set
    And a machine that is not enrolled, not logged in, rooted at Core, scanning its network
    When the Edge screen renders at 100 columns
    Then the three fact labels are distinguishable as text alone

  @tui
  Scenario: the status is read once per frame like everything else
    Given a status that changes while the screen is open
    When two frames are rendered
    Then each frame is internally consistent
    And r re-reads the status

  # =========================================================================
  # 5. THE CONSOLE SAYS THE SAME
  # =========================================================================

  @console
  Scenario: the Edge read carries this machine's status
    Given a console over a machine that is not enrolled, rooted at Core, scanning every 30 seconds
    When the Edge data is read
    Then it carries a self status with enrolled false, authority "Core", discovery "scanning" and an interval of 30 seconds

  @console
  Scenario: the console's empty state shows the three facts and both ways to add a node
    When the console shell is served
    Then the empty state has a slot for THIS MACHINE, AUTHORITY and DISCOVERY
    And the Edge code fills them from the self status
    And it names both ways to add a node

  @console
  Scenario: the console shows a discovery that is off as off
    Given a console over a machine whose discovery is off
    When the Edge data is read
    Then the self status says discovery "off"

  @console
  Scenario: a console whose status cannot be read says so, and the fleet still draws
    Given a console over a machine whose Edge record cannot be read
    When the Edge data is read
    Then the self status carries the error
    And the nodes are still listed

  # =========================================================================
  # 6. THE CLI SAYS THE SAME
  # =========================================================================

  @cli
  Scenario: `roger edge` with nothing set up prints the three facts before the hints
    Given an owner who is not logged in
    When they run "roger edge"
    Then it prints that this machine is the only node
    And it prints THIS MACHINE as not enrolled
    And it prints AUTHORITY as Core
    And it prints the scan and adopt hints
    And it names "roger edge enroll" and "roger edge authority local"

  @cli
  Scenario: `roger edge` without a running host does not claim to be scanning
    Given an owner who is not logged in
    When they run "roger edge"
    Then it prints DISCOVERY as not scanning in this process
    And it names "roger edge scan" as the way to look now
