# Layer 1 (protocol) spec - a shared model can say, in words, what it is for.
#
# The taxonomy (features/categories/offer_taxonomy.feature) says WHICH AISLE a model sits in.
# It cannot tell two siblings apart: four adapters on one base model can all be
# "language/classification" with the same base model and license. People need a sentence.
#
# protocol.Taxonomy gains three optional, operator-authored text fields:
#
#   summary   one line: what the model is                       (at most 140 characters)
#   best_at   what it does well, in the operator's words        (at most 200 characters)
#   not_for   what it should not be used for                    (at most 200 characters)
#
# and one claim label, details_source, which is set ONLY by the broker or the node itself:
#   "declared"  the station's operator wrote the text
#   "rogerai"   the text comes from RogerAI's curated catalog (curated_catalog.feature)
# A node can never send "rogerai"; the broker strips it, as it strips "verified" elsewhere.
#
# Like every taxonomy field these are DISPLAY attributes: unverified, never routed on, never
# filtered on, omitempty on the wire, and EXCLUDED from the registration possession proof
# (regSigningBytes already zeroes the whole Taxonomy), so mixed-version nodes and brokers
# never disagree about the signed bytes.
#
# The text never decides the declared-vs-inferred switch. Inference never produces text, so
# there is nothing to mix: declaring only a summary keeps the inferred category on air, with
# the summary attached. (Contrast declaration.feature, where any taxonomy claim replaces the
# whole inference.)
#
# Modality and "adapter of" are NOT new fields. Modality is the offer's own modality plus its
# vision capability. "Adapter of" is tuning=adapter + base_model, which already exist.

Feature: Offers carry optional, bounded, unverified details text

  Background:
    Given a node registering an offer with the broker

  # ---------------------------------------------------------------- back-compat
  @protocol
  Scenario: An offer with no details claims nothing
    Given an offer for model "demo-1b-chat" with no details fields
    When the broker normalizes the offer
    Then the offer has no summary, best_at or not_for
    And the offer has no details source

  @broker
  Scenario: A node that predates details registers unchanged
    Given a registration body produced by a node without the details fields
    When the broker accepts the registration
    Then the registration is accepted
    And the offer for that model has no details source

  @protocol
  Scenario: The JSON for an offer with no details carries none of the keys
    Given an offer for model "demo-1b-chat" with no details fields
    When the offer is encoded as JSON
    Then the JSON has no "summary", "best_at", "not_for" or "details_source" key

  @protocol
  Scenario: Details never enter the signed registration bytes
    Given an offer for model "demo-1b-chat" with summary "A small chat model."
    When the registration signing bytes are computed
    Then the signing bytes equal those of the same offer with no details
    And the signing bytes contain neither "summary" nor "best_at" nor "not_for"

  @protocol
  Scenario: A signed registration verifies after a broker strips or rewrites details
    Given a node signs a registration whose offer has summary "A small chat model."
    When the broker canonicalizes the details and verifies the registration
    Then the possession proof verifies

  # ---------------------------------------------------------------- claim source
  @protocol
  Scenario: Text from a node is labelled declared
    Given an offer for model "demo-1b-chat" with summary "A small chat model."
    When the broker normalizes the offer
    Then the offer's details source is "declared"

  @protocol
  Scenario Outline: A node cannot claim RogerAI or any other source for its text
    Given an offer for model "demo-1b-chat" with summary "A small chat model." and details source "<sent>"
    When the broker normalizes the offer
    Then the offer's details source is "declared"

    Examples:
      | sent      |
      | rogerai   |
      | ROGERAI   |
      | verified  |
      | curated   |
      | inferred  |
      | anything  |
      |           |

  @protocol
  Scenario: A details source with no surviving text is cleared
    Given an offer for model "demo-1b-chat" with details source "declared" and no details text
    When the broker normalizes the offer
    Then the offer has no details source

  @protocol
  Scenario: Text that sanitizes to nothing leaves no details source
    Given an offer for model "demo-1b-chat" with summary "\u200b\u200b\u001b"
    When the broker normalizes the offer
    Then the offer has no summary
    And the offer has no details source

  @protocol
  Scenario: A node cannot send a model page link
    Given an offer for model "demo-1b-chat" carrying a "page" key "https://example.invalid/x"
    When the broker normalizes the offer
    Then the offer has no page link

  # ---------------------------------------------------------------- independence from the taxonomy switch
  @node
  Scenario: Declaring only details keeps the inferred taxonomy on air
    Given a share controller with a detected model "demo-1b-chat" inferred as "language/chat"
    And the operator declares for "demo-1b-chat" only summary "A small chat model."
    When the operator puts "demo-1b-chat" on air
    Then the on-air taxonomy for "demo-1b-chat" is category "language", subcategory "chat" and source "inferred"
    And the on-air summary for "demo-1b-chat" is "A small chat model."
    And the on-air details source for "demo-1b-chat" is "declared"

  @node
  Scenario: Declaring a category and details puts both on air as declared
    Given a share controller with a detected model "demo-1b-chat" inferred as "language/chat"
    And the operator declares for "demo-1b-chat" category "language/classification" and summary "Picks one action."
    When the operator puts "demo-1b-chat" on air
    Then the on-air taxonomy for "demo-1b-chat" is category "language", subcategory "classification" and source "declared"
    And the on-air summary for "demo-1b-chat" is "Picks one action."

  @protocol
  Scenario: A details-only declaration does not count as a taxonomy claim
    Given a taxonomy with only summary "A small chat model."
    Then the taxonomy is not Declared
    And the taxonomy has declared details

  @protocol
  Scenario: A taxonomy-only declaration has no declared details
    Given a taxonomy with only category "language/chat"
    Then the taxonomy is Declared
    And the taxonomy has no declared details

  @node
  Scenario: Clearing the declaration removes the details too
    Given a share controller with a detected model "demo-1b-chat" inferred as "language/chat"
    And the operator declares for "demo-1b-chat" only summary "A small chat model."
    And the operator clears the declaration for "demo-1b-chat"
    When the operator puts "demo-1b-chat" on air
    Then the on-air offer for "demo-1b-chat" has no summary, best_at or not_for

  # ---------------------------------------------------------------- not routing, not filtering
  @broker
  Scenario: Details never change routing
    Given two stations for "demo-1b-chat" identical except that one has summary "Fast and cheap."
    When a consumer asks for "demo-1b-chat" 1000 times
    Then the routing split equals the split with neither station carrying a summary

  @broker
  Scenario Outline: Details are not a discovery filter
    When a consumer queries /discover with "<query>"
    Then the response is the same as /discover with no query

    Examples:
      | query                  |
      | summary=chat           |
      | best_at=fire           |
      | not_for=medical        |
      | details_source=rogerai |

  # ---------------------------------------------------------------- voice
  @protocol
  Scenario: A voice offer can carry details
    Given an offer for model "kokoro" with modality "tts" and summary "English voices."
    When the broker normalizes the offer
    Then the offer's summary is "English voices."
    And the offer's category is "audio" and subcategory "tts"
