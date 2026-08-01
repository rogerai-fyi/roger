# WAVE INFINITE on the public site.
#
# CANONICAL SOURCE: WAVE-INFINITE-PROGRAM-2026-07-31.md, which supersedes the earlier
# framing in terms this spec has to follow exactly:
#
#   "Wave Infinite is our prototype model program - a new type of model that uses
#    self-training techniques with reflection, built from the components we have
#    defined, measured, and preregistered. It is NOT 'just a runtime'; the certified
#    runtime is one layer of it."
#
# An earlier version of this page called it "a runtime, not a size". That was wrong and
# is retired. Three layers, each with its own status:
#
#   1 Reflective substrate   - built and self-verifying
#   2 Certified adaptation   - primitives measured; the tok/s payoff is NOT measured
#   3 Self-directed growth   - preregistered (CURE), UNRUN
#
# THE HARD PUBLIC CONSTRAINT, from the program doc's standing constraints:
#
#   "Never claim 'self-training' in public material until CURE's gates pass -
#    internally the term is accurate for the program's GOAL; externally it is an
#    overclaim until measured."
#
# So the page describes layer 3 as preregistered and in development, never as a
# capability the model has. The published website draft (v2 explainer) models the right
# register: "Self-directed growth (in development)".
#
# Interfaces: web/src/research-wave-family.html, web/src/research.html,
#   web/src/research-models.html, web/src/styles/wave-family.css.
#
# Out of scope: a tok/s figure, a release date, a download, an "available" badge, and any
# statement that the model trains or improves itself today.

Feature: Wave Infinite is presented as the prototype program it is
  A reader should understand that Wave Infinite is a model programme with three layers
  at three different states of proof, see the measurement that forced it to exist, and
  come away unable to believe any of it is finished.

  Background:
    Given a visitor reads the Wave family field guide

  # ---- what it is -----------------------------------------------------------

  Scenario: It is never filed among the models
    Then it is not a row in the model and status catalogue
    And it stays reachable from the research hub and the family page

  Scenario: It is a programme, not a runtime and not a size class
    Then the page calls Wave Infinite a prototype model programme
    And it does not reduce it to a runtime
    And it is not given a parameter class
    And it is not placed on the size axis of the scope
    And it is not listed as a row in the slot or jobs tables

  Scenario: The measurement that forced it to exist leads
    Then the page states that specialisation damage can hide inside benchmark noise
    And it states that the same change is far worse once the workload shifts
    And it concludes that you cannot monitor your way to safe specialisation

  Scenario Outline: Each layer is shown at its real state
    Then layer "<layer>" is shown as "<state>"

    Examples:
      | layer                 | state                    |
      | Reflection            | built and self-verifying |
      | Certified adaptation  | primitives measured      |
      | Self-directed growth  | in development, unrun    |

  Scenario: The runtime is presented as one layer, not the whole thing
    Then the certified runtime is described as a layer of the programme
    And no copy claims the programme is only a runtime

  # ---- the claim ceiling ----------------------------------------------------

  Scenario: The page never claims self-training
    Then no copy uses the phrase "self-training"
    And no copy says the model trains itself, learns by itself, or improves itself today
    And layer 3 is described as preregistered and unrun rather than as a capability

  # Amended 2026-07-31 on founder direction. The CLAIMS are unchanged and none is
  # softened; the supporting figures are held until they clear re-verification, and the
  # page says so, which is why the absence reads as discipline rather than vagueness.
  # The hold's basis and the restore condition live in the internal docs repo - this is a
  # public file, and printing the held figures here would defeat the hold.
  Scenario: What is proven is claimed, with the figures held until re-verified
    Then each proven claim is stated in full
    And the certificate result is described as bit-identical
    And the guard is described for both the in-domain and the cross-domain case
    And no interim-hardware figure is printed for any of them
    And the page states that the figures are published once re-verified

  Scenario: What is NOT proven is published deliberately, at equal prominence
    Then the page states that the end-to-end speed benefit is unmeasured
    And it states that the growth layer's quality gains are unmeasured
    And it calls Wave Infinite a prototype programme
    And these appear in the main flow, not only in a caption or a tooltip

  Scenario: It is never described as a shipped product
    Then no download, checkpoint, artifact id, or release date is offered
    And no benchmark number is presented as a product capability

  # ---- the treatment --------------------------------------------------------

  Scenario: Wave Infinite is visually distinct without implying completion
    Then its treatment differs from the size-class entries
    And no visual cue implies availability, completion, or superiority

  Scenario: The shimmer stays inside the RogerAI palette
    Then the animated treatment uses the RogerAI ink and live-red palette
    And it does not introduce a multi-hue spectrum

  Scenario: The motion carries no information
    Given the visitor prefers reduced motion
    Then the shimmer does not animate
    And every fact remains readable in the static state

  Scenario: The treatment degrades without JavaScript
    Given scripting is disabled
    Then Wave Infinite is present and fully readable
