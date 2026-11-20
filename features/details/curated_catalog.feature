# RogerAI's own words for RogerAI's own models.
#
# Station text is one stranger describing their copy. For models RogerAI publishes (the Wave
# family and its adapters) we want ONE description, written and reviewed by us, shown the
# same way on every surface. The broker holds a small curated list, embedded in its binary
# (internal/catalog/notes.json via go:embed, read by catalog.Notes), keyed by model id.
#
# Why the broker and not each client: every surface (TUI, local console, website, /v1/models,
# CLI) already reads /discover offers. Stamping the curated text onto the offer view at the
# broker means no client carries a copy that can drift, and a wording fix ships with one
# broker deploy. Why not reuse catalog.Entry: Entry is the acquisition manifest (repo, sha256,
# bytes, serve memory) and refuses an entry without them; a description must exist for a
# model we do not distribute as a download (an adapter served beside its base). Notes is a
# sibling type in the same package, so "the models RogerAI knows about" stays in one place.
#
# Why not generate it from web/src/data/model-cards.json: that file is website build input
# with measured numbers and R-numbers; the notes are four short sentences per id. A test
# (catalog notes <-> model cards) holds every note's page link to a page the website builds,
# so the two cannot point at different pages.
#
# A curated note OVERRIDES station text for summary, best_at, not_for, and fills base_model
# and license when the note has them. It sets details_source "rogerai" and a page link. It
# never touches category, tags, tuning, content policy, price or routing.
#
# Honesty: the note describes the MODEL ID. Anyone can name an offer "demo-1b-chat". The
# details view always shows the curated text together with the station's existing lineage
# mark (◆ verified lineage, or its absence), and says "RogerAI's description of this model"
# rather than "this station is". Reserving RogerAI's model ids for verified stations is a
# separate decision (open question in TEST-PLAN.md), not part of this spec.

Feature: RogerAI's curated notes describe RogerAI's own models on every surface

  Background:
    Given RogerAI's curated notes contain:
      | id                     | summary                                         | best_at                                   | not_for                                  | base_model | license    | page                       |
      | demo-1b-chat           | General chat on the demo 1B base.               | Conversation, short technical writing.    | Fire decisions or reading sensors.       | demo-1b    | Apache-2.0 | /research-demo-1b.html     |
      | demo-1b-fire-decision  | Picks one fire-defense action from site state.  | Choosing among a fixed list of actions.   | Chat. It answers with one action only.   | demo-1b    | Apache-2.0 | /research-demo-1b.html#fire |
      | demo-1b-fire-vl        | Confirms or rejects smoke or flame in a frame.  | Single camera frames, yes or no.          | Video, or deciding what to do about it.  | demo-1b    | Apache-2.0 | /research-demo-1b.html#vl  |

  # ---------------------------------------------------------------- stamping
  Scenario: A curated model's offer carries RogerAI's text
    Given a station on air with model "demo-1b-fire-decision" and no details
    When a consumer reads /discover
    Then the offer for "demo-1b-fire-decision" has summary "Picks one fire-defense action from site state."
    And its best_at is "Choosing among a fixed list of actions."
    And its not_for is "Chat. It answers with one action only."
    And its details source is "rogerai"
    And its page is "/research-demo-1b.html#fire"

  Scenario: Curated text overrides a station's own text for the same id
    Given a station on air with model "demo-1b-chat" and summary "Best model ever, try it."
    When a consumer reads /discover
    Then the offer for "demo-1b-chat" has summary "General chat on the demo 1B base."
    And its details source is "rogerai"

  Scenario: Curated base model and license fill the offer
    Given a station on air with model "demo-1b-fire-vl" declaring base model "someone/else" and license "MIT"
    When a consumer reads /discover
    Then the offer for "demo-1b-fire-vl" has base model "demo-1b" and license "Apache-2.0"

  Scenario: A curated note with no license leaves the station's license
    Given RogerAI's curated note for "demo-1b-chat" has no license
    And a station on air with model "demo-1b-chat" declaring license "MIT"
    When a consumer reads /discover
    Then the offer for "demo-1b-chat" has license "MIT"

  Scenario: Curated notes never change category, tags, tuning, policy or price
    Given a station on air with model "demo-1b-chat" declaring category "language/chat", tags "ops", tuning "adapter", policy "aligned" and price out 0.40
    When a consumer reads /discover
    Then the offer for "demo-1b-chat" keeps category "language/chat", tags "ops", tuning "adapter", policy "aligned" and price out 0.40

  Scenario: An id not in the notes keeps the station's text
    Given a station on air with model "someone-7b" and summary "A 7B chat model."
    When a consumer reads /discover
    Then the offer for "someone-7b" has summary "A 7B chat model."
    And its details source is "declared"
    And it has no page

  Scenario Outline: Curated matching is exact on the canonical model id
    Given a station on air with model "<sent>" and summary "station text"
    When a consumer reads /discover
    Then the offer's details source is "<source>"

    Examples:
      | sent                     | source   |
      | demo-1b-chat             | rogerai  |
      | DEMO-1B-CHAT             | rogerai  |
      | demo-1b-chat-v2          | declared |
      | demo-1b-chat:Q4_K_M      | declared |
      | xdemo-1b-chat            | declared |
      | demo-1b-chat\u200b       | rogerai  |

  Scenario: Curated text is stamped on the cached feed, not per request
    Given a station on air with model "demo-1b-chat"
    When /discover is read 100 times within the cache window
    Then the curated notes were applied once per cache fill

  Scenario: Curated stamping applies to an offline offer too
    Given a station with model "demo-1b-chat" that is off air but still listed
    When a consumer reads /discover
    Then the offer for "demo-1b-chat" has details source "rogerai"

  Scenario: Curated notes reach /v1/models
    Given a station on air with model "demo-1b-chat"
    When a consumer reads /v1/models/demo-1b-chat
    Then the entry's "description" is "General chat on the demo 1B base."
    And the entry's rogerai.details.source is "rogerai"
    And the entry's rogerai.details.page is "https://rogerai.fm/research-demo-1b.html"

  # ---------------------------------------------------------------- honesty
  Scenario: The details view says whose description it is
    Given a station on air with model "demo-1b-chat" without verified lineage
    When the TUI, the console and the website show the details for "demo-1b-chat"
    Then each labels the text "RogerAI's description of this model"
    And each shows that the station's weights are not verified

  Scenario: Verified lineage is shown beside curated text when present
    Given a station on air with model "demo-1b-chat" with verified lineage
    When the TUI shows the details for "demo-1b-chat"
    Then the about block shows the ◆ verified lineage mark beside "RogerAI's description of this model"

  # ---------------------------------------------------------------- the list itself (unit-level rules, run as godog too)
  Scenario: The embedded notes load and validate at broker start
    When the broker starts
    Then the curated notes load with no error

  Scenario Outline: A malformed note fails the broker test suite, never ships
    Given a notes file where <defect>
    When the notes are validated
    Then validation fails naming "<reason>"

    Examples:
      | defect                                         | reason                    |
      | an entry has no id                             | has no id                 |
      | two entries share an id                        | duplicate note id         |
      | two ids differ only in case                    | duplicate note id         |
      | an entry has no summary                        | has no summary            |
      | a summary has 141 characters                   | summary: at most 140      |
      | a best_at has 201 characters                   | best at: at most 200      |
      | a not_for has 201 characters                   | not for: at most 200      |
      | a summary contains a control character         | control character         |
      | a page is "https://example.invalid/x"          | page must be a site path  |
      | a page is "javascript:alert(1)"                | page must be a site path  |
      | a page is "//example.invalid/x"                | page must be a site path  |
      | a page is "/research-x.html" with a space      | page must be a site path  |
      | a summary uses the reserved word "diagnosis"   | reserved use              |
      | the file is not valid JSON                     | notes: invalid JSON       |

  Scenario: Every note's page is a page the website builds
    When the notes are compared with the built website pages
    Then every note's page path, without its fragment, names a page in the build

  Scenario: The page link is absolute only on the public feed
    Given the note for "demo-1b-chat" has page "/research-demo-1b.html"
    Then /discover carries page "/research-demo-1b.html"
    And /v1/models carries page "https://rogerai.fm/research-demo-1b.html"
    And the TUI shows "rogerai.fm/research-demo-1b.html"

  Scenario: An empty notes list changes nothing
    Given RogerAI's curated notes are empty
    And a station on air with model "demo-1b-chat" and summary "station text"
    When a consumer reads /discover
    Then the offer for "demo-1b-chat" has summary "station text" and details source "declared"
