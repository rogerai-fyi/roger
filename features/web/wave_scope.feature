# THE WAVE SCOPE - the PPI radar on the research hub, made into an instrument.
#
# Today the scope draws one dimension (parameter class as range) and then spends a
# five-sentence caption apologising for the dimension it does NOT draw: "bearing
# carries no meaning". A reader has to hold the legend and the picture apart in their
# head, and the disclaimer is longer than the data.
#
# This spec gives bearing a meaning that we already publish, makes the picture and the
# legend one linked object, and moves the caption's load onto the instrument itself.
#
# THE HONESTY CONSTRAINT, which outranks every visual goal below: nothing on this
# instrument may assert a measurement. Every slot is an unreleased design program.
# Radius is a DECLARED SIZE TARGET; bearing is a DECLARED VARIATION from the §5
# table on research-wave-family.html (Tool, Text, Embed, Guard, Vision, Audio and the
# slots each lives at). Both are already-published design intent, not results. The
# instrument must make that legible without a paragraph, and must never imply a
# benchmark, a footprint, or a speed.
#
# Interfaces: web/src/research.html (figure.scope), web/src/styles/research.css,
#   web/src/js/scope.js, web/src/research-wave-family.html (the §5 source of truth).
#
# Out of scope: any performance number, any released-checkpoint claim, a WebGL
# dependency (see the DEGRADATION rules - this has to survive with no JS at all).

Feature: The Wave scope reads as an instrument, not a chart with a disclaimer
  A visitor should learn what each Wave slot is sized for AND what it is meant to do,
  by looking at the picture, and should be able to interrogate any one of them without
  losing the others.

  Background:
    Given a visitor opens the research hub
    And the scope figure is on the page

  # ---- what the two axes mean -----------------------------------------------

  Scenario: Range still means parameter class, on a decade scale
    Then each grid ring is one decade of parameters
    And the rings are labelled 100K through 1B on the instrument
    And a slot sits at the radius its declared parameter class maps to
    And the mapping is logarithmic so a decade is a constant distance

  Scenario: Bearing now means capability variation
    Then the instrument is divided into named bearing sectors
    And the sectors are the published variations: Tool, Text, Embed, Guard, Vision, Audio
    And each sector is labelled on the instrument itself, not only in a caption
    And the labels remain readable at the smallest supported width

  Scenario: A contact spans the variations its slot actually carries
    Given the variation table states which slots host which variation
    Then a slot's contact spans exactly the bearings for the variations it hosts
    And a slot that hosts every variation spans every bearing
    And a slot hosts no bearing it is not listed against

  Scenario: The scope and the family field guide cannot disagree
    Then every slot the scope draws appears in the field guide's slot table
    And every variation the scope draws appears in the field guide's variation table
    And a slot-to-variation pairing on the scope matches the field guide's "Lives at"

  # ---- the interaction ------------------------------------------------------

  Scenario: Pointing at a contact raises its legend row
    When the visitor points at a contact on the scope
    Then that contact is emphasised
    And its legend row is emphasised at the same time
    And every other contact and row is de-emphasised, not hidden

  Scenario: Pointing at a legend row raises its contact
    When the visitor points at a legend row
    Then that row's contact is emphasised on the scope
    And the link works in both directions from either side

  Scenario: The link is keyboard reachable, not pointer-only
    When the visitor moves focus to a legend row
    Then the same emphasis appears as on hover
    And focus is visible on the row
    And the emphasis clears when focus leaves

  Scenario: Selection persists so a reader can study one slot
    When the visitor activates a legend row
    Then that slot stays emphasised after the pointer leaves
    And activating it again releases it
    And activating a different row moves the selection rather than adding to it

  Scenario Outline: Every slot is individually interrogable
    When the visitor selects "<slot>"
    Then the scope shows that slot's range band and its bearing span
    And the reading states its parameter class and its program status

    Examples:
      | slot       |
      | Roger Edge |
      | Wave Nano  |
      | Wave Micro |
      | Wave Core  |

  # ---- what the caption no longer has to say --------------------------------

  Scenario: The instrument explains itself
    Then the axes are named on the instrument
    And the decade scale is stated where the rings are read
    And the status of each slot is visible without hovering
    And the caption is shorter than the copy it replaced

  Scenario: The honesty facts survive the caption shrinking
    Then the instrument still states that ranges are declared design targets
    And it still states that no slot has released a checkpoint
    And neither fact is carried only by a hover, a tooltip, or an animation

  Scenario: The in-progress slot is distinguishable from the designed ones
    Then the slot that is in progress is marked differently from the ones in design
    And the difference is not carried by colour alone

  # ---- degradation ----------------------------------------------------------

  Scenario: The scope is fully legible with no JavaScript
    Given scripting is disabled
    Then every contact is drawn
    And every legend row is readable
    And the axes and their labels are present
    And only the linked emphasis is lost

  Scenario: The scope respects reduced motion
    Given the visitor prefers reduced motion
    Then the sweep does not animate
    And the contacts are drawn in their final state
    And no information is available only after an animation completes

  Scenario: The scope is described for screen readers
    Then the figure has an accessible name
    And the legend is a list a screen reader can walk
    And the decorative grid, sweep and labels are hidden from the accessibility tree
    And a text alternative conveys each slot's class, variations, and status

  Scenario: The scope survives a narrow viewport
    Given the visitor is on a narrow screen
    Then the instrument and the legend stack rather than overflowing
    And no bearing label overlaps another
    And the page does not scroll horizontally
