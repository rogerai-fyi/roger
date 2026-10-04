# TUI - routing profiles, the [3] CONFIG editor and the dial filters that BIND (CONTRACT §1, §5,
# §9; defects 3 and 6 from features/routing/regression_pins.feature).
#
# PURPOSE: the booth exposes every routing knob a person would set interactively, every dial
# filter that hides supply also binds what a turn may route to (via the body object, not
# exclude lists derived from the last scan), the cost meter reads the new final usage chunk,
# and none of it touches the brand or the key map.
#
# GROUND TRUTH at origin/main 518c698b (internal/tui):
#   - [3] CONFIG is modeLimits, the spend-limits editor (view_money.go:155-262): a virtualized
#     table of band / max $/1M out / min t/s, editField 0=out 1=tps (tui.go:866-867), the wallet
#     panel with the editable monthly budget on top, `a` adds a row, enter edits, esc cancels.
#     Limit.Quants is edited elsewhere (the band detail) and persisted as limits.models.<m>.quants.
#   - Dial filters (update.go:560-600): F free, C confidential, O on air, U hide curated, Q cycle
#     quant, ~ private freq entry, f name filter. The filter set bounds pickAutoBand via
#     visibleBands (tui.go:857-863 comment; features/operator/auto_tune.feature).
#   - Binding today is exclude lists computed from m.bands (quant_route.go:34-130): the tuned
#     row's quant, the standing Limit.Quants, unioned in routeExcludes; chatExcludes for the
#     connected band. Known gaps in-code: stations that registered after the scan are not
#     excluded; standalone `roger use` does not apply the rule at all (quant_route.go:65-70).
#   - fNoCurated: agent turns on curated-ONLY bands are refused (agent.go:1543-1553) but a mixed
#     band can still route to a curated station because routeExcludes never adds curated ids
#     (defect 3; features/curated/curated_dial.feature:51-55 promises otherwise).
#   - The compact windowshade (m / alt+m) and ctrl+p = perms are pinned by
#     features/tui/* and the TUI-shortcuts memory; nothing here may collide.
#   - Cost meter: X-RogerAI-Cost header and the `: rogerai-cost=` comment.
#
# PROPOSED (marked): the CONFIG editor's new fields and their keys; `p` cycles pref inside
# CONFIG; the windowshade pref glyph; F emits `:free` sugar.
#
# Enforced by: internal/tui/routing_profiles_bdd_test.go (future; drives the real bubbletea
# model with key messages and a recording httptest broker, no mocks).

Feature: The booth exposes routing knobs, and every filter that hides supply also binds routing

  Background:
    Given the TUI is running against a broker that accepts the routing body object
    And the dial shows bands "qwen3-32b" (Q8_0 on n1, Q4_K_M on n2, curated on n3) and "llama-3.3-70b" (BF16 on n4)

  # --- [3] CONFIG becomes the profile editor -------------------------------------------

  Scenario: PROPOSED - the CONFIG table keeps its two headline columns and gains a detail plate
    When the operator opens [3] CONFIG
    Then the table still shows band / max $/1M out / min t/s exactly as today
    And the selected row's plate below the table lists the remaining routing fields for that band

  Scenario Outline: PROPOSED - tab walks every routing field of the selected band in this order
    Given the operator is on the row for "qwen3-32b" in [3] CONFIG
    When the operator presses tab <n> times
    Then the focused field is "<field>"

    Examples:
      | n  | field          |
      | 0  | max $/1M out   |
      | 1  | min t/s        |
      | 2  | max $/1M in    |
      | 3  | max $/request  |
      | 4  | quant          |
      | 5  | pref           |
      | 6  | require        |
      | 7  | params (B)     |
      | 8  | min ctx        |
      | 9  | max ttft       |
      | 10 | trust          |
      | 11 | self-hosted    |
      | 12 | region         |
      | 13 | max $/1M out   |

  Scenario: PROPOSED - shift+tab walks backwards and wraps
    Given the focused field is "max $/1M out"
    When the operator presses shift+tab
    Then the focused field is "region"

  Scenario Outline: PROPOSED - each field edits with the existing plate and persists to limits.models.<m>
    Given the focused field is "<field>" for "qwen3-32b"
    When the operator presses enter, types "<typed>", and presses enter
    Then config limits.models.qwen3-32b.<key> equals <stored>
    And the row's plate shows "<shown>"

    Examples:
      | field         | typed        | key          | stored          | shown              |
      | max $/1M out  | 2.5          | max_out      | 2.5             | out ≤ $2.50/1M     |
      | min t/s       | 20           | min_tps      | 20              | ≥20 t/s            |
      | max $/1M in   | 0.5          | max_in       | 0.5             | in ≤ $0.50/1M      |
      | max $/request | 0.02         | max_cost     | 0.02            | ≤ $0.02/req        |
      | params (B)    | 7-70         | params_b     | [7,70]          | 7-70B              |
      | min ctx       | 32k          | min_ctx      | 32768           | ctx ≥ 32k          |
      | max ttft      | 1500         | max_ttft_ms  | 1500            | ttft ≤ 1.5s        |
      | region        | eu,us        | region       | ["eu","us"]     | eu,us              |

  Scenario Outline: PROPOSED - choice fields cycle with space/enter instead of typing
    Given the focused field is "<field>" for "qwen3-32b"
    When the operator presses space <n> times
    Then config limits.models.qwen3-32b.<key> equals <stored>

    Examples:
      | field       | n | key         | stored          |
      | pref        | 1 | pref        | "cheap"         |
      | pref        | 2 | pref        | "fast"          |
      | pref        | 3 | pref        | "reliable"      |
      | pref        | 4 | pref        | (unset=balanced)|
      | trust       | 1 | trust_min   | "verified"      |
      | trust       | 2 | trust_min   | "confidential"  |
      | trust       | 3 | trust_min   | (unset=any)     |
      | self-hosted | 1 | self_hosted | true            |
      | self-hosted | 2 | self_hosted | (unset)         |

  Scenario: PROPOSED - require toggles tools and vision independently
    Given the focused field is "require" for "qwen3-32b"
    When the operator presses t
    Then config limits.models.qwen3-32b.require equals ["tools"]
    When the operator presses v
    Then config limits.models.qwen3-32b.require equals ["tools","vision"]
    When the operator presses t
    Then config limits.models.qwen3-32b.require equals ["vision"]

  Scenario: PROPOSED - quant picks from the labels actually on the dial for that model, plus "any"
    Given the focused field is "quant" for "qwen3-32b"
    Then the choices offered are exactly "any", "Q8_0", "Q4_K_M" (the labels on air for that model, verbatim)
    When the operator picks "Q8_0"
    Then config limits.models.qwen3-32b.quants equals ["Q8_0"]

  Scenario: An invalid typed value is refused on the plate and nothing is persisted
    Given the focused field is "params (B)" for "qwen3-32b"
    When the operator presses enter, types "70-7", and presses enter
    Then the plate shows "min must be ≤ max" in ember
    And config is unchanged
    And esc returns to the table

  Scenario: The default row edits limits.default with the same fields
    Given the operator is on the "default" row in [3] CONFIG
    When the operator sets pref to "cheap"
    Then config limits.default.pref equals "cheap"

  Scenario: PROPOSED - p in [3] CONFIG cycles the selected band's pref directly
    Given the operator is on the row for "qwen3-32b" and no field is being edited
    When the operator presses p
    Then config limits.models.qwen3-32b.pref equals "cheap"
    And the status line reads "qwen3-32b · pref cheap"

  Scenario: p while a field is being typed just types
    Given the operator is typing into "region"
    When the operator presses p
    Then the character "p" is appended to the edit buffer

  Scenario: The monthly budget row and wallet panel are unchanged
    When the operator opens [3] CONFIG
    Then the wallet panel and the editable monthly budget render exactly as approved

  Scenario: The table still virtualizes and clamps on a 24-row terminal
    Given 34 bands with limits and a 24-row terminal
    When the operator opens [3] CONFIG
    Then the "more above / more below" hints appear and nothing scrolls the alt buffer
    And the detail plate is dropped before any table row when height is short

  Scenario: The plate fits a 60-column terminal without breaking its border
    Given a 60-column terminal
    When the operator focuses "region" with value "eu,us,apac,latam"
    Then the plate's right border is on screen and the value is truncated with "…" before the keys are dropped

  Scenario: The editor stays mono + red: no new colors, no grids, no glows
    When the operator opens [3] CONFIG with every field set
    Then only the existing styles (dim, ink, ember, live, brand, selection bar) are used
    And NO_COLOR renders every field legibly

  Scenario: Config writes from the editor are atomic and preserve every other key
    When the operator edits any routing field
    Then config.json is written by saveConfig (temp + fsync + rename)
    And share, voices, palette, agent_perms are byte-identical afterwards

  # --- profiles in the booth ---------------------------------------------------------------

  Scenario: PROPOSED - the CONFIG screen lists named profiles under the bands table
    Given profiles "coding" and "cheap" exist
    When the operator opens [3] CONFIG
    Then a "profiles" section lists "default", "cheap", "coding" with one-line summaries

  Scenario: PROPOSED - enter on a profile shows its resolved body object read-only
    Given profile "coding" exists
    When the operator selects "coding" and presses enter
    Then the plate shows the resolved keys with sources, freq shown as "(set, hidden)"
    And editing profiles is done with `roger profile set` (the plate says so)

  Scenario: PROPOSED - the TUNE IN confirm offers the profile to tune under
    Given profile "coding" exists
    When the operator tunes "qwen3-32b" and reaches the confirm
    Then the confirm shows "routing: default · p cycles profile"
    When the operator presses p
    Then the confirm shows "routing: coding"
    And r on the confirm still re-scans the band as today (p, not r, because r is taken)
    And accepting tunes with profile "coding" resolved into the body object

  Scenario: The tuned body object is what every in-booth path sends
    Given the operator tuned "qwen3-32b" under profile "coding" (require tools, pref fast)
    When an in-channel chat turn, an agent turn, and a guest relay each go out
    Then each broker request carries roger.require = ["tools"] and roger.pref = "fast"

  Scenario: The connect-time est-cost line uses the profile's out cap
    Given profile "cheap" sets provider.max_price.completion = 1
    When the operator tunes under "cheap"
    Then the est-cost line is computed against $1/1M, not the band's max

  # --- dial filters emit body keys and bind ----------------------------------------------

  Scenario: F (free) tunes with :free sugar so only free-now stations may serve
    Given the operator toggled F on and tuned "qwen3-32b"
    Then the tune-time body carries model = "qwen3-32b:free"
    And a station that stops being free mid-session is no longer eligible for the next turn

  Scenario: C (confidential) tunes with roger.confidential
    Given the operator toggled C on and tuned "qwen3-32b"
    Then the tune-time body carries roger.confidential = true
    And no X-Roger-Confidential header is sent when the broker accepts the body

  Scenario: Q (quant) tunes with provider.quantizations, not an exclude list (defect 6 in the booth)
    Given the operator cycled Q to "Q8_0" and tuned "qwen3-32b"
    Then the tune-time body carries provider.quantizations = ["Q8_0"]
    And the tune-time body carries no provider.ignore derived from the dial

  Scenario: Tuning a band ROW binds its quant even with Q off
    Given Q is off and the operator tunes the "qwen3-32b · Q4_K_M" row
    Then the tune-time body carries provider.quantizations = ["Q4_K_M"]

  Scenario: A row with no quant label binds nothing (unknown is not a choice)
    Given the operator tunes a "qwen3-32b" row whose stations state no quant
    Then the tune-time body carries no provider.quantizations

  Scenario: The standing Limit.Quants rule and the tuned row both apply (intersection)
    Given limits.models.qwen3-32b.quants is ["Q8_0","BF16"]
    And the operator tunes the "qwen3-32b · Q8_0" row
    Then the tune-time body carries provider.quantizations = ["Q8_0"]

  Scenario: A tuned row outside the standing rule is refused at the confirm, not silently routed
    Given limits.models.qwen3-32b.quants is ["BF16"]
    When the operator tunes the "qwen3-32b · Q4_K_M" row
    Then the confirm shows "Q4_K_M is outside your quant rule (BF16) - edit it in [3] CONFIG or pick another row"
    And accepting is not offered

  Scenario: A station that registers after the scan is bound by the quant rule (closes the in-code gap)
    Given the operator tuned "qwen3-32b · Q8_0"
    When a new station "n7" registers "qwen3-32b" at Q4_K_M after the last scan
    And a turn goes out
    Then the broker never picks "n7" (provider.quantizations binds server-side)

  Scenario: U (hide curated) tunes with roger.self_hosted_only and binds on a MIXED band (defect 3)
    Given the operator toggled U on and tuned "qwen3-32b" (n1, n2 human; n3 curated)
    Then the tune-time body carries roger.self_hosted_only = true
    When five in-channel chat turns and five agent turns go out
    Then every one of the ten broker requests carries roger.self_hosted_only = true
    And none is served by "n3"
    And X-RogerAI-Provider never names "n3"

  Scenario: U with only curated supply refuses the turn as approved, and now says why in the same words
    Given the operator toggled U on and the only stations for "mistral-large" are curated
    When an agent turn targets "mistral-large"
    Then the turn is refused before dispatch with the approved wording
    And a tuned in-channel chat on it gets the broker's 503 no_match naming "self_hosted_only"

  Scenario: Turning U off mid-session re-admits curated on the next turn
    Given the operator tuned with U on
    When the operator toggles U off
    Then the next turn's body carries no roger.self_hosted_only
    And the in-flight turn is unaffected

  Scenario: O (on air) remains a view filter and emits nothing
    Given the operator toggled O on and tuned "qwen3-32b"
    Then the tune-time body carries no key derived from O

  # corrected 2026-10-04 (founder-approved): the band code travels only as the X-Roger-Freq header, never in the body
  Scenario: ~ private freq tunes with the X-Roger-Freq header and the code is never on screen after entry
    When the operator enters a valid code at ~ and tunes
    Then the tune-time request carries the X-Roger-Freq header = the code
    And the tune-time body carries no roger.freq
    And the header reads PRIVATE FREQ without the code

  Scenario: Filters bound pickAutoBand exactly as approved (auto_tune.feature), unchanged
    Given U and Q filters are on
    When the AGENT [0] silent auto-tune runs
    Then it picks only from visibleBands
    And the chosen band's tune carries the same body keys the filters emit

  Scenario: The filter state is session-scoped; the CONFIG rule is durable
    Given U is on and limits.default.self_hosted is unset
    When the TUI restarts
    Then U is off and nothing in config.json mentions self_hosted

  Scenario: Every filter combination composes into one body object
    Given F, C, U on and Q at "Q8_0"
    When the operator tunes "qwen3-32b"
    Then the body carries model "qwen3-32b:free", roger.confidential true, roger.self_hosted_only true, provider.quantizations ["Q8_0"]

  # --- auto-band ladder unchanged -----------------------------------------------------------

  Scenario: pickAutoBand's free-first / not-small / signal-or-cheapest ladder is unchanged
    Given bands with mixed free and paid supply
    When the AGENT [0] auto-tune runs
    Then the chosen band matches the approved ladder in features/operator/auto_tune.feature
    And no profile is applied unless one is the tuned default

  # --- pref picker and the windowshade glyph ---------------------------------------------

  Scenario Outline: PROPOSED - the active pref shows as one glyph on the compact windowshade strip
    Given the operator tuned with pref "<pref>"
    When the TUI is in compact mode
    Then the strip carries the glyph "<glyph>" after the band name

    Examples:
      | pref     | glyph |
      | cheap    | $     |
      | balanced |       |
      | fast     | »     |
      | reliable | ◆     |

  Scenario: PROPOSED - the glyph is absent for balanced so the strip is byte-identical to today
    Given the operator tuned with no pref
    When the TUI is in compact mode
    Then the strip equals the approved compact strip

  Scenario: The glyph survives NO_COLOR and a 40-column strip
    Given pref "fast" and NO_COLOR
    When the TUI is in compact mode at 40 columns
    Then "»" is present and nothing wraps

  Scenario: Changing pref in CONFIG re-tunes the live proxy options without a re-tune
    Given the operator is connected to "qwen3-32b"
    When the operator sets pref to "fast" in [3] CONFIG
    Then the next turn's body carries roger.pref = "fast"
    And the endpoint URL and bearer key are unchanged (features/proxy/live_options.feature)

  # --- the cost meter reads the usage chunk -------------------------------------------------

  Scenario: A streamed turn's cost comes from the final usage chunk
    When a streamed turn settles with usage.cost = 0.0123
    Then the session cost meter increases by exactly 0.0123
    And the trailing ": rogerai-cost=" comment is not counted again

  Scenario: The turn's served model and node come from the chunk's rogerai block
    When a streamed turn is served by "n2" with model "llama-3.3-70b" (a fallback)
    Then the transcript's turn footer reads "llama-3.3-70b · n2"
    And the band header still names the tuned primary

  Scenario: An old broker with only the comment still meters as approved
    Given a broker that sends only ": rogerai-cost="
    When a streamed turn settles
    Then the meter increases from the comment

  Scenario: The cost strip shows the receipt's price lock when present
    When a turn settles with usage.rogerai.locked_until
    Then the cost line shows "locked until <time>" in dim

  # --- key map: no collisions ---------------------------------------------------------------

  Scenario Outline: New keys live only inside [3] CONFIG and never shadow a global
    Given the operator is on <screen>
    When the operator presses <key>
    Then the approved binding for <screen> fires and nothing routing-related happens

    Examples:
      | screen          | key    |
      | the dial        | p      |
      | the dial        | r      |
      | the dial        | t      |
      | the dial        | v      |
      | chat            | p      |
      | agent           | r      |
      | any screen      | ctrl+p |
      | any screen      | m      |
      | any screen      | alt+m  |

  Scenario: ctrl+p is perms everywhere, including inside [3] CONFIG
    Given the operator is editing a routing field
    When the operator presses ctrl+p
    Then the perms screen opens as approved

  Scenario: m / alt+m minimize from inside the profile editor
    Given the operator is on [3] CONFIG with the detail plate open
    When the operator presses alt+m
    Then the windowshade collapses as approved and the edit buffer is discarded

  Scenario: The footer hint for [3] CONFIG lists the new keys in the existing hint style
    When the operator opens [3] CONFIG
    Then the footer reads "tab field · enter edit · space cycle · p pref · a add · esc back"

  Scenario: help (?) documents the new keys under CONFIG only
    When the operator opens help
    Then the CONFIG section lists tab / space / p / t / v
    And no other section changed
