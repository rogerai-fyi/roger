# Founder-approved direction, 2026-08-15. This feature supersedes the v20
# verdict-only face and WHO'S WATCHING policy presentation. The Wave models
# always inspect the replay; response routing happens after their finding.
Feature: The Wave Mesh workbench explains a run at a glance
  In order to understand what each Wave tier contributes without learning a dashboard
  As an engineer trying the recorded mesh simulation
  I want the TV to lead with the model result, its evidence, and the route it took

  Scenario: Staffing never rewrites the model result
    Given a recorded sensor window has passed through the selected chain
    When I change the response from log only to human review or policy queue
    Then the model answer and status colour stay unchanged
    And only the destination of the finding changes

  Scenario: A missed fault remains a missed fault
    Given the recorded truth is a fault
    And every recorded model in the chain answered incorrectly
    Then the monitor is red and says MODEL LIMIT
    And it places the model answer beside the recorded truth
    And it says the replay label, not a model, exposed the miss
    And it explains why Pico did not ask Nano
    And it shows Nano's recorded counterfactual when Nano was not called
    And it names an independent reference, a site invariant, or an audit of all-clear decisions as the way to challenge the miss
    And the Wave Micro upgrade opens that independent audit exercise instead of claiming a new model answer
    And it explains that no knob setting catches this recorded read
    And it never turns the miss green as the best the chain can do

  Scenario: The set is useful before I open the detail
    Then the CRT shows the selected channel and condition
    And it plots the actual recorded samples
    And it shows the final model answer and the model that supplied it
    And every seated model shows what it answered, escalated, observed, or could not run
    And the active model route remains visible in chain order
    And it scores caught and missed faults across the recorded fleet
    And semantic colour is an accent on neutral glass rather than a full-screen flood
    And the television is the largest instrument on the bench
    And its verdict, model responses, and detailed trace remain readable without zooming

  Scenario: The first run leaves somewhere useful to grow
    Given I open the recorded mesh workbench
    Then Wave Pico and Wave Nano are already seated in ladder order
    And the screen shows the channel answer and escalation handoff without setup
    And Wave Micro remains visibly available as the next mission upgrade
    And I can still remove or replace any tier in the chain

  Scenario: The recorded trace feels alive without pretending to be live
    Then the full recorded waveform stays visible
    And a phosphor sweep travels across that same waveform
    And the screen continues to say RECORDED REPLAY
    And reduced-motion mode shows the complete waveform without the sweep

  Scenario: The run trace opens on purpose
    Then the glass visibly says PRESS SCREEN and OPEN FULL MODEL OUTPUT
    And it says that raw data, every model, and fleet detail are inside
    And the entire engraved glass opening is one activation target
    And the decorative tilt never moves or narrows that activation target
    When I activate the screen by click, Enter, or Space
    Then the detailed cascade opens inside the same set
    And the control exposes aria-expanded and aria-controls
    And resting or moving a pointer over the glass does not open it
    And the hardware key says DETAILS before opening and ANSWER inside the detail

  Scenario: A mission upgrade visibly unlocks the next play
    Given a recorded fault is dealt with Wave Pico and Wave Nano seated
    Then the shift console shows Pico detecting, Nano checking, and Micro still needed
    When I seat Wave Micro from the shift console
    Then the safe site-triage checklist replaces the locked prompt
    And focus lands on the first available check
    And no recorded Micro inference is invented

  Scenario: The response control says what it controls
    Then the workbench says MODELS WATCHING
    And it asks what happens AFTER A FINDING
    And I can choose LOG ONLY, HUMAN REVIEW, or POLICY QUEUE
    And the default is LOG ONLY

  Scenario: The game layer stays honest
    Then progress is computed from the 120 committed records
    And a tuning opportunity is counted only when the recorded senior had the right answer
    And no random score, invented prediction, or simulated plant reading is shown

  Scenario: A case deals recorded cards rather than fabricating events
    Then the case board shows CATCH, TRACE, and CLOSE as the game loop
    And it counts the fault cards and independent OK checks in the committed replay
    When I press START NEXT CASE
    Then the selector moves to one of the committed condition records
    And an OK card requires no response
    And a fault draw opens an incident with the recorded model result
    And no timer changes the machine while I am reading it

  Scenario: A fault becomes a playable clue route
    Given the current committed record is a fault
    Then the monitor asks who should hear the signal and why
    And without Wave Micro it offers Micro as the site-context upgrade
    When I add Wave Micro
    Then the monitor opens a three-clue case kit
    And its folded evidence note says those clues are authored for the game rather than model output
    And it lists a verification tool, a context check, and an authorized maintenance handoff
    And it never issues a machine command or claims to know the root cause

  Scenario: A missed fault is explained without inventing model prose
    Given the recorded model answer disagrees with the committed fault label
    Then the monitor explains which recorded signal clue can expose the miss
    And it labels the explanation BENCH EXPLANATION · RECORDED SIGNAL · NOT MODEL OUTPUT
    And it never leaves me with only "no model-authored explanation"

  Scenario Outline: Every tier receives the same current-case packet
    Given a committed sensor condition is selected
    When I seat <tier>
    Then its stage names the active sensor, condition, and record
    And it shows the actual Pico-to-Nano handoff for that read
    And it understands the Pico-to-Exa family map regardless of which model answered
    And it gives that tier a distinct contribution to the current case
    And it carries the condition-specific field mechanic

    Examples:
      | tier       |
      | Wave Pico  |
      | Wave Nano  |
      | Wave Micro |
      | Wave Giga  |
      | Wave Tera  |
      | Wave Peta  |
      | Wave Exa   |

  Scenario: Higher-tier intelligence stays honest about the evidence ceiling
    Then Pico and Nano contributions are labelled recorded model output
    And Micro and Giga contributions are labelled bench synthesis from committed records
    And Tera, Peta, and Exa contributions are labelled role simulation beyond this one-plant replay

  Scenario: Site and plant intelligence follows the active sensor case
    Given MOTOR CURRENT and STUCK are selected
    When I inspect Wave Micro
    Then its first comparison is the selected machine and its recorded STUCK card
    And the remaining comparisons are MOTOR CURRENT cards from that machine's site
    And every comparison names its recorded condition and current-chain outcome
    And it does not lead with a site-wide leaderboard of unrelated channels
    When I change the sensor or condition
    Then the comparison set changes with the active case

  Scenario: The model ladder is a playable decision rather than a caption
    Given a recorded fault incident is active
    Then the shift console asks one concrete question about the current chain result
    And it offers model or threshold moves whose roles are visible before I choose
    When I choose a move that does not fit the evidence
    Then the case does not advance
    And immediate feedback explains the capability mismatch
    When I choose the move supported by the recorded handoff
    Then the next case beat unlocks or the chain changes visibly
    And no model output or successful diagnosis is invented

  Scenario: Different chain outcomes teach different moves
    Given the dealt card comes from the committed replay
    Then an unheard Pico escalation asks for Wave Nano
    And a recoverable confident Pico miss asks me to raise the handoff floor
    And a Pico or Nano catch asks me to identify who made the call before adding site context
    And a Pico-and-Nano blind spot asks for Wave Micro's independent site audit
    And each recommendation is computed from the selected record, chain, and threshold

  Scenario: The first shift teaches one ladder idea at a time
    Given I have not dealt a shift card before
    Then the first three deals are chosen from committed records
    And they introduce a Nano handoff, a shared Pico-and-Nano blind spot, and a recoverable threshold miss
    And each case still uses its actual sensor values, model answers, margins, and label
    And later deals shuffle the remaining committed cards

  Scenario: Each field control asks a diagnostic question
    Given Wave Micro has unlocked a fault exercise
    Then OBSERVE asks what independent evidence would confirm the symptom
    And ISOLATE asks where the disagreement enters the signal path
    And HAND OFF asks for the safe next action
    And every fault condition has different questions, choices, and feedback
    And a correct answer produces an evidence receipt rather than a generic checkmark

  Scenario: A tier tab leads with that tier rather than the training console
    Given a fault case and Wave Micro are active
    When I choose the WAVE MICRO monitor tab
    Then the first card is Wave Micro's current-case brief
    And the Shift Console remains on ALL rather than obscuring the selected tier

  Scenario: Returning to OK releases a batch without inventing causality
    Given I completed every safe diagnostic step
    When I choose CLOSE CASE WITH A HEALTHY READ
    Then the selector advances to a separately committed OK window of the same sensor type
    And the monitor counts one completed training incident
    And it says the replay does not prove those steps repaired the prior machine

  Scenario: The factory challenge is the front door
    Given I open the Wave Mesh for the first time
    Then I see one challenge: ship three reliable batches
    And I see CATCH IT, TRACE IT, and CLOSE IT before any engineering controls
    And the only primary action is START A MYSTERY CASE
    And I may choose the full workbench as a secondary sandbox

  Scenario: A bad signal stops the production line
    Given a mystery case is active
    Then the production belt shows RAW FEED, PROCESS CELL, QUALITY GATE, and PACKOUT
    And packout is held until the signal case is complete
    And the active sensor and recorded condition appear on the process cell
    And nothing advances on a timer

  Scenario: The Wave ladder becomes a buildable defense line
    Then the factory shows model slots ON MACHINE, at the LINE GATEWAY, in SITE CONTROL, and in PLANT CONTROL
    And those slots map to Pico, Nano, Micro, and Giga scope
    And the factory asks me to place or identify the tier supported by the current recorded handoff
    When I choose the wrong model placement
    Then no model is added and the board explains the scope mismatch
    When I choose the supported placement
    Then that model appears online in its real deployment layer
    And Tera, Peta, and Exa remain visible as locked enterprise, region, and flagship-lab contracts
    And the board labels those tiers beyond this one-plant replay

  Scenario: The workbench is the inspection room behind the game
    Given a production batch is held
    Then the dense sensor wall, technical notes, prompt, and CRT detail are hidden
    When I choose INSPECT THE RECORDED SIGNAL ON THE CRT
    Then the existing evidence monitor and clue controls open below the factory
    And I can return to the factory without losing the case

  Scenario: Solving a case has a production payoff
    Given I built the supported model route and solved all three clues
    When I close the case with a separate healthy read
    Then the game returns me to the factory
    And the quality gate says PACKOUT READY
    When I choose RELEASE TO PACKOUT
    Then one visible batch ships
    And the next player-driven case begins
    And three shipped batches complete the contract

  Scenario: The measured sweep explains the small-model trade
    Then WHY A SENIOR shows which reads cross from Pico to Nano at the selected floor
    And WHY NOT ONE BIG MODEL compares Pico only, the 1.5 mesh, and Nano direct
    And recall, escalation rate, and the residency proxy come from the committed sweep
    And the page says gateway placement is topology rather than a privacy guarantee
