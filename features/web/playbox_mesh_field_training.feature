Feature: Condition-specific field training on the recorded Wave Mesh bench
  The workbench should turn a recorded model finding into a short, playable
  diagnostic exercise without pretending that a browser controls machinery or
  that the recording contains a proven root cause.

  Background:
    Given the Wave Mesh workbench is replaying committed recorded sensor windows
    And Wave Pico and Wave Nano are seated by default
    And the field panel is a local training simulation only
    And no field-panel action is sent to a sensor, controller, model, or broker

  Scenario: The empty left bay becomes the field training panel
    Then a panel labeled "4 · FIELD TRAINING" appears below the selected sensor gauge
    And it remains in the sensor column instead of covering the monitor
    And it shows the selected channel, unit when recorded, and current condition
    And it carries a persistent "SIMULATION ONLY · NO MACHINERY CONTROL" legend
    And on a narrow screen it remains after the sensor controls and before the model chain

  Scenario: The field panel has an honest idle state
    Given no training incident is active
    Then the panel says to deal an incident or choose a recorded fault pad
    And no diagnostic control claims to have measured or changed anything
    And the panel does not award progress

  Scenario: A recorded OK window needs no intervention
    Given the selected recorded condition is OK
    Then the field panel shows "NO INTERVENTION"
    And its diagnostic controls are unavailable
    And it offers another recorded window
    And it never implies that an earlier training action caused this OK recording

  Scenario: A fault panel explains why Wave Micro is the next upgrade
    Given a recorded fault incident is active
    And Wave Micro is not seated
    Then the condition-specific controls are visible but locked
    And the panel connects Pico to detection and Nano to doubt adjudication
    And it says Wave Micro adds the site context that unlocks the field exercise
    When I seat Wave Micro from either the monitor or the model chain
    Then the first field control unlocks
    And focus moves to that first control
    And the monitor checklist and field panel both show 0 of 3 checks complete
    And no recorded Micro inference is invented

  Scenario Outline: Every fault gets its own realistic training rig
    Given Wave Micro is seated
    And a recorded <condition> incident is active
    Then the field panel title is <title>
    And the first control is <first_control>
    And its successful interaction is <first_success>
    And the second control is <second_control>
    And its successful interaction is <second_success>
    And the final action is <handoff>
    And the panel labels the diagnosis clue as authored training context, not recorded evidence

    Examples:
      | condition | title                   | first_control                    | first_success                              | second_control                  | second_success                                   | handoff                                  |
      | stuck     | "FROZEN INPUT"          | "REFERENCE · CHANNEL/INDEPENDENT" | "INDEPENDENT reference moves"              | "TRACE POINT · SENSOR/WIRE/INPUT" | "INPUT remains held in this training scenario"  | "OPEN INPUT-CHANNEL WORK ORDER"          |
      | drifting  | "CALIBRATION OFFSET"    | "CAL POINT · 0/50/100%"          | "0, 50, and 100 percent visited in order"  | "COMPARE · PROCESS/NEIGHBOR/RECORD" | "CALIBRATION RECORD exposes the offset"         | "OPEN CALIBRATION WORK ORDER"            |
      | dropout   | "INTERMITTENT LOOP"     | "TIMELINE · DISPLAY/SOURCE"       | "SOURCE timestamps confirm the gaps"       | "TEST POINT · SUPPLY/FIELD/INPUT" | "FIELD terminal is intermittent in this scenario" | "OPEN FIELD-CONNECTION WORK ORDER"       |
      | noisy     | "NOISY SIGNAL PATH"     | "COMPARE · CHANNEL/INDEPENDENT"   | "INDEPENDENT reference stays stable"       | "INSTALLATION · MOUNT/SHIELD/ROUTE" | "SHIELD path carries the scenario clue"         | "OPEN SHIELD-ROUTING WORK ORDER"         |
      | railed    | "RANGE MISMATCH"        | "RANGE SOURCE · DISPLAY/RECORD"   | "APPROVED RECORD supplies the expected range" | "INPUT RANGE · LOW/MATCH/HIGH" | "MATCH aligns with the approved range"          | "OPEN CONFIGURATION-CHANGE REVIEW"       |

  Scenario: Training context is visibly separate from measured evidence
    Given a fault exercise is open
    Then recorded values, model outputs, margins, and traces retain their recorded labels
    And simulated clues use the label "TRAINING SCENARIO · AUTHORED CLUE"
    And the field panel does not call a simulated clue measured, recorded, or model-generated
    And the panel never names a physical root cause as proven

  Scenario: Correct controls advance the same checklist shown in the monitor
    Given Wave Micro is seated
    And a fault exercise has 0 of 3 checks complete
    When I complete the first condition-specific control correctly
    Then its lamp turns complete
    And the matching first monitor step turns complete
    And the second field control unlocks
    When I complete the second condition-specific control correctly
    Then its lamp turns complete
    And the matching second monitor step turns complete
    And the condition-specific work-order action unlocks
    When I activate the correct work-order action
    Then the third lamp and matching monitor step turn complete
    And "COMPARE WITH SEPARATE RECORDED OK" becomes available

  Scenario: The monitor checklist and controls stay in one visual flow
    Given a fault exercise is open
    Then every incomplete monitor step names the field control directly below it
    And the current switch, dial, or work-order action is operable inside the shift console
    And activating an incomplete monitor step moves focus to that visible control
    And completed monitor steps report the observed simulated clue
    And the left field panel mirrors the same training state for the bench overview
    And either copy of a control advances one shared local exercise

  Scenario: The playbook feels like an incident sequence rather than a form
    Given a fault exercise is open with Wave Micro seated
    Then the shift console states a condition-specific mission objective
    And it shows OBSERVE, ISOLATE, and HAND OFF as a three-beat route
    And only the current beat carries a live control
    And later beats visibly remain locked
    When a correct check completes
    Then its beat becomes a compact evidence receipt
    And the next beat and its control visibly wake up
    When all three checks complete
    Then the console says CASE READY and shows three collected evidence receipts
    And the final action says it will compare with a separate recorded OK window
    And it does not imply that the simulated checks repaired the machinery

  Scenario: A wrong setting teaches without fabricating damage
    Given a condition-specific field control is unlocked
    When I choose a wrong detent or test point
    Then the exercise does not advance
    And the recorded condition and model answer do not change
    And an amber result explains why that choice did not isolate this training scenario
    And the next useful evidence is named
    And no penalty, machine damage, or unsafe physical consequence is invented
    And I can try another setting immediately

  Scenario: Steps cannot be completed out of order
    Given a fault exercise has 0 of 3 checks complete
    Then the second control and work-order action are disabled
    When I complete the first control
    Then only the second control unlocks
    And the work-order action remains disabled
    When I complete the second control
    Then the work-order action unlocks

  Scenario: A work-order action is not a machinery command
    Given the condition-specific work-order action is unlocked
    When I activate it
    Then the panel records an authorized-maintenance handoff in the training state
    And it does not claim that a component was repaired, calibrated, replaced, bypassed, or restarted
    And it repeats that the site's procedure and authorized personnel control physical work
    And hazardous-energy isolation remains a prerequisite where servicing requires it

  Scenario: Verification finishes the game without rewriting history
    Given all three condition-specific checks are complete
    When I activate "COMPARE WITH SEPARATE RECORDED OK"
    Then the workbench advances to a different committed OK window for the same sensor type
    And the prior incident record remains unchanged
    And the monitor says "WORKFLOW VERIFIED · NOT PROOF OF REPAIR"
    And the field panel shows the incident-to-OK handoff
    And the completed-incident counter increases once
    And another activation cannot count the same incident twice

  Scenario: Choosing a different recorded incident resets unverified controls
    Given a fault exercise is partly complete
    When I choose another sensor, condition pad, or dealt window
    Then every unverified field selection and clue is cleared
    And the new selected record starts at 0 of 3 checks
    And no step completion crosses from one incident to another
    And the total count of already verified incidents is preserved

  Scenario: Removing Wave Micro removes the site-context gate
    Given a fault exercise is partly complete
    When I remove Wave Micro from the chain
    Then the condition-specific controls lock
    And the unverified progress is cleared
    And the recorded incident remains selected
    And the completed-incident total is preserved
    When I seat Wave Micro again
    Then the exercise restarts at its first control

  Scenario: The panel is fully operable without precision pointing
    Given a condition-specific field control is unlocked
    Then every dial exposes its detents and current value to assistive technology
    And Arrow keys move a dial by one detent
    And every switch and work-order action is a semantic button
    And focus styling is visible on the dark and light themes
    And touch targets remain usable on a narrow screen
    And reduced-motion mode removes arrival and lamp animations without hiding state

  Scenario: The game state stays local and disposable
    Given I have made field-panel selections
    Then those selections exist only in the current page session
    And reloading the workbench starts with no active field intervention
    And no training selection is submitted to the broker or written as a production event
