# ROGER EDGE - ONBOARDING: set up your Edge from the TUI (and the CLI), like a game.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-20: pressing [3] on a machine that has set nothing up shows only
# passive CLI hints. Make it INTERACTIVE and game-like, driven from the TUI itself and mirrored
# in the CLI: detect "not set up", ask whether to start a NEW Edge (this machine becomes the
# authority) or JOIN an existing one, name the machine, run the steps with a live handshake
# animation, and finish a step that was missed on the CLI. "super beautiful ... easy ... like a
# video game ... colorful on that 3 tab".
#
# WHAT THIS IS, AND IS NOT
#
# It is ADDITIVE. The empty screen's three facts (THIS MACHINE / AUTHORITY / DISCOVERY) and the
# named commands (features/edge/empty_edge.feature) STAY, verbatim - a wizard is a faster path,
# never a replacement for the honest status. The wizard adds one entry line and a sub-flow.
#
# It calls ONE source of truth. The enrollment orchestration lives in cmd/rogerai
# (edgeenroll.go: cmdEdgeEnroll, edgeDesignateLocal) and is security- and spec-load-bearing
# (enrollment.feature: enrollment is never automatic; a failed enrollment leaves the machine
# exactly as it was; a clean no-op re-enroll; one-authority refusal). The TUI does NOT
# re-implement it: the host exposes it through the Hooks seam (as LoginBegin/LoginPoll already
# do), so the wizard and `roger edge setup` drive the same code.
#
# THE HANDSHAKE ANIMATION is a legitimate exception to the fleet's "motion only from real
# events" rule (topology_view.feature): a real operation is in flight. It runs in the wizard's
# OWN operation-scoped loop, never the graph's pulse loop, starts when the enroll is dispatched,
# stops the instant it returns, and degrades to static phase lines under NO_COLOR,
# prefers-reduced-motion, a pipe, and narrow widths. It is the radio metaphor made literal: a
# carrier sweep listening for the authority, a lock when the certificate is issued, on air.
#
# GROUND TRUTH: internal/tui/edge.go (onEdgeKey, refreshEdge, the anim tick), internal/tui/
# edge_view.go (edgeEmptyView, the empty state this extends), internal/tui/tui.go (Hooks, the
# mode enum, modeConnecting as the staged-animation precedent, bubbles/textinput already in
# use), cmd/rogerai/edgeenroll.go (the ops the hooks wrap), cmd/rogerai/edge.go (the subcommand
# table + edgeStdin), internal/edgeauth (Designate, NewRequest, ErrUnknownMachine, enrollhttp),
# internal/client/identity.go (UserPubHex - the joiner's key to display).
#
# Tags: @tui runs in internal/tui (the real model, the real renderer, host closures over a
# fake enroll backend); @cli in cmd/rogerai (the real dispatch and the real enroll, over a
# local authority on an isolated config dir); @console in internal/webui.

Feature: A machine is set up onto its Edge through an interactive, game-like wizard, in the TUI and the CLI, calling the same enrollment as the commands

  # =========================================================================
  # 1. THE ENTRY - additive to the honest empty state
  # =========================================================================

  @tui
  Scenario: the empty screen keeps its facts and gains a way in
    Given a machine that is not enrolled, rooted at Core, scanning its network
    When the Edge screen renders at 100 columns
    Then it still says this machine is the only node on the Edge
    And it still names "roger edge enroll <name>"
    And it also invites the owner to press e to set this up interactively

  @tui
  Scenario: pressing e opens the wizard
    Given a machine that is not enrolled
    When the owner presses e on the Edge screen
    Then the setup wizard opens
    And it asks whether to start a new Edge or join an existing one
    And a step rail shows where the owner is in the flow

  @tui
  Scenario: e does nothing special once this machine is a member
    Given this machine is enrolled as "workshop"
    When the owner presses e on the Edge screen
    Then the wizard does not open
    And the screen is unchanged

  @tui
  Scenario: a step missed on the CLI is offered as a finish path
    Given this machine is the local authority "shed" but has not enrolled itself
    When the Edge screen renders at 100 columns
    Then it offers to finish setting up by enrolling this machine
    When the owner presses e
    Then the wizard opens straight at enrolling this machine, not at the choice

  # =========================================================================
  # 2. THE CHOICE
  # =========================================================================

  @tui
  Scenario: the choice is two clear paths with a sensible default
    Given the wizard is open at the choice
    Then one path is "start a new Edge - this machine becomes the authority"
    And the other is "join an existing Edge"
    And one is selected by default so enter alone makes progress

  @tui
  Scenario: the choice explains what each path means before the owner commits
    Given the wizard is open at the choice
    Then the new-Edge path says it needs no internet and roots the fleet here
    And the join path says it needs the authority's address and to be allowed on it

  @tui
  Scenario: esc at the choice leaves the wizard and returns to the Edge screen
    Given the wizard is open at the choice
    When the owner presses esc
    Then the wizard closes
    And the Edge screen is shown, unchanged

  # =========================================================================
  # 3. NAMING
  # =========================================================================

  @tui
  Scenario: the name step offers a default derived from this machine
    Given the wizard is open at the name step
    Then a default name is offered, not a blank field
    And the owner can accept it with enter or type their own

  @tui
  Scenario: a name that cannot be a node name is refused in place
    Given the wizard is at the name step
    When the owner enters "not a valid name!!"
    Then the wizard says why in a line, and does not advance
    And the owner can correct it without leaving the step

  @tui
  Scenario: esc at a later step goes back one step, not all the way out
    Given the wizard is at the name step, having chosen to start a new Edge
    When the owner presses esc
    Then it returns to the choice, keeping what was chosen
    And a second esc leaves the wizard

  # =========================================================================
  # 4. START A NEW EDGE (this machine becomes the authority)
  # =========================================================================

  @tui
  Scenario: starting a new Edge designates this machine and enrolls it, with no network
    Given the wizard is at the new-Edge name step
    When the owner names it "shed" and confirms
    Then the handshake runs and this machine is designated the authority and enrolled
    And no network was needed
    And the wizard reaches the done step naming this machine on its Edge

  @tui
  Scenario: the done step of a new Edge says how to add the next machine
    Given a new Edge was just created as "shed"
    Then the done step names this machine's address and the command another machine runs to join
    And it says the mode is LOCAL, formed with no internet

  @tui
  Scenario: a new-Edge handshake that fails leaves the machine exactly as it was
    Given the wizard is at the new-Edge name step
    And designating the authority will fail
    When the owner names it "shed" and confirms
    Then the wizard shows the failure in the backend's own words
    And it offers to try again
    And this machine is still not enrolled and roots no Edge

  # =========================================================================
  # 5. JOIN AN EXISTING EDGE
  # =========================================================================

  @tui
  Scenario: joining asks for the authority's address as well as a name
    Given the wizard is on the join path
    Then it asks for a name for this machine
    And it asks for the authority's address, shaped like http://host:port

  @tui
  Scenario: a successful join enrolls against the authority over the LAN
    Given the wizard is on the join path, named "bench", authority "http://192.168.1.10:8791"
    And that authority will accept this machine
    When the owner confirms
    Then the handshake runs and this machine is enrolled against that authority
    And the wizard reaches the done step

  @tui
  Scenario: joining before being allowed shows this machine's key and the exact allow command
    Given the wizard is on the join path, named "bench", authority "http://192.168.1.10:8791"
    And that authority does not yet allow this machine
    When the owner confirms
    Then the wizard says this machine is not allowed on that Edge yet
    And it shows this machine's user key
    And it shows the exact command to run ON THE AUTHORITY: roger edge authority allow <this key>
    And it offers to retry once that is done

  @tui
  Scenario: retrying after being allowed completes the join
    Given the wizard is showing the allow instructions for "bench"
    And the owner has since allowed this machine on the authority
    When the owner retries
    Then the handshake runs and this machine is enrolled
    And the wizard reaches the done step

  @tui
  Scenario: a refusal that is not "allow me" is shown plainly, never as the allow step
    Given the wizard is on the join path, named "bench", authority "http://192.168.1.10:8791"
    And that authority refuses for another reason
    When the owner confirms
    Then the wizard shows the refusal in the authority's own words
    And it does NOT show the allow-this-key instructions
    And it offers to try again

  @tui
  Scenario: an unreachable authority says so, not "you are not allowed"
    Given the wizard is on the join path, authority "http://192.168.1.10:8791"
    And that authority cannot be reached
    When the owner confirms
    Then the wizard says the authority could not be reached, with the address
    And it offers to try again

  # =========================================================================
  # 6. THE HANDSHAKE ANIMATION
  # =========================================================================

  @tui
  Scenario: the handshake is a carrier sweep that locks when the certificate is issued
    Given a join is in flight
    When frames render while it is in flight
    Then a carrier sweep moves across the band
    When the enrollment returns success
    Then the sweep locks and the machine's place on the Edge resolves in

  @tui
  Scenario: the handshake animation runs in its own loop, never the fleet's pulse loop
    Given a handshake is in flight
    Then the fleet graph's pulses are untouched
    And when the handshake ends its loop stops, so a still fleet stays still

  @tui
  Scenario: the handshake never outlives the operation
    Given a join is in flight
    When the enrollment returns
    Then the animation stops on the next frame
    And nothing keeps ticking once there is nothing in flight

  @tui
  Scenario: under NO_COLOR the handshake is phase lines, not a still bar
    Given NO_COLOR is set
    And a join is in flight, then succeeds
    When the wizard renders
    Then it shows the phases as plain lines: contacting the authority, then enrolled
    And the frame carries no ANSI

  @tui
  Scenario: under reduced motion the handshake shows the phases without the sweep
    Given the compact mode is on
    And a join is in flight
    When the wizard renders
    Then the phase is stated without a travelling sweep

  @tui
  Scenario: the wizard holds up at every width
    Given the wizard is on the join path
    When it renders at 120, 100, 80 and 60 columns
    Then no line exceeds the width at any of them
    And the choice, the fields and the hint line are readable at each

  # =========================================================================
  # 7. THE SAME ENROLLMENT AS THE COMMANDS
  # =========================================================================

  @tui
  Scenario: the wizard enrolls through the host, not its own copy of the logic
    Given a machine whose host wires the enrollment hooks
    When the wizard completes a new Edge
    Then it called the host's designate-and-enroll, the same the CLI calls
    And a machine whose host wired no such hooks is told setup is unavailable here, rather than half-doing it

  @tui
  Scenario: enrollment stays never-automatic
    Given the wizard is open
    Then nothing is designated or enrolled until the owner confirms a step
    And leaving the wizard before confirming changes nothing on this machine

  # =========================================================================
  # 8. THE CLI MIRROR
  # =========================================================================

  @cli
  Scenario: roger edge setup is in the help and starts the interactive flow
    When they run "roger edge setup --help"
    Then it describes setting up this machine's Edge interactively
    When they run "roger edge" with nothing set up
    Then the output points at "roger edge setup" as well as the individual commands

  @cli
  Scenario: roger edge setup starts a new Edge end to end
    Given an owner at a machine that has set nothing up
    When they run "roger edge setup" and choose to start a new Edge named "shed"
    Then this machine is designated the authority and enrolled
    And it prints how another machine joins

  @cli
  Scenario: roger edge setup joins, and guides the allow step when not yet allowed
    Given an owner whose machine is not yet allowed on the authority
    When they run "roger edge setup", choose to join, name it "bench" and give the authority address
    Then it prints this machine's user key and the allow command to run on the authority
    And it exits without pretending the machine joined

  @cli
  Scenario: roger edge setup declines cleanly when it cannot prompt
    Given no interactive input is available
    When they run "roger edge setup"
    Then it explains it needs a terminal, and names the non-interactive commands to use instead
    And it changes nothing

  # =========================================================================
  # 9. THE CONSOLE KNOWS SETUP IS A TERMINAL THING (for now)
  # =========================================================================

  @console
  Scenario: the console's empty Edge points at the terminal for setup
    Given a console over a machine that is not enrolled
    When the Edge data is read
    Then the panel says setting up the Edge is done from the terminal for now
    And it names roger edge setup
