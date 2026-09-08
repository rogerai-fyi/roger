# TRUST - AN UPSTREAM THROTTLE IS NOT OPERATOR MISCONDUCT.
#
# THE INCIDENT (verified, prod 2026-09-07 18:03-18:24 UTC): one consumer relayed ~83K-token
# prompts to the house Cerebras curated station house-cb-qwen-3-8-27b at 400K-770K input tokens
# per minute. Cerebras' pay-as-you-go tier caps qwen-3.8-27b at 150K uncached / 450K total tokens
# per minute, so it answered 66 requests with HTTP 429. The station forwarded each 429 verbatim
# (correct), the broker voided each one and refunded the consumer's hold (correct, $0 receipts),
# and then maybeFlagEmptyOutput() struck the OPERATOR 66 times for "billed input but produced no usable
# output". strike() calls SetAccountRecountHold(acct, true) UNCONDITIONALLY on every strike, so
# the first 429 froze every payout lot of the house account; by strike 5 it was HELD-but-not-banned
# under the corroboration guard. A provider saying "slow down" was booked as fraud evidence.
# The 2026-09-05 purge (43 rows deleted by hand, founder-approved) was the same failure family.
#
# THE RULE THIS SPEC PINS: the empty-output strike exists to catch a station that TAKES billable
# input and returns nothing usable - a fraud / quality signal about the operator. An upstream
# HTTP 429 is a capacity signal about the provider behind the station. It is voided for the
# consumer exactly as today, it is recorded for audit, it feeds ROUTING (cooldown, see
# features/routing/upstream_backpressure.feature), and it is NEVER a strike, never a hold, never
# a ban input, and never a success in the health average either.
#
# PRECEDENT: features/security/known_vulnerabilities.feature "An all-reasoning reply is real
# output - never billed $0-and-struck for empty output" carved a false-positive class out of the
# same strike; toolcall.go recordToolProbe(transient) already treats a 429 as a NON-verdict that
# must not move state. This spec applies the same shape to the void path.
#
# AMENDS (needs the founder's explicit re-approval, listed so nothing is weakened silently):
#   - features/money/recount_billing.feature "A node erroring with status >= 400 is voided
#     regardless of claimed input" - the VOID clause stands unchanged; the title's universal
#     "and the empty-output strike is flagged" gains the carve-out "except an upstream 429".
#     Its concrete example (503) still strikes. The VOID gate truth table and the output-usability
#     outline are untouched: a 429 stays voided.
#   - The hold-on-first-strike behavior (strikes.go:117) is a DECISION POINT below (Rule 4),
#     not silently changed by this spec.
#
# GROUND TRUTH (origin/main e7d492d5): cmd/rogerai-broker/recount.go:482 producedUsableOutput
# (void gate, status >= 400 -> void; UNCHANGED by this spec), tunnel.go:2036-2053 (non-stream
# void branch: maybeFlagEmptyOutput at 2038, $0 Settle, status+body passed through) and
# tunnel.go:2434-2445 (stream void branch; the no-capture fallback at 2437 hard-codes
# res.Status < 400 and must gain the same carve-out), tunnel.go:1989 and :2387 exitInflight(...,
# res.Status < 500) (today a 429 is folded into the success EWMA as a SUCCESS), strikes.go:439
# maybeFlagEmptyOutput (its existing escapes: context-overflow confession, oversized request -
# this spec adds the third: upstream 429), strikes.go:166 strike() and :209 the unconditional
# SetAccountRecountHold(acct, true), strikes.go:461 flagEmptyOutput (evidence.status already
# records the raw status), internal/agent/agent.go serve/serveStream (the station forwards
# resp.StatusCode verbatim; a transport failure posts 502 "upstream unreachable"),
# internal/protocol/protocol.go UsageReceipt + signingBytes (any broker-stamped void field must be
# zeroed there like GrantID/Broker*Tokens or VerifyNode breaks), internal/store safety*.go
# OwnerStrike/OwnerStrikeStats/SetAccountRecountHold/ExpireRecountHolds, recourse.go
# /owner/strikes (today omits "held" for an operator) and /admin/unhold. Step definitions: real
# in-memory store AND the testcontainers Postgres for the money/strike rows (no mocks); a real
# httptest upstream behind a real station loop returning scripted statuses.
#
# ALSO AMENDS: features/curated/curated_tower.feature "A tower's curated station fails over like
# any station" asserts "When the tower's upstream refuses a request / Then the standard
# empty-output strike applies" - a REFUSAL that is a 429 no longer strikes; a 5xx refusal still
# does. That scenario is re-worded in the same change, not left contradicting this file.

Feature: An upstream HTTP 429 voids the request for the consumer but never strikes, holds, or grades the operator

  Background:
    Given a broker with a real store and one owned station "st1" (owner "op1") serving "m"
    And the station's upstream is a scripted HTTP server

  # ===========================================================================
  # 1. THE CARVE-OUT - 429 voids, refunds, records; it does not strike
  # ===========================================================================

  Scenario: a non-stream upstream 429 is voided and refunded but not struck
    Given a funded consumer with a 10.000000 credit balance
    And the upstream is scripted to return 429 with body {"error":{"message":"rate limit exceeded"}}
    When the consumer relays a 5000-token prompt on "m" (non-stream)
    Then the consumer receives status 429 with the upstream error body
    And the consumer is charged 0.000000 credits and the hold is refunded in full
    And a receipt exists for the request with cost 0 and owner_share 0
    And NO owner_strikes row exists for "op1"
    And "op1" is NOT in account_recount_holds
    And the log line is "THROTTLED upstream-429 user=... node=st1 - $0, hold refunded, not a strike"

  Scenario: a streaming upstream 429 is voided and refunded but not struck
    Given the upstream is scripted to return 429 with a JSON error body (no SSE)
    When a funded consumer relays with "stream": true
    Then the stream carries the upstream error and ends
    And the consumer is charged 0.000000 credits
    And NO owner_strikes row exists for "op1"

  Scenario: the stream no-capture fallback path also exempts 429
    Given the broker runs without stream capture (sink.cap is nil)
    And the upstream is scripted to return 429
    When a funded consumer relays with "stream": true
    Then the request is voided
    And NO owner_strikes row exists for "op1"

  Scenario Outline: only 429 is exempt - every other no-output status still strikes exactly as today
    Given the upstream is scripted to return <status> with <body>
    When a funded consumer relays a prompt
    Then the request is voided and the consumer is charged 0.000000 credits
    And exactly one owner_strikes row of kind "empty-output" exists for "op1" with evidence.status <status>

    Examples:
      | status | body                              |
      | 400    | {"error":"bad request"}           |
      | 401    | {"error":"upstream key rejected"} |
      | 404    | {"error":"model not found"}       |
      | 500    | {"error":"internal"}              |
      | 502    | {"error":"upstream unreachable"}  |
      | 503    | {"error":"overloaded"}            |
      | 504    | {"error":"upstream timeout"}      |
      | 200    | {"choices":[]}                    |

  Scenario: a 429 that somehow carries completion text is STILL voided (the void gate is unchanged)
    Given the upstream is scripted to return 429 with a body containing choices[0].message.content "hello"
    When a funded consumer relays a prompt
    Then the request is voided and the consumer is charged 0.000000 credits
    And NO owner_strikes row exists for "op1"

  Scenario: a 429 claiming prompt tokens does not become a billed-input strike either
    Given the upstream is scripted to return 429 with usage.prompt_tokens 5000
    When a funded consumer relays a prompt
    Then the request is voided
    And the receipt records prompt_tokens 0 (a throttled request consumed nothing upstream)
    And NO owner_strikes row exists for "op1"

  Scenario: a 429 is idempotent under a retried settle
    Given the upstream is scripted to return 429
    When a funded consumer relays a prompt
    And the same job result is delivered twice (multi-instance bus replay)
    Then exactly one receipt exists for the request
    And NO owner_strikes row exists for "op1"

  # ===========================================================================
  # 2. AUDITABILITY - the 429 must survive without a strike row to carry it
  # ===========================================================================

  Scenario: a voided receipt carries the void reason and the upstream status
    Given the upstream is scripted to return 429
    When a funded consumer relays a prompt
    Then the stored receipt JSON has "void_reason": "upstream-throttled" and "upstream_status": 429

  Scenario Outline: every void reason is named on the receipt
    Given the upstream is scripted to return <status> with <body>
    When a funded consumer relays a prompt
    Then the stored receipt JSON has "void_reason": "<reason>" and "upstream_status": <status>

    Examples:
      | status | body                           | reason              |
      | 429    | {"error":"rate limit"}         | upstream-throttled  |
      | 503    | {"error":"overloaded"}         | upstream-error      |
      | 502    | {"error":"upstream unreachable"} | upstream-error    |
      | 200    | {"choices":[]}                 | empty-output        |

  Scenario: a settled (non-void) receipt carries neither key
    Given the upstream is scripted to return 200 with a real completion
    When a funded consumer relays a prompt
    Then the stored receipt JSON has no "void_reason" and no "upstream_status" key

  Scenario: the broker-stamped void fields do not break the node's signature
    Given the upstream is scripted to return 429
    When a funded consumer relays a prompt
    Then the stored receipt's node_sig still verifies against the node's key
    And the broker_sig covers the void fields (tampering with void_reason fails broker verification)

  Scenario: the void receipt is not mistakable for a free relay
    Given a free band relay that settled normally at $0
    And a throttled relay voided at $0
    When the founder lists receipts for the consumer
    Then the free relay shows no void_reason and the throttled one shows "upstream-throttled"

  Scenario: the operator can see their throttle count on the strikes endpoint
    Given 3 requests to "st1" were throttled upstream today
    When the owner calls GET /owner/strikes
    Then the JSON has "held": false
    And "throttled_24h": 3 with the note that throttles are not strikes

  Scenario: /owner/strikes always reports "held" for an operator (today it is omitted)
    Given "op1" has one empty-output strike from a 503
    When the owner calls GET /owner/strikes
    Then the JSON has a boolean "held" key that reflects account_recount_holds

  Scenario: the admin per-node view separates throttles from strikes
    Given "st1" had 66 upstream 429s and 1 genuine empty-output today
    When the founder calls GET /admin/node/st1 with the admin token
    Then it reports strikes=1 and throttled=66 as distinct counters

  # ===========================================================================
  # 3. HEALTH GRADING - a throttle is neither a success nor a failure
  # ===========================================================================

  Scenario: a 429 does not raise the station's success average
    Given "st1" has a measured success average of 0.50
    And the upstream is scripted to return 429
    When a funded consumer relays a prompt
    Then the success average is still 0.50 (unchanged, like toolcall's transient non-verdict)

  Scenario: a 429 does not lower the station's success average either
    Given "st1" has a measured success average of 1.00
    And the upstream is scripted to return 429
    When a funded consumer relays a prompt
    Then the success average is still 1.00

  Scenario: a 5xx still lowers the success average as today
    Given "st1" has a measured success average of 1.00
    And the upstream is scripted to return 503
    When a funded consumer relays a prompt
    Then the success average is below 1.00

  Scenario: in-flight accounting exits cleanly on a 429
    Given the upstream is scripted to return 429
    When a funded consumer relays a prompt
    Then the station's in_flight count returns to 0
    And the shared-store in-flight mirror agrees

  # ===========================================================================
  # 4. DECISION POINT - when does a strike freeze payouts? (founder ruling needed)
  # ===========================================================================
  # Today (strikes.go:117) EVERY strike of EVERY kind calls SetAccountRecountHold(acct, true)
  # before any threshold is consulted: one honest 503 freezes an operator's entire payout queue
  # for up to ROGERAI_RECOUNT_HOLD_DAYS (7), and each fresh strike re-inserts the hold with a new
  # created_at, so a station with one error a day is held forever. The 429 carve-out above fixes
  # the Sep 7 incident on its own. The scenarios in this Rule propose the payout freeze move to
  # the WARN threshold (windowed >= strikeWarnAt, default 3) so a single accident is evidence,
  # not a freeze. If the founder prefers today's freeze-on-first-strike, DELETE this Rule and the
  # rest of the spec stands.

  Rule: The payout hold engages at the warn threshold, not on the first strike

    Scenario: a single genuine empty-output strike is recorded but does not hold payouts
      Given "op1" has no strikes
      And the upstream is scripted to return 503
      When a funded consumer relays a prompt
      Then exactly one owner_strikes row exists for "op1"
      And "op1" is NOT in account_recount_holds
      And the log says "STRIKE recorded (1/3 to warn, 1/5 to ban) - payouts not held"

    Scenario: reaching the warn threshold holds payouts
      Given "op1" has 2 empty-output strikes inside the decay window
      And the upstream is scripted to return 503
      When a funded consumer relays a prompt
      Then "op1" IS in account_recount_holds
      And the warning email is sent as today

    Scenario: an impossible-input (zeroDoubt) strike still holds AND bans immediately
      Given the station claims more prompt tokens than the request could contain
      When the request settles
      Then "op1" is banned and in account_recount_holds (unchanged)

    Scenario: strikes outside the decay window do not count toward the hold
      Given "op1" has 2 empty-output strikes dated 31 days ago
      And the upstream is scripted to return 503
      When a funded consumer relays a prompt
      Then "op1" is NOT in account_recount_holds

    Scenario: a hold does not refresh on a strike below the threshold once expired
      Given "op1" was held, the hold expired, and 1 strike remains inside the window
      And the upstream is scripted to return 503
      When a funded consumer relays a prompt
      Then "op1" is NOT in account_recount_holds

  # ===========================================================================
  # 5. ADVERSARIAL - the carve-out cannot be abused
  # ===========================================================================

  Scenario: an operator that answers everything with 429 earns nothing and is quarantined by probes
    Given the upstream is scripted to return 429 to every request including probes
    When 6 probe rounds run
    Then "st1" is excluded from pickFor (probe dead streak)
    And "op1" has zero earnings and zero strikes
    And the market lists "st1" as offline

  Scenario: a 429 cannot be used to launder a real empty reply
    Given the upstream is scripted to return 200 with an empty completion
    When a funded consumer relays a prompt
    Then the request is voided AND struck (the 200-empty path is untouched)

  Scenario: a fabricated 429 from a station does not refund more than the hold
    Given a funded consumer with a 1.000000 credit balance and a 0.300000 hold
    And the upstream is scripted to return 429
    When the request settles
    Then the consumer's balance is exactly 1.000000 (the hold released, nothing more)
    And the ledger has one hold and one hold_release for the request and no earn row

  Scenario: an operator cannot clear an existing hold by generating 429s
    Given "op1" is held for a genuine recount discrepancy
    And the upstream is scripted to return 429
    When 10 funded relays are throttled
    Then "op1" is still in account_recount_holds
    And no strike rows were added

  # ===========================================================================
  # 6. REGRESSION - Sep 7 replayed
  # ===========================================================================

  Scenario: the 2026-09-07 burst leaves the house account untouched
    Given the upstream is scripted to return 429 for 66 of 228 requests and 200 with real completions otherwise
    When one funded consumer relays 228 prompts on "m"
    Then 162 receipts settled with cost > 0 and 66 voided with void_reason "upstream-throttled"
    And NO owner_strikes rows exist for "op1"
    And "op1" is NOT in account_recount_holds
    And the operator's pending earnings equal the sum of the 162 settled owner shares
    And the consumer's balance equals the start balance minus the 162 settled costs
