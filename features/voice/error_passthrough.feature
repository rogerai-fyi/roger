# Layer 3 (broker relay, result leg) — NODE-SIDE FAILURE PASSTHROUGH for the voice money path.
# Today a station-side failure is blanketed into a bare 502 ("station returned nothing"), and the
# CDN edge REPLACES origin 502/504 bodies with its branded HTML page — so a consumer sees an
# opaque HTML 504 with no reason (the iOS "voice error (504)" bug: the real cause, Kokoro's
# 500 "Voice ... not found", never reached the app). The founder's contract (2026-07-02, pinned in
# roger-ios docs/BROKER-VOICE-API.md "Error passthrough"):
#   1. keep the 401/402/403/503 semantics exactly as-is;
#   2. node-side failures stay 5xx but carry a SHORT SANITIZED REASON in the standard error body;
#   3. never the node's raw error verbatim (leaks local paths/hostnames — screen and truncate
#      like STT output).
#
# Ground truth (real code this spec is anchored in):
#   cmd/rogerai-broker/audio.go  audioRelayCore — the shared TTS/STT money spine. Result leg today:
#       node status <200/>=400 or empty body -> 502 "station returned nothing" (hold refunds)
#       receipt fails VerifyNode              -> 502 "station receipt failed verification"
#       STT result unreadable (resultText)    -> 502 "station returned an unreadable result"
#       no result within nonStreamRelayWait   -> 504 "station timed out"
#   cmd/rogerai-broker/moderation.go  b.mod.screen — the SAME screen STT output passes today
#   internal/agent/agent.go  serve() — the node relays the upstream's status + body verbatim
#       (after redactUpstreamKey; that node-side redaction is defense-in-depth, NEVER trusted)
#   EDGE FACT (verified live 2026-07-01 with signed probes): the edge passes an origin 500/503
#       JSON body through intact, but REPLACES origin 502/504 bodies with its branded HTML page.
#       Any status carrying a reason the consumer must read therefore has to avoid 502/504.
#
# DECISIONS PINNED BY THIS SPEC (founder-approved 2026-07-02; E4 examples extended the
# same day after review found bypass forms — each is a permanent regression below):
#   E1  every PRE-DISPATCH status is byte-identical to today: 400 (bad request shape), 401
#       (signature), 402 (funds), 403 (anon paid), 413 (too large), 429 (rate), 451 (moderation
#       input block), 503 (no station / saturated / busy) — requirement 1.
#   E2  a NODE-SIDE FAILURE (the station's JobResult arrives with status >=400, or with an empty
#       body) returns HTTP 500 with the standard error body
#       {"error":{"message":"station error: <sanitized reason>"}} — 5xx per requirement 2, and
#       500 specifically because the edge passes its JSON through (502/504 bodies are replaced
#       with HTML, which is the very bug being fixed).
#   E3  the reason is EXTRACTED only from a standard error shape in the node's body — {"error":
#       "s"}, {"error":{"message":"s"}}, {"detail":"s"} (FastAPI), {"message":"s"} — else (plain
#       text, HTML, binary, unparseable) the reason degrades to the generic
#       "station error (status <n>)"; an EMPTY body degrades to "station error (empty result)"
#       (the status alone can mislead there). The node's raw body is NEVER echoed — req 3.
#   E4  sanitization happens ON THE BROKER (the node is untrusted; its own redaction is only
#       defense-in-depth): strip/redact absolute unix paths (INCLUDING quoted/colon/paren-
#       prefixed forms — the canonical FastAPI "directory: '/path'" shape), windows paths
#       (back- OR forward-slash), IPv4s and IPv6s (bracketed/zoned/with port), host:port,
#       URLs, bearer/key material (any case: Bearer/BEARER, rog-*/rog_*, sk-*/sk_*), and
#       email addresses; collapse whitespace/control chars; hard-truncate to 120 runes
#       (rune-safe) with an ellipsis.
#   E5  the sanitized reason passes the SAME moderation screen as STT output before relay; a
#       FLAGGED reason or a screen OUTAGE degrades to the generic reason (the 500 itself still
#       returns — an error is never withheld, and an error is never billable).
#   E6  the two broker-side result-integrity failures also move 502 -> 500 with their existing
#       broker-authored messages ("station receipt failed verification", "station returned an
#       unreadable result") so those reasons survive the edge too. The pure relay TIMEOUT stays
#       504 "station timed out" (there is no node reason to carry; the code is the message).
#   E7  the MONEY INVARIANT on every failure above is unchanged: the hold refunds in full, the
#       wallet balance is untouched, nothing settles, NO receipt enters lineage, and no grant
#       usage is recorded.
#   E8  TTS and STT behave identically (they share audioRelayCore), single- and multi-instance.
#
# The 200 binary success path is pinned by binary_relay.feature and is untouched here.

Feature: Voice relay node-side failure passthrough
  As a voice consumer (the iOS app, roger say, a grant-key bot)
  I want a station-side failure to come back as a 5xx with a short, sanitized reason
  So that I can show WHY the voice failed instead of an opaque edge error page,
  while station internals (paths, hosts, keys) never leak and a failed synth never charges

  Background:
    Given a broker with a funded consumer wallet "u_test" holding $10.00
    And an on-air tts station "@op/voice-a" priced at $10 per 1M input chars
    And an on-air stt station "@op/ears-a" priced at $10 per 1M audio bytes

  # ── E1: pre-dispatch semantics are byte-identical to today ─────────────────────────────

  Scenario: an unsigned spend on a paid voice still returns 401
    When an unsigned /v1/audio/speech request for "@op/voice-a" arrives
    Then the response status is 401
    And the error message is "spending requires a signed request"
    And no job is dispatched to the station

  Scenario: a garbled signature still returns 401
    When a /v1/audio/speech request for "@op/voice-a" arrives with an invalid signature
    Then the response status is 401
    And the error message is "invalid request signature"

  Scenario: insufficient funds still returns 402
    Given the consumer wallet "u_test" holds $0.00
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hello" arrives
    Then the response status is 402
    And the error message contains "insufficient balance"
    And no job is dispatched to the station

  Scenario: an anonymous key on a paid voice still returns 403
    When an anonymous-keypair /v1/audio/speech request for "@op/voice-a" arrives
    Then the response status is 403
    And the error message is "sign in to use this voice model"

  Scenario: no station on air still returns 503
    When a signed /v1/audio/speech request for "@op/no-such-voice" arrives
    Then the response status is 503
    And the error message contains "no station on air"

  Scenario: an empty input still returns 400
    When a signed /v1/audio/speech request for "@op/voice-a" with input "" arrives
    Then the response status is 400
    And no hold is placed

  Scenario: the consumer rate limit still returns 429
    Given the consumer "u_test" has exhausted its rate limit
    When a signed /v1/audio/speech request for "@op/voice-a" arrives
    Then the response status is 429

  Scenario: a moderation-blocked input still returns 451 before dispatch
    Given the moderation screen flags the input text
    When a signed /v1/audio/speech request for "@op/voice-a" with the flagged input arrives
    Then the response status is 451
    And no job is dispatched to the station

  # ── E2: the node-failure passthrough (the new behavior) ────────────────────────────────

  Scenario: the regression that motivated this spec - Kokoro voice-not-found reaches the consumer as a readable 500
    Given the station's local server answers 500 with body:
      """
      {"error": "Voice @op/voice-a not found in available voices"}
      """
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hello there" arrives
    Then the response status is 500
    And the response body is the standard error shape
    And the error message is "station error: Voice @op/voice-a not found in available voices"
    And the response Content-Type is "application/json"

  Scenario Outline: any node result with status >= 400 returns 500 with the sanitized reason
    Given the station's local server answers <node_status> with body:
      """
      {"error": {"message": "synth failed"}}
      """
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 500
    And the error message is "station error: synth failed"

    Examples:
      | node_status |
      | 400         |
      | 404         |
      | 500         |
      | 503         |

  Scenario: a node result with an empty body returns 500 with the generic reason
    Given the station's job result arrives with status 200 and an empty body
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 500
    And the error message is "station error (empty result)"

  # ── E3: reason extraction - standard shapes only, never the raw body ───────────────────

  Scenario Outline: the reason is extracted from each standard error shape
    Given the station's local server answers 500 with body:
      """
      <node_body>
      """
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 500
    And the error message is "station error: <reason>"

    Examples:
      | node_body                                     | reason      |
      | {"error": "bad voice"}                        | bad voice   |
      | {"error": {"message": "bad voice"}}           | bad voice   |
      | {"detail": "bad voice"}                       | bad voice   |
      | {"message": "bad voice"}                      | bad voice   |

  Scenario Outline: a non-standard node body degrades to the generic reason - the raw body never leaks
    Given the station's local server answers 500 with body:
      """
      <node_body>
      """
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 500
    And the error message is "station error (status 500)"
    And the response body does not contain "<leak_marker>"

    Examples:
      | node_body                                        | leak_marker |
      | plain text panic: goroutine stack trace          | goroutine   |
      | <html><body>Internal Server Error</body></html>  | html        |
      | {"weird": {"nested": "shape"}}                   | nested      |

  Scenario: a binary (non-UTF-8) node error body degrades to the generic reason
    Given the station's local server answers 500 with a 64 KiB binary body
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 500
    And the error message is "station error (status 500)"

  # ── E4: broker-side sanitization of the extracted reason ───────────────────────────────

  Scenario Outline: station internals are stripped from the reason on the broker
    Given the station's local server answers 500 with body:
      """
      {"error": "<raw_reason>"}
      """
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 500
    And the error message does not contain "<leak>"
    And the error message starts with "station error"

    Examples:
      | raw_reason                                                   | leak            |
      | model missing at /home/op/voices/af_heart.pt                 | /home/op        |
      | cannot open C:\Users\op\voices\af.pt                         | C:\Users        |
      | upstream 127.0.0.1:8095 refused                              | 127.0.0.1:8095  |
      | connect to gpu-box.local:8880 failed                         | gpu-box.local   |
      | fetch http://192.168.1.7:8095/v1/voices failed               | http://         |
      | auth rejected for Bearer sk-abc123def456                     | sk-abc123       |
      | key rog-grant_a1b2c3 expired                                 | rog-grant_      |
      | mail the operator at op@example.com                          | op@example.com  |
      | No such file or directory: '/home/op/voices/af_heart.pt'    | /home/op        |
      | model missing (/home/op/voices/af.pt)                        | /home/op        |
      | see path:/home/op/voices for details                         | /home/op        |
      | cannot open C:/Users/op/voices/af.pt                         | C:/Users        |
      | auth rejected for BEARER SK-ABC123DEF456                     | SK-ABC123       |
      | key sk_live_a1b2c3d4 expired                                 | sk_live_        |
      | upstream [2001:db8::1]:8880 refused                          | 2001:db8        |
      | connect fe80::1%eth0 failed                                  | fe80::1         |

  Scenario: the reason is whitespace-collapsed and control characters are removed
    Given the station's local server answers 500 with body:
      """
      {"error": "synth\n\n   failed\t\tbadly"}
      """
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the error message is "station error: synth failed badly"

  Scenario: a long reason is truncated to 120 runes with an ellipsis, rune-safely
    Given the station's local server answers 500 with a 500-character error reason containing multibyte runes
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the error message reason is at most 121 runes and ends with "…"
    And the error message is valid UTF-8

  # ── E5: the reason passes the same moderation screen as STT output ─────────────────────

  Scenario: a moderation-flagged reason degrades to the generic reason but the 500 still returns
    Given the moderation screen flags the text "obscene station reason"
    And the station's local server answers 500 with body:
      """
      {"error": "obscene station reason"}
      """
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 500
    And the error message is "station error (status 500)"

  Scenario: a moderation-screen outage degrades to the generic reason - never a withheld error
    Given the moderation screen is unavailable
    And the station's local server answers 500 with body:
      """
      {"error": "some station reason"}
      """
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 500
    And the error message is "station error (status 500)"

  # ── E6: broker-side result-integrity failures also become edge-visible 500s ────────────

  Scenario: a result whose receipt fails node verification returns 500 with the broker's reason
    Given the station returns a valid result whose receipt signature does not verify
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 500
    And the error message is "station error: station receipt failed verification"

  Scenario: an unreadable STT result returns 500 with the broker's reason
    Given the stt station returns 200 with a body that is not the transcription shape
    When a signed /v1/audio/transcriptions request for "@op/ears-a" arrives
    Then the response status is 500
    And the error message is "station error: station returned an unreadable result"

  Scenario: the pure relay timeout is unchanged - 504 with no carried reason
    Given the station never returns a result within the relay wait
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hi" arrives
    Then the response status is 504
    And the error message is "station timed out"

  # ── E7: the money invariant on every failure path ──────────────────────────────────────

  Scenario: a node-side failure never charges - the hold refunds in full
    Given the station's local server answers 500 with body:
      """
      {"error": "synth failed"}
      """
    When a signed /v1/audio/speech request for "@op/voice-a" with input "hello" arrives
    Then the response status is 500
    And the consumer wallet "u_test" still holds $10.00
    And no hold remains open for the request
    And no receipt enters lineage for the request
    And the response carries no X-RogerAI-Cost header

  Scenario: a node-side failure on a grant-key request records no grant usage
    Given a grant key funded by "u_test" scoped to "@op/voice-a"
    And the station's local server answers 500 with body:
      """
      {"error": "synth failed"}
      """
    When a grant-key /v1/audio/speech request for "@op/voice-a" with input "hello" arrives
    Then the response status is 500
    And the grant records zero usage for the request

  Scenario: an operator self-use failure is identical (and was already $0)
    Given the consumer is the operator of "@op/voice-a"
    And the station's local server answers 500 with body:
      """
      {"error": "synth failed"}
      """
    When the operator's signed /v1/audio/speech request with input "hello" arrives
    Then the response status is 500
    And the error message is "station error: synth failed"

  # ── E8: TTS/STT symmetry and multi-instance parity ─────────────────────────────────────

  Scenario: the stt path passes a node failure through identically
    Given the stt station's local server answers 500 with body:
      """
      {"error": "whisper model not loaded"}
      """
    When a signed /v1/audio/transcriptions request for "@op/ears-a" arrives
    Then the response status is 500
    And the error message is "station error: whisper model not loaded"

  Scenario: multi-instance - a node failure crossing the bus carries the same sanitized reason
    Given two broker instances sharing one bus and one store
    And the station polls instance A while the consumer calls instance B
    And the station's local server answers 500 with body:
      """
      {"error": "synth failed at /home/op/voices"}
      """
    When a signed /v1/audio/speech request for "@op/voice-a" arrives on instance B
    Then the response status is 500
    And the error message is "station error: synth failed at"
    And the error message does not contain "/home/op"
