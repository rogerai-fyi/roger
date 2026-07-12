# PERMANENT regression suite: one @regression scenario per real money/security bug found
# while reading the broker this session. Each asserts the FIXED behavior so the bug can
# never silently return. Adversarial framing throughout: the actor is a malicious node,
# operator, or consumer trying to get free service, inflate earnings, dodge a ban, or
# double-credit.
#
# Ground truth per regression is cited inline. EXECUTABLE under godog via
# cmd/rogerai-broker/known_vulnerabilities_bdd_test.go - every scenario drives the REAL,
# now-fixed code (no mocks) so each bug stays a permanent, always-green regression guard.

@security @regression
Feature: Known-vulnerability regression guards

  Background:
    Given node "n1" is owned by account "op1"
    And the platform fee rate is 30%

  # ===========================================================================
  # C1 - FREE PAID PUBLIC INFERENCE
  # Ground: cmd/rogerai-broker/tunnel.go (relay hold sizing, holdIn/holdOut from
  #         offer.ActivePrice when !pricing.fixed), cmd/rogerai-broker/c1_hold_test.go
  # ===========================================================================

  Rule: A paid public relay sizes the HOLD at the real active price, never the 1e-6 floor

    @c1
    Scenario: A paid public request whose real worst-case exceeds the cap is rejected at the hold gate
      # Note: before the fix the public plan's prices were 0, so the hold floored to ~1e-6,
      # slipped under any cap, and the settle clamp then capped the real charge to ~$0 - free paid inference.
      Given a public-market offer "paid-m" priced at price_out 0.5 per 1M with ctx 8192
      And consumer "carol" has a monthly spend cap of 0.002 credits and 0.00 spent
      When carol sends a paid public request with max_tokens 8192
      Then the hold is sized at the offer's real active price of about 0.0041 credits
      And the request is rejected with 402 Payment Required at the hold gate
      And no free paid inference is served

    @c1
    Scenario: A paid public request within budget holds at the real price and bills the real cost
      Given a public-market offer "paid-m" priced at price_out 0.5 per 1M with ctx 8192
      And consumer "carol" has 100.00 real credits and no cap
      When carol sends a paid public request that produces 1000 completion tokens
      Then the hold placed was sized at the offer's active price, not ~1e-6
      And the captured cost is 0.000500 credits
      And operator "op1" earns the 0.000350 owner share (70% of the 0.000500 cost)

    @c1
    Scenario Outline: The hold is the offer's true upper bound across prices
      Given a public-market offer priced at price_out <price> per 1M with ctx <ctx>
      When a public request with max_tokens <maxtok> is sized for a hold
      Then the hold is approximately <hold> credits
      And the hold is never floored to ~1e-6 for a priced model

      Examples:
        | price | ctx  | maxtok | hold     |
        | 0.5   | 8192 | 8192   | 0.004096 |
        | 1.0   | 8192 | 4096   | 0.004096 |
        | 2.0   | 4096 | 4096   | 0.008192 |

    @c1
    Scenario: A genuinely FREE model still places no hold (the floor is not abused either way)
      Given a public-market offer "free-m" priced at 0 on both axes
      When a consumer sends a request to "free-m"
      Then no priced hold is placed (a $0 active price yields a $0 worst-case; only the bare 1e-6 floor remains, and it is released uncaptured)
      And the captured cost is 0.000000 credits

  # ===========================================================================
  # REASONING-OUTPUT FALSE BAN
  # Ground: cmd/rogerai-broker/recount.go (completionText folds message.reasoning;
  #         producedUsableOutput), cmd/rogerai-broker/reasoning_output_test.go
  # ===========================================================================

  Rule: An all-reasoning reply is real output - never billed $0-and-struck for empty output

    @reasoning
    Scenario: A reasoning-only reply is usable output and is not struck
      # Note: reasoning models (gpt-oss/deepseek) put the answer in `reasoning` with empty
      # `content`; counting only content false-fired empty-output AND recount-over-report
      # strikes that stacked to 5 and auto-banned the founder's own honest nodes.
      Given a node returns empty content with reasoning "the answer is 42" claiming 7 completion tokens
      When the broker evaluates and settles the request
      Then the request produced usable output
      And the request is NOT voided to $0
      And the empty-output strike does NOT fire
      And the recount-over-report strike does NOT fire
      And owner "op1" is NOT banned

    @reasoning
    Scenario: A reasoning-only reply is billed on its re-counted reasoning tokens
      Given a served request at price_out 1.00 per 1M
      And a node returns empty content with a 300-token reasoning channel claiming 300 completion tokens
      And the broker exactly re-counts 300 completion tokens from the reasoning text
      When the request settles
      Then the billed completion tokens are 300
      And operator "op1" earns the 0.000210 owner share (70% of the 0.000300 cost)

    @reasoning
    Scenario: A genuinely empty reply (no text, no reported tokens) is still voided and flagged
      # The TRUE-negative keys on completion_tokens==0: no output text of any kind AND the node
      # reported no completion tokens. An over-claim (empty text but reported tokens>0) is caught
      # by the RE-COUNT layer (billed on the lesser count, struck past the strike tolerance), not
      # by this void - so the void narrows to "produced nothing and claimed nothing".
      Given a node returns empty content and empty reasoning claiming 0 completion tokens
      When the broker evaluates and settles the request
      Then the request is voided to $0
      And the empty-output strike fires against owner "op1"

    @reasoning
    Scenario Outline: Reasoning rescue does not weaken the true-empty void
      # Usable when there is ANY text OR the usage backstop reports completion tokens; the void
      # fires ONLY for the true-negative (no text AND completion_tokens==0). Over-claims with
      # empty text are handled by the re-count layer, not this predicate.
      Given a reply with content <content> and reasoning <reasoning> claiming <claim> completion tokens
      When the broker evaluates the response
      Then producing usable output is <usable>

      Examples:
        | content | reasoning | claim | usable |
        | ""      | "answer"  | 7     | true   |
        | "hi"    | ""        | 2     | true   |
        | ""      | ""        | 5     | true   |
        | "  "    | "  "      | 5     | true   |
        | ""      | ""        | 0     | false  |
        | "  "    | "  "      | 0     | false  |

  # ===========================================================================
  # CHARGEBACK OVER-CLAW ACROSS MULTIPLE TOP-UPS
  # Ground: internal/store/store.go ChargebackLineage (costClawed capped on CONSUMER
  #         dollars, not operator gross), internal/store/store_test.go
  # ===========================================================================

  Rule: A chargeback claws only the operator SHARE of the disputed amount, not unrelated lots

    @chargeback
    Scenario: With multiple top-ups, the claw stops once the disputed amount is covered
      # Note: capping the loop on operator GROSS instead of consumer COST over-clawed by
      # 1/(1-fee), reaching into lots funded by the consumer's OTHER, non-disputed top-ups.
      Given wallet "alice" has 1000.00 real credits across multiple top-ups
      And alice has two settled requests of cost 100.00 each with owner share 70.00 each
      When a chargeback of 100.00 is processed for alice
      Then exactly 70.00 is clawed back from operator "op1"
      And the platform loss is 30.00
      And the operator's other 70.00 lot is untouched
      And clawed plus platform loss equals the disputed 100.00

    @chargeback
    Scenario: The claw never reaches a DIFFERENT consumer's operator lots
      Given consumer "alice" and consumer "dave" each funded one request of cost 100.00 to operator "op1"
      When a chargeback of 100.00 for alice is processed
      Then only alice's lot is clawed
      And dave's lot for the same operator is untouched

    @chargeback
    Scenario Outline: Claw is bounded by the consumer-dollar disputed amount
      Given alice has settled requests totalling <lots> at cost 100.00 each with owner share 70.00 each
      When a chargeback of <dispute> is processed for alice
      Then the total clawed from operators is <clawed>
      And the platform loss is <loss>
      And clawed plus loss equals <dispute>

      Examples:
        | lots | dispute | clawed | loss  |
        | 2    | 100.00  | 70.00  | 30.00 |
        | 2    | 200.00  | 140.00 | 60.00 |
        | 1    | 100.00  | 70.00  | 30.00 |
        | 2    | 50.00   | 35.00  | 15.00 |
        | 1    | 300.00  | 70.00  | 230.00|

    @chargeback
    Scenario: An already-paid lot comes back as a Stripe transfer reversal, not a double-claw
      Given alice has one ALREADY-PAID lot of gross 70.00 for operator "op1"
      When a chargeback of 100.00 for alice is processed
      Then a transfer reversal of 70.00 is emitted for operator "op1"
      And the remaining 30.00 is a platform loss
      And the lot is not clawed twice

  # ===========================================================================
  # GRANT-CAP FAIL-OPEN ON POSTGRES (the bucket/window column)  [FIXED]
  # Ground: cmd/rogerai-broker/grant.go grantCapCheck - now FAILS CLOSED: a GrantUsageOf
  #         error on a CAPPED grant returns 429 (grant.go:115-122), mirroring the monthly
  #         SPEND cap's posture; an UNCAPPED grant short-circuits before any read.
  #         internal/store/grant_postgres.go GrantUsageOf (day/month bucket columns).
  #         Regression test: cmd/rogerai-broker/grant_test.go TestGrantCapFailsClosedOnUsageError.
  # ===========================================================================

  Rule: A grant token cap fails CLOSED - a usage-read error must reject, never wave through

    @grant-cap
    Scenario: A grant over its monthly token cap is rejected
      Given a grant "g1" with a monthly cap of 1000 tokens and 1000 tokens already used this month
      When a request is dispatched under grant "g1"
      Then the request is rejected with 429 Too Many Requests
      And the message names the grant monthly token cap

    @grant-cap
    Scenario: A grant over its daily token cap is rejected
      Given a grant "g1" with a daily cap of 500 tokens and 500 tokens already used today
      When a request is dispatched under grant "g1"
      Then the request is rejected with 429 Too Many Requests
      And the message names the grant daily token cap

    @grant-cap
    Scenario: A failed usage read (Postgres bucket/window column unavailable) must FAIL CLOSED
      # FIXED: grantCapCheck now FAILS CLOSED - a GrantUsageOf error on a capped grant returns
      # 429 (grant.go:115-122), so an unreadable cap rejects instead of silently uncapping,
      # exactly like the monthly SPEND cap's fail-closed posture.
      Given a grant "g1" with a monthly cap of 1000 tokens
      And the grant-usage read errors (bucket/window column unavailable)
      When a request is dispatched under grant "g1"
      Then the request is rejected rather than served
      And the cap is never silently bypassed by a usage-read error

    @grant-cap
    Scenario: A grant with no caps configured is unaffected (no read, no rejection)
      Given a grant "g1" with no daily or monthly cap
      When a request is dispatched under grant "g1"
      Then no usage read is performed
      And the request is allowed

    @grant-cap
    Scenario Outline: Cap enforcement boundary (>= cap rejects)
      Given a grant with daily cap <daily> and monthly cap <monthly>
      And <dayUsed> tokens used today and <monthUsed> used this month
      When a request is dispatched under the grant
      Then the dispatch decision is <decision>

      Examples:
        | daily | monthly | dayUsed | monthUsed | decision |
        | 500   | 0       | 499     | 0         | allow    |
        | 500   | 0       | 500     | 0         | reject   |
        | 0     | 1000    | 0       | 999       | allow    |
        | 0     | 1000    | 0       | 1000      | reject   |
        | 500   | 1000    | 500     | 0         | reject   |

  # ===========================================================================
  # SEED CREDITS NEVER MINT OPERATOR EARNINGS
  # Ground: internal/store/store.go realEarnShare / consumeSeedLocked / grantSeedLocked;
  #         Settle/Finalize call realEarnShare exactly once.
  # ===========================================================================

  Rule: Free seed credits earn the operator nothing - you cannot pay out money never paid in

    @seed
    Scenario: A fully seed-funded request mints zero operator earning
      Given wallet "seeded" has 100.00 in FREE seed credits and 0 real credits
      When a request from "seeded" settles at cost 2.00 with owner share 1.40 to operator "op1"
      Then the consumer is debited 2.00
      And operator "op1" earns 0.00
      And no payable earning lot is minted from seed credits

    @seed
    Scenario: A partially seed-funded request earns only the real-funded fraction
      Given wallet "mix" has 1.00 in FREE seed credits and 9.00 real credits
      When a request from "mix" settles at cost 2.00 with owner share 1.40 to operator "op1"
      Then seed credits cover 1.00 of the cost and real credits cover 1.00
      And operator "op1" earns 0.70 (the real-funded half of the owner share)

    @seed
    Scenario: Seed is consumed before real credits (so later requests earn fully)
      Given wallet "mix" has 1.00 in FREE seed credits and 9.00 real credits
      When a 1.00-cost request settles, then a second 1.00-cost request settles (owner share 0.70 each)
      Then the first request mints 0.00 operator earning (seed-funded)
      And the second request mints 0.70 operator earning (real-funded)

    @seed
    Scenario Outline: Real-funded fraction of the owner share is what the operator earns
      Given wallet "w" has <seed> seed credits and <real> real credits
      When a request settles at cost <cost> with owner share <share> to operator "op1"
      Then operator "op1" earns <earn>

      Examples:
        | seed | real  | cost | share | earn |
        | 100  | 0     | 2.00 | 1.40  | 0.00 |
        | 0    | 100   | 2.00 | 1.40  | 1.40 |
        | 1.00 | 9.00  | 2.00 | 1.40  | 0.70 |
        | 0.50 | 9.50  | 2.00 | 1.40  | 1.05 |
        | 5.00 | 5.00  | 2.00 | 1.40  | 0.00 |

    @seed
    Scenario: A free / self request mints no earning regardless of seed state
      Given wallet "w" has 0 seed and 100.00 real credits
      When a FREE (self-use) request settles at cost 0.00
      Then operator "op1" earns 0.00
      And only a $0 metering receipt is recorded

    @seed
    Scenario: The seed grant itself is idempotent (a wallet is seeded at most once)
      Given wallet "newbie" has never been seeded
      When the wallet is seeded once with the starter amount
      And the seed is attempted again
      Then the second attempt is a no-op
      And the balance reflects exactly one starter grant

    @seed
    Scenario: The seed cap bounds total free-credit liability
      Given the seed limit is 2 distinct wallets
      When wallets "w1", "w2", and "w3" each first appear
      Then "w1" and "w2" receive the starter seed
      And "w3" is created at 0 with no seed (cap exhausted)
