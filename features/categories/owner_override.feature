# Slice 5 - the owner sets a model's category from the website, without touching the node.
#
# A signed-in owner declares (or clears) the taxonomy of one of THEIR OWN (node, model)
# pairs with POST /provider/meta. It is stored as an owner-scoped override in its own table
# (rogerai.offer_meta_overrides - a sibling of the price override, never the same row, so a
# category edit can never change a price or the other way round), applied to the live offer
# at once, and re-applied on every node registration, so it survives the node reconnecting
# and the broker restarting - exactly how the owner's web-set PRICE already behaves
# (cmd/rogerai-broker/provider_models.go, store.OfferOverride).
#
# The owner's override is the declaration on air: it wins over the node's own declaration
# and over its guess, and rides as "declared". Validation is the same strict check the node
# applies (protocol.Taxonomy.Declare), so the website refuses exactly what `roger share`,
# the TUI and the local console refuse, with the same reason.
# Founder ruling 2026-10-05: website editing is its own slice.

Feature: The owner sets a model's taxonomy from the website

  Background:
    Given owner "alice" is signed in
    And alice's node "n1" is on air serving "qwen3-vl-8b" with its own taxonomy "language/chat" inferred

  # ---------------------------------------------------------------- set
  Scenario: The owner's category goes on air at once, as declared
    When alice POSTs /provider/meta for "n1" "qwen3-vl-8b" with category "vision/ocr" and tags "nameplate-ocr,manufacturing"
    Then the response status is 200
    And the public offer for "n1" "qwen3-vl-8b" has category "vision/ocr", tags "nameplate-ocr,manufacturing" and source "declared"

  Scenario: Every declarable field is carried
    When alice POSTs /provider/meta for "n1" "qwen3-vl-8b" with category "vision/ocr", tags "ppe", tuning "finetune", base model "Qwen/Qwen3-VL-8B-Instruct", content policy "aligned" and license "Apache-2.0"
    Then the public offer for "n1" "qwen3-vl-8b" carries tuning "finetune", base model "Qwen/Qwen3-VL-8B-Instruct", content policy "aligned" and license "Apache-2.0"

  Scenario: Accepted values are stored in canonical form
    When alice POSTs /provider/meta for "n1" "qwen3-vl-8b" with category "Vision/OCR" and tags " PPE ,smoke fire"
    Then the stored override for "n1" "qwen3-vl-8b" has category "vision/ocr" and tags "ppe,smoke-fire"

  Scenario: The owner's override survives the node re-registering with its own taxonomy
    Given alice set the category of "n1" "qwen3-vl-8b" to "vision/ocr"
    When node "n1" re-registers serving "qwen3-vl-8b" with its own taxonomy "language/code" declared
    Then the public offer for "n1" "qwen3-vl-8b" has category "vision/ocr", tags "" and source "declared"

  Scenario: The owner's override survives a broker restart
    Given alice set the category of "n1" "qwen3-vl-8b" to "vision/ocr"
    When the broker restarts on the same store
    And node "n1" re-registers serving "qwen3-vl-8b" with its own taxonomy "language/chat" inferred
    Then the public offer for "n1" "qwen3-vl-8b" has category "vision/ocr", tags "" and source "declared"

  Scenario: An override for a node that is not on air is stored and applies when it registers
    Given alice also owns node "n2" which is not on air
    When alice POSTs /provider/meta for "n2" "florence-2" with category "vision/detection" and tags ""
    Then the response status is 200
    And the response says the category applies the next time the node is on air
    When node "n2" registers serving "florence-2" with its own taxonomy "language/chat" inferred
    Then the public offer for "n2" "florence-2" has category "vision/detection", tags "" and source "declared"

  Scenario: The owner's models list shows the override
    Given alice set the category of "n1" "qwen3-vl-8b" to "vision/ocr"
    When alice GETs /provider/models
    Then her row for "n1" "qwen3-vl-8b" has category "vision/ocr", source "declared" and is marked meta-overridden

  Scenario: Without an override the models list is not marked meta-overridden
    When alice GETs /provider/models
    Then her row for "n1" "qwen3-vl-8b" has category "language/chat", source "inferred" and is not marked meta-overridden

  # ---------------------------------------------------------------- independence from price
  Scenario: Setting a category never changes the price
    Given alice set the price of "n1" "qwen3-vl-8b" to 2 in and 3 out
    When alice POSTs /provider/meta for "n1" "qwen3-vl-8b" with category "vision/ocr" and tags ""
    Then the public offer for "n1" "qwen3-vl-8b" is priced 2 in and 3 out

  Scenario: Setting a price never changes the category
    Given alice set the category of "n1" "qwen3-vl-8b" to "vision/ocr"
    When alice sets the price of "n1" "qwen3-vl-8b" to 5 in and 6 out
    Then the public offer for "n1" "qwen3-vl-8b" has category "vision/ocr", tags "" and source "declared"

  Scenario: Clearing the category keeps the price override
    Given alice set the price of "n1" "qwen3-vl-8b" to 2 in and 3 out
    And alice set the category of "n1" "qwen3-vl-8b" to "vision/ocr"
    When alice clears the category of "n1" "qwen3-vl-8b"
    Then the stored price override for "n1" "qwen3-vl-8b" is 2 in and 3 out

  # ---------------------------------------------------------------- clear
  Scenario: Clearing the override restores the node's own taxonomy on its next registration
    Given alice set the category of "n1" "qwen3-vl-8b" to "vision/ocr"
    When alice clears the category of "n1" "qwen3-vl-8b"
    Then the response status is 200
    And there is no stored override for "n1" "qwen3-vl-8b"
    When node "n1" re-registers serving "qwen3-vl-8b" with its own taxonomy "language/chat" inferred
    Then the public offer for "n1" "qwen3-vl-8b" has category "language/chat", tags "" and source "inferred"

  Scenario: Clearing when there is no override is a harmless no-op
    When alice clears the category of "n1" "qwen3-vl-8b"
    Then the response status is 200

  # ---------------------------------------------------------------- validation
  Scenario Outline: An invalid category is refused with the reason, and nothing is stored
    When alice POSTs /provider/meta for "n1" "qwen3-vl-8b" with <declaration>
    Then the response status is 400
    And the error mentions "<reason>"
    And there is no stored override for "n1" "qwen3-vl-8b"
    And the public offer for "n1" "qwen3-vl-8b" has category "language/chat", tags "" and source "inferred"

    Examples:
      | declaration                                          | reason                   |
      | category "medical" and tags ""                       | unknown category         |
      | category "vision/code" and tags ""                   | not a vision subcategory |
      | category "vision/ocr" and tags "a/b"                 | invalid tag              |
      | category "vision/ocr" and tags "diagnosis"           | reserved tag             |
      | category "vision/ocr" and tags "t1,t2,t3,t4,t5,t6,t7,t8,t9" | at most 8 tags    |
      | content policy "nsfw"                                | unknown content policy   |

  Scenario: A voice model can only be declared as audio
    Given alice's node "n3" is on air serving "kokoro" as a tts model
    When alice POSTs /provider/meta for "n3" "kokoro" with category "language/chat" and tags ""
    Then the response status is 400
    And the error mentions "a tts model is always audio/tts"

  Scenario: A declaration with no claim in it is a clear
    Given alice set the category of "n1" "qwen3-vl-8b" to "vision/ocr"
    When alice POSTs /provider/meta for "n1" "qwen3-vl-8b" with category "" and tags ""
    Then the response status is 200
    And there is no stored override for "n1" "qwen3-vl-8b"

  Scenario Outline: A malformed request is refused
    When alice POSTs /provider/meta the raw body <body>
    Then the response status is 400

    Examples:
      | body                                 |
      | not json                             |
      | {"model":"qwen3-vl-8b"}              |
      | {"node":"n1"}                        |

  # ---------------------------------------------------------------- who may
  Scenario: Another owner's node cannot be edited
    Given owner "mallory" is signed in
    When mallory POSTs /provider/meta for "n1" "qwen3-vl-8b" with category "vision/ocr" and tags ""
    Then the response status is 403
    And there is no stored override for "n1" "qwen3-vl-8b"

  Scenario: Another owner cannot clear alice's override
    Given alice set the category of "n1" "qwen3-vl-8b" to "vision/ocr"
    And owner "mallory" is signed in
    When mallory clears the category of "n1" "qwen3-vl-8b"
    Then the response status is 403
    And the stored override for "n1" "qwen3-vl-8b" has category "vision/ocr" and tags ""

  Scenario: A node bound to no account cannot be edited
    Given an unbound node "n9" is on air serving "m9"
    When alice POSTs /provider/meta for "n9" "m9" with category "vision/ocr" and tags ""
    Then the response status is 403

  Scenario: A request that is not signed in is refused
    When an anonymous client POSTs /provider/meta for "n1" "qwen3-vl-8b" with category "vision/ocr"
    Then the response status is 401
    And there is no stored override for "n1" "qwen3-vl-8b"

  Scenario: Only POST is accepted
    When alice GETs /provider/meta
    Then the response status is 405

  # The override is the operator's DESCRIPTION; it never touches screening (policy_honesty
  # .feature) or routing.
  Scenario: An owner-declared uncensored grade still goes through screening
    When alice POSTs /provider/meta for "n1" "qwen3-vl-8b" with content policy "uncensored"
    Then the public offer for "n1" "qwen3-vl-8b" has content policy "uncensored"
    And moderation still screens requests to "qwen3-vl-8b"

  # ---------------------------------------------------------------- review regressions (founder OK 2026-10-05)
  # Multi-instance: a live owner edit is published to the shared registry like a register,
  # so every instance serves it, and a reconcile never reverts it. This was a PRE-EXISTING
  # gap for the owner's web-set PRICE too (fixed in the same shared helper).
  Scenario: A live category set on one instance is served by every instance
    Given a second broker instance shares the registry and the store
    And alice set the category of "n1" "qwen3-vl-8b" to "vision/ocr"
    When both instances sync the shared registry
    Then on every instance the public offer for "n1" "qwen3-vl-8b" has category "vision/ocr"

  Scenario: A live price set on one instance is served by every instance
    Given a second broker instance shares the registry and the store
    And alice set the price of "n1" "qwen3-vl-8b" to 2 in and 3 out
    When both instances sync the shared registry
    Then on every instance the public offer for "n1" "qwen3-vl-8b" is priced 2 in and 3 out

  # An override that no longer fits the model as it registers (saved while the node was off
  # air, so checked as chat; the node then registers it as a voice) falls back to the node's
  # own taxonomy instead of blanking it.
  Scenario: An override that no longer fits falls back to the node's own taxonomy
    Given alice also owns node "n2" which is not on air
    And alice set the category of "n2" "kokoro" to "language/chat"
    When node "n2" registers serving "kokoro" as a tts model with its own tags "radio" declared
    Then the public offer for "n2" "kokoro" has category "audio/tts", tags "radio" and source "declared"

  Scenario: An edit for a node that is known but off air says it applies next time
    Given node "n1" has gone off air
    When alice POSTs /provider/meta for "n1" "qwen3-vl-8b" with category "vision/ocr" and tags ""
    Then the response status is 200
    And the response says the category applies the next time the node is on air
