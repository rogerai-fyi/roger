# MODERATION: the broker's MANDATORY pre-dispatch content screen. The broker is the single
# choke point where an illegal prompt (CSAM and similar) is blocked BEFORE it ever reaches a
# provider node. The screen is a pluggable hook; what these scenarios pin is the POSTURE
# (fail-open dev vs fail-closed launch), the verdict-to-HTTP mapping, and the legally-distinct
# CSAM preserve-and-report path that must NEVER silently discard.
#
# GROUND TRUTH (cmd/rogerai-broker/moderation.go):
#   backend resolved from MODERATION_PROVIDER ("url" | "groq"); inferred when empty:
#     "url" if MODERATION_URL set, else "groq" if a Groq key present (gpt-oss-safeguard,
#     since Groq retired Llama Guard), else OFF.
#   modResult.status: 0 = ALLOW; 451 = flagged (reject); 503 = fail-closed.
#   posture:
#     no backend + require=false -> DISABLED (pass-through + LOUD startup warning; dev only)
#     ROGERAI_REQUIRE_MODERATION=1 -> fail CLOSED: unset/unreachable screen => 503, never served
#   csam=true ONLY for a matched csamCats category (default Llama-Guard S4 + OpenAI
#     sexual/minors; ROGERAI_CSAM_CATEGORIES overrides): the relay must PRESERVE the incident
#     and QUEUE a CyberTipline report (18 USC 2258A), not just reject+discard.
#   The screen runs BEFORE dispatch, so a blocked prompt never reaches any node.
#
# Enforced by: cmd/rogerai-broker/moderation_test.go (+ report.go / safety.go for the CSAM queue).

Feature: Moderation — the mandatory pre-dispatch content screen

  # --- the allow / block decision --------------------------------------------
  Scenario: A clean prompt is allowed and dispatched
    Given a moderation backend that returns "safe"
    When a relay request is screened
    Then the screen returns status 0 (ALLOW)
    And the request proceeds to routing + dispatch

  Scenario: A flagged prompt is rejected before any node sees it
    Given a moderation backend that returns "unsafe S1"
    When a relay request is screened
    Then the screen returns 451 (flagged)
    And no node is ever dispatched the prompt

  # --- posture: dev (fail-open) ----------------------------------------------
  Scenario: With no backend and require=false the screen is DISABLED (dev pass-through)
    Given no moderation backend is configured and ROGERAI_REQUIRE_MODERATION is unset
    When the broker starts
    Then a LOUD startup warning is logged that moderation is OFF (not safe for public traffic)
    And requests pass through unscreened

  # --- posture: launch (fail-closed) -----------------------------------------
  Scenario: require=true with no backend fails CLOSED
    Given ROGERAI_REQUIRE_MODERATION=1 and no moderation backend configured
    When a relay request is screened
    Then the screen returns 503 (fail-closed), never served unscreened

  Scenario: require=true with an unreachable backend fails CLOSED
    Given ROGERAI_REQUIRE_MODERATION=1 and a backend URL that times out / errors
    When a relay request is screened
    Then the screen returns 503, never falling open to "allow"

  # --- backend selection ------------------------------------------------------
  Scenario: MODERATION_URL selects the url backend
    Given MODERATION_URL is set and MODERATION_PROVIDER is empty
    When the backend is resolved
    Then the "url" backend is used (OpenAI-moderation-compatible {input}->{flagged})

  Scenario: A Groq key with no URL selects the native gpt-oss-safeguard backend
    Given MODERATION_GROQ_KEY (or GROQ_API_KEY) is set, no MODERATION_URL, provider empty
    When the backend is resolved
    Then the "groq" backend is used, calling the content-safety model with a policy prompt
    And a "safe" / "unsafe <codes>" verdict is parsed

  # --- CSAM: preserve + report (NOT silently discard) ------------------------
  Scenario: A CSAM-category hit is preserved and queued for a CyberTipline report
    Given a backend that returns "unsafe" with a category in csamCats
    When a relay request is screened
    Then modResult.csam is true with the matched category
    And the incident is PRESERVED and QUEUED for a CyberTipline report (18 USC 2258A)
    And the request is rejected (never dispatched, never silently dropped)

  Scenario: A non-CSAM unsafe hit is rejected but NOT queued as CSAM
    Given a backend that returns "unsafe S1" (not a csamCats category)
    When a relay request is screened
    Then modResult.csam is false
    And the request is rejected (451) without opening a CSAM incident

  Scenario: csamCats is configurable and matched case-insensitively
    Given ROGERAI_CSAM_CATEGORIES overrides the default category set
    When a verdict carries a configured category in any letter case
    Then it is matched as CSAM
