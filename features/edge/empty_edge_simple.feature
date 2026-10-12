# STATUS: PROPOSED 2026-10-11 (awaiting founder approval)
# BUILD STATUS: NOT BUILT.
#
# THE SIMPLE EMPTY EDGE: what [3] EDGE (and the console's Edge view) shows on a machine that is
# in no Edge yet. Today it lists THIS MACHINE "not enrolled · roger edge enroll <name>",
# AUTHORITY "Core · enrolling a new node needs the network ... not logged in · roger login
# first, or roger edge authority local <name>", DISCOVERY, ADD A NODE, "press e to set up",
# an "a adopt" footer and a ROGER·EDGE ON AIR banner, all while nothing is set up. The founder
# approved this target (2026-10-11), reached from the welcome card's Edge line
# (features/onboarding/welcome_card.feature):
#
#   YOUR EDGE · not set up yet
#   Roger Edge links your own devices (laptops, Pis, phones) so they
#   can use each other's models, on your network.
#
#   e  start an Edge here        this machine becomes the authority
#   j  join an Edge you run      paste an invite, or ask by address
#
#   scanning this network while this screen is open (every 30s)
#   2 roger machines seen nearby · none in an Edge yet
#
# A rendered mockup with the real palette: docs/welcome-and-empty-edge-mockups.md and
# docs/empty-edge-mockup.png.
#
# GROUND TRUTH (read 2026-10-11): internal/tui/edge_view.go (the facts block, the "a adopt"
# footer), internal/tui/edge.go (the setup wizard on e: new Edge vs join, the name step, the
# authority choice), internal/edge/status.go (SelfStatus, JoinAgainstLine), cmd/rogerai
# joinpair.go / jointower.go (`roger join` invite parser and the address ask with the matching
# phrase), features/edge/scan_on_request.feature (scan only while this screen is open),
# internal/fleetview (the limoni Health/Fleet look: palette.go, brandBand, actionBar, eye).
#
# DECISIONS (each with a recommendation):
#   1. Where the Core-vs-local authority choice lives. RECOMMEND: inside `e` (start an Edge
#      here), as one line each with LOCAL selected by default: "on this network only (works with
#      no internet)" vs "through RogerAI (needs a login; reach it from anywhere)". The empty
#      screen itself never mentions Core, login or `roger edge authority local`.
#   2. The nearby count's wording when nothing is known yet. RECOMMEND: "scanning... no pass yet"
#      until the first pass completes; never "0 machines" before a pass.
#   3. Counting rule. RECOMMEND: count distinct roger machines advertising _rogerai._tcp on this
#      LAN (this machine excluded), and split "in an Edge" vs "not in an Edge" from their adverts.
#   4. `j` with an invite vs address. RECOMMEND: one prompt that accepts either; a roger:// string
#      goes to the invite parser, anything else is treated as an address (the same rule as
#      `roger join`).
#   5. The console. RECOMMEND: the console's Edge view mirrors this state with the same two
#      actions as buttons and the same nearby line.
#   6. Look. RECOMMEND: built from internal/fleetview's components and palette (the limoni look
#      the founder approved for Health and Fleet on 2026-09-26): the ROGER · EDGE brand row and
#      red rule, an amber eyebrow, paper key caps, the magic-eye scan glyph, the action bar.
#   7. The brand row's on-air lamp. RECOMMEND: no lamp at all on the empty state (the ◉ lamp is
#      the ON AIR signal); it appears only once this machine is enrolled.
#
# SUPERSEDES (approved, not edited here; proposed text):
#   features/edge/empty_edge.feature:51 "a fresh machine sees the three facts and a next command
#     for each" -> "a fresh machine sees YOUR EDGE · not set up yet, the two ways in (e start an
#     Edge here, j join an Edge you run) and the scan line; it names no command, no Core, no
#     login" (this file, section 1).
#   empty_edge.feature:76 "a machine rooted at Core with no login says what a login is for" ->
#     moved into `e`: the authority step says a RogerAI-rooted Edge needs a login and names it.
#   empty_edge.feature:111-122 (ADD A NODE and the enroll-against hint on the EMPTY screen) ->
#     shown only once this machine is in an Edge (unchanged there).
#   empty_edge.feature:151 / :167 (widths, NO_COLOR) -> re-pointed at the new empty layout.
#   empty_edge.feature:194 "the console's empty state shows the three facts and both ways to add
#     a node" -> "the console's empty state shows YOUR EDGE · not set up yet, start and join
#     buttons, and the nearby line".
#   empty_edge.feature:218 `roger edge` with nothing set up -> prints the same three lines and
#     names `roger edge setup` (start) and `roger join` (join).
#   features/edge/onboard.feature:52 "the empty screen keeps its facts and gains a way in" ->
#     "the empty screen offers e (start an Edge here) and j (join an Edge you run)".
#   onboard.feature:60 "pressing e opens the wizard ... asks whether to start a new Edge or join"
#     -> "pressing e opens the wizard at starting an Edge here (the choice step is gone: join has
#     its own key)".
#   onboard.feature:87 / :94 (the choice between two paths) -> retired from the e flow; their
#     explanations move to the e and j lines on the empty screen.
#   features/edge/console_view.feature:164 "an empty Edge explains itself" -> the console mirror
#     in section 6 below.
#   features/edge/scan_on_request.feature:102 "the DISCOVERY fact says it scans while this screen
#     is open" -> the scan line reads "scanning this network while this screen is open (every
#     30s)" with the nearby count under it.

Feature: A simple empty Edge screen

  Background:
    Given this machine is in no Edge (not enrolled and not an authority)

  # 1. What it says

  @tui
  Scenario: the empty Edge says what it is and offers two ways in
    When the owner opens [3] EDGE at 100 columns
    Then it shows "YOUR EDGE" and "not set up yet"
    And one sentence says Roger Edge links your own devices so they can use each other's models, on your network
    And it offers "e" with "start an Edge here" and "this machine becomes the authority"
    And it offers "j" with "join an Edge you run" and "paste an invite, or ask by address"

  @tui
  Scenario: the empty Edge names no commands, no Core and no login
    When the owner opens [3] EDGE
    Then it does not say "not enrolled"
    And it does not name "roger edge enroll", "roger edge authority local" or "roger login"
    And it does not mention Core

  @tui
  Scenario: nothing on the empty Edge looks like it is on air
    When the owner opens [3] EDGE
    Then no ON AIR banner or lamp is drawn
    And the footer offers no "a adopt"

  @tui
  Scenario: the footer offers exactly the two ways in and back
    When the owner opens [3] EDGE
    Then the action bar reads "e start an Edge here", "j join an Edge" and "esc back"

  # 2. The scan line

  @tui
  Scenario: the scan line says it scans only while the screen is open
    When the owner opens [3] EDGE
    Then it says "scanning this network while this screen is open (every 30s)"

  @tui
  Scenario: before the first pass completes the count is not invented
    Given no scan pass has completed yet
    When the owner opens [3] EDGE
    Then the second scan line says no pass has completed yet
    And it never says "0 roger machines"

  @tui
  Scenario Outline: the nearby count after a pass
    Given a completed pass saw <seen> other roger machines, <inedge> of them in an Edge
    When the owner opens [3] EDGE
    Then the second scan line says "<line>"

    Examples:
      | seen | inedge | line                                              |
      | 0    | 0      | no other roger machines seen nearby                |
      | 1    | 0      | 1 roger machine seen nearby · none in an Edge yet  |
      | 2    | 0      | 2 roger machines seen nearby · none in an Edge yet |
      | 3    | 2      | 3 roger machines seen nearby · 2 in an Edge        |

  @tui
  Scenario: discovery switched off says so, and names the switch
    Given discovery is switched off with ROGERAI_EDGE_DISCOVERY=0
    When the owner opens [3] EDGE
    Then the scan line says scanning is off and names the switch
    And the e and j lines are still offered

  @tui
  Scenario: leaving the screen stops the scan
    Given the owner opened [3] EDGE and a scan is running
    When the owner presses esc
    Then the scan stops within about a second (features/edge/scan_on_request.feature)

  # 3. e: start an Edge here

  @tui
  Scenario: e starts an Edge here with this machine as the authority
    When the owner presses e
    Then the setup opens at "start an Edge here"
    And it never asks whether to start or join (join has its own key)

  @tui
  Scenario: starting an Edge asks where its authority lives, one line each, local by default
    Given the owner pressed e
    Then the authority step offers "on this network only (works with no internet)" selected by default
    And it offers "through RogerAI (needs a login; reach it from anywhere)"
    And enter alone continues with the local choice

  @tui
  Scenario: choosing RogerAI without a login asks to log in there
    Given the owner pressed e and is not logged in
    When the owner chooses "through RogerAI"
    Then the step says a login is needed and offers it in place
    And esc returns to the authority step without changing anything

  @tui
  Scenario: starting an Edge here works with no internet
    Given the machine has no internet
    When the owner presses e and accepts the defaults
    Then this machine becomes the local authority and enrolls itself
    And no network beyond this LAN is contacted

  @tui
  Scenario: after starting, the full Edge view replaces the empty state
    Given the owner finished "start an Edge here" as "workshop"
    Then [3] EDGE shows the existing full view with "workshop" as this machine and the authority
    And it says how to add the next device: roger edge invite, then roger join on that device

  # 4. j: join an Edge you run

  @tui
  Scenario: j opens one prompt for an invite or an address
    When the owner presses j
    Then a prompt asks to paste an invite, or type the authority's address

  @tui
  Scenario: a pasted invite joins in one step
    Given the owner pressed j
    When the owner pastes a valid Edge invite and presses enter
    Then this machine joins that Edge using the same invite check as `roger join`
    And the full Edge view replaces the empty state

  @tui
  Scenario: an address asks to join and waits for approval with the matching phrase
    Given the owner pressed j
    When the owner types an authority's address and presses enter
    Then a matching phrase is shown with "the owner of that Edge should see the same phrase"
    And the screen waits for approval with a countdown and esc to cancel

  @tui
  Scenario: an approved ask lands in the full Edge view
    Given an ask by address is waiting
    When the authority's owner approves it
    Then this machine is a member and the full Edge view replaces the empty state

  @tui
  Scenario Outline: a join that does not succeed says why in one line and returns to the prompt
    Given the owner pressed j
    When <what happens>
    Then the prompt says "<says>" and the empty state is unchanged

    Examples:
      | what happens                                | says                                                    |
      | the invite was already used                 | that invite was already used: ask for a new one        |
      | the invite expired                          | that invite expired: ask for a new one                 |
      | the authority refused the ask               | the owner refused                                       |
      | the ask timed out                           | no answer in 2 minutes: ask again                       |
      | the address does not answer                 | nothing answered at that address                        |
      | the invite is for a Tower, not an Edge      | that is a Tower invite: use it from the dial (roger join) |
      | the authority's key changed                 | that is not the Edge you joined before                  |

  @tui
  Scenario: what is pasted is never echoed into logs
    When the owner pastes an invite at the j prompt
    Then the invite's code appears in no log or status line

  # 5. Once enrolled, nothing changes

  @tui
  Scenario: an enrolled machine never sees the empty state
    Given this machine is enrolled as "workshop"
    When the owner opens [3] EDGE
    Then the existing full Edge view is shown, unchanged
    And e and j have their existing meanings there (none for j)

  # 6. The console mirrors it

  @console
  Scenario: the console's Edge view mirrors the empty state
    When the console's Edge view is opened
    Then it shows "YOUR EDGE · not set up yet", the same sentence, and two buttons: start an Edge here, join an Edge you run
    And the same scan line and nearby count

  @console
  Scenario: the console's join accepts an invite or an address
    When the owner clicks join an Edge you run
    Then one field accepts an invite or an address, with the same outcomes as the TUI's j prompt

  @console
  Scenario: the console never shows the old facts block on the empty state
    When the console's Edge view is opened
    Then it does not say "not enrolled", "Core", "roger edge enroll" or "roger login"

  # 7. The CLI says the same

  Scenario: `roger edge` with nothing set up says the same three things
    When the owner runs `roger edge` on a terminal
    Then it prints "your Edge: not set up yet", the one-sentence explanation, and two next steps: `roger edge setup` (start an Edge here) and `roger join <invite or address>`

  # 8. Look

  @tui
  Scenario: the empty Edge is drawn with the Health and Fleet look
    When the owner opens [3] EDGE
    Then it uses internal/fleetview's palette (paper, ink, muted, the amber eyebrow, the one red)
    And the brand row reads "▟•▙ ROGER · EDGE" with the red rule under it
    And the keys are drawn as paper key caps
    And the scan line leads with the magic-eye glyph

  @tui
  Scenario Outline: the empty Edge reads well at every width it will meet
    When the owner opens [3] EDGE at <width> columns
    Then no line overflows
    And <what folds>

    Examples:
      | width | what folds                                                     |
      | 100   | nothing is cut                                                  |
      | 80    | nothing is cut                                                  |
      | 60    | the description column after each key is dropped before a label |

  @tui
  Scenario: the empty Edge in the windowshade
    Given the compact windowshade mode is on
    When the owner opens [3] EDGE
    Then it shows one line: "your Edge: not set up · e start · j join"

  @tui
  Scenario: the empty Edge under NO_COLOR
    Given NO_COLOR is set
    When the owner opens [3] EDGE
    Then key caps read as bracketed keys, the magic eye folds to an ASCII mark, and every word is still there

  @tui
  Scenario: a golden frame pins the look
    When the empty Edge is rendered at 100x20 through the limoni uitest harness
    Then the frame matches the committed golden snapshot for the empty Edge
