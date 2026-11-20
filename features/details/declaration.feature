# The operator writes the details where they already declare a category:
#
#   roger share ... --summary "..." --best-at "..." --not-for "..."     (cmd/rogerai/share_meta.go)
#   the TUI SHARE tab `g` editor (modeShareMeta, internal/tui/taxonomy.go)
#   the local web console category modal (internal/webui/meta.go, console.js cat-* fields)
#   the website owner editor (web/app/lib/console/CatEditor.svelte -> broker /provider/meta,
#     cmd/rogerai-broker/provider_meta.go), stored in offer_meta_overrides.taxonomy (JSONB, so
#     no migration)
#
# Every one of them goes through protocol.Taxonomy.Declare, so the rules (text_sanitization)
# are identical everywhere. Saved per model in config.json share_meta beside the category.
#
# The flag rule stays the existing one (share_cli.feature): ANY taxonomy or details flag
# replaces the model's saved declaration as a whole, so what is on air is exactly what the
# command line says. The help text says so, because a summary-only command now clears a saved
# category.

Feature: The operator declares a model's details in every surface that declares a category

  Background:
    Given a share controller with a detected model "demo-1b-fire-decision" inferred as "language/chat"

  # ---------------------------------------------------------------- roger share flags
  @cli
  Scenario: Details flags ride the offer as declared
    When the operator runs share with '--summary "Picks one action." --best-at "Fixed action lists." --not-for "Chat."'
    Then the on-air summary for "demo-1b-fire-decision" is "Picks one action."
    And the on-air best_at for "demo-1b-fire-decision" is "Fixed action lists."
    And the on-air not_for for "demo-1b-fire-decision" is "Chat."
    And the on-air details source for "demo-1b-fire-decision" is "declared"

  @cli
  Scenario: Details flags are saved for the model
    When the operator runs share with '--summary "Picks one action."'
    Then config.json share_meta for "demo-1b-fire-decision" has summary "Picks one action."

  @cli
  Scenario: A saved summary is used when no flag is given
    Given config.json share_meta for "demo-1b-fire-decision" has summary "Picks one action."
    When the operator runs share with ""
    Then the on-air summary for "demo-1b-fire-decision" is "Picks one action."

  @cli
  Scenario: Any details flag replaces the saved declaration as a whole
    Given config.json share_meta for "demo-1b-fire-decision" has category "language/classification" and summary "Old words."
    When the operator runs share with '--best-at "New words."'
    Then config.json share_meta for "demo-1b-fire-decision" has no category and no summary
    And config.json share_meta for "demo-1b-fire-decision" has best_at "New words."

  @cli
  Scenario: Details flags combine with taxonomy flags in one declaration
    When the operator runs share with '--category language/classification --tuning adapter --base-model demo-1b --summary "Picks one action."'
    Then config.json share_meta for "demo-1b-fire-decision" has category "language/classification", tuning "adapter", base model "demo-1b" and summary "Picks one action."

  @cli
  Scenario Outline: An invalid details flag stops the share before anything goes on air
    Given config.json share_meta for "demo-1b-fire-decision" has summary "Kept."
    When the operator runs share with "<flags>"
    Then share fails with an error mentioning "<reason>"
    And nothing went on air
    And config.json share_meta for "demo-1b-fire-decision" is unchanged

    Examples:
      | flags                                 | reason                    |
      | --summary <141 a>                     | summary: at most 140      |
      | --best-at <201 a>                     | best at: at most 200      |
      | --not-for <201 a>                     | not for: at most 200      |
      | --summary "patient triage"            | reserved use              |
      | --best-at "weapons guidance"          | reserved use              |

  @cli
  Scenario: The details flags are listed in share --advanced help
    When the operator runs share with "--advanced"
    Then the help lists "--summary", "--best-at" and "--not-for"
    And the help says each is shown to people browsing the market
    And the help says any taxonomy or details flag replaces the saved declaration

  @cli
  Scenario: The flags appear in the help next to the taxonomy flags, not in the basic help
    When the operator runs share with "--help"
    Then the basic help does not list "--summary"

  @cli
  Scenario: An empty flag value clears that field within the new declaration
    When the operator runs share with '--summary "" --best-at "Fixed action lists."'
    Then config.json share_meta for "demo-1b-fire-decision" has no summary and best_at "Fixed action lists."

  # ---------------------------------------------------------------- TUI g editor
  @tui
  Scenario: The g editor shows the three details fields after base model
    Given the SHARE tab with "demo-1b-fire-decision" selected
    When the operator presses "g"
    Then the editor rows are "category, subcategory, tags, tuning, policy, license, base model, summary, best at, not for"

  @tui
  Scenario: Typing into a details field and saving declares it
    Given the g editor is open for "demo-1b-fire-decision"
    When the operator moves to "summary", types "Picks one action." and saves
    Then the saved declaration for "demo-1b-fire-decision" has summary "Picks one action."
    And the status line reads "category saved for demo-1b-fire-decision"

  @tui
  Scenario: The editor shows a live character count against the cap
    Given the g editor is open for "demo-1b-fire-decision"
    When the operator types 130 characters into "summary"
    Then the summary row shows "130/140"

  @tui
  Scenario: The editor stops accepting input at the cap
    Given the g editor is open for "demo-1b-fire-decision"
    When the operator types 150 characters into "summary"
    Then the summary field holds 140 characters

  @tui
  Scenario: Pasted control characters never reach the field
    Given the g editor is open for "demo-1b-fire-decision"
    When the operator pastes "a\u001b[31mb\nc" into "summary"
    Then the summary field holds "a[31mb c"

  @tui
  Scenario: A refused save keeps the editor open with the reason
    Given the g editor is open for "demo-1b-fire-decision"
    When the operator sets summary "patient triage" and saves
    Then the editor is still open
    And the editor shows "reserved use \"triage\""

  @tui
  Scenario: A voice model's editor also offers the details fields
    Given a detected voice model "kokoro" with modality "tts"
    When the operator opens the g editor for "kokoro"
    Then the editor rows include "summary, best at, not for"

  @tui
  Scenario: Long field values scroll inside the row and never widen the screen
    Given the g editor is open at 60 columns with a 200-character best_at
    Then no rendered line is wider than 60 columns

  # ---------------------------------------------------------------- local web console
  @web
  Scenario: The console category modal has the three fields with maxlength
    When the operator opens the console category modal for "demo-1b-fire-decision"
    Then it has a "summary" input with maxlength 140
    And it has a "best at" textarea with maxlength 200
    And it has a "not for" textarea with maxlength 200

  @webui
  Scenario: Saving the console modal declares the details
    When the operator saves summary "Picks one action." in the console modal
    Then POST /api/share/meta carried summary "Picks one action."
    And the saved declaration for "demo-1b-fire-decision" has summary "Picks one action."

  @webui
  Scenario: A console request bypassing maxlength is still refused
    When a client posts /api/share/meta with a 141-character summary
    Then the response is 400 with "summary: at most 140 characters (got 141)"

  @web
  Scenario: The console modal prefills the saved details
    Given the saved declaration for "demo-1b-fire-decision" has summary "Picks one action."
    When the operator opens the console category modal for "demo-1b-fire-decision"
    Then the summary input holds "Picks one action."

  @web
  Scenario: The console modal's guess detection counts details as a declaration
    Given the saved declaration for "demo-1b-fire-decision" has only summary "Picks one action."
    When the operator opens the console category modal for "demo-1b-fire-decision"
    Then the modal does not say the values are a guess

  # ---------------------------------------------------------------- website owner editor (broker override)
  @broker
  Scenario: The owner sets details from the website and they apply live
    Given the owner of node "n1" is signed in on the website
    And node "n1" is on air with "demo-1b-fire-decision"
    When the owner saves summary "Picks one action." in the website editor
    Then /provider/meta answers applied "live"
    And /discover shows summary "Picks one action." for that offer

  @broker
  Scenario: An owner override with details is stored and re-applied at the next register
    Given the owner saved summary "Picks one action." for node "n1" while it was off air
    When node "n1" registers "demo-1b-fire-decision"
    Then /discover shows summary "Picks one action." for that offer

  @broker
  Scenario: A non-owner cannot set details
    When a signed-in user who does not own node "n1" posts details for it
    Then the response is 403 "you do not own this node"

  @broker
  Scenario: The broker owner endpoint applies the same strict rules
    When the owner posts a 201-character best_at to /provider/meta
    Then the response is 400 with "best at: at most 200 characters (got 201)"

  @broker
  Scenario: Clearing the owner override clears the details too
    Given the owner saved summary "Picks one action." for node "n1"
    When the owner clears the override
    Then the next registration of node "n1" carries only the node's own details

  @broker
  Scenario: An owner override for a curated model id is accepted but curated text still shows
    Given "demo-1b-fire-decision" is in RogerAI's curated notes
    When the owner saves summary "My words." in the website editor
    Then the save succeeds
    And /discover shows RogerAI's summary for "demo-1b-fire-decision"
    And the website editor says "RogerAI describes this model; your text shows only where it does not"
