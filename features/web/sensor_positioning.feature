# PROPOSED 2026-10-15. APPROVED by the founder 2026-10-26 for the next web release, with
# the headline ruling (the word cycles in the page).
#
# The positioning: "your device, now a sensor". A site installs RogerAI on a computer it
# already owns, picks a Wave model, and picks an adapter for the job. Every sentence is in
# design tense ("built to", "is designed to"); no number, no site count, no customer.
#
# ONE SOURCE OF TRUTH: web/src/data/capabilities.json holds every adapter, base skill and
# device row with its status from a fixed vocabulary. Pages render that status at build
# time through a {{cap:<id>.<field>}} token (web/scripts/wave-status.mjs), the same way
# Wave tier labels render from wave-status.json. The data file is build input and never
# ships.
#
# THE LINES these scenarios hold, inherited from the approved wildfire spec
# (healthcare_defense_markets.feature): the models warn and explain; the approved term is
# warning or early warning, never fire detection; the model is advisory, beside the control
# system, never in it, and never moves a machine; nothing is called installed, deployed or
# running in the field; no site counts, no outcome, insurance or life-safety claim. Handing
# off to a person is drawn as the good outcome (playbox_edge_honesty.feature).
#
# Out of this pass (each a separate founder decision): the FireDefense film recut, any
# autonomous activation, subscriptions or paid monitoring, hardware sales, customer counts,
# investor figures, camera or vision claims, and any iPad sensor claim before it is true.
Feature: Your device, now a sensor
  In order to see that RogerAI needs no box to buy
  As a site owner reading the website
  I want the install-model-adapter path shown plainly, with every status read from one file

  Scenario: the capabilities file is valid
    Then web/src/data/capabilities.json defines exactly the stages planned, lab,
      release-candidate, and available, in that order, each with a one-line definition
    And each stage has a short public label
    And every capability has an id, a name, a kind of adapter or base-skill, a base tier
      that exists in the Wave status file, a stage from the vocabulary, and a one-line
      public description
    And every device row has an id, a name, whether RogerAI installs on it today, a stage
      from the vocabulary for running a Wave model and an adapter on it, a one-line public
      description, and whether it is drawn as a ghost
    And ids are unique across capabilities and devices
    And no text in the file carries an em dash
    And the file is build input only and is not in the built site

  Scenario: the first entries say what is true today
    Then fire early warning is an adapter on Wave Pico, in the lab
    And telemetry drift is a base skill of the model, not an adapter
    And "your own" is an adapter that is planned
    And vibration is not listed as an adapter
    And no entry uses the words fire detection
    And the iPad row is planned and drawn as a ghost, because the iPad does not run Wave
      models or adapters today
    And a row is drawn as a ghost exactly when its stage is planned
    And the file offers no hardware, no monitoring, and no subscription

  Scenario: a status token renders from the file
    Then a {{cap:<id>.<field>}} token renders the name, stage, label, or description of
      that capability or device, verbatim
    And an unknown id or field fails the build rather than shipping a blank
    And a malformed token fails the build rather than shipping literally
    And no cap token is left unresolved anywhere in the built site

  Scenario: every page's rendered status matches the file
    Then every element on every built page that carries data-cap names a known id
    And its data-cap-stage equals that entry's stage in the file
    And the stage words it shows equal that stage's public label
    And a lamp is drawn red only for the available stage; every other stage is a quiet mark

  Scenario: no page claims an adapter is out ahead of the file
    Then no built page says an adapter or device row is available, live, running,
      installed, deployed, or shipping within 160 characters of its name, unless the file
      gives it the available stage, or the claim is negated
    And no built page uses the words fire detection, fire-detection, or detects fires
    And no built page puts the iPad next to a sensor, adapter, or Wave claim without
      saying it is planned
    And no built page offers hardware for sale while the file offers none

  Scenario: the homepage shows the path between the company and the tune
    Then the homepage carries a section headed "Your device, now a sensor." between
      the company section and the tune
    And it lays out three steps on the shared step path: install RogerAI, pick a Wave
      model, pick an adapter
    And its paragraph says the model is built to run on site, offline if you want,
      hands what it cannot vouch for to a person, and stays advisory beside the control
      system, never moving a machine
    And its adapter dial shows each capability's lamp from the file
    And the section numbers and the contents tuner run in order with the new section
    And the hero is unchanged

  Scenario: the industrial page carries the adapter shelf
    Then the industrial page carries an adapter shelf after the placement figure, headed
      "Capabilities come as adapters."
    And it shows the same adapter dial, read from the file
    And it carries the "escalates when unsure" figure, where handing off to a person is
      drawn as the good outcome, labelled illustrative and carrying no number
    And the shelf never names a company, never says installed, deployed, or running, and
      never says the model opens, triggers, or activates a valve or sprinkler
    And the wildfire card, boundary, and use case are unchanged

  Scenario: the Wave family page explains one base and a small add-on per job
    Then the variations section carries a block headed "One base model, a small add-on
      per job."
    And it gives the first adapter's status from the file
    And it says reading a drifting signal is built into the base, not an adapter
    And it carries no size or score

  Scenario: the hardware page shows the devices a site already has
    Then the hardware page carries a "three devices, one install" row with a laptop or
      desktop, an industrial PC, and an iPad
    And each device's status comes from the file
    And the iPad is drawn as a ghost and labelled planned
    And no device carries a speed, because none is measured
    And the board cards and their count are unchanged

  Scenario: the Tower page says it runs offline on your own machines
    Then the Tower page's standalone note says a site that never dials out can run it
      offline on its own machines

  Scenario: every new figure is complete without motion or script
    Then each new figure reads complete with JavaScript off and with reduced motion
    And it uses only the ink tokens and the one red, with no glow, grid, or pinned bar
    And every label in it is at least 11px on a 390px phone
