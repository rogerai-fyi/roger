# Founder-approved 2026-08-01 (brief: rogerai-internal-docs/
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
Feature: Healthcare and defense join the market set
  In order to show the family is well rounded across the industries we serve
  As a reader of the research pages
  I want healthcare and defense named, with their boundaries stated plainly

  Scenario: the site names eight markets, not six
    Then the industrial page names oil and gas, power generation, manufacturing,
      aerospace, mining, water, healthcare, and defense
    And the company page's industry line agrees with that set

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
