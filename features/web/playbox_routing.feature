# PLAYBOX - the routing drawer (CONTRACT §1, §7, §9 as they reach the browser).
#
# PURPOSE: a visitor at the deck can say how a live station turn should be routed, with the
# same body object every other client sends, and see afterwards which model and station
# actually served. Nothing here fakes a live number, nothing is sent as a header, and the
# page keeps the founder's rulings (the wallet lives in the site header, not on the deck).
#
# GROUND TRUTH at origin/main 518c698b (web/src/js/playbox.js):
#   - stationSend (1226-1231) posts {model, stream: true, max_tokens: 1024, messages: last 8}
#     with credentials: "include" (the browser-session cookie is the identity,
#     features/relay/browser_session.feature). No routing header, no routing body key.
#   - The deck reads /discover (CORS, no creds) and folds offers into bands (128-186): ctx,
#     hw, region, confidential, ttft, verified, price floors, free_now, tier, signal, tps, caps.
#     "WAVE" size labels (238-291) are display text, not a routing input.
#   - localStorage holds the remembered deck (tape model + kind) under STORE_KEY (770-790),
#     read once at start-up, written by rememberDeck; private mode simply forgets.
#   - /me gives the HANDLE only; wallet, spend and history are deliberately NOT rendered on
#     this page (480-490, founder ruling). There is NO cost strip on the Playbox today; the
#     approved scenario is "the account panel's balance reflects the spend after the reply
#     settles" (features/web/playbox.feature:38-40). FLAG for the parent: the directive's
#     "existing cost strip" does not exist; this spec shows served model/station in the
#     transcript and leaves money in the site header, per the ruling.
#   - Design rules: web/DESIGN-SYSTEM.md - one role one pattern, no grids/glows/pinned bars,
#     text never --ink-300, widgets with controls are role="group", phone width with a 16px
#     gutter, dark-mode contrast, deferred scripts, weight budgets.
#   - The anonymous free path: a paid station asks for sign-in before the first send
#     (playbox.feature:34-36); anonymous relays are free-station only.
#
# Enforced by: web/test/playbox-routing.test.mjs (future; the real playbox.js against a
# recorded /discover and a fetch stub that captures the request body) plus the existing
# design-system consistency tests.

Feature: The Playbox routing drawer sends the same body object and shows who served

  Background:
    Given the Playbox is served from "https://rogerai.fm"
    And /discover lists "qwen3-32b" on stations n1 (Q8_0, eu, 40 t/s, verified), n2 (Q4_K_M, us, 90 t/s), n3 (curated, us)
    And the visitor has tuned the "qwen3-32b" tape

  # --- the drawer ---------------------------------------------------------------------------

  Scenario: The drawer is closed by default and the deck is byte-identical to today
    When the page loads
    Then no routing drawer is open
    And the deck's DOM outside the drawer toggle equals the approved deck

  Scenario: The drawer opens from one control on the j-card and is a named group
    When the visitor activates "routing" on the j-card
    Then a panel with role="group" and aria-label "routing" opens under the j-card
    And it is a tinted panel (the design system's one pattern for a settings inset), not a grid or a pinned bar

  Scenario Outline: Each drawer field maps to exactly one body key
    Given the drawer is open
    When the visitor sets "<field>" to "<value>"
    And keys up a turn
    Then the request body carries <key> = <body>

    Examples:
      | field           | value             | key                            | body                    |
      | also try        | llama-3.3-70b     | models                         | ["llama-3.3-70b"]       |
      | max $/1M out    | 2                 | provider.max_price.completion  | 2                       |
      | max $/1M in     | 0.5               | provider.max_price.prompt      | 0.5                     |
      | max $/turn      | 0.02              | provider.max_price.request     | 0.02                    |
      | min t/s         | 30                | roger.min_tps                  | 30                      |
      | self-hosted     | on                | roger.self_hosted_only         | true                    |
      | confidential    | on                | roger.confidential             | true                    |
      | needs tools     | on                | roger.require                  | ["tools"]               |
      | needs vision    | on                | roger.require                  | ["vision"]              |
      | quant           | Q8_0              | provider.quantizations         | ["Q8_0"]                |
      | size            | 7-70B             | roger.params_b                 | [7,70]                  |
      | region          | eu                | roger.region                   | ["eu"]                  |
      | prefer          | fast              | roger.pref                     | "fast"                  |
      | sort by         | price             | provider.sort                  | "price"                 |
      | trust           | verified          | roger.trust_min                | "verified"              |
      | min ctx         | 32k               | roger.min_ctx                  | 32768                   |
      | max first token | 1.5s              | roger.max_ttft_ms              | 1500                    |

  Scenario: With nothing set the body is exactly today's body
    Given the drawer was opened and closed with no change
    When the visitor keys up a turn
    Then the request body is {model, stream: true, max_tokens: 1024, messages}
    And no `provider`, `models` or `roger` key is present

  Scenario: Routing is sent in the body only, never as headers
    Given the drawer sets min t/s 30 and confidential on
    When the visitor keys up a turn
    Then the request has no header beginning with X-Roger-
    And the body carries roger.min_tps and roger.confidential

  Scenario: prefer and sort by are exclusive in the UI, so the broker's 400 cannot happen from here
    Given the drawer sets "prefer" to "fast"
    When the visitor sets "sort by" to "price"
    Then "prefer" resets to "balanced" and a one-line note says "sort by replaces prefer"

  Scenario: The quant, region and size choices are built from the live /discover feed
    Given the drawer is open
    Then "quant" offers exactly "any", "Q8_0", "Q4_K_M"
    And "region" offers exactly "any", "eu", "us"
    And "size" offers preset ranges plus "any", with the label naming that size is station-declared

  Scenario: Choices that no station on the tape can satisfy are shown, marked, not hidden
    Given no station for the tape states a quant
    Then "quant" offers "any" only, with the note "no station on this tape states its quant"

  Scenario: Validation happens in the drawer with the contract's rules
    When the visitor types "70-7" into "size"
    Then the field shows "min must be at most max" and the turn cannot be keyed up until fixed
    When the visitor types "-1" into "max $/1M out"
    Then the field shows "must be 0 or more"

  Scenario: A too-long "also try" list is capped at four extra models
    When the visitor adds five models to "also try"
    Then only four are kept and the field says "up to 4 fallbacks after the tape"

  # --- persistence --------------------------------------------------------------------------

  Scenario: Drawer settings persist per viewer in localStorage under their own key
    Given the drawer sets self-hosted on and prefer fast
    When the page reloads
    Then the drawer restores self-hosted on and prefer fast
    And the settings live under a key separate from the remembered deck

  Scenario: Persistence is per viewer and never reaches the broker or the account
    Given the drawer has settings
    Then no request to /me, /account or any endpoint carries them except the turn's own body

  Scenario: Private mode or blocked storage degrades to session-only settings without error
    Given localStorage throws on write
    When the visitor changes a drawer field
    Then the setting applies to the next turn
    And no error surfaces on the page

  Scenario: A persisted setting that no longer applies is dropped honestly
    Given the drawer persisted quant "Q8_0"
    And a reload finds no station stating "Q8_0" for the tape
    Then "quant" reads "any" with the note "Q8_0 is not on this tape right now"
    And the body carries no provider.quantizations

  Scenario: Changing tapes keeps the drawer's generic settings and clears tape-specific ones
    Given the drawer set prefer fast, quant Q8_0, also try llama-3.3-70b
    When the visitor tunes another tape
    Then prefer fast survives
    And quant and also try are cleared with a one-line note

  Scenario: reset returns every field to unset and clears the stored settings
    Given the drawer has settings
    When the visitor activates "reset"
    Then every field is unset and the stored key is removed

  # --- the transcript's summary line ---------------------------------------------------------

  Scenario: A turn with routing set shows one dim summary line above the reply
    Given the drawer sets prefer fast, self-hosted on, max $/1M out 2
    When the visitor keys up a turn
    Then the transcript shows "routing: fast · self-hosted · out ≤ $2/1M" in the dim style
    And the line is one line and wraps on a phone without a horizontal scroll

  Scenario: A turn with nothing set shows no summary line
    When the visitor keys up a turn with the drawer unset
    Then no "routing:" line appears

  # --- anonymous vs signed-in -----------------------------------------------------------------

  Scenario: Anonymous visitors see money fields disabled with the reason
    Given the visitor is not signed in
    When the drawer opens
    Then "max $/1M out", "max $/1M in", "max $/turn" and "sort by price" are disabled
    And the note reads "signed-out turns run on free stations only - price caps do nothing here"
    And the body of an anonymous turn carries model "<tape>:free"

  Scenario: Anonymous turns send :free sugar so a paid station is never planned
    Given the visitor is not signed in and the tape has one free and two paid stations
    When the visitor keys up a turn
    Then the request body carries model "qwen3-32b:free"
    And the broker plans only the free station

  Scenario: Signing in enables the money fields without a reload
    Given the visitor was anonymous with the drawer open
    When the visitor signs in and /me reports a handle
    Then the disabled fields become enabled and the note disappears

  Scenario: A paid station still asks for sign-in before the first send (approved)
    Given the visitor is not signed in and picks a paid-only tape
    When the visitor keys up
    Then the sign-in ask appears exactly as approved, before any request

  Scenario: The drawer never shows a balance or spend (founder ruling)
    Given the visitor is signed in
    When the drawer opens
    Then no balance, spend or history is rendered anywhere on the deck
    And the site header's wallet reflects spend after settle, as approved

  # --- who served, from the usage chunk -----------------------------------------------------

  Scenario: The reply's footer names the served model and station from the final usage chunk
    When a turn streams and ends with a usage chunk carrying rogerai.model "llama-3.3-70b" and rogerai.node "n2"
    Then the reply footer reads "served by llama-3.3-70b · n2"
    And the footer is the dim style and not a new component

  Scenario: A fallback to another model is said plainly
    Given the tape is "qwen3-32b" and "also try" holds "llama-3.3-70b"
    When the turn is served by "llama-3.3-70b"
    Then the footer reads "served by llama-3.3-70b (fallback) · n2"

  Scenario: The footer shows the cost only when the visitor is signed in, and from the chunk, never estimated
    Given the visitor is signed in
    When the chunk carries usage.cost 0.0031
    Then the footer appends "· $0.0031"
    Given the visitor is anonymous
    Then the footer appends "· free"

  Scenario: No usage chunk means no footer numbers, never a guess
    When a turn ends without a usage chunk
    Then the footer shows "served by <X-RogerAI-Provider>" only if the header was readable, otherwise nothing

  Scenario: The station's own usage object is never mistaken for the broker's
    When a station emits a usage object mid-stream and the broker emits its final chunk with a rogerai block
    Then the footer reads from the chunk that carries the rogerai block only

  # --- errors surface as themselves (approved) ---------------------------------------------

  Scenario: A 503 no_match names the constraint in the reply
    Given the drawer sets quant "Q8_0" and region "us"
    When the broker answers 503 with error.code "no_match" and message naming quantizations
    Then the reply shows "no station matches: quantizations Q8_0 in us - loosen a routing setting"
    And the request is not retried

  Scenario: A 503 band_cooling shows the wait and is not retried into (approved rate-limit rule)
    When the broker answers 503 band_cooling with Retry-After 12
    Then the reply says the band is cooling for 12 s
    And no automatic retry is issued

  Scenario: A 400 from a routing value is shown with the key named
    When the broker answers 400 invalid_routing_value for roger.params_b
    Then the reply names "size" (the drawer's label for roger.params_b)

  # --- design and accessibility --------------------------------------------------------------

  Scenario: The drawer meets the design system on a phone
    Given a 360px viewport
    When the drawer opens
    Then there is no horizontal page scroll, the gutter is 16px, and every control's hit area is at least the touch minimum

  Scenario: Every control is keyboard reachable and labelled
    When the visitor tabs through the drawer
    Then focus visits every field in reading order with a visible focus ring
    And each field has a label element or aria-label

  Scenario: Dark mode contrast holds for every drawer text
    Given prefers-color-scheme: dark
    Then no drawer text uses --ink-300 and all text meets the site's contrast rule

  Scenario: The drawer uses existing tokens and components only
    Then the drawer's CSS introduces no colour literal outside tokens, no glow, no grid layout for form rows, and no new component name without an entry in DESIGN-SYSTEM.md

  Scenario: prefers-reduced-motion disables the drawer's open animation
    Given prefers-reduced-motion: reduce
    When the drawer opens
    Then it appears without motion

  Scenario: The drawer's script adds no parser-blocking work and stays inside the page's weight budget
    Then playbox.js remains deferred and the page's first-load budget test still passes

  # --- approved Playbox scenarios stay true -------------------------------------------------

  Scenario: Off-origin, the drawer explains the Tower cannot be reached exactly as the deck does
    Given the page is served from a preview origin
    When the visitor keys up with routing set
    Then the off-origin note appears and no request leaves the browser

  Scenario: Ping remains the default operator and the drawer is hidden when no tape is tuned
    Given no station is selected
    Then the routing control is absent and the composer talks to Ping via the concierge

  Scenario: The quiet band and unreachable broker states are unchanged
    Given the broker is reachable but no stations are on air
    Then the page says the band is quiet and the routing control is absent
