# Founder-approved 2026-08-01 (brief: docs-internal/
# HEALTHCARE-AND-DEFENSE-USE-CASES-2026-08-01.md): the market set grows from six to
# eight - healthcare and defense join oil and gas, power generation, manufacturing,
# aerospace, mining, and water.
#
# THE APPROVED LINE, which governs every healthcare claim we publish: our models work
# on hospital EQUIPMENT AND FACILITIES, never on patients. Diagnosis, treatment,
# triage, dosing, or the analysis of a medical image or physiologic signal is Software
# as a Medical Device and is out of scope - it is also what breaks the Cures Act
# clinical-decision-support exemption. Defense is SUSTAINMENT ONLY: maintenance,
# readiness, and supply. No targeting, no weapons release, no ISR exploitation.
#
# PROPOSED 2026-09-24, AWAITING FOUNDER APPROVAL (rulings of 2026-09-24): the set grows
# to NINE - wildfire mitigation joins, ninth and last, after defense. "Wildfire" on the
# icon strip, "Wildfire mitigation" on the card, slug market-wildfire.
#
# THE LINE for wildfire: the models WARN AND EXPLAIN. They are never a listed fire alarm
# or sprinkler system, never in the control path of one, never an evacuation directive,
# and never an outcome claim. The model raises the alert and makes the threat call; a
# valve opens only on the owner's command or under a rule the owner armed in advance,
# which the controller executes with its own abort window. The model never actuates
# equipment by itself, so the page's universal "never actuates equipment" promise stands
# unqualified - no carve-out, no exception.
#
# FireDefense Systems is NAMED (founder ruling), as a separate company - a customer and
# partner, never a RogerAI product. Its sprinkler systems and live fire map are real; its
# app and Safety Box are in development; the watch mast (camera, gas sensors, on-board
# models) is demonstrated in the film, not deployed. Wave Pico is on air
# (wave-pico-293m); Wave Nano is in training. Naming FireDefense requires their written
# agreement before publishing - the founder's to obtain.
#
# "Wildfire copy" below means the visible text of the wildfire card, the wildfire boundary
# paragraph, and the wildfire use case (photo credits excluded). The boundary's own
# negations ("not a listed fire alarm", "an evacuation order") are the only place a
# forbidden term may appear, and only in that negated form.
#
# Out of this pass: wildfire rows in the family jobs table (and the scope plot derived
# from it), and any NFPA 1140 / California Fire Code citation.
Feature: Healthcare, defense, and wildfire mitigation join the market set
  In order to show the family is well rounded across the industries we serve
  As a reader of the research pages
  I want every market named, with its boundary stated plainly

  Scenario: the site names nine markets, not eight
    Then the industrial page names oil and gas, power generation, manufacturing,
      aerospace, mining, water, healthcare, defense, and wildfire mitigation
    And the research hub names the same nine
    And the company page's industry line agrees with that set, naming every one of the nine
    And the industrial page's meta description names the same nine
    And the nine appear in that order on the strip, in the grid, on the hub, and on the
      company page, with wildfire mitigation last

  Scenario: every stated count of the markets says nine
    Then the industrial page's markets heading reads "Nine industries whose data cannot
      leave the site."
    And the hub's link to the industrial page reads "The nine markets"
    And no page that names the set says four, five, six, seven, or eight industries or
      markets
    And the stated count on each page equals the number of cards in the grid

  Scenario: the wildfire card exists with its name and slug
    Then the use-case strip carries nine deep links, one per market
    And the ninth strip link is labelled "Wildfire" and points at #market-wildfire
    And the grid carries an article with id "market-wildfire" titled "Wildfire mitigation"
    And every strip link resolves to exactly one article id on the same page
    And the wildfire card names a concrete workload, not just the sector
    And the wildfire card's photo shows no fire on a home: its alt text carries none of
      the words fire, flame, smoke, burning, or blaze

  Scenario: nine markets lay out without a lone orphan
    Then the icon strip runs nine across at full width and three across at every
      narrower breakpoint, so it closes 3x3
    And the grid runs three across at full width, so nine cards close 3x3
    And at a two-column breakpoint the ninth card spans the full row
    And at one column the cards simply stack

  Scenario: wildfire is warning and mitigation, not fire protection
    Then the page carries a wildfire boundary paragraph beside the healthcare and defense
      boundaries, led by "Wildfire: warning and mitigation, not fire protection."
    And it states the models are not a listed fire alarm or sprinkler system
    And it states they are never in the control path of one
    And it states they do not replace code-required detection or suppression, an
      evacuation order, or the fire service
    And the wildfire card, boundary, and use case never present RogerAI as a fire alarm,
      a fire protection system, or a suppression control

  Scenario: a valve opens only on the owner's command or an owner-armed rule
    Then the wildfire boundary says the model raises the alert and makes the threat call
    And it says a valve opens only on the owner's command or under a rule the owner armed
      in advance
    And it says the controller, not the model, executes that rule, with its own abort window
    And it says the model never opens a valve by itself
    And no wildfire copy says the model opens, triggers, activates, or controls a valve,
      a sprinkler, or a zone
    And the page's statement that the model never actuates equipment and is never part of
      a protection or safety path still stands, with no exception or carve-out attached
    And the placement caption still says Roger Edge is wired to nothing in the control system

  Scenario: the wildfire copy makes no outcome, insurance, or life-safety claim
    Then no wildfire copy claims a home survives, a loss is prevented, or lives are saved
    And no wildfire copy mentions insurance, a premium, or an insurer
    And no wildfire copy claims a listing, approval, certification, code compliance, or a
      fire rating, other than in the boundary's own stated negation
    And no wildfire copy gives a percentage, a detection rate, or a promise to catch every fire
    And no wildfire copy tells anyone to evacuate, to stay, or to shelter, other than in the
      boundary's own stated negation
    And no wildfire copy gives a count of installations or names a property or a place
      smaller than a region

  Scenario: the wildfire use case is honest about what runs today
    Then the use case says the watch mast, with its camera, gas sensors, and on-board
      models, is shown in the FireDefense film
    And no wildfire copy says the mast or the fire watch is installed, deployed, running,
      or in service
    And Wave Pico is named with the stage the Wave status file gives it, on air, and its
      on-air band wave-pico-293m
    And Wave Nano is named with the stage the Wave status file gives it, in training
    And if the status file's stage for either tier changes, the check fails until the copy
      follows it
    And no wildfire copy carries the film's model sizes 287M or 0.8B
    And no wildfire copy describes the FireDefense app or the Safety Box as available,
      shipped, or installed

  Scenario: FireDefense Systems is named as a separate company, with its film
    Then the use case names FireDefense Systems as a separate company that designs and
      installs exterior wildfire sprinkler systems for homes
    And it never calls FireDefense, its sprinklers, its app, or its fire map a RogerAI product
    And it links the FireDefense film itself, so the link always plays that film rather
      than a reel that may open on another
    And the FireDefense name appears only in the use case and in the markets section's
      naming sentence, never in the card title, the strip, the boundary, the hub, or the
      company line

  Scenario: the wildfire market is not mistaken for defense
    Then the wildfire strip label and card title do not contain the word defense
    And the defense card's title stays "Defense sustainment" and its strip label "Defense"
    And the check for the defense market matches "Defense sustainment" on its card title,
      so a wildfire card can never satisfy it
    And the check for the wildfire market matches "Wildfire mitigation" on its card title
    And no wildfire copy uses defense as a standalone word, the company name FireDefense
      Systems being one word and not a use of it
    And the wildfire icon is a different glyph from the defense shield

  Scenario: the customer-naming promise is reconciled, not broken
    Then the markets section still says the industrial engagements are under NDA and not
      named here
    And it says the one company the page names, FireDefense Systems, is named with its
      agreement
    And the page names no other company as a customer or partner
    And the page still never claims an empty customer list

  Scenario: the healthcare boundary is stated where healthcare is claimed
    Then the page says the models work on equipment and facilities, not on patients
    And it states plainly that they do not diagnose, treat, or read scans
    And the no-actuation contract is named as the same one that keeps them out of
      a plant's control loop

  Scenario: defense is scoped to sustainment
    Then the page presents defense as maintenance, readiness, and supply
    And it never claims targeting, weapons, or intelligence exploitation

  Scenario: healthcare jobs appear in the family jobs table
    Then the jobs table carries imaging fleet health, biomed work orders, alarm
      configuration review, device reportability, cold-chain excursions, and
      sterile processing records
    And each names a slot the family actually has
    And each states the constraint that keeps the work local

  Scenario: defense jobs appear in the family jobs table
    Then the jobs table carries platform fault triage, readiness paperwork, and
      supply catalog lookup
    And each names a slot the family actually has

  Scenario: no healthcare job describes clinical work
    Then no job in the table mentions diagnosis, treatment, dosing, triage of a
      patient, or reading a scan or waveform

  Scenario: the scope plot follows the table
    Then the industrial axis of the model scope plots every job in the table
    And the plot and the table can never disagree, because the test derives one
      from the other
