# ROGER EDGE - ONBOARDING IN THE BROWSER: set up your Edge from the console (PROPOSED).
#
# STATUS: PROPOSED, awaiting founder approval. NOT IMPLEMENTED. This file is the spec for the
# WebUI half of the onboarding the founder asked for ("do all of this from the TUI as well and
# the WebUI, so lets start with the TUI"). The TUI + CLI halves are BUILT and green
# (features/edge/onboard.feature); this is the next slice.
#
# WHAT IT CHANGES, AND WHY IT NEEDS APPROVAL
#
# It SUPERSEDES the one @console scenario in onboard.feature ("the console's empty Edge points at
# the terminal for setup"). That scenario deliberately said setup was a terminal thing FOR NOW;
# this proposal is the "now" ending. When this is approved and built, that @console scenario is
# replaced by the @web scenarios here (the console stops pointing at the terminal and runs the
# wizard itself), and SelfStatus.SetupConsoleLine / edgeFacts.Setup are removed.
#
# It is SECURITY-LOAD-BEARING, which is why it stops at the founder gate before any code:
#   - It adds POST /api/edge/setup, an endpoint that ENROLLS THIS MACHINE. Enrollment is the
#     spec's most guarded ceremony (enrollment.feature: never automatic; a failure leaves the
#     machine exactly as it was; one Edge, one authority). A browser-reachable path to it must
#     inherit every one of those guarantees.
#   - The console already runs ON the machine (localhost + token guard, as /api/edge/adopt does),
#     so the endpoint runs the SAME server-side edgeSetupHooks the TUI wizard drives - the browser
#     never signs or holds a private key. The user key SHOWN for the allow step is the PUBLIC half
#     only (as `roger account` already prints it).
#
# GROUND TRUTH once approved: internal/webui/edge.go (the /api/edge handlers, the localhost+token
# guard, edgeSnapshot/writeEdge), internal/webui/assets/console.{html,js,css} (the EDGE tab, the
# empty-facts panel this extends), cmd/rogerai/edgesetup.go (edgeSetupHooks - the SAME backend,
# exposed to the console the way EdgeHooks.Adopt already is), features/edge/onboard.feature (the
# TUI/CLI behaviour this mirrors word-for-word where it can).
#
# Tags: @web runs in internal/webui (the real Server over httptest, the real localhost+token
# guard, the shipped console assets, host closures over a fake setup backend of the edgeSetupHooks
# shape - exactly as the @tui suite fakes it and the console EDGE-tab suite drives /api/edge).

Feature: A machine is set up onto its Edge from the browser console, through the same never-automatic enrollment the terminal uses

  # =========================================================================
  # 1. THE ENTRY - additive to the console's honest empty panel
  # =========================================================================

  @web
  Scenario: the empty console panel offers to set up the Edge instead of pointing at the terminal
    Given a console over a machine that is not enrolled
    When the Edge data is read
    Then the panel still shows THIS MACHINE, AUTHORITY and DISCOVERY
    And it offers a "Set up your Edge" action in the browser
    And it no longer says setup is only a terminal thing

  @web
  Scenario: a machine already a member shows no setup action
    Given a console over a machine enrolled as "workshop"
    When the Edge data is read
    Then no setup action is offered

  # =========================================================================
  # 2. THE ENDPOINT - POST /api/edge/setup, guarded like every console mutation
  # =========================================================================

  @web
  Scenario: setup is POST-only
    Given a console over a machine that is not enrolled
    When a GET is made to /api/edge/setup
    Then it is rejected as method-not-allowed
    And nothing on the machine changed

  @web
  Scenario: setup requires the console token and localhost, like adopt
    Given a console over a machine that is not enrolled
    When a setup request arrives without the console token
    Then it is refused
    And nothing on the machine changed

  @web
  Scenario: nothing is enrolled until the browser submits a setup request
    Given a console over a machine that is not enrolled
    When the Edge data is read but no setup request is made
    Then the backend's designate and enroll were never called

  # =========================================================================
  # 3. START A NEW EDGE (this machine becomes the authority)
  # =========================================================================

  @web
  Scenario: starting a new Edge from the browser designates and enrolls this machine, no network
    Given a console over a machine that is not enrolled
    When a setup request starts a new Edge named "shed"
    Then the backend designated this machine and enrolled it, the same the CLI calls
    And the response says this machine is on its Edge, mode LOCAL
    And it names the command another machine runs to join

  @web
  Scenario: a new-Edge failure leaves the machine exactly as it was
    Given a console over a machine that is not enrolled
    And designating the authority will fail
    When a setup request starts a new Edge named "shed"
    Then the response reports the failure in the backend's own words
    And this machine is still not enrolled and roots no Edge

  @web
  Scenario: a name that cannot be a node name is refused before anything is written
    Given a console over a machine that is not enrolled
    When a setup request starts a new Edge named "not a valid name!!"
    Then the response says why the name is not usable
    And the backend's designate and enroll were never called

  # =========================================================================
  # 4. JOIN AN EXISTING EDGE
  # =========================================================================

  @web
  Scenario: joining from the browser enrolls against the authority over the LAN
    Given a console over a machine that is not enrolled
    And an authority at "http://192.168.1.10:8791" that will accept this machine
    When a setup request joins as "bench" against that authority
    Then the backend enrolled this machine against that authority
    And the response says this machine joined

  @web
  Scenario: joining before being allowed returns this machine's key and the allow command
    Given a console over a machine that is not enrolled
    And an authority at "http://192.168.1.10:8791" that does not yet allow this machine
    When a setup request joins as "bench" against that authority
    Then the response says this machine is not allowed on that Edge yet
    And it returns this machine's PUBLIC user key
    And it returns the exact command to run on the authority: roger edge authority allow <that key>
    And this machine did not join

  @web
  Scenario: a refusal that is not "allow me" is returned plainly, never as the allow step
    Given a console over a machine that is not enrolled
    And an authority at "http://192.168.1.10:8791" that refuses for another reason
    When a setup request joins as "bench" against that authority
    Then the response reports the refusal in the authority's own words
    And it does NOT return the allow-this-key instructions

  @web
  Scenario: an unreachable authority says so, with the address, not "you are not allowed"
    Given a console over a machine that is not enrolled
    And an authority at "http://192.168.1.10:8791" that cannot be reached
    When a setup request joins as "bench" against that authority
    Then the response says the authority could not be reached, with the address
    And it does NOT return the allow-this-key instructions

  # =========================================================================
  # 5. THE BROWSER FLOW (the shipped console assets)
  # =========================================================================

  @web
  Scenario: the setup action opens a modal that asks new-or-join with a step rail
    Given the shipped console assets
    Then the EDGE tab has a setup modal with a new-Edge choice and a join choice
    And the join choice reveals a field for the authority's address
    And a handshake indicator is shown while a setup request is in flight

  @web
  Scenario: the handshake indicator is not a bare spinner that lies
    Given the shipped console assets
    Then the handshake indicator is shown only while a setup request is actually in flight
    And it resolves to the done, not-allowed or failed result the endpoint returned

  @web
  Scenario: the browser never holds private key material
    Given the shipped console assets
    Then the browser sends only the chosen mode, name and authority address
    And the only key it ever displays is the PUBLIC user key the endpoint returned for the allow step

  # =========================================================================
  # 6. THE SAME ENROLLMENT AS EVERY OTHER SURFACE
  # =========================================================================

  @web
  Scenario: the console drives the host's enrollment, not its own copy
    Given a console whose host wires the setup backend
    When a setup request completes a new Edge
    Then it called the host's designate-and-enroll, the same the TUI and CLI call
    And a console whose host wired no setup backend reports setup is unavailable, rather than half-doing it
