# Layer 1 (protocol) spec - a shared model can say what it is FOR.
#
# A ModelOffer gains an optional taxonomy: one primary category + subcategory from a small
# versioned list, up to eight free domain tags, how the weights were tuned, the base model
# it derives from, a content-policy grade, a license, and WHO made the claim (the operator
# declared it, or the node's detector inferred it). Nothing here is verified: these are
# DISPLAY + FILTER attributes, exactly like quant/weights/variant. The broker routes on
# none of them. Founder rulings 2026-10-05: one primary category + tags; content policy is
# graded and shown normally; best-in-category routing is client-side.
# Design: docs-internal MODEL-CATEGORIES-DESIGN-2026-10-05.md.

Feature: Offers carry an optional, normalized, unverified model taxonomy

  Background:
    Given a node registering an offer with the broker

  # ---------------------------------------------------------------- back-compat
  Scenario: An offer with no taxonomy claims nothing
    Given an offer for model "llama3.2" with no taxonomy fields
    When the broker normalizes the offer
    Then the offer has no category
    And the offer has no subcategory
    And the offer has no tags
    And the offer has no tuning, base model, content policy, license or claim source

  Scenario: An offer with no taxonomy serializes with no taxonomy keys
    Given an offer for model "llama3.2" with no taxonomy fields
    When the offer is marshalled to JSON
    Then the JSON has none of the keys "category,subcategory,tags,tuning,base_model,content_policy,license,taxonomy_source"

  Scenario: The taxonomy survives a JSON round-trip
    Given an offer for model "qwen3-vl-8b" with category "vision", subcategory "ocr", tags "nameplate-ocr,manufacturing", tuning "finetune", base model "Qwen/Qwen3-VL-8B-Instruct", content policy "aligned", license "Apache-2.0" and source "declared"
    When the offer is marshalled to JSON and back
    Then every taxonomy field is unchanged

  # ---------------------------------------------------------------- the category list (v1)
  Scenario Outline: Every v1 category and subcategory pair is accepted
    Given an offer with category "<category>" and subcategory "<subcategory>"
    When the broker normalizes the offer
    Then the offer category is "<category>"
    And the offer subcategory is "<subcategory>"

    Examples:
      | category   | subcategory          |
      | language   | chat                 |
      | language   | code                 |
      | language   | reasoning            |
      | language   | summarize            |
      | language   | translate            |
      | language   | extraction           |
      | language   | classification       |
      | language   | creative             |
      | vision     | vlm                  |
      | vision     | ocr                  |
      | vision     | document             |
      | vision     | detection            |
      | vision     | segmentation         |
      | vision     | classification       |
      | audio      | audio-classification |
      | retrieval  | embeddings           |
      | retrieval  | rerank               |
      | generation | image                |
      | generation | video                |
      | generation | music                |
      | signals    | time-series          |
      | signals    | anomaly              |
      | signals    | tabular              |

  Scenario: A category with no subcategory is kept
    Given an offer with category "vision" and no subcategory
    When the broker normalizes the offer
    Then the offer category is "vision"
    And the offer has no subcategory

  Scenario Outline: Category and subcategory are case- and space-insensitive
    Given an offer with category "<sent_cat>" and subcategory "<sent_sub>"
    When the broker normalizes the offer
    Then the offer category is "<category>"
    And the offer subcategory is "<subcategory>"

    Examples:
      | sent_cat     | sent_sub    | category  | subcategory |
      | Vision       | OCR         | vision    | ocr         |
      | Language     | Code        | language  | code        |
      | RETRIEVAL    | Embeddings  | retrieval | embeddings  |

  Scenario: Surrounding spaces on category and subcategory are ignored
    Given an offer with category "  language " and subcategory " Code  "
    When the broker normalizes the offer
    Then the offer category is "language"
    And the offer subcategory is "code"

  # A category GROUPS browse and routing, so an unknown one is dropped like an unknown
  # capability - otherwise every typo becomes a new aisle on the dial.
  Scenario Outline: An unknown category drops the whole category claim
    Given an offer with category "<category>" and subcategory "<subcategory>"
    When the broker normalizes the offer
    Then the offer has no category
    And the offer has no subcategory

    Examples:
      | category     | subcategory |
      | medical      | radiology   |
      | weapons      | targeting   |
      | general      | chat        |
      | vision/ocr   |             |
      | language;rm  | chat        |

  Scenario Outline: A subcategory that does not belong to its category is dropped alone
    Given an offer with category "<category>" and subcategory "<subcategory>"
    When the broker normalizes the offer
    Then the offer category is "<category>"
    And the offer has no subcategory

    Examples:
      | category  | subcategory |
      | language  | ocr         |
      | vision    | code        |
      | audio     | embeddings  |
      | retrieval | radiology   |
      | signals   | chat        |

  Scenario: A subcategory with no category is dropped
    Given an offer with no category and subcategory "ocr"
    When the broker normalizes the offer
    Then the offer has no category
    And the offer has no subcategory

  # ---------------------------------------------------------------- modality is the truth
  # The modality already decides billing and routing. A category can never contradict it.
  Scenario Outline: A voice modality fixes the category, whatever was declared
    Given an offer with modality "<modality>", category "<sent_cat>" and subcategory "<sent_sub>"
    When the broker normalizes the offer
    Then the offer category is "audio"
    And the offer subcategory is "<subcategory>"

    Examples:
      | modality | sent_cat | sent_sub | subcategory |
      | tts      |          |          | tts         |
      | tts      | language | chat     | tts         |
      | tts      | audio    | stt      | tts         |
      | stt      |          |          | stt         |
      | stt      | vision   | ocr      | stt         |

  Scenario Outline: A chat offer cannot claim a voice subcategory
    Given an offer with modality "chat", category "audio" and subcategory "<subcategory>"
    When the broker normalizes the offer
    Then the offer category is "audio"
    And the offer has no subcategory

    Examples:
      | subcategory |
      | tts         |
      | stt         |

  Scenario: A chat offer may be an audio classifier
    Given an offer with modality "chat", category "audio" and subcategory "audio-classification"
    When the broker normalizes the offer
    Then the offer subcategory is "audio-classification"

  # ---------------------------------------------------------------- tags
  Scenario: Tags are lowercased, trimmed, deduplicated and keep their declared order
    Given an offer with tags " Manufacturing ,surface-defect,MANUFACTURING,ppe "
    When the broker normalizes the offer
    Then the offer tags are "manufacturing,surface-defect,ppe"

  Scenario Outline: Inner spaces and underscores in a tag become hyphens
    Given an offer with tags "<sent>"
    When the broker normalizes the offer
    Then the offer tags are "<tags>"

    Examples:
      | sent                    | tags                   |
      | smoke fire              | smoke-fire             |
      | predictive_maintenance  | predictive-maintenance |
      | vehicle   damage        | vehicle-damage         |
      | --edge--                | edge                   |

  Scenario Outline: A tag outside [a-z0-9-] is dropped, not repaired
    Given an offer with tags "ok,<bad>"
    When the broker normalizes the offer
    Then the offer tags are "ok"

    Examples:
      | bad                 |
      | fire\u001b[31m      |
      | <script>            |
      | über                |
      | a/b                 |
      | tag.with.dots       |
      | 🔥                  |

  Scenario: An empty tag is dropped
    Given an offer with tags "ok,, ,-"
    When the broker normalizes the offer
    Then the offer tags are "ok"

  Scenario: A tag longer than 32 characters is dropped
    Given an offer with tags "ok" and one tag of 33 characters
    When the broker normalizes the offer
    Then the offer tags are "ok"

  Scenario: A tag of exactly 32 characters is kept
    Given an offer with one tag of 32 characters
    When the broker normalizes the offer
    Then the offer has 1 tag

  Scenario: Only the first eight distinct valid tags are kept
    Given an offer with tags "t1,t2,t1,t3,t4,t5,t6,t7,t8,t9,t10"
    When the broker normalizes the offer
    Then the offer tags are "t1,t2,t3,t4,t5,t6,t7,t8"

  Scenario: A thousand tags cost nothing past the cap
    Given an offer with 1000 distinct tags
    When the broker normalizes the offer
    Then the offer has 8 tags

  # Eight-markets rule (features/web/healthcare_defense_markets.feature): healthcare means
  # equipment and facilities, never patients; defense means sustainment, never effects.
  # A tag is a public claim about intended use, so the same lines hold.
  Scenario Outline: A reserved clinical or targeting tag is dropped
    Given an offer with tags "manufacturing,<reserved>"
    When the broker normalizes the offer
    Then the offer tags are "manufacturing"

    Examples:
      | reserved         |
      | diagnosis        |
      | triage           |
      | dosing           |
      | radiology        |
      | patient          |
      | medical-imaging  |
      | targeting        |
      | target-acquisition |
      | weapons          |
      | lethal           |
      | DIAGNOSIS        |

  Scenario Outline: Industrial and maintenance tags near the reserved words are kept
    Given an offer with tags "<tag>"
    When the broker normalizes the offer
    Then the offer tags are "<tag>"

    Examples:
      | tag                     |
      | medical-equipment       |
      | facility-maintenance    |
      | predictive-maintenance  |
      | sustainment             |
      | smoke-fire              |

  # ---------------------------------------------------------------- tuning + lineage
  Scenario Outline: Tuning is one of a closed set
    Given an offer with tuning "<sent>"
    When the broker normalizes the offer
    Then the offer tuning is "<tuning>"

    Examples:
      | sent      | tuning    |
      | base      | base      |
      | instruct  | instruct  |
      | finetune  | finetune  |
      | Merge     | merge     |
      | quantized | quantized |
      | adapter   | adapter   |
      | lora      |           |
      | unknown   |           |

  # Same sanitizer as quant/weights/variant: the ESC is what makes an escape sequence an
  # attack, so it is removed and the now-inert printable remainder stays (founder 2026-10-05).
  Scenario: The base model is kept verbatim, control characters stripped
    Given an offer with base model "  unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF\u001b[2J "
    When the broker normalizes the offer
    Then the offer base model is "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF[2J"

  Scenario: A base model longer than 128 characters is truncated to 128
    Given an offer with a base model of 300 characters
    When the broker normalizes the offer
    Then the offer base model is 128 characters long

  # Display-spoofing characters: a bidi override can make "evil-gguf" render as "fuga-live",
  # and zero-width or line-separator characters break a row the same way a newline does. The
  # shared display sanitizer (base model, license, quant, weights, variant) drops them.
  # Founder request 2026-10-05 (review finding).
  Scenario Outline: Bidi, zero-width and line-separator characters are stripped
    Given an offer with base model "Qwen<char>-8B"
    When the broker normalizes the offer
    Then the offer base model is "Qwen-8B"

    Examples:
      | char   |
      | \u202e |
      | \u202a |
      | \u200b |
      | \u200c |
      | \u200d |
      | \u200e |
      | \u200f |
      | \u2066 |
      | \u2069 |
      | \u2028 |
      | \u2029 |
      | \ufeff |

  Scenario: Printable non-ASCII in a name survives
    Given an offer with base model "Qwen/通义千问-7B"
    When the broker normalizes the offer
    Then the offer base model is "Qwen/通义千问-7B"

  # Caps count CHARACTERS, not bytes: a non-ASCII name gets the same room as an ASCII one.
  Scenario: A base model of 128 non-ASCII characters is kept whole
    Given an offer with a base model of 128 "通" characters
    When the broker normalizes the offer
    Then the offer base model is 128 characters long

  Scenario: A base model of 129 non-ASCII characters is truncated to 128 characters
    Given an offer with a base model of 129 "通" characters
    When the broker normalizes the offer
    Then the offer base model is 128 characters long

  Scenario: A license of 65 non-ASCII characters is truncated to 64 characters
    Given an offer with a license of 65 "許" characters
    When the broker normalizes the offer
    Then the offer license is 64 characters long

  # ---------------------------------------------------------------- content policy
  # Graded and shown normally (founder ruling). It describes the WEIGHTS; it never turns
  # the broker's screening off - see policy_honesty.feature.
  Scenario Outline: Content policy is one of a closed set
    Given an offer with content policy "<sent>"
    When the broker normalizes the offer
    Then the offer content policy is "<policy>"

    Examples:
      | sent            | policy          |
      | aligned         | aligned         |
      | reduced-refusal | reduced-refusal |
      | Uncensored      | uncensored      |
      | abliterated     | uncensored      |
      | nsfw            |                 |
      | none            |                 |

  # ---------------------------------------------------------------- license
  Scenario: A license is kept verbatim, trimmed and bounded to 64 characters
    Given an offer with license "  Apache-2.0  "
    When the broker normalizes the offer
    Then the offer license is "Apache-2.0"

  Scenario: An over-long license is truncated to 64 characters
    Given an offer with a license of 200 characters
    When the broker normalizes the offer
    Then the offer license is 64 characters long

  # ---------------------------------------------------------------- who claimed it
  # Declared (the operator said so) and inferred (the node's detector guessed) are both
  # node-originated and both unverified. "verified" can only ever be earned broker-side,
  # so a node cannot send it - the CapTools precedent (stripDeclaredTools).
  Scenario Outline: The claim source is declared or inferred, never verified
    Given an offer with category "vision" and claim source "<sent>"
    When the broker normalizes the offer
    Then the offer claim source is "<source>"

    Examples:
      | sent     | source   |
      | declared | declared |
      | inferred | inferred |
      | Inferred | inferred |
      |          | declared |
      | verified | declared |
      | trusted  | declared |

  Scenario: An offer with no taxonomy has no claim source even if one was sent
    Given an offer with no taxonomy fields and claim source "inferred"
    When the broker normalizes the offer
    Then the offer has no tuning, base model, content policy, license or claim source

  # ---------------------------------------------------------------- normalization is safe
  Scenario: Normalizing twice gives the same offer
    Given an offer with category "Vision", subcategory "OCR", tags "B, a ,b", tuning "Merge", content policy "abliterated" and claim source "Inferred"
    When the broker normalizes the offer twice
    Then the second result equals the first

  # Regression (slice 2): the category a voice modality DERIVES is the broker's, not a claim.
  # Re-normalizing at emission once read it as a claim and stamped source "declared" on a
  # voice station that declared nothing.
  Scenario Outline: A voice offer that declared nothing has no claim source, however often it is normalized
    Given an offer with modality "<modality>", category "" and subcategory ""
    When the broker normalizes the offer twice
    Then the second result equals the first
    And the offer has no tuning, base model, content policy, license or claim source

    Examples:
      | modality |
      | tts      |
      | stt      |

  Scenario: A voice offer with a real claim keeps its claim source
    Given an offer with modality "tts", category "" and subcategory ""
    And the offer also declares content policy "aligned" with claim source "inferred"
    When the broker normalizes the offer twice
    Then the offer claim source is "inferred"

  # ---------------------------------------------------------------- signing (rollout safety)
  # Same rule as capabilities/quant/variant: display fields are excluded from the possession
  # proof and omitted when empty, so mixed-version nodes and brokers never 401 each other.
  Scenario: A node with a taxonomy verifies against a broker that ignores the fields
    Given a node signs a registration whose offer has category "vision", tags "ppe" and content policy "aligned"
    When a broker that predates the taxonomy decodes and verifies it
    Then the registration signature is valid

  Scenario: A node without a taxonomy verifies against a broker that has the fields
    Given a node that predates the taxonomy signs a registration
    When a broker with the taxonomy decodes and verifies it
    Then the registration signature is valid

  Scenario: The taxonomy fields do not change the signed bytes
    Given two registrations identical except one offer has a full taxonomy
    Then their signing bytes are identical

  Scenario: Verifying does not strip the caller's taxonomy
    Given a signed registration whose offer has category "vision" and tags "ppe"
    When the broker verifies it
    Then the offer still has category "vision" and tags "ppe"

  Scenario: The model id is still covered by the signature
    Given a node signs a registration for model "qwen3-vl-8b" with category "vision"
    When the model id is changed to "other-model" in transit
    Then the registration signature is invalid
