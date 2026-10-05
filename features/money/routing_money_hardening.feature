# ROUTING MONEY HARDENING (slice 6, after the 2026-10-02 product / security / fairness audit).
# PURPOSE: what a consumer pays and what an operator earns are right in the cases the routing
# work exposed: price ranking that sees the input price; a default input cap; a busy station
# that fails over instead of erroring or returning an empty stream; usage reported once and as
# billed; work that was delivered is paid for and work that was not is not; holds that do not
# lock up a small wallet; a price lock that protects against hikes without turning a short
# promo into a day-long obligation. Contract text: features/routing/ROUTING-HARDENING-CONTRACT-
# DRAFT-A.md. Audit BUGs #3, #9 and #23; founder rulings 2026-10-02 #5, #11, #14, #24.
#
# GROUND TRUTH (wt/routing-slice6 at b1754466):
#   #3  edgeplan.go:106 edgeMetricLess (sortPrice) and pickFor's strict sort rank on OUT price,
#       IN price only on a tie; router.go:172 priceMod scores OUT price only;
#       pricesafety.go:31 consumerDefaultMaxOut applies a $10/1M default to OUT; IN is bounded
#       only by the $50/1M register ceiling (pricesafety.go:22 maxPriceInCeiling).
#   #5  tunnel.go ~2846 non-stream: dispatchBusy / dispatchBusErr / dispatchOffAir /
#       dispatchLost -> writeDispatchFailure and return (no failover; the hold is released).
#       Stream: tunnel.go ~3653 a remote dispatch error and ~3758 dispatchFailed call
#       lw.commit() then a voided usage chunk: the consumer gets an EMPTY 200 stream.
#   #9  non-stream: the station's body (with the station's own claimed `usage`) is passed
#       through; billed counts are only in X-RogerAI-Tokens-In/Out and X-RogerAI-Cost.
#       Stream: tunnel.go:4921 ensureStreamIncludeUsage forces include_usage on the station,
#       whose usage chunk passes through (only usage.rogerai is stripped), followed by the
#       broker's usage chunk: two usage chunks.
#   #11 a client disconnect is never passed to the station; the stream runs to the end and
#       settles in full (features/trust/stream_receipt_parity.feature:312,
#       features/routing/model_fallback_list.feature:431).
#   #14 tunnel.go ~3749: a stall after content was delivered writes a voided chunk and settles
#       nothing (stream_receipt_parity.feature:292); a non-stream 504 discards the station's
#       later result (unreg).
#   #23 tunnel.go:4078 estimateMaxCost reads only max_tokens; with none it assumes the full
#       context window (8192 when undeclared) as output; max_completion_tokens is ignored.
#   #24 main.go:1068 quotedPrice mints a lock of b.lockWin (24 h) at the price in effect and
#       bills min(lock, current) for the whole window, however briefly that price was posted.
#
# SUPERSEDES (founder approval of this set approves each override):
#   features/routing/model_fallback_list.feature:437 "a dispatch failure that is not an upstream
#     verdict is answered as today" (now a failover trigger).
#   features/routing/model_fallback_list.feature:431 "a client that disconnects while the first
#     model is being tried ends the plan" - its "the finished attempt settles" line: now the
#     station is cancelled and only delivered tokens are billed.
#   features/trust/stream_receipt_parity.feature:292 "A stream that stalls after content is
#     voided per today's rule and the chunk says so" (now delivered tokens settle).
#   features/trust/stream_receipt_parity.feature:312 "A client that disconnects mid-stream gets
#     no chunk, but the settle still happens" - its "settles at the billed cost" line: now
#     settles at the tokens produced up to the cancel.
#   features/routing/node_preference.feature:766 "sort price takes the cheapest out-price every
#     time" and :771 "sort price breaks an out-price tie on in-price" (price now means estimated
#     request cost; the band rule is in fairness_and_abuse.feature).
#
# DECISION (left for the founder, default chosen): a non-stream request answered 504 to the
#   consumer whose station's result arrives LATE within the grace window. Option A (default):
#   the consumer is billed $0 (they received nothing), the operator is not paid, and is not
#   struck; the late receipt is recorded as void_reason "late-after-timeout" for lineage.
#   Option B: the operator is paid from the held amount and the consumer billed for work they
#   never received. B is rejected by default because the consumer got a 504.
#
# DESIGN CHOICES MADE HERE (knobs):
#   - Expected output for the cost estimate: max_completion_tokens, else max_tokens, else the
#     bounded default output (ROGERAI_DEFAULT_OUTPUT_TOKENS, 4096), never above the declared
#     context window minus the measured prompt.
#   - The blended price on /v1/models uses a reference shape of 3:1 input:output tokens
#     (ROGERAI_BLEND_INPUT_RATIO) and is labeled as such.
#   - "Delivered" on a stream = the completion text the broker forwarded to the client before
#     the cancel or stall, recounted by the broker's tokenizer (the station's claim is capped
#     at that recount, never raised).
#   - A promo-length lock: if the price in effect when a lock is minted had been posted for
#     less than ROGERAI_LOCK_MIN_POSTED (1 h), the lock expires when that price stops being in
#     effect (the earlier of that moment and the 24 h window).
#
# Enforced by: cmd/rogerai-broker/routing_money_hardening_bdd_test.go (TestRoutingMoneyHardeningBDD).

Feature: What the consumer pays and the operator earns are right in every routing case

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And the consumer default out-cap is $10/1M
    And the consumer default in-cap is $5/1M
    And the default output token budget is 4096
    And a funded consumer "alice" holds $10.00

  # --- #3: price ranking sees the input price -------------------------------------------

  Scenario: A station with a near-zero out price and a huge in price no longer wins :floor
    Given station "s-trick" serves "qwen3-32b" at in $4.90/1M out $0.01/1M
    And station "s-fair" serves "qwen3-32b" at in $0.20/1M out $0.60/1M
    When "alice" relays a 50000-token prompt with max_tokens 500 for "qwen3-32b:floor"
    Then the response is served by "s-fair"

  Scenario: Sort price ranks on estimated request cost
    Given station "s1" at in $1.00/1M out $1.00/1M and station "s2" at in $0.10/1M out $3.00/1M
    When "alice" relays a 10000-token prompt with max_tokens 100 and provider.sort "price"
    Then "s2" is ranked first (estimated $0.0013 vs $0.0101)
    When "alice" relays a 100-token prompt with max_tokens 10000 and provider.sort "price"
    Then "s1" is ranked first (estimated $0.0101 vs $0.03001)

  Scenario: The expected output for the estimate prefers max_completion_tokens
    Given stations "s1" and "s2" as in the previous scenario
    When "alice" relays a 10000-token prompt with max_completion_tokens 100 and max_tokens 9000 and provider.sort "price"
    Then "s2" is ranked first

  Scenario: With no output limit stated the estimate uses the default output budget
    Given stations "s1" and "s2" as in the previous scenario
    When "alice" relays a 10000-token prompt with no max_tokens and provider.sort "price"
    Then the estimate uses 4096 output tokens

  Scenario: The default ranking's price term uses the estimated request cost too
    Given station "s-trick" at in $4.90/1M out $0.01/1M and "s-fair" at in $0.20/1M out $0.60/1M
    And both are equally healthy and fast
    When 200 consumers relay 50000-token prompts with no routing object
    Then "s-fair" serves more requests than "s-trick"

  Scenario: Free offers still price-tie and never move the price range
    Given a free station "s-free" and paid stations "s1" and "s2"
    When the price range for scoring is computed
    Then "s-free" does not move it

  Scenario: The default input cap excludes a station above $5/1M input
    Given station "s-dear-in" serves "qwen3-32b" at in $6.00/1M out $0.50/1M
    When "alice" relays to "qwen3-32b" with no caps stated
    Then "s-dear-in" is NOT a candidate

  Scenario Outline: The default input cap composes stricter with the header and the body
    Given station "s" serves "qwen3-32b" at in $<price>/1M out $0.50/1M
    When "alice" relays with <stated>
    Then "s" <is> a candidate

    Examples:
      | price | stated                                                       | is     |
      | 4.00  | nothing                                                      | is     |
      | 6.00  | nothing                                                      | is NOT |
      | 6.00  | header X-Roger-Max-Price "8"                                 | is     |
      | 6.00  | body provider.max_price.prompt 8                             | is     |
      | 6.00  | header X-Roger-Max-Price "8" and body max_price.prompt 3     | is NOT |
      | 2.00  | header X-Roger-Max-Price "1"                                 | is NOT |
      | 49.00 | body provider.max_price.prompt 100                           | is     |
      | 60.00 | body provider.max_price.prompt 100                           | is NOT |

  Scenario: An explicit input cap above the register ceiling is clamped to the ceiling
    When "alice" relays with body provider.max_price.prompt 1000
    Then the effective input cap is $50/1M

  Scenario: The default input cap is a knob
    Given ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_IN is 8
    And station "s" serves at in $6.00/1M
    When "alice" relays with no caps stated
    Then "s" is a candidate

  @proxy
  Scenario: The local proxy owner's input cap is a ceiling for guests (unchanged rule)
    Given the proxy owner tuned with --max-in 1
    When a guest sends provider.max_price.prompt 4
    Then the broker receives provider.max_price.prompt 1

  Scenario: /v1/models shows a blended price beside the per-axis prices
    Given station "s1" serves "qwen3-32b" at in $0.20/1M out $0.60/1M
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry's rogerai.blended_price_per_1m is 0.30
    And the entry labels the blend ratio "3:1 input:output"

  # corrected 2026-10-04 (founder-approved): at 3:1, s1 blends to 0.825 and s2 to 0.875; the lowest single-station blend is 0.825
  Scenario: A blended price never mixes the in price of one station with the out price of another
    Given "s1" at in $0.10 out $3.00 and "s2" at in $1.00 out $0.50 serve "m"
    When a consumer GETs /v1/models
    Then rogerai.blended_price_per_1m is the lowest blended price of a single station (0.825)

  # --- #5: a dispatch failure before any work fails over -------------------------------

  Scenario Outline: A non-stream dispatch failure before any work fails over to a sibling
    Given nodes "n-1" and "n-2" are on air for "qwen3-32b"
    And dispatching to "n-1" fails with <outcome>
    When "alice" relays to "qwen3-32b"
    Then the response is 200 served by "n-2"
    And exactly one hold was placed and it covered the plan
    And "n-1" is not struck

    Examples:
      | outcome             |
      | no poller free      |
      | station off air     |
      | handoff lost        |
      | dispatch bus error  |

  Scenario Outline: A stream dispatch failure before any frame fails over instead of an empty 200
    Given nodes "n-1" and "n-2" are on air for "qwen3-32b"
    And dispatching to "n-1" fails with <outcome>
    When "alice" relays a streaming request to "qwen3-32b"
    Then the stream carries "n-2"'s content
    And its usage chunk names "n-2"

    Examples:
      | outcome             |
      | no poller free      |
      | station off air     |
      | handoff lost        |
      | dispatch bus error  |

  Scenario: Every station failing to dispatch answers 503 with a code and Retry-After, never an empty 200
    Given every station for "qwen3-32b" is busy
    When "alice" relays a streaming request to "qwen3-32b"
    Then the response status is 503 with error code "station_busy" and a Retry-After
    And no SSE frame was written
    And the hold is released in full

  Scenario: The non-stream all-busy answer has a code and Retry-After too
    Given every station for "qwen3-32b" is busy
    When "alice" relays to "qwen3-32b"
    Then the response status is 503 with error code "station_busy" and a Retry-After

  Scenario: A dispatch failure walks a model list like any failover trigger
    Given "a1" serves "a" but no poller is listening on it, and "b1" serves "b"
    When "alice" relays with "model": "a" and "models": ["b"]
    Then the response is 200 served as "b"

  Scenario: A dispatch failure respects allow_fallbacks false
    Given nodes "n-1" and "n-2" are on air and dispatching to "n-1" fails with "no poller free"
    When "alice" relays with provider.order ["n-1"] and provider.allow_fallbacks false
    Then the response status is 503 with error code "station_busy"
    And "n-2" received nothing

  Scenario: A dispatch failure respects the deadline rule for a new attempt
    Given less than 10 seconds of the request deadline remain when dispatch to "n-1" fails
    When the failover would start a new attempt
    Then no new attempt is started and the 503 is answered

  Scenario: The non-stream 504 timeout is still answered, not failed over
    Given "n-1" never answers within nonStreamRelayWait and "n-2" is on air
    When "alice" relays to "qwen3-32b"
    Then the response is 504 "node timed out" and "n-2" received nothing

  # --- #9: usage is reported once and as billed -------------------------------------------

  Scenario: A non-stream response's usage is the billed counts with cost and a rogerai block
    Given node "n-1" claims 1000 prompt and 300 completion tokens and the broker recounts 950 and 300
    When "alice" relays to "qwen3-32b"
    Then the response body's usage.prompt_tokens is 950 and usage.completion_tokens is 300
    And usage.total_tokens is 1250
    And usage.cost equals X-RogerAI-Cost
    And usage.rogerai.node is "n-1" and usage.rogerai.receipt verifies

  Scenario: The station's own usage figures never reach the consumer unaltered
    Given node "n-1" claims 5000 completion tokens for a 300-token completion
    When "alice" relays to "qwen3-32b"
    Then usage.completion_tokens is at most 300

  Scenario: A stream where the broker injected include_usage carries exactly one usage chunk
    Given "alice"'s streaming request does not set stream_options
    And node "n-1" emits its own usage chunk
    When the stream completes
    Then exactly one chunk with a usage object reaches "alice"
    And it is the broker's chunk with a usage.rogerai block

  Scenario: A stream where the consumer asked for include_usage still carries exactly one usage chunk
    Given "alice"'s streaming request sets stream_options.include_usage true
    And node "n-1" emits its own usage chunk
    When the stream completes
    Then exactly one chunk with a usage object reaches "alice"
    And it is the broker's chunk

  Scenario: include_usage false does not suppress the broker's chunk (approved rule kept)
    Given "alice"'s streaming request sets stream_options.include_usage false
    When the stream completes
    Then exactly one usage chunk reaches "alice" and it is the broker's

  Scenario: A station's usage chunk carrying content is kept as content minus its usage object
    Given node "n-1" emits a final chunk with both a content delta and a usage object
    When the stream completes
    Then "alice" receives the content delta
    And the station's usage object is not forwarded

  Scenario: A voided non-stream answer reports zero usage cost and the void reason
    Given node "n-1" answers 200 with empty output and no sibling exists
    When "alice" relays to "qwen3-32b"
    Then usage.cost is 0 and usage.rogerai.void_reason is "empty-output"

  # --- #11 + #14: bill what was delivered ------------------------------------------------

  Scenario: A client disconnect cancels the station's job
    Given node "n-1" streams to "alice"
    When "alice" disconnects after 40 completion tokens were forwarded
    Then the broker sends "n-1" a cancel for the job
    And "n-1" stops generating

  Scenario: A disconnect bills the tokens forwarded up to the cancel, recounted
    Given node "n-1" streams at out $0.60/1M
    When "alice" disconnects after 40 completion tokens were forwarded
    Then the settle bills the prompt plus 40 completion tokens
    And the operator earns their share of that amount
    And the hold's remainder is released

  Scenario: A disconnect bill is clamped to the hold
    Given the hold was sized for 100 completion tokens
    And a station claims 500 tokens produced before the cancel arrived
    When "alice" disconnects
    Then the settle bills at most the hold

  Scenario: A disconnect is never a strike
    Given node "n-1" owned by "op1" is streaming
    When "alice" disconnects mid-stream
    Then no owner_strikes row exists for "op1"

  Scenario: The station's claim after a cancel is capped at the forwarded recount
    Given node "n-1" claims 300 completion tokens after "alice" disconnected at 40 forwarded
    When the receipt arrives
    Then the settle bills 40 completion tokens, not 300

  Scenario: A disconnect before the first content frame bills only what the station already did
    Given "alice" disconnects before any content frame was forwarded
    And node "n-1" had processed the prompt
    When the receipt arrives
    Then the settle bills the prompt tokens and 0 completion tokens

  Scenario: A disconnect on a non-stream request cancels and bills nothing delivered
    Given "alice"'s non-stream request is being served by "n-1"
    When "alice" disconnects before the result arrives
    Then the broker cancels "n-1"'s job
    And "alice" is billed $0 because nothing was delivered

  Scenario: A stall after content settles the delivered tokens
    Given node "n-1" streams 60 completion tokens to "alice" then goes silent past the idle window
    When the stall is detected
    Then the stream ends with a usage chunk billing the prompt plus 60 completion tokens
    And usage.rogerai.void_reason is absent and usage.rogerai.partial is "stall"
    And the operator earns their share
    And [DONE] follows the chunk

  Scenario: A stall after content is clamped to the hold and keeps the existing stall signal only
    Given node "n-1" owned by "op1" stalls after content
    When the stall is settled
    Then the settle bills at most the hold
    And no empty-output strike is recorded for "op1"
    And the station's stall counter used by health grading increments as today

  Scenario: A stall before any content is still a void (unchanged)
    Given node "n-1" sends no content frame and goes silent past the idle window
    When the stall is detected
    Then the usage chunk bills 0 and names the stall
    And the plan fails over if a sibling exists and no frame was written

  Scenario: The delivered-token bill uses the broker's recount, never the station's claim
    Given node "n-1" stalls after forwarding text the broker recounts as 60 tokens
    And the station's partial claim says 400
    When the stall is settled
    Then 60 completion tokens are billed

  Scenario: The late non-stream receipt after a 504 is recorded, not billed, not paid (default decision)
    Given "n-1"'s result arrives 10 seconds after "alice" was answered 504
    When the late receipt is processed within the 30-second grace window
    Then "alice" is billed $0
    And the receipt is recorded in "n-1"'s chain with void_reason "late-after-timeout"
    And no owner_strikes row is recorded for the lateness

  Scenario: A late receipt after the grace window is discarded as today
    Given "n-1"'s result arrives 45 seconds after the 504
    Then no receipt is recorded for it

  # --- #23: holds sized to what the request can actually produce ---------------------------

  Scenario: The hold reads max_completion_tokens
    Given station "s1" at in $0.20/1M out $0.60/1M with a declared window of 131072
    When "alice" relays a 1000-token prompt with max_completion_tokens 200
    Then the hold covers 1000 prompt tokens and 200 output tokens

  Scenario: With no output limit the hold uses the default output budget, and max_tokens is sent to match
    Given station "s1" at in $0.20/1M out $0.60/1M with a declared window of 131072
    When "alice" relays a 1000-token prompt with no max_tokens
    Then the hold covers 4096 output tokens, not 131072
    And the body forwarded to "s1" carries max_tokens 4096

  Scenario: A consumer's explicit max_tokens above the default is honored
    When "alice" relays with max_tokens 20000 to a station with a 131072 window
    Then the hold covers 20000 output tokens and the forwarded max_tokens is 20000

  Scenario: The default output budget never exceeds the window left after the prompt
    Given a station with a declared window of 8192
    When "alice" relays a 6000-token prompt with no max_tokens
    Then the hold and the forwarded max_tokens are 2192

  Scenario: The default output budget is a knob
    Given ROGERAI_DEFAULT_OUTPUT_TOKENS is 1024
    When "alice" relays with no max_tokens
    Then the forwarded max_tokens is 1024

  Scenario: A thin wallet degrades to a Tower-free plan instead of 402
    Given "alice" holds $0.05
    And a direct station whose hold for this request is $0.01 and a Tower row whose bridged hold is $2.00 both serve "m"
    When "alice" relays for "m"
    Then the response is 200 served by the direct station
    And the plan carried no Tower pair

  Scenario: A thin wallet with only Tower supply is still a 402
    Given "alice" holds $0.05 and only a Tower row with a $2.00 bridged hold serves "m"
    When "alice" relays for "m"
    Then the response is 402 with error code "insufficient_balance"

  Scenario: Concurrent requests from one small wallet each get a plan that fits what is left
    Given "alice" holds $0.03 and each direct hold for her request is $0.01
    When "alice" sends 3 concurrent requests
    Then all 3 are served
    And a 4th concurrent request is 402

  # --- #24: a price lock protects against hikes only --------------------------------------

  Scenario: A lock still protects against a hike for the full window
    Given station "s1" has posted out $0.60/1M for 3 hours
    And "alice" was served by "s1" at $0.60 (a lock is minted)
    When the owner raises the price to $1.20 and "alice" relays again within 24 hours
    Then "alice" is billed at $0.60

  Scenario: Billing within a lock is the lower of the lock and the current price
    Given "alice" holds a lock at $0.60 on "s1"
    When the owner lowers the price to $0.40
    Then "alice" is billed at $0.40

  Scenario: A lock minted during a short promo lasts only as long as the promo price
    Given station "s1" normally charges out $0.60/1M
    And the owner posts $0.10 for 10 minutes
    And "alice" is served during the promo (a lock is minted at $0.10)
    When the owner restores $0.60 and "alice" relays an hour later
    Then "alice" is billed at $0.60

  Scenario: A lock minted under a price posted for at least the minimum lasts the full window
    Given the owner posted $0.40 for 2 hours before "alice"'s first serve
    When the owner raises the price to $0.80 after 3 hours
    And "alice" relays within the 24-hour window
    Then "alice" is billed at $0.40

  Scenario: A promo-length lock still protects a consumer while the promo price stands
    Given the owner posted $0.10 five minutes ago and "alice" was served at $0.10
    When "alice" relays again while $0.10 is still posted
    Then "alice" is billed at $0.10

  Scenario: The minimum-posted window is a knob
    Given ROGERAI_LOCK_MIN_POSTED is 10 minutes
    And the owner posted $0.10 for 15 minutes before "alice" was served
    When the owner restores $0.60
    Then "alice" keeps $0.10 for the rest of the 24-hour window

  Scenario: The posted-since time is shared across instances
    Given two broker instances share one store
    And the owner's current price was first seen on instance A 2 hours ago
    When "alice" is served on instance B
    Then the lock minted on instance B lasts the full window

  # added 2026-10-04 (founder directive on shared state): the posted-since time lives in the
  # shared store; when it cannot be read the full lock is minted (today's consumer-protective rule).
  # approved 2026-10-05 (founder)
  Scenario: A shared-store outage mints the full lock
    Given station "s1" normally charges out $0.60/1M
    And the owner posts $0.10 for 10 minutes
    And the shared store is unreachable
    And "alice" is served during the promo (a lock is minted at $0.10)
    When the owner restores $0.60 and "alice" relays an hour later
    Then "alice" is billed at $0.10

  Scenario: A scheduled (time-of-use) price is never locked (unchanged rule)
    Given station "s1" publishes a free window from 02:00 to 04:00
    When "alice" is served at 03:00
    Then no lock is minted

  Scenario: A voided attempt mints no lock (unchanged rule)
    Given node "n-1" answers with 500 and "n-2" serves
    Then no lock exists for "alice" on "n-1"
