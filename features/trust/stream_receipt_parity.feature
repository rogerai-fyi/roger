# STREAM RECEIPT PARITY: a streamed relay tells the consumer the same things a non-streamed
# one does - the signed receipt, the billed tokens, the exact cost, the price lock, the
# throughput - inside the stream, in the one place every OpenAI-shaped client already reads:
# a final usage chunk before `data: [DONE]`. Contract: ROUTING-EXPRESSION-CONTRACT.md §7
# (second bullet).
#
# GROUND TRUTH (origin/main 518c698b):
#   - Non-stream success sets X-RogerAI-Receipt, -Provider, -Cost (exact, fmtCostHeader),
#     -Tokens-In/-Out (billed = min(claim, broker re-count) per axis), -Balance, -Price
#     (`in=;out=;locked_until=`), -TPS, -Quality (cmd/rogerai-broker/tunnel.go:2252-2273).
#   - A stream's headers are committed by lazySSE on the first `data:` frame or at the 64 KiB
#     pre-commit cap (tunnel.go:2504-2535); commit sets ONLY Content-Type, Cache-Control and
#     X-RogerAI-Provider (tunnel.go:2524-2528). Everything settle-time cannot ride a header.
#   - The station's frames stream through the sink (tunnel.go:2711-2720); the receipt arrives
#     AFTER the station's final chunk; the broker re-counts prompt and completion at settle
#     (settleRecountPrompt / settleRecount, tunnel.go:2831-2833), clamps cost to the hold
#     (tunnel.go:2838), settles, then - only when settled - writes the SSE comment
#     `: rogerai-cost=<exact>` after the node's [DONE] has already streamed through
#     (tunnel.go:2875-2889, founder ruling 2026-07-07).
#   - The local proxy meters streamed spend from that comment (internal/client/client.go:
#     883-889, features/proxy/budget.feature "accounted from the SSE meter comment") and
#     forwards the fixed safe-header allowlist (client.go:975-978).
#   - Receipt signing/verification: features/trust/lineage_receipts.feature and
#     receipt_signature_versions.feature (SigVersion 1 covers the broker-set billing fields).
#   - Re-count and void rules: features/trust/recount.feature, reasoning_stream_output.feature.
#
# THE CHANGE. The broker WITHHOLDS the station's `data: [DONE]` frame, waits for the receipt
# and the settle exactly as today, writes ONE usage chunk of its own, then releases [DONE].
# The chunk is OpenAI-shaped (`choices: []`, `usage: {...}`) so SDKs that already read the
# final usage chunk (OpenAI, OpenRouter) read ours; the `usage.rogerai` object is the broker's
# alone. The `: rogerai-cost=` comment stays for one release so old proxies keep metering.
# Headers known BEFORE commit (Provider, Model, Relay, Price, Monthly-*, and Key-* when a key
# funded the request) are set before the first frame; nothing is re-sent.
#
# THE CHUNK (every field, exact names):
#   data: {"id":"<request id>","object":"chat.completion.chunk","created":<unix>,
#          "model":"<served model, bare id>","choices":[],
#          "usage":{"prompt_tokens":<billed in>,"completion_tokens":<billed out>,
#                   "total_tokens":<sum>,"cost":<exact settled cost, dollars>,
#                   "rogerai":{"receipt":"<X-RogerAI-Receipt encoding>","node":"<node id>",
#                              "model":"<served model>","relay":"<tower id or absent>",
#                              "tokens_in":<billed in>,"tokens_out":<billed out>,
#                              "tps":<measured>,"price_in":<$/1M>,"price_out":<$/1M>,
#                              "locked_until":<unix or 0>,"balance":<after settle>,
#                              "void_reason":"<present only on a voided stream>",
#                              "key_id":"<present only when a key funded the request>",
#                              "key_limit":<$>,"key_spend":<$ after settle>,"key_pct":<0-100>,
#                              "key_reset_at":<unix or 0>   (the four: only with key_id)}}}
#
# Enforced by: cmd/rogerai-broker/stream_receipt_parity_bdd_test.go (broker),
# internal/client/stream_usage_chunk_bdd_test.go (proxy pass-through + meter),
# internal/tui/stream_meter_bdd_test.go (TUI meter).

Feature: A streamed relay ends with the broker's signed usage chunk, equal to the non-stream headers

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And node "n-1" is on air for "qwen3-32b" at in-price $0.20/1M and out-price $0.60/1M
    And the caller has a funded wallet

  # --- shape and placement -------------------------------------------------------

  Scenario: The final data frame before [DONE] is the broker's usage chunk
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then the last `data:` frame before `data: [DONE]` is an object with "choices" equal to []
    And its "object" is "chat.completion.chunk"
    And its "id" is the request id
    And its "model" is "qwen3-32b"
    And `data: [DONE]` is the final frame of the stream

  Scenario: The usage chunk carries every field of the contract
    When a streaming request for "qwen3-32b" is served by "n-1" with 120 prompt and 40 completion tokens
    Then the usage chunk has usage.prompt_tokens 120
    And usage.completion_tokens 40
    And usage.total_tokens 160
    And usage.cost equal to the settled cost
    And usage.rogerai has the keys receipt, node, model, tokens_in, tokens_out, tps, price_in, price_out, locked_until, balance
    And usage.rogerai.node is "n-1"
    And usage.rogerai.model is "qwen3-32b"
    And usage.rogerai has no key "relay"
    And usage.rogerai has no key "void_reason"
    And usage.rogerai has none of the keys key_id, key_limit, key_spend, key_pct, key_reset_at

  @slice5
  Scenario: A key-funded stream carries the key state after settle in the chunk
    # features/relay/key_limits.feature owns the semantics; this pins the parity shape.
    Given the caller holds key "key_a1" with limit_usd 5.00 reset monthly and $1.00 already spent this window
    When a streaming request for "qwen3-32b" funded by "key_a1" is served by "n-1"
    Then the pre-commit headers include X-RogerAI-Key-Limit, X-RogerAI-Key-Spend and X-RogerAI-Key-Pct with the PRE-request state
    And the usage chunk has usage.rogerai.key_id "key_a1"
    And usage.rogerai.key_limit 5.00
    And usage.rogerai.key_spend equal to 1.00 plus usage.cost
    And usage.rogerai.key_pct equal to the post-settle percentage
    And usage.rogerai.key_reset_at equal to the window's reset time

  Scenario: The chunk is written exactly once
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then exactly one frame in the stream carries a usage.rogerai object

  Scenario: The chunk is a complete SSE event on its own
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then the usage chunk is framed as `data: <json>\n\n`
    And it is not merged into the station's last frame

  Scenario Outline: The chunk is sent regardless of stream_options.include_usage
    When a streaming request for "qwen3-32b" with stream_options <opts> is served by "n-1"
    Then the stream ends with the broker's usage chunk before [DONE]

    Examples:
      | opts                     |
      | absent                   |
      | {"include_usage": true}  |
      | {"include_usage": false} |

  Scenario: The station's [DONE] is withheld until the settle and then released
    Given "n-1" streams three content frames, then [DONE], then its receipt
    When a streaming request for "qwen3-32b" is served
    Then the consumer receives the three content frames as they arrive
    And the consumer does not receive [DONE] before the broker's usage chunk
    And the consumer receives [DONE] after it

  # superseded 2026-10-04 by contract §14 (founder-approved): §14.9 reports usage exactly once,
  # as billed, so the station's usage-only frame is no longer forwarded. Old Then: the
  # station's usage frame is forwarded unchanged / And the broker's usage chunk follows it.
  Scenario: The station's own usage chunk (if any) passes through before the broker's
    Given "n-1" streams a final frame with usage {"prompt_tokens":120,"completion_tokens":40}
    When a streaming request for "qwen3-32b" is served
    Then the station's usage frame is not forwarded
    And the broker's usage chunk is the only event with a usage object
    And only the broker's carries usage.rogerai

  Scenario: The trailing `: rogerai-cost=` comment is still emitted and agrees with the chunk
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then a comment line `: rogerai-cost=<x>` follows the usage chunk
    And <x> equals usage.cost formatted by fmtCostHeader
    And the comment is marked deprecated in the OpenAPI description

  Scenario: SSE comments and keepalives from the station are unaffected
    Given "n-1" streams ": keepalive" comments between content frames
    When a streaming request for "qwen3-32b" is served
    Then every station comment is forwarded as it arrives
    And the broker's usage chunk still comes last before [DONE]

  @proxy
  Scenario: The reasoning-to-content fallback in the proxy never touches the usage chunk
    Given the proxy's reasoning fallback is on
    And "n-1" streams reasoning deltas then a content delta
    When a streaming request for "qwen3-32b" goes through the local proxy
    Then the usage chunk reaches the guest byte-identical to what the broker wrote

  # --- equality with the non-stream headers ---------------------------------------

  Scenario: The receipt in the chunk is the same encoding X-RogerAI-Receipt would carry
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then usage.rogerai.receipt decodes with DecodeReceipt
    And the decoded receipt names the request id, "n-1" and "qwen3-32b"

  Scenario: The receipt in the chunk verifies under the current broker signature version
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then the decoded receipt carries broker signature version 1
    And VerifyBroker verifies it over the broker canonical form
    And the coverage report says the billed counts are covered

  Scenario: The node signature in the chunk's receipt verifies
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then the decoded receipt's node signature verifies against "n-1"'s key

  Scenario: The billed counts in the chunk are the receipt's broker-set counts
    Given the broker re-count is enabled
    And "n-1" claims 200 completion tokens for a completion the broker counts as 150
    When a streaming request for "qwen3-32b" is served
    Then usage.completion_tokens is 150
    And the decoded receipt's BrokerCompletionTokens is 150
    And usage.rogerai.tokens_out is 150

  Scenario: The cost in the chunk is the settled cost, after re-count and hold clamp
    Given the broker re-count is enabled
    And "n-1" claims 200 completion tokens for a completion the broker counts as 150
    When a streaming request for "qwen3-32b" is served
    Then usage.cost equals (prompt_billed * 0.20 + 150 * 0.60) / 1e6
    And the ledger debit for the request equals usage.cost

  Scenario: The cost never exceeds the hold in the chunk either
    Given the request's hold authorizes at most $0.001000
    And "n-1" claims tokens whose cost would be $0.002000
    When a streaming request for "qwen3-32b" is served
    Then usage.cost is 0.001000
    And the ledger debit is $0.001000

  Scenario: The cost is exact, never rounded to a bare zero
    Given "n-1" serves a stream billed at $0.00000036
    When the stream ends
    Then usage.cost is 0.00000036
    And the comment reads `: rogerai-cost=0.00000036`

  Scenario: Prompt tokens in the chunk are min(claim, re-count) clamped to the byte floor - never above the claim
    # Approved features/money/recount_billing.feature:111-119: input billing is min(claimed,
    # exact re-count), clamped to body bytes. An over-claim is cut DOWN; an under-claim is
    # never raised.
    Given "n-1" claims 100000 prompt tokens for a 4 KiB prompt the broker re-counts as 900
    When a streaming request for "qwen3-32b" is served
    Then usage.prompt_tokens is 900, the clamped re-count, not 100000

  Scenario: An under-claim on the prompt axis is billed as claimed (never raised)
    Given "n-1" claims 1 prompt token for a 4 KiB prompt the broker re-counts as 900
    When a streaming request for "qwen3-32b" is served
    Then usage.prompt_tokens is 1

  Scenario: The price fields equal what X-RogerAI-Price would say
    Given the caller was first quoted "n-1" for "qwen3-32b" at $0.20/$0.60 with a 24h lock
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then usage.rogerai.price_in is 0.20
    And usage.rogerai.price_out is 0.60
    And usage.rogerai.locked_until is the lock's unix expiry

  Scenario: A time-of-use free window shows 0/0 and a zero cost
    Given "n-1" is inside a published free window
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then usage.rogerai.price_in is 0
    And usage.rogerai.price_out is 0
    And usage.cost is 0

  Scenario: The balance in the chunk is the post-settle balance
    Given the caller's balance is $1.000000 before the request
    When a streaming request for "qwen3-32b" is served and settles at $0.000420
    Then usage.rogerai.balance is 0.999580

  Scenario: The tps in the chunk is the stream's measured throughput
    When a streaming request for "qwen3-32b" is served by "n-1" at a measured 42.0 tok/s
    Then usage.rogerai.tps is 42.0
    And the node's stored tps estimate was updated from the same figure

  Scenario: Non-stream and stream relays of the same shape yield equal meters
    Given "n-1" answers identically whether streamed or not
    When a non-streaming request for "qwen3-32b" is served
    And a streaming request for "qwen3-32b" is served
    Then the stream's usage.prompt_tokens equals the non-stream X-RogerAI-Tokens-In
    And usage.completion_tokens equals X-RogerAI-Tokens-Out
    And usage.cost equals X-RogerAI-Cost
    And usage.rogerai.price_in / price_out equal X-RogerAI-Price
    And usage.rogerai.node equals X-RogerAI-Provider

  # --- headers before commit ------------------------------------------------------

  Scenario: Headers known before dispatch are set before the first frame
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then the response headers at the first frame include X-RogerAI-Provider "n-1"
    And X-RogerAI-Model "qwen3-32b"
    And X-RogerAI-Price with the locked in/out prices
    And Content-Type "text/event-stream"

  Scenario: The monthly-cap headers ride the pre-commit headers on a stream
    Given the caller has a monthly cap of $5.00 and has spent $4.00
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then the response headers at the first frame include X-RogerAI-Monthly-Cap and X-RogerAI-Monthly-Spend

  Scenario: Settle-time values are never promised as headers on a stream
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then the response headers carry no X-RogerAI-Cost, X-RogerAI-Receipt, X-RogerAI-Tokens-In, X-RogerAI-Tokens-Out, X-RogerAI-Balance, X-RogerAI-TPS or X-RogerAI-Quality
    And no HTTP trailer is used

  Scenario: The SSE headers are still not committed before the first upstream verdict (invariant)
    Given "n-1" answers with an upstream 429 before any frame
    And node "n-2" is on air for "qwen3-32b" and serves
    When a streaming request for "qwen3-32b" is served
    Then the consumer sees a 200 whose X-RogerAI-Provider is "n-2"
    And the usage chunk's usage.rogerai.node is "n-2"

  # --- failover, errors, disconnects ----------------------------------------------

  Scenario: A stream that failed over before its first frame carries the served station's receipt only
    Given "n-1" answers with an upstream 429 before any frame
    And node "n-2" is on air for "qwen3-32b" and serves
    When a streaming request for "qwen3-32b" is served
    Then the usage chunk's receipt names "n-2" and the attempt id of the second attempt
    And the failed attempt's $0 receipt exists in "n-1"'s chain
    And it is not in the stream

  Scenario: A model-fallback stream names the served model in the chunk
    Given no station is on air for "qwen3-235b"
    When a streaming request with model "qwen3-235b" and models ["qwen3-32b"] is served by "n-1"
    Then the chunk's "model" is "qwen3-32b"
    And usage.rogerai.model is "qwen3-32b"
    And X-RogerAI-Model at the first frame is "qwen3-32b"

  Scenario: A stream that errors after content does not fail over and still carries a chunk
    Given "n-1" streams two content frames, then an error frame, then a receipt claiming 12 completion tokens
    When a streaming request for "qwen3-32b" is served
    Then no second station is tried
    And the stream ends with a usage chunk whose usage.completion_tokens is the billed count for 12
    And usage.cost equals the settled cost for those tokens
    And [DONE] follows the chunk

  # superseded 2026-10-04 by contract §14 (founder-approved): §14.10 settles the content
  # delivered before a stall (recounted, clamped to the hold) and marks the chunk
  # partial "stall"; a stall before any content is still a void. Old Then: the stream ends
  # with a usage chunk whose usage.cost is 0 / And usage.rogerai.void_reason names the stall.
  Scenario: A stream that stalls after content is voided per today's rule and the chunk says so
    Given "n-1" streams one content frame and then goes silent past the idle window
    When a streaming request for "qwen3-32b" is served
    Then the stream ends with a usage chunk billing the delivered content, marked partial "stall"
    And [DONE] follows the chunk

  Scenario: A genuinely empty final stream bills zero and says so
    Given "n-1" is the only station and streams no content, zero tokens, then [DONE]
    When a streaming request for "qwen3-32b" is served
    Then the usage chunk has usage.completion_tokens 0 and usage.cost 0
    And usage.rogerai.void_reason is present
    And the hold is released in full

  Scenario: A 2xx-empty stream before the first frame fails over, and the chunk is the sibling's
    Given "n-1" returns 200 with zero output before any frame
    And node "n-2" is on air for "qwen3-32b" and serves
    When a streaming request for "qwen3-32b" is served
    Then the usage chunk's usage.rogerai.node is "n-2"

  Scenario: A client that disconnects mid-stream gets no chunk, but the settle still happens
    Given the consumer disconnects after the first content frame
    When "n-1" finishes the stream and sends its receipt
    Then no usage chunk is written to the closed connection
    And the request settles at the billed cost
    And the receipt is in "n-1"'s chain
    And GET /generation for the request shows the cost and "cancelled" true

  Scenario: A settle failure refunds and the chunk reports zero
    Given the ledger refuses the stream's settle
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then the hold is released
    And the stream ends with a usage chunk whose usage.cost is 0
    And usage.rogerai.void_reason is "settle-failed"
    And no `: rogerai-cost=` comment is written

  Scenario: Reasoning tokens are billed where today's re-count bills them
    Given "n-1" streams reasoning deltas then a content delta, with usage completion_tokens 90
    When a streaming request for "qwen3-32b" is served
    Then usage.completion_tokens equals the billed count today's rule produces for that stream
    And the stream is neither voided nor struck

  Scenario: A reasoning-only stream with finish_reason length is billed off the usage
    Given "n-1" streams reasoning deltas only and ends with finish_reason "length" and usage completion_tokens 300
    When a streaming request for "qwen3-32b" is served
    Then usage.completion_tokens is the billed count for 300 under today's rule
    And usage.cost is above 0

  # --- the bridge / Tower path ------------------------------------------------------

  @later
  Scenario: A stream served through a Tower carries the same chunk with relay set
    Given no direct node is on air for "gemma-3-27b"
    And an approved Tower "tw-1" serves "gemma-3-27b" via its hub
    When a streaming request for "gemma-3-27b" is served through the bridge
    Then the usage chunk has usage.rogerai.relay "tw-1"
    And usage.rogerai.node names the relay as X-RogerAI-Provider does on the bridge path
    And the receipt in the chunk verifies
    And the decoded receipt's node signature verifies against Tower "tw-1"'s relay key (the party the broker dispatched to)
    And the decoded receipt's broker signature verifies with VerifyBroker exactly as on the direct path
    # today a bridged answer carries NO receipt at all (edgebridge.go:126-130); contract §7

  Scenario: The bridge's pre-commit headers include X-RogerAI-Relay
    Given an approved Tower "tw-1" serves "gemma-3-27b" via its hub
    When a streaming request for "gemma-3-27b" is served through the bridge
    Then the response headers at the first frame include X-RogerAI-Relay "tw-1"

  Scenario: A Tower cannot write the broker's chunk
    Given an approved Tower "tw-1" emits a frame carrying a usage.rogerai object of its own
    When a streaming request for "gemma-3-27b" is served through the bridge
    Then that frame reaches the consumer without its usage.rogerai key
    And the broker's own chunk follows with the settled figures

  # --- the local proxy and the meters ----------------------------------------------

  @proxy
  Scenario: The local proxy forwards the usage chunk untouched
    When a streaming request goes through the local proxy and is served by "n-1"
    Then the guest receives the broker's usage chunk byte-identical
    And the guest receives [DONE] after it

  @proxy
  Scenario: The proxy budget meters a stream once, from the chunk or the comment, never both
    Given the proxy session has a budget of $1.00
    When a streaming request goes through the local proxy and settles at $0.000420
    Then the session spend increases by exactly $0.000420

  @proxy
  Scenario: A stream with a chunk but no comment (a future broker) still meters
    Given the broker omits the `: rogerai-cost=` comment
    When a streaming request goes through the local proxy and settles at $0.000420
    Then the session spend increases by exactly $0.000420

  @proxy
  Scenario: A stream with a comment but no chunk (an old broker) still meters (unchanged)
    Given the broker writes only the `: rogerai-cost=0.000420` comment
    When a streaming request goes through the local proxy
    Then the session spend increases by exactly $0.000420

  @proxy
  Scenario: A stream with neither meters nothing and does not error (unchanged)
    Given the broker writes neither a chunk nor a comment
    When a streaming request goes through the local proxy
    Then the session spend is unchanged
    And the guest sees a complete stream

  @proxy
  Scenario: The proxy never trusts a station-shaped usage frame for the meter
    Given "n-1" streams a frame with usage {"cost": 0.000001} and no rogerai object
    And the broker's chunk says usage.cost 0.000420
    When a streaming request goes through the local proxy
    Then the session spend increases by exactly $0.000420

  @proxy
  Scenario: The proxy still forwards only the safe header allowlist on a stream (invariant)
    When a streaming request goes through the local proxy
    Then the guest sees X-RogerAI-Provider and X-RogerAI-Model
    And the guest does not see hop-by-hop or Set-Cookie headers

  @tui
  Scenario: The TUI cost meter reads the chunk
    Given the TUI is tuned to "qwen3-32b"
    When a streamed chat turn settles at $0.000420 with 120 in and 40 out
    Then the TUI meter shows the cost, the in and out tokens and the tps from the chunk
    And it shows them without waiting for a separate lookup

  @harness
  Scenario: The agent harness meter reads the chunk for streamed turns
    When a streamed agent turn settles
    Then the harness's per-turn meter shows the in/out tokens and cost from the chunk

  Scenario: An old client that stops reading at [DONE] loses nothing it had before
    Given a client that ignores unknown frames and stops at [DONE]
    When a streaming request for "qwen3-32b" is served by "n-1"
    Then the client sees every content frame and [DONE]
    And the client's assembled completion is unchanged

  # --- adversarial -------------------------------------------------------------------

  # corrected 2026-10-02 (founder-approved): billing is min(claim, re-count) (recount_billing.feature); an under-claim is billed as claimed
  Scenario: A station cannot alter the billed cost by emitting usage figures
    Given "n-1" streams a usage frame claiming completion_tokens 1 for a 150-token completion
    And "n-1"'s receipt claims 1 completion token
    When a streaming request for "qwen3-32b" is served
    Then the broker's chunk bills min(claim, re-count) = 1 completion token
    And the ledger debit follows the broker's chunk, not the station's frame

  Scenario: A station cannot inflate the billed cost by emitting usage figures
    Given "n-1" streams a usage frame claiming completion_tokens 5000 for a 150-token completion
    When a streaming request for "qwen3-32b" is served
    Then usage.completion_tokens is at most 150
    And usage.cost is at most the cost of 150 completion tokens

  Scenario: A station-emitted usage.rogerai object is stripped and logged, never forwarded
    Given "n-1" streams a frame carrying usage.rogerai with a forged receipt
    When a streaming request for "qwen3-32b" is served
    Then the forwarded frame has no usage.rogerai key
    And a log line names "n-1" and "forged rogerai usage object"
    And the station is not struck for it
    And the broker's own chunk follows with the genuine receipt

  # superseded 2026-10-04 by contract §14 (founder-approved): the content the station delivered
  # before going silent settles as a partial "stall" (§14.10), so withholding the receipt
  # neither skips settlement nor voids delivered work. Old Then: the stream ends with a
  # voided usage chunk (cost 0, void_reason set) and then [DONE] / And the hold is released.
  Scenario: A station cannot end the stream early with its own [DONE] to skip settlement
    Given "n-1" streams [DONE] and then never sends a receipt
    When a streaming request for "qwen3-32b" is served
    Then the broker withholds [DONE] until the idle window expires
    And the stream ends with a usage chunk billing the delivered content, marked partial "stall"
    And the request settles once

  Scenario: A station cannot send two receipts to get two chunks
    Given "n-1" sends two receipts for the same job
    When a streaming request for "qwen3-32b" is served
    Then exactly one usage chunk is written
    And the request settles once

  Scenario: The chunk never carries the prompt, the completion text or a band code
    Given the request was made on a private band
    When a streaming request is served
    Then the usage chunk contains no message text
    And it contains no band code

  Scenario: The chunk's cost cannot exceed the non-stream cost for the same billed counts (invariant)
    When a stream and a non-stream relay bill the same counts at the same locked price
    Then their costs are equal to the last digit
