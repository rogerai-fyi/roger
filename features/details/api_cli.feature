# The machine-readable side, and the command line.
#
# /v1/models (cmd/rogerai-broker/models.go) is the OpenAI/OpenRouter-shaped catalog every SDK
# reads. OpenRouter carries a top-level "description" per model; we fill it from the band's
# summary (same vote as the TUI) and add a rogerai.details block with the rest. Nothing is
# added when there is no text, so an entry with no details is byte-for-byte as today.
#
# CLI: `roger models` (alias of `roger search`, client.Search) lists what is on air. With a
# model id argument it prints that model's details, the same lines as the TUI ABOUT block,
# plain text with no colour when stdout is not a terminal, and `--json` prints the
# rogerai.details block. No new command name.

Feature: /v1/models and `roger models <id>` carry what a model is for

  Background:
    Given these on-air chat offers:
      | node | model                  | tuning  | base_model | license    | summary          | best_at       | not_for        |
      | n1   | demo-1b-fire-decision  | adapter | demo-1b    | Apache-2.0 |                  |               |                |
      | n2   | someone-7b             | instruct| base-7b    | MIT        | A 7B chat model. | Long answers. | Short replies. |
      | n3   | someone-7b             | instruct| base-7b    | MIT        | A 7B chat model. | Long answers. | Short replies. |
      | n4   | someone-7b             | instruct| base-7b    | MIT        | Other words.     |               |                |
      | n5   | mystery-m              |         |            |            |                  |               |                |
    And RogerAI's curated notes describe "demo-1b-fire-decision" as in curated_catalog.feature

  # ---------------------------------------------------------------- /v1/models
  Scenario: A curated model's entry carries description and details
    When a consumer reads /v1/models/demo-1b-fire-decision
    Then the entry's "description" is "Picks one fire-defense action from site state."
    And the entry's rogerai.details is:
      | key        | value                                                |
      | source     | rogerai                                              |
      | summary    | Picks one fire-defense action from site state.       |
      | best_at    | Choosing among a fixed list of actions.              |
      | not_for    | Chat. It answers with one action only.               |
      | base_model | demo-1b                                              |
      | tuning     | adapter                                              |
      | license    | Apache-2.0                                           |
      | page       | https://rogerai.fm/research-demo-1b.html#fire        |

  Scenario: A declared model's entry uses the band vote
    When a consumer reads /v1/models/someone-7b
    Then the entry's "description" is "A 7B chat model."
    And the entry's rogerai.details.source is "declared"
    And the entry's rogerai.details.declared_by is 2
    And the entry's rogerai.details.stations is 3
    And the entry's rogerai.details has no "page"

  Scenario: A model with no text has no description and no details block
    When a consumer reads /v1/models/mystery-m
    Then the entry has no "description" key
    And the entry's rogerai block has no "details" key

  Scenario: Entries without details are unchanged from today
    Given the same offers with every details field removed
    When a consumer reads /v1/models
    Then the response is byte-identical to the response before this change

  Scenario: The list and the single-id endpoint agree
    When a consumer reads /v1/models and /v1/models/someone-7b
    Then the "someone-7b" entry is identical in both

  Scenario: Filters still apply and keep details
    When a consumer reads /v1/models?category=language
    Then every returned entry with text carries its description

  Scenario: Class entries (@class/...) carry no description
    When a consumer reads /v1/models
    Then no "@class/" entry has a "description" key

  Scenario: The cached catalog is not recomputed per request for details
    When /v1/models is read 100 times within the cache window
    Then the details were computed once per cache fill

  Scenario: A JSON consumer receives the text escaped, never raw markup
    Given an on-air offer for "x-model" with summary "</script><script>alert(1)</script>"
    When a consumer reads /v1/models/x-model
    Then the response Content-Type is "application/json"
    And the "description" decodes to exactly "</script><script>alert(1)</script>"

  # ---------------------------------------------------------------- roger models <id>
  Scenario: roger models with an id prints the details
    When the user runs "roger models demo-1b-fire-decision"
    Then the output is:
      """
      demo-1b-fire-decision
        RogerAI's description of this model
        Picks one fire-defense action from site state.
        best at   Choosing among a fixed list of actions.
        not for   Chat. It answers with one action only.
        kind      text in, text out  ·  adapter on demo-1b
        license   Apache-2.0
        page      https://rogerai.fm/research-demo-1b.html#fire
        on air    1 station   ·   roger use demo-1b-fire-decision
      """
    And the exit code is 0

  Scenario: Piped output has no colour or control bytes
    When the user runs "roger models someone-7b" with stdout not a terminal
    Then the output contains no byte 0x1b

  Scenario: --json prints the details block
    When the user runs "roger models someone-7b --json"
    Then the output is one JSON object equal to the /v1/models rogerai.details for "someone-7b" plus "id"

  Scenario: An id that is not on air is a clear error
    When the user runs "roger models nope-9b"
    Then the output says "nope-9b is not on air - roger models lists what is"
    And the exit code is 1

  Scenario: A near-miss id suggests the on-air ids that contain it
    When the user runs "roger models fire"
    Then the output says "fire is not on air"
    And the output lists "demo-1b-fire-decision" as a close match

  Scenario: roger models with no id is unchanged
    When the user runs "roger models"
    Then the output is the same listing as "roger search" before this change

  Scenario: The listing shows each model's summary when there is room
    When the user runs "roger models" in a 120-column terminal
    Then the "someone-7b" line ends with "A 7B chat model." cut to fit
    And the "mystery-m" line has no summary

  Scenario: roger models <id> in LAN mode reads the LAN tower
    Given the CLI is in LAN mode
    When the user runs "roger models someone-7b"
    Then the details are read from the LAN tower's /discover

  # ---------------------------------------------------------------- docs
  Scenario: The manual documents the details
    When the manual is built
    Then the share section documents "--summary", "--best-at" and "--not-for" with their limits
    And the TUNE IN key table lists "i" as "about + station log"
    And the API reference documents "description" and rogerai.details on /v1/models
    And it says station text is the station's own words and is not verified
