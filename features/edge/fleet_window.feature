# ROGER EDGE - THE FLEET WINDOW: SEE, MONITOR AND CONTROL EVERY UNIT FROM THE AUTHORITY.
#
# STATUS: APPROVED by the founder 2026-10-01 ("approve as written").
# BUILD STATUS (2026-10-01, wt/edge-limoni): BUILT in the rack (`roger edge fleet`, o from [3]) with
# the [3] cards and the console sharing its state words, EXCEPT: the phone's own vitals need the Roger
# app to send presence v2 (Edge side built and pinned; app change needs a Mac).
# HEALTH FOLDED IN (2026-10-01, wt/limoni-fold): the rack has no HEALTH tab any more. The FLEET tab
# carries it - temperature, memory and disk on a unit's row where it has room, and the selected
# unit's health (enter, or 6, the old HEALTH key, which now lands on FLEET) with every reading and
# its age, its history, its alarms, and the rules in force, edited in place; esc comes back to the
# rack on the same unit. Pinned by internal/fleetview/fold_test.go.
#
# FOUNDER DIRECTION 2026-10-01: "i want to test soon to install roger on a phone and on a pi device
# and i should be able to see them and monitor and control them from the authoritive edge machine.
# we did the research on what other edge systems work and look like ... come back with a better
# window."
#
# =============================================================================================
# WHAT IS WRONG WITH THE WINDOW TODAY (seen on the founder's own authority, 2026-10-01)
# =============================================================================================
#
#   - A unit is a card that says "offline · 1 alert". Not WHEN it was last heard, not what it is
#     running, not how hot or full it is. The rack (`roger edge fleet`) has bars; the [3] EDGE map
#     and list, which is where an owner actually is, have none.
#   - The authority drew itself twice - once as the hub, once as "another machine" under its raw id
#     - and the fleet screen titled it with its full 64-character id. (FIXED 2026-10-01, pinned.)
#   - "Online" is one dot. A unit can be heard (its heartbeat arrives) and still not be CONTROLLABLE
#     (its face cannot be dialled), and today those look the same until an action fails.
#   - No unit says which model it is serving, although the dispatch ladder routes on exactly that.
#   - A value from a unit that went dark two days ago is drawn the same as a value from four seconds
#     ago.
#   - Nothing can be asked to report NOW; the owner waits for the next heartbeat.
#
# =============================================================================================
# WHAT THE RESEARCH SAYS (2026-10-01; sources and gaps in the private design notes)
# =============================================================================================
#
# Fleet managers: balena, Mender, Portainer Edge, Tailscale, UniFi, Home Assistant, Golioth,
# Particle, AWS Greengrass, Azure IoT Edge, NVIDIA Fleet Command, Raspberry Pi Connect.
# Terminal monitors: k9s, lazydocker, btop, bottom, Glances, ctop, nvtop.
#
#   1. SPLIT LIVENESS BY CHANNEL. balena shows Heartbeat and Cloudlink separately, and a device with
#      one of the two is "Reduced Functionality" - which says WHAT you can do right now. Golioth
#      separates session, last report and settings sync.
#   2. AN AGE BESIDE EVERY HEALTH WORD. Greengrass warns its status may be stale and shows when it
#      was set; Particle "last heard"; Tailscale lastseen.
#   3. ASK NOW. Particle "refresh vitals" and "run health check"; Azure pings the edge agent.
#   4. PENDING IS A REAL ROW WITH ONE ACTION. Mender Pending > Accept, Portainer Waiting Room,
#      Tailscale "Needs approval", UniFi "Pending Adoption" > Adopt.
#   5. ACTIONS THAT CANNOT WORK NOW SAY WHY. Pi Connect shows Deploy only when the device is online
#      AND remote update is on.
#   6. UNAVAILABLE IS NOT UNKNOWN (Home Assistant): never draw a stale value as current.
#   7. STICKY HARDWARE WARNINGS: balena's undervoltage warning holds until reboot. (BUILT 2026-10-01
#      from the approved health.feature scenario that had never been implemented.)
#   8. ONE DENSE ROW PER UNIT WITH METERS IN IT, WORST FIRST (ctop, Glances browser, Beszel).
#   9. HISTORY BESIDE THE VALUE: a spike looks like a spike (nvtop, btop).
#  10. ENTER DRILLS DOWN, ESC COMES BACK, `/` FILTERS, KEYS ARE VISIBLE (k9s, lazydocker, ctop).
#  11. DESTRUCTIVE ACTIONS ARE SAFE ON PURPOSE: the default is No (k9s issues #319, #961, #2076,
#      where Enter on a dialog deleted).
#  12. THE MONITOR IS CHEAP AND CALM: bounded polling, bounded memory, restrained styling (Glances
#      CPU complaints, the btop leak).
#
# THE PROPOSED WINDOW (the [3] EDGE list view, and the rack it already shares a vocabulary with):
#
#   5 on your Edge · 3 on air · 1 trouble · 1 dark · 1 new              / filter · s sort · ? keys
#   NEW  found on this network, not on your Edge yet
# ▸ ◌  raspberrypi    MACHINE Pi 5        asked 12s ago · lan · roger 6.14.0           a adopt
#
#      ON YOUR EDGE                        state         temp   memory   disk     serving
#   ◉◉ house-or       MACHINE RTX PRO     you are here   58°   ▇▇▇▁┊    ▇▇▁▁┊    gpt-oss-120b +1
#   ◉◉ orin-garage    MACHINE Jetson Orin ON AIR 3s      61°   ▇▇▇▇┊    ▇▁▁▁┊    qwen3-8b Q4 · 8K
#   ◉◎ greenhouse     MACHINE Pi 4        TROUBLE 2s   ▲ 83° ⚡ ▇▇▁▁┊    ▇▇▇▇┊    cannot serve here
#   ◉◐ nina's phone   PHONE   iPhone      HEARD 9s · no control   ┈┈┈  ┈┈┈┈     ┈┈┈┈     no readings
#   ·· studio-mac     MACHINE Mac mini    DARK 6h        ┈┈┈   ┈┈┈┈     ┈┈┈┈     -
#   ────────────────────────────────────────────────────────────────────────────────────────
#   (the selected unit's readout: below the list at 120 columns, beside it at 200, a page at 80)
#
#   ◉◉ heard and controllable   ◉◐ heard, not controllable   ◉◎ trouble   ·· dark   ◌ asking
#   ┈ no reading (never a zero, never a stale value)   ┊ the rule's line inside a meter   ⚡ power
#
# A Raspberry Pi reports kind "host", so it reads MACHINE, with WHICH machine beside it (Pi 4) -
# edge.KindWord is the one source and a Pi is not to be "corrected" into a BOARD.
#
# WHERE IT LIVES (recommendation in the private brief): the Limoni FLEET tab (`roger edge fleet`)
# absorbs HEALTH, because they are two lists of the same units today; [3] EDGE stays the front door,
# its cards gain the same state words, and `o` opens the window on the selected unit.
#
# GROUND TRUTH: features/edge/health.feature (the facts and rules), presence.feature (what "heard"
# means), topology_view.feature (the map), inventory.feature (software), remote_model.feature
# (switching a model from here), local_models.feature (what "serving" means), node_identity.feature
# (no id is ever a name), internal/fleetview (the rack), internal/tui/edge_*.go (the [3] screen).
#
# Tags: @tui, @fleet (the rack), @web, @ios (needs roger-ios work), @edge (the fabric).

Feature: From the authority, every unit can be seen, judged and acted on without walking to it

  # =========================================================================
  # 1. ONE ROW PER UNIT
  # =========================================================================

  @tui @fleet
  Scenario: every unit is one row that answers what it is, whether it is there, and how it is
    Given an Edge with a PC, a Pi and a phone
    When the owner opens the fleet window
    Then each unit is one row with its name, its kind word, its link, when it was last heard, its
      load, its temperature, its memory, what it is serving and how many alerts stand on it
    And no row uses an id as a name

  @tui @fleet
  Scenario: the worst unit is at the top
    Given a fleet where one unit is dark, one has a firing alert and one is healthy
    Then by default the dark and alerting units are listed before the healthy one
    And within each group the order is stable, so a unit does not jump while it is being read
    And the owner can sort by name, kind, last heard or temperature instead

  @tui @fleet
  Scenario: the summary line says what needs the owner before any detail
    Then the first line counts the units, the ones that need a hand, and the alerts standing
    And it counts units waiting to be adopted

  @tui @fleet
  Scenario Outline: the window is correct at every width it will meet
    When the window is drawn at <width> columns
    Then every row is one line that does not wrap
    And the columns that do not fit are dropped in a fixed order, the name and the link last

    Examples:
      | width |
      | 80    |
      | 116   |
      | 160   |

  @tui @fleet
  Scenario: the window reads with no colour
    When it is drawn under NO_COLOR
    Then every state is still readable from its glyph and its word

  @tui
  Scenario: the [3] cards use the window's state words, and o opens the window on that unit
    Given the [3] EDGE map
    Then every member card says ON AIR, HEARD, TROUBLE or DARK with an age, never only "on your
      Edge"
    And the words come from one shared source, so the card, the window and the console agree
    When the owner presses o on a card
    Then the fleet window opens with that unit selected

  @tui @fleet
  Scenario: a unit nobody named is called by what it is, not by an id
    Given a unit adopted without a name
    Then it is called by its hardware word, or its kind word, with a short suffix only when two
      units would otherwise read the same
    And its id appears only on its own page, where every command that takes an id is described

  @tui @fleet
  Scenario: at wide widths every row carries its last hour
    When the window is at least 200 columns wide
    Then each row draws the last hour as on air or dark, so "has it been up" is answered beside
      "is it up"

  # =========================================================================
  # 2. LIVENESS IS TWO CHANNELS, AND EVERY VALUE HAS AN AGE
  # =========================================================================

  @tui @fleet @edge
  Scenario: heard and controllable are different, and the row says which
    Given a unit whose heartbeat arrives but whose face cannot be dialled
    Then it is drawn as heard but not controllable
    And the actions that need its face are shown unavailable, each saying why
    And the actions that need only the authority still work

  @tui @fleet
  Scenario: a value from a dark unit is drawn as old, never as current
    Given a unit that went dark two days ago
    Then its last values are drawn dimmed with their age
    And none of them is coloured as a live reading
    And a value the unit never reported is drawn as absent, which looks different from old

  @tui @fleet
  Scenario: last heard is always beside the state
    Then every row carries how long ago the unit was last heard
    And a unit heard within the last few seconds says "now" rather than a count

  # =========================================================================
  # 3. ASK NOW
  # =========================================================================

  @tui @fleet @edge
  Scenario: the owner can ask a unit to report now
    Given a selected unit that is controllable
    When the owner asks it to report
    Then it is asked over its face at once, not on the next heartbeat
    And the row shows the fresh values and how long the round trip took
    And a unit that does not answer within a short bound says so rather than spinning

  @tui @fleet
  Scenario: asking is bounded so a monitor cannot become the load
    When the owner asks the same unit repeatedly
    Then asks closer together than a floor are answered from the last one, saying so

  # =========================================================================
  # 4. A UNIT WAITING TO JOIN IS A ROW WITH ONE ACTION
  # =========================================================================

  @tui @fleet @web
  Scenario: a phone or Pi asking to join appears in the window with an adopt action
    Given a phone running the Roger app pointed at this authority
    Then it appears under "waiting to be adopted" with its kind and how it was found
    And adopting it is one key, asked once, default No
    And once adopted it moves to the fleet rows without the window being reopened

  @tui @fleet @edge
  Scenario: adoption shows its stages, and a stall says which stage and why
    When the owner adopts a unit
    Then its row shows each stage as it is seen: claim issued, certificate fetched, first presence
    And each stage carries its age
    And a stage that has not completed within a bound says which stage it is waiting on and what
      that unit must do, never only a spinner

  @tui @fleet
  Scenario: an adopted unit says which rules now watch it
    When a unit is adopted
    Then its page names the rules that cover it, including the defaults, so no alert needs setting
      up per unit

  # =========================================================================
  # 5. WHAT A UNIT IS SERVING
  # =========================================================================

  @tui @fleet @edge
  Scenario: a unit's row says which model it is serving, proven
    Given a unit serving a model that its serving probe has proven
    Then the row shows the band, the quantisation and the context length
    And a band that is advertised but not proven is not shown as serving

  @tui @fleet @edge
  Scenario: a unit that cannot serve here says so in a few words
    Given a Pi whose system libraries are too old for the runtime
    Then its serving cell says it cannot serve here
    And its page says why in one sentence and what it can still do

  @tui @fleet @edge
  Scenario: a unit fetching or switching a model shows the progress in its row
    Given a unit asked to serve a different model
    Then its row shows the fetch with bytes and rate, and still names what it serves NOW
    And when the switch completes the row names the new model only after it is proven

  # =========================================================================
  # 6. THE UNIT'S OWN PAGE
  # =========================================================================

  @tui @fleet
  Scenario: enter opens the unit, escape comes back to the same row
    When the owner opens a unit
    Then its page shows every reading with its age, its power, its software, its path and its
      history
    And escape returns to the window with the same unit selected

  @tui @fleet
  Scenario: the page draws the last hour as a line, so a spike looks like a spike
    Given a unit that has reported for the last hour
    Then its page draws temperature, load and memory over that hour
    And a gap where it was dark is drawn as a gap, not interpolated

  @tui @fleet
  Scenario: every action on the page says whether it can work right now
    Then each action is shown with its key
    And an action that cannot work now is drawn unavailable with the reason in a few words
    And nothing is hidden only because it cannot work now, so the owner learns what exists

  @tui @fleet @security
  Scenario: a destructive action is asked once and the safe answer is the default
    When the owner forgets a unit, stops a roger on it or switches its model
    Then the window asks once, naming the unit and what will happen
    And Enter alone answers No

  # =========================================================================
  # 7. FILTER, AND KEYS THE OWNER CAN SEE
  # =========================================================================

  @tui @fleet
  Scenario: slash filters the window by what an owner would type
    When the owner types a filter
    Then it matches names, kinds, tags and states
    And "kind:phone", "tag:greenhouse" and "state:dark" narrow by that field
    And the summary line says how many units the filter hides

  @tui @fleet
  Scenario: the keys in force are always on screen
    Then the footer names the keys that do something in the current view
    And question mark lists them all

  # =========================================================================
  # 8. THE PHONE AND THE PI, SPECIFICALLY
  # =========================================================================

  @ios @edge
  Scenario: a phone reports what a phone can honestly report
    Given an adopted phone running the Roger app
    Then it reports its battery level, whether it is charging, its thermal state and whether the
      app is in front
    And it reports no processor load or temperature figure, because iOS does not give one
    And the window draws what is absent as absent

  @ios @edge
  Scenario: a phone that the system has suspended is not drawn as broken
    Given a phone whose app the system has put in the background
    Then the window says it is asleep and when it was last heard
    And it does not raise an offline alert until the owner's offline rule says so

  # RULED 2026-10-13 (founder): "ok, enhance" the phone's vitals. Checked against this build: the
  # Edge side draws ASLEEP from a phone's last signed beat that said its app was in the background,
  # for the liveness window, then DARK (review 2026-10-01, unitword_test.go); the default offline rule
  # no longer calls it (ruled 2026-10-13, below). A phone with no battery reading yet drew the
  # temperature and memory placeholders on its row; it now draws its battery column absent and never
  # those columns (fleetview, TestAPhoneWithNoBatteryReadingHasNoProcessorColumns).
  # The next scenario is PROPOSED: it is an app change.

  # PROPOSED: today the app beats only while the Edge tab is in front and sends no beat as it leaves,
  # so its last beat says "app in front" and the window reads HEARD, then DARK, never ASLEEP.
  @ios @edge
  Scenario: a phone says it is going to sleep as its app leaves the front
    Given a phone member whose Edge tab is in front
    When the app moves to the background
    Then it sends one last signed v3 (or v2) presence with foreground false before it is suspended
    And the window reads ASLEEP with when it was last heard

  # RULED 2026-10-13 (founder): the DEFAULT offline rule skips a phone whose last beat said its app
  # went to the background. An owner's own offline rule (scoped to the phone, a tag it carries, or
  # kind:mobile) still judges it. Built: Evaluate (alert.go), TestTheDefaultOfflineRuleSkipsASleepingPhone.
  @ios @edge
  Scenario: the default offline rule does not call a sleeping phone offline
    Given a phone whose last beat said its app went to the background
    When it has not been heard for longer than the liveness window
    Then the default offline rule raises nothing for it
    And an offline rule the owner wrote for that phone, a tag it carries, or kind:mobile still does
    And a phone that went silent with its app in front is called offline as before

  # =========================================================================
  # 8b. POWER, FOR EVERY KIND OF NODE
  # =========================================================================

  # RULED 2026-10-13 (founder): "low power alert is probably valuable on all devices and edge systems
  # ... something we keep track of since many edge devices might be running on long term batteries".
  # Power is a first-class reading for EVERY kind of node, not a phone's alone:
  #   battery_pct (0..100), charging, source ("ac" | "battery" | "unknown"; solar and ups are not
  #   words: no operating system this reads reports them), low_power (the OS energy saver), and
  #   minutes_left when the OS estimates it. Absent is absent, never zero; a machine with no battery
  #   reports nothing, and mains is never claimed on its behalf.
  # A node that dials out says it in presence v3 (presence.feature); a node the authority dials says it
  # in its health readings (roger reads /sys/class/power_supply on Linux, pmset on macOS; Windows is
  # not read yet). Thermal and "app in front" stay a phone's: a host's temperature is its CPU reading.
  @edge
  Scenario Outline: any node reports its power, and the window draws it
    Given a <kind> whose power report says <report>
    Then its page carries a battery line reading "<line>"
    And nothing it did not report is drawn

    Examples:
      | kind               | report                                              | line                                           |
      | phone              | 64%, not charging, on battery, low power            | battery 64% · not charging · on battery · low power mode |
      | laptop host        | 57%, charging, on external power                    | battery 57% · charging · on external power     |
      | Pi on a UPS HAT    | 81%, not charging, on battery, about 2 hours left   | battery 81% · not charging · on battery · about 2h left |

  @edge
  Scenario: a machine with no battery reports no power at all
    Given a desktop host with mains power and no battery
    Then its health readings carry no power block
    And no power rule judges it

  @edge
  Scenario Outline: the default power rules, for every kind of node, with hysteresis
    Given the default rules
    When a node's power reads <reading>
    Then the call "<rule>" <happens>

    Examples:
      | reading                                          | rule             | happens                                          |
      | battery 19%, not charging                        | battery          | fires                                            |
      | battery 22% after firing at 19%                  | battery          | stands: it clears only at 25%                    |
      | battery 9%, not charging                         | battery_critical | fires                                            |
      | the same battery, plugged in and charging        | battery          | clears at once: a charging battery is never low  |
      | source battery on a host or a Pi                 | on_battery       | fires: external power was lost                   |
      | source battery on a phone                        | on_battery       | does not fire: phones run on battery by nature   |
      | low power mode on, any kind                      | low_power        | fires                                            |

  # Audit 2026-10-27: a power call held while the reading was missing never cleared, because "no
  # reading is no verdict" was the only rule. A node still heard that no longer reports that reading
  # (its battery came out, or its power report went stale while it answers) has nothing left to judge.
  @edge
  Scenario: a power call closes when a node that is still heard stops reporting that reading
    Given "pi" has the call "on_battery" standing
    When "pi" is heard again with no power report, or with one older than the liveness window
    Then the call is closed with a line saying "pi no longer reports its power"
    But while "pi" is dark the call stands, because a dark node is no evidence either way

  @edge
  Scenario: the owner sets power thresholds per node, per tag or per kind
    Given a rule "battery lt 40" scoped to the sensor "sensor-1"
    And a rule "on_battery gt 0" scoped to "kind:mobile"
    Then sensor-1 is judged at 40%, every other node at the default 20%
    And phones are now called when they leave external power

  @edge
  Scenario: power calls reach the phone through the trouble feed, with no new transport
    When a power rule fires or clears
    Then it is one line in the Edge's alert log, in words ("pi lost external power and is running on battery")
    And the phone's trouble feed carries it like any other call (trouble_feed.feature)

  @edge
  Scenario: a Pi's power, card wear and temperature are on its row and its page
    Given a Pi reporting under-voltage now
    Then its row carries the power mark
    And its page names the condition and what has happened since boot
    And a default rule warns while it is happening now

  # =========================================================================
  # 9. CALM
  # =========================================================================

  @tui @fleet
  Scenario: the window costs little and moves only when something changed
    Then it reads the fleet on a bounded interval
    And nothing animates when nothing changed
    And a long session does not grow without bound
