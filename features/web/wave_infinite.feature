# WAVE INFINITE on the public site.
#
# Wave Infinite exists as a RogerAI Labs design brief (2026-07-30, status update
# 2026-07-31) and a plain-language explainer (2026-07-31), which opens: "Wave Infinite
# is a model RUNTIME that watches its own execution, proves which parts of the model
# the current workload can never reach, skips those parts, and falls back safely the
# instant the workload changes." It is absent from the site entirely, which is why it
# reads as missing from the family. But the shape of what it is is easy to misread,
# and the site must not repeat the misreading:
#
#   Wave Infinite is NOT a size class. Roger Edge, Nano, Micro and Core are model
#   sizes. Wave Infinite is a RUNTIME - "a self-modifying model runtime in which
#   every modification carries a machine-checkable certificate that it is
#   behaviour-preserving on the observed operating region, and any input leaving
#   that region deoptimizes to the unmodified path." It runs UNDER a model rather
#   than sitting at a point on the size axis, which is why it cannot be a blip on
#   that axis - but "part of the family" must not slide into "works with all of it".
#   The certificate is reachability certification of MoE EXPERTS and needs a
#   per-expert selection-bias tensor, so it is a base-model property. It does not
#   reach Roger Edge, which is a classifier rather than a language model.
#
# THE CLAIM CEILING. The brief sets this itself and this spec enforces it verbatim:
#
#   "Wave Infinite is a design with one causally validated primitive and zero
#    demonstrated performance benefit. Soundness is proven; speedup is unmeasured.
#    ... it stays a research architecture and must not be described as a working
#    system. The self-improvement half remains entirely unmeasured and out of scope."
#
# The brief also names the naming risk directly: "'Infinite' is a naming risk. The
# tower is infinite in specification and finite in implementation. That distinction
# is a theorem and we should state it every time, not let it drift into a capability
# claim." Every scenario below exists to keep that promise on a public page - this is
# the same failure mode as the retracted Wave Micro release, caught before shipping.
#
# Interfaces: web/src/research-wave-family.html, web/src/research.html,
#   web/src/research-models.html, web/src/styles/wave-family.css.
#
# Out of scope: a benchmark, a tok/s figure, a release date, a download, an "available"
# badge, and any description of the self-improvement half as a capability.

Feature: Wave Infinite is presented as the research architecture it is
  A reader should understand that Wave Infinite is a runtime the whole family could
  run under, see why it is different in kind from a size class, and come away unable
  to believe it is finished.

  Background:
    Given a visitor reads the Wave family field guide

  # ---- what it is -----------------------------------------------------------

  Scenario: Wave Infinite is named as part of the family
    Then the family page presents Wave Infinite
    And it is reachable from the research hub
    And it appears in the model and status catalogue

  Scenario: It is distinguished from the size classes, not listed among them
    Then Wave Infinite is not given a parameter class
    And it is not placed on the size axis of the scope
    And the page states that it is a runtime rather than a model size
    And the page states that it runs under a model rather than beside one
    And the page names what a base model must provide for the certificate to apply
    And it does not imply that every slot in the family could use it

  Scenario: The one-sentence definition is the brief's own
    Then the page states that every modification carries a machine-checkable certificate
    And it states that the certificate is behaviour-preserving on the observed region
    And it states that input leaving that region falls back to the unmodified path

  Scenario: The name is explained the moment it is used
    Then the page states that the tower is infinite in specification
    And states that it is finite in implementation
    And presents that as a theorem rather than a capability
    And the explanation sits with the name, not in a footnote a reader may not reach

  # ---- the claim ceiling ----------------------------------------------------

  Scenario: The page states what is proven
    Then it states that the certificate is causally validated
    And it may state that ablating certified-dead experts changed no routing decisions
    And any such figure is attributed to the run that produced it

  Scenario: The page states what is NOT proven, with equal prominence
    Then it states that no performance benefit has been demonstrated
    And it states that soundness is proven while speedup is unmeasured
    And it states that the self-improvement half is unmeasured and out of scope
    And these appear in the main flow, not only in a caption or a tooltip

  Scenario: It is never described as a working system
    Then no copy claims Wave Infinite runs, ships, serves, or is available
    And no copy states or implies that a model improves itself today
    And no download, checkpoint, artifact id, or release date is offered
    And no benchmark number is attributed to it

  Scenario Outline: Build stages are shown at their real state
    Then stage "<stage>" is shown as "<state>"

    Examples:
      | stage                   | state       |
      | Reify                   | done        |
      | Live meta-level         | not built   |
      | Speculate with a guard  | unrun       |
      | Persist the specialisation | not started |
      | The tower               | not started |

  Scenario: A reader cannot mistake the stage table for a roadmap with dates
    Then no stage carries a delivery date
    And no stage is described as in progress unless it is

  # ---- the treatment --------------------------------------------------------

  Scenario: Wave Infinite is visually special without being loud about capability
    Then its treatment is distinct from the size-class entries
    And the distinction reads as "different in kind", not "further along"
    And no visual cue implies availability, completion, or superiority

  Scenario: The shimmer stays inside the RogerAI palette
    Then the animated treatment uses the RogerAI ink and live-red palette
    And it does not introduce a multi-hue spectrum
    And it does not compete with the one red accent the page already spends

  Scenario: The motion carries no information
    Given the visitor prefers reduced motion
    Then the shimmer does not animate
    And every fact remains readable in the static state
    And the treatment still reads as distinct without motion

  Scenario: The treatment degrades without JavaScript
    Given scripting is disabled
    Then Wave Infinite is present and fully readable
    And its distinction from the size classes survives

  # ---- the explainer's own framing, kept intact -----------------------------

  Scenario: The three words are scored honestly, because two of them are wrong
    Given the explainer scores "reflecting", "evolving" and "self-learning"
    Then the page states that it reflects, in the procedural sense
    And states that the DEPLOYMENT evolves while the model does not
    And states plainly that nothing learns and no weights change
    And the page never uses "self-learning", "self-improving", or "trains itself"

  Scenario: The four steps are shown as a loop with a way out
    Then the page shows Reify, Certify, Specialise and Guard
    And Reify is marked built and verified
    And Guard is shown as the path taken when input leaves the certified region
    And the loop makes clear that leaving the region costs speed, never correctness

  Scenario: The sceptic's sentence leads
    Then the page states that ablating every certificate-marked expert changed
      "0 of 391,386" routing decisions
    And describes that result as bit-identical
    And does not surround it with a speed or quality claim it does not support

  Scenario: The JIT analogy is used, since it is the one that lands
    Then the page compares the runtime to a just-in-time compiler
    And explains deoptimization as the fallback that makes speculation safe
