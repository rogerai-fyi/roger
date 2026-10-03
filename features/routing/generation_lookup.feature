# GENERATION LOOKUP: `GET /generation?id=<request id>` tells the caller what happened to one
# request - every station tried, in order, with its status and timing; who served; what it
# cost; the receipt - the way OpenRouter's /generation does, scoped to the identity that made
# the request. Contract: ROUTING-EXPRESSION-CONTRACT.md §7 (third bullet).
#
# GROUND TRUTH (origin/main 518c698b):
#   - A request id is protocol.NewRequestID(): 8 random bytes, 16 lowercase hex chars
#     (internal/protocol/protocol.go:790-794). A failover attempt's receipt is its own row
#     under attemptID = "<id>" for the first attempt and "<id>-n" for the n-th
#     (cmd/rogerai-broker/cooling.go:94-101), so the lineage of one request is readable.
#   - There is NO per-request lookup today. The nearest is GET /console: recent lineage for
#     the resolved identity - consumer (logged-in wallet via dashIdentity) or owner (payout
#     owner via payoutOwner) - with only a `limit` param (1..100, default 20), served through
#     the wallet-namespaced authed cache (cmd/rogerai-broker/metrics_series.go:430-480,
#     dashboards.go:243-252). /usage and /me are aggregates.
#   - Receipts carry request id, node, model, claimed and broker-set token counts, prices,
#     void_reason + upstream_status on a voided attempt, node and broker signatures
#     (features/trust/lineage_receipts.feature, receipt_chain.feature).
#   - Providers only ever see a per-(user, node) pseudonym (tunnel.go:2085); the consumer's
#     wallet never reaches a station or its owner.
#   - The ledger keeps entries until account deletion anonymizes them (cmd/rogerai-broker/
#     account.go:78); the console reads that same lineage. No separate retention window exists.
#   - No `roger receipt` / `roger generation` command exists (cmd/rogerai/main.go subcommand
#     switch); `roger payout history` is unrelated.
#
# THE RECORD. One per request id, written when the relay ends (served, failed, refused):
#   { id, created_at, model_requested, models (the effective list), streamed, cancelled,
#     status (the HTTP status the consumer got), error_code (routing code or null),
#     served: { node, model, relay } | null,
#     cost, tokens_in, tokens_out, tps, ttft_ms, latency_ms,
#     moderation: { mode, verdict, latency_ms },
#     key_id (the funding key, or null),
#     attempts: [ { n, node, model, status, error_code, duration_ms, retry_after_s, void_reason } ],
#     receipt (the served attempt's encoded receipt) | null }
#   `attempts` lists REAL dispatches only; a model skipped for having no eligible station is not
#   an attempt (contract §3 "silently"; `models` already shows the list). void_reason uses the
#   approved vocabulary of upstream_failover.feature (upstream-throttled, upstream-error,
#   empty-output) plus this set's context-window and settle-failed.
#   Key limit/spend state (key_limit, key_spend_after) appears ONLY in the consumer view.
#   Never in the record: message text, band codes, station addresses, the consumer's wallet
#   (in the owner view), the pseudonym mapping.
#
# SCOPING (consistent with /console's two roles): the CONSUMER view goes to the identity that
# made the request - the signed wallet, the browser session identity, or the very grant token
# that made it. The OWNER view - the same record with consumer-side fields (balance, wallet)
# absent, and NO `receipt` (its `user` IS the per-(user, node) pseudonym, and a TRIED owner
# must never see the served owner's node or pseudonym: tunnel.go:2082-2085) - goes to the
# payout owner whose station served or was tried, showing ONLY that owner's own attempt(s),
# and to the owner who minted the grant. Everyone else gets a uniform 404, byte-identical to
# an unknown id.
#
# Enforced by: cmd/rogerai-broker/generation_lookup_bdd_test.go,
# cmd/rogerai/generation_cmd_bdd_test.go, internal/tui/last_request_bdd_test.go.

Feature: GET /generation returns one request's full routing and billing history to its owner

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And nodes "n-1" and "n-2" are on air for "qwen3-32b" at out-price $0.60/1M
    And a logged-in consumer "alice" with a funded wallet

  # --- the record for each relay outcome -------------------------------------------

  Scenario: A completed non-stream relay has a one-attempt record
    Given "alice" makes a non-streaming request for "qwen3-32b" served by "n-1" at $0.000420
    When "alice" GETs /generation?id=<that request id>
    Then the status is 200
    And the record's id is the request id
    And served.node is "n-1", served.model is "qwen3-32b", served has no relay
    And cost is 0.000420, tokens_in and tokens_out are the billed counts
    And attempts has exactly one entry with n 1, node "n-1", status 200, no error_code
    And streamed is false and cancelled is false
    And receipt decodes and verifies

  Scenario: A completed stream has streamed true and a measured ttft
    Given "alice" makes a streaming request for "qwen3-32b" served by "n-1"
    When "alice" GETs /generation for it
    Then streamed is true
    And ttft_ms is the time to the first content frame
    And latency_ms is the time to the settle
    And tps equals the usage chunk's usage.rogerai.tps

  Scenario: A failed-over relay lists every attempt in order
    Given "n-1" answers with an upstream 429 and Retry-After 15
    And "alice" makes a request for "qwen3-32b" that fails over to "n-2" and is served
    When "alice" GETs /generation for it
    Then attempts[0] is { n 1, node "n-1", model "qwen3-32b", status 429, retry_after_s 15, void_reason "upstream-throttled" }
    And attempts[1] is { n 2, node "n-2", model "qwen3-32b", status 200 }
    And served.node is "n-2"
    And receipt is the second attempt's receipt
    And cost is the second attempt's settled cost

  Scenario: Every attempt carries its own duration
    Given "n-1" takes 800 ms to answer 502 and "n-2" takes 1200 ms to serve
    And "alice" makes a request that fails over from "n-1" to "n-2"
    When "alice" GETs /generation for it
    Then attempts[0].duration_ms is about 800
    And attempts[1].duration_ms is about 1200
    And latency_ms is about 2000

  Scenario: A model-fallback relay names each attempt's model
    Given no station is on air for "qwen3-235b"
    And "alice" makes a request with model "qwen3-235b" and models ["qwen3-32b"] served by "n-1"
    When "alice" GETs /generation for it
    Then model_requested is "qwen3-235b"
    And models is ["qwen3-235b", "qwen3-32b"]
    And attempts has exactly one entry
    And attempts[0] is { n 1, model "qwen3-32b", node "n-1", status 200 }
    And served.model is "qwen3-32b"
    # the skipped "qwen3-235b" is not a synthetic attempt: nothing was dispatched (contract §3)

  @slice5
  Scenario: A key-funded request records key_id, and the consumer view carries the key state
    # features/relay/key_limits.feature owns the semantics; this pins the record shape.
    Given "alice" holds key "key_a1" with limit_usd 5.00 reset monthly
    And "alice" makes a request for "qwen3-32b" funded by "key_a1" served by "n-1"
    When "alice" GETs /generation for it
    Then key_id is "key_a1"
    And key_limit is 5.00
    And key_spend_after equals the key's spend in the window after this settle
    When "carol", the owner of "n-1", GETs /generation for it as the payout owner
    Then key_id is "key_a1"
    And the record has no key_limit and no key_spend_after

  Scenario: A wallet-funded request records key_id null
    Given "alice" makes a request for "qwen3-32b" from her wallet served by "n-1"
    When "alice" GETs /generation for it
    Then key_id is null

  Scenario: A 503 no_match has a record with zero station attempts
    Given "alice" makes a request for "qwen3-32b" with X-Roger-Min-TPS "999"
    When "alice" GETs /generation for it
    Then the status is 200
    And status is 503 and error_code is "no_match"
    And served is null and receipt is null
    And cost is 0
    And attempts is []

  Scenario: A 503 band_cooling records the Retry-After it gave
    Given "n-1" and "n-2" are both cooling for 20 more seconds
    And "alice" makes a request for "qwen3-32b"
    When "alice" GETs /generation for it
    Then status is 503, error_code is "band_cooling"
    And retry_after_s is 20
    And attempts is []

  Scenario: A moderation rejection has a record with the verdict and no attempts
    Given moderation mode is sync
    And "alice" makes a request the screener rejects with 451
    When "alice" GETs /generation for it
    Then status is 451
    And moderation is { mode "sync", verdict "rejected", latency_ms <the screen time> }
    And attempts is [] and served is null and cost is 0
    And the record contains no category beyond "rejected"

  Scenario: An async moderation verdict that landed after serving is recorded
    Given moderation mode is async
    And "alice" makes a request that is served and later flagged by the off-path screener
    When "alice" GETs /generation for it
    Then moderation.mode is "async"
    And moderation.verdict is "flagged_after_serve"
    And served and cost are unchanged by the verdict

  Scenario: A 400 routing error still leaves a record
    Given "alice" makes a request with provider.sort "price" and roger.pref "cheap"
    When "alice" GETs /generation for it
    Then status is 400 and error_code is "conflicting_routing_keys"
    And attempts is []

  Scenario: A 402 before dispatch leaves a record with no attempts
    Given "alice"'s wallet cannot cover the cheapest hold
    And "alice" makes a request for "qwen3-32b"
    When "alice" GETs /generation for it
    Then status is 402 and attempts is [] and cost is 0

  Scenario: A cancelled stream shows cancelled true and the settled cost
    Given "alice" disconnects mid-stream and the settle still bills $0.000100
    When "alice" GETs /generation for it
    Then cancelled is true
    And cost is 0.000100
    And receipt is present

  Scenario: A voided attempt names its void_reason and upstream status
    Given "n-1" returns 200 with empty output and "n-2" serves
    And "alice" makes a request for "qwen3-32b"
    When "alice" GETs /generation for it
    Then attempts[0].void_reason is "empty-output"
    And attempts[0].status is 200
    And attempts[1].status is 200 and has no void_reason

  Scenario: A bridge-served request names the relay
    Given no direct node is on air for "gemma-3-27b"
    And an approved Tower "tw-1" serves "gemma-3-27b"
    And "alice" makes a request for "gemma-3-27b" served through the bridge
    When "alice" GETs /generation for it
    Then served.relay is "tw-1"
    And attempts[0].node names the relay as X-RogerAI-Provider does on the bridge path

  Scenario: A grant-funded request records the grant's pricing outcome without the grant token
    Given owner "bob" minted a free grant for "qwen3-32b" on his node "n-1"
    And a bearer of that grant makes a request served by "n-1"
    When the bearer GETs /generation for it
    Then cost is 0
    And the record contains no grant token and no wallet

  Scenario: The receipt in the record verifies under both signatures
    Given "alice" makes a request served by "n-1"
    When "alice" GETs /generation for it
    Then the receipt's node signature verifies against "n-1"'s key
    And the broker signature verifies with coverage of the billed counts

  Scenario: The record's cost equals the ledger debit
    Given "alice" makes a request served by "n-1" at $0.000420
    When "alice" GETs /generation for it
    Then cost equals the ledger's debit for the request id
    And cost equals the X-RogerAI-Cost header the response carried

  # --- scoping -----------------------------------------------------------------------

  Scenario: The signed wallet that made the request can read it
    Given "alice" made a request served by "n-1" with a signed wallet
    When "alice" GETs /generation for it with the same signed identity
    Then the status is 200

  Scenario: A browser session identity that made the request can read it
    Given a Playbox session made a request served by "n-1"
    When the same browser session GETs /generation for it
    Then the status is 200

  Scenario: The grant token that made the request can read it
    Given a bearer of grant G made a request served by "n-1"
    When the same bearer GETs /generation for it with Authorization Bearer G
    Then the status is 200

  Scenario: A different grant token of the same owner cannot read it
    Given grants G1 and G2 minted by "bob"
    And a bearer of G1 made a request served by "n-1"
    When a bearer of G2 GETs /generation for it
    Then the status is 404

  Scenario: The owner who minted the grant reads the owner view
    Given owner "bob" minted grant G and a bearer of G made a request served by "bob"'s node
    When "bob" GETs /generation for it as the payout owner
    Then the status is 200
    And the record has no balance and no wallet
    And attempts and cost are present

  Scenario: The owner of a station that SERVED reads the owner view
    Given "alice" made a request served by "n-1", owned by "carol"
    When "carol" GETs /generation for it as the payout owner
    Then the status is 200
    And the record has no balance and no wallet
    And served.node is "n-1"
    And the record has no receipt
    And the record has no key_limit and no key_spend_after

  Scenario: The owner of a station that was only TRIED sees only their own attempt
    # A tried owner must not learn who served: the receipt's `user` is the per-(user, node)
    # pseudonym and the cross-provider correlation tunnel.go:2082-2085 forbids is exactly
    # "carol sees dave's node". The owner view is the reader's own attempt(s), nothing else.
    Given "n-1" (owned by "carol") answered 429 and "n-2" (owned by "dave") served "alice"
    When "carol" GETs /generation for it as the payout owner
    Then the status is 200
    And attempts has exactly one entry, carol's: { n 1, node "n-1", status 429, void_reason "upstream-throttled" }
    And served is absent from the body
    And the body does not contain "n-2"
    And the record has no receipt, no balance and no wallet

  Scenario: The owner whose station served sees only their own attempt, not the tried station
    Given "n-1" (owned by "carol") answered 429 and "n-2" (owned by "dave") served "alice"
    When "dave" GETs /generation for it as the payout owner
    Then attempts has exactly one entry, dave's: { n 2, node "n-2", status 200 }
    And the body does not contain "n-1"
    And served.node is "n-2"

  Scenario: The consumer view lists every attempt and carries the receipt
    Given "n-1" (owned by "carol") answered 429 and "n-2" (owned by "dave") served "alice"
    When "alice" GETs /generation for it
    Then attempts lists both attempts in order
    And receipt is present and verifies

  Scenario: Another consumer gets a uniform 404
    Given "alice" made a request served by "n-1"
    When logged-in consumer "eve" GETs /generation for it
    Then the status is 404
    And the body equals the body for an unknown id

  Scenario: An unrelated owner gets a uniform 404
    Given "alice" made a request served by "n-1"
    When owner "frank", who owns no station involved, GETs /generation for it
    Then the status is 404

  Scenario: An anonymous caller gets a uniform 404, not a 401 that confirms the id
    Given "alice" made a request served by "n-1"
    When an unauthenticated caller GETs /generation for it
    Then the status is 404
    And the body equals the body for an unknown id

  Scenario: An unknown id returns the same 404 as a foreign id
    When "alice" GETs /generation?id=0123456789abcdef
    Then the status is 404
    And the response body and headers equal those of the foreign-id case
    And the lookup ran the same store read and the same scope comparison as the foreign-id case (constant-work structure, no early return on a miss)

  Scenario Outline: A malformed id is a 400 before any lookup
    When "alice" GETs /generation?id=<id>
    Then the status is 400
    And error.code is "invalid_request_id"
    And no store read happened

    Examples:
      | id                         |
      |                            |
      | abc                        |
      | 0123456789ABCDEF           |
      | 0123456789abcdef0          |
      | 0123456789abcdef-2         |
      | ../../etc/passwd           |
      | 0123456789abcdeg           |
      | %00                        |

  Scenario: An attempt id is not a request id
    Given "alice" made a request <id> that failed over once
    When "alice" GETs /generation?id=<id>-2
    Then the status is 400
    And error.code is "invalid_request_id"

  Scenario: A missing id parameter is a 400
    When "alice" GETs /generation with no id
    Then the status is 400

  Scenario: Only GET is served
    When "alice" POSTs /generation?id=<a valid id>
    Then the status is 405

  # --- privacy: what is never in the record -----------------------------------------

  Scenario: The record never contains message text
    Given "alice" made a request whose prompt contains "PROMPT-MARKER" and whose completion contains "REPLY-MARKER"
    When "alice" GETs /generation for it
    Then the response body contains neither marker

  Scenario: The record never contains a band code
    Given "alice" made a request on a private band by its code
    When "alice" GETs /generation for it
    Then the response body does not contain the code
    And the record does not say the request was on a private band beyond served.node

  Scenario: The record never contains a station's address
    Given "n-1" registered from a known IP
    When "alice" GETs /generation for a request "n-1" served
    Then the response body does not contain that IP

  Scenario: The owner view never contains the consumer's wallet, pseudonym or receipt
    Given "alice" made a request served by "carol"'s node
    When "carol" GETs /generation for it as the payout owner
    Then the body contains neither "alice"'s wallet nor her pubkey
    And the body contains no pseudonym
    And the body contains no receipt (the receipt's user field is the pseudonym)

  Scenario: The consumer view never contains another consumer's data
    Given "alice" and "eve" each made a request served by "n-1"
    When "alice" GETs /generation for her request
    Then the body contains nothing from "eve"'s request

  # --- retention, caching, limits -------------------------------------------------------

  Scenario: A request older than the lineage the console can read is 404
    Given a request id whose lineage the console feed no longer holds
    When "alice" GETs /generation for it
    Then the status is 404

  Scenario: After account deletion the records are unreadable
    Given "alice" made a request served by "n-1"
    And "alice" deletes her account
    When the anonymized identity GETs /generation for it
    Then the status is 404
    And the anonymized ledger row still exists for the legal window

  Scenario: The lookup is rate-limited like /console
    Given "alice" exhausts the per-identity limiter on /generation
    When "alice" GETs /generation once more
    Then the status is 429
    And Retry-After is set

  Scenario: The lookup is cached per identity for the authed window, never across identities
    Given "alice" GETs /generation for her request and it is cached
    When "eve" GETs /generation for the same id
    Then the status is 404
    And "eve" did not receive "alice"'s cached body

  Scenario: A record is readable the moment the relay's response ends
    Given "alice" makes a non-streaming request served by "n-1"
    When "alice" GETs /generation for it immediately after the response
    Then the status is 200

  Scenario: A record for an in-flight request is 404 until it ends
    Given "alice"'s streaming request is still in flight
    When "alice" GETs /generation for it
    Then the status is 404
    When the stream ends
    And "alice" GETs /generation for it
    Then the status is 200

  # --- multi-instance ----------------------------------------------------------------------

  Scenario: A lookup on another instance finds a request served on this one
    Given a two-instance broker sharing a store
    And "alice"'s request was served on instance A
    When "alice" GETs /generation for it on instance B
    Then the status is 200
    And the record equals the one instance A returns

  # deferred 2026-10-02 (founder-approved): needs stations polling two instances; the harness cannot build it yet
  @later
  Scenario: A failover that crossed instances still lists every attempt in order
    Given a two-instance broker
    And attempt 1 ran on instance A and attempt 2 on instance B
    When "alice" GETs /generation for it
    Then attempts lists n 1 then n 2 with their nodes and statuses

  Scenario: With the shared store unreachable the lookup fails closed
    Given the shared store is unreachable
    When "alice" GETs /generation for a real request id
    Then the status is 503
    And no partial record is returned

  # --- clients --------------------------------------------------------------------------------

  @cli
  Scenario: `roger generation <id>` prints the record
    Given "alice" made a request served by "n-1" that failed over from "n-2"
    When the operator runs `roger generation <id>`
    Then the output shows the served station, model, cost, tokens, tps
    And lists each attempt with its status and duration
    And says the receipt verifies

  @cli
  Scenario: `roger generation --last` looks up the most recent request of this session
    Given the local proxy just relayed a request
    When the operator runs `roger generation --last`
    Then the record for that request id is printed

  @cli
  Scenario: `roger generation` with a malformed id fails locally
    When the operator runs `roger generation nope`
    Then the command exits non-zero without contacting the broker

  @cli
  Scenario: `roger generation --json` prints the raw record
    When the operator runs `roger generation <id> --json`
    Then stdout is the broker's JSON body unchanged

  @tui
  Scenario: The TUI last-request pane reads the record
    Given the TUI is tuned to "qwen3-32b" and a turn just settled
    When the operator opens the last-request pane
    Then it shows served station, cost, tokens, tps, ttft, and each attempt
    And it reads them from /generation, not from a local guess

  @web
  Scenario: The Playbox request inspector reads the record for its own session
    Given a Playbox session made a request
    When the inspector opens that request
    Then it shows the served station, cost and attempts from /generation

  @docs
  Scenario: OpenAPI documents /generation
    Then openapi.yaml has GET /generation with the id query param
    And documents the 200 record schema, 400, 404, 429 and 503

  # --- telemetry and invariants ----------------------------------------------------------------

  Scenario: Writing the record does not change what is billed
    Given "alice" makes a request served by "n-1" at $0.000420
    Then the ledger debit is $0.000420 whether or not the record write succeeded

  Scenario: A record write failure is logged and never fails the relay
    Given the record store refuses writes
    When "alice" makes a request served by "n-1"
    Then the response is 200 with the full meter headers
    And a log line names the record write failure

  Scenario: The lookup counts on /admin/live
    When "alice" GETs /generation for a request three times
    Then /admin/live shows generation_lookups 3

  Scenario: The record does not count as a relay for the operator's stats
    When "alice" GETs /generation for a request
    Then "n-1"'s served count and earnings are unchanged
