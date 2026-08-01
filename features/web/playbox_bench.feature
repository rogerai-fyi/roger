# The Playbox faceplate: the page stops looking like a web app with tabs and starts
# looking like the operator's bench - dense, technical, every control load-bearing,
# in the site's ink+live palette. Visuals are never claims: the meter and lamps are
# driven by REAL directory data, and reduced-motion freezes everything legible.
Feature: The Playbox bench
  In order to feel like an operator at a rig, not a visitor at a dashboard
  As a Playbox visitor
  I want the console drawn as one instrument faceplate with real meters

  Scenario: the deck is one faceplate and the tabs read as a mode selector
    Then the console deck is a bordered faceplate panel
    And the three surfaces present as positions on a MODE selector
    And keyboard tab semantics (arrow keys, roles) are unchanged

  Scenario: the S-meter shows a real signal
    Given the directory has loaded
    Then an S-meter needle shows the tuned station's live signal strength
    And with nothing tuned it rests at the band's strongest signal
    And the meter is drawn from the same data as the directory rows, never invented

  Scenario: the station list reads as dial detents
    Then each station row carries its signal, callsign, and price as engraved markings
    And the tuned row is marked by the needle edge, not only a color change

  Scenario: the transcript is the logbook
    Then chat lines sit on a ruled logbook with a mono callsign column
    And each entry carries its time in UTC, the operator's convention

  Scenario: the composer is the key
    Then the send control is styled as the operator's key with a KEY label
    And it remains a real submit button to assistive tech

  Scenario: reduced motion freezes the bench legible
    Given the visitor prefers reduced motion
    Then the needle sits at its true value without animation
    And no lamp blinks
