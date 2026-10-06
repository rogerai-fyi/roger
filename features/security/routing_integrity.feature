# ROUTING INTEGRITY (slice 6, audit items #2, #12, #13, #19, #21): the measurements routing
# trusts cannot be gamed by a station recognizing probes; a routing object cannot buy
# unbounded broker work; consumers cannot be linked across stations; Tower streams behave
# like direct ones; self-declared attributes are labeled as such.
#
# GROUND TRUTH (routing-slice6 at b1754466):
#   #2  Probes are labeled: the liveness/quality canary sends Job{User: "probe"}
#       (cmd/rogerai-broker/probe.go:702) and the tool-call canary sends Job{User: "probe"}
#       (toolcall.go:391), while real jobs carry b.pseudonym(user, node) = "u_" + 16 hex
#       (tunnel.go:2839, main.go:1212). A station can serve probes from the real model and
#       customers from something cheaper, and every trust/capability/latency filter
#       (verifiedFreshLocked, needTools, max_ttft_ms, sort:latency; netfilters.go:57-90) rests
#       on the probe.
#   #12 Picks are unbounded per request: pickModel re-picks once per cap-dropped station
#       (tunnel.go ~2230 loop), each pass can run up to 32 ordered pickFor calls under b.mu and
#       metricsMu; estimateMaxCost re-parses the body per candidate.
#   #13 Stations receive the broker request id: job.ID = attemptID(requestID, n), which is the
#       request id itself for attempt 1 and "<id>-<n>" after (cooling.go:96-100). Two stations on
#       one failover chain see the same id prefix and can link the per-(user, node) pseudonyms
#       they each hold. The /generation owner view still carries key_id (slice 5), models and the
#       moderation verdict (genrecord.go owner view), and /generation draws from the relay rate
#       bucket (genrecord.go:396 uses b.rl).
#   #19 Bridged (Tower) stream answers arrive whole and are written as one delta frame
#       (tunnel.go bridged stream branch); nothing is sent while waiting; sort:latency ranks a
#       Tower row by its node's TTFT although the consumer waits for the whole answer.
#   #21 region, quant and params_b are self-declared (region also free-form on registration) and
#       act as hard filters (netfilters.go); the broker already computes a coarse network bucket
#       per station address (coarseNetBucket, tunnel.go:5007) but never compares it to region.
#
# RULES (contract draft B §14.B7):
#   #2  Probe and canary jobs carry a pseudonym produced by the same derivation as real users
#       (pseudonym(<probe identity>, node)), indistinguishable in shape from a real pseudonym;
#       canary prompts are drawn from a rotating pool of realistic prompts (no fixed sentinel
#       text, the nonce embedded naturally); probes use the same request shape as real traffic
#       for the model (stream when the model's traffic streams). Shadow canaries: a sampled share
#       of probe prompts mirror the SHAPE (length band, tools present or not, stream or not) of
#       recent organic traffic for that model. `verified` is withdrawn (not only aged out) when
#       organic evidence contradicts it: K recount strikes or an organic success rate below the
#       Tier-A bar within the window, while the probe still passes (knobs: K=3, window 1h).
#   #12 A per-request pick budget: at most 64 pickFor invocations per relay (ROGERAI_PICK_BUDGET,
#       default 64); exceeding it answers 503 no_match with code routing_budget_exceeded (no
#       dispatch, no hold). Per-request cap drops are computed in one pass inside the pick (a
#       station the cap buys nothing at is filtered like any other ineligible station). The body
#       is parsed once per request; per-candidate cost estimates reuse the parsed token counts.
#   #13 The job id a station receives is an unlinkable per-attempt id:
#       "att_" + hex(HMAC-SHA256(broker secret, request id || ":" || n))[:24]. The broker keeps
#       the mapping for settlement; receipts bind to it as today. The /generation owner view
#       drops key_id, models and moderation. /generation has its own rate bucket (separate from
#       the relay bucket; same per-identity limits as /console).
#   #19 While a bridged stream attempt waits for the Tower's answer, the broker writes an SSE
#       keepalive comment (`: rogerai keepalive`) every 10 s after headers are committed
#       (knob); sort:latency ranks a bridged row on its measured TOTAL latency (time to the whole
#       answer), not TTFT; a bridged stream attempt gets the same deadline as a direct stream
#       attempt (no zero deadline).
#   #21 /discover offers and the /v1/models rogerai block carry
#       `attribute_sources: {region: "declared", quant: "declared", params_b: "declared"|
#       "estimated", ctx: "declared"|"estimated", tools: "verified", vision: "declared",
#       tps: "measured", ttft: "measured"}`. Region cross-check: when a station's coarse network
#       bucket maps to a known continent and its declared region names a different one, the
#       station is ineligible under `roger.region`, listed in /admin/live region_mismatch, and
#       /discover marks its region "contradicted". Docs say region is not a data-residency
#       guarantee.
#
# SUPERSEDES: none of the approved scenarios assert the job id equals the request id or that
# probes carry user "probe"; ROUTING-EXPRESSION-CONTRACT.md §7 ("attempts ... node, model") is
# unaffected (the broker maps the per-attempt id back). The /generation owner-view field list in
# §7 / features/routing/generation_lookup.feature (owner view) loses key_id: that scenario set
# must be updated alongside.
#
# Enforced by: cmd/rogerai-broker/routing_integrity_bdd_test.go

Feature: Routing measurements cannot be gamed, picks are bounded, and consumers stay unlinkable

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And station "s1" is on air for "m" at in $0.10 out $0.30 per 1M
    And "u-1" is a funded consumer

  # --- #2 probes look like customers ----------------------------------------------------

  Scenario: A liveness probe job carries a pseudonym shaped like a real user's
    When the probe sends its canary to "s1"
    Then the job's user is not "probe"
    And the job's user matches the pattern of a real pseudonym "u_" followed by 16 hex characters

  Scenario: A tool-call canary carries a pseudonym shaped like a real user's
    When the tool-call canary is sent to "s1" for "m"
    Then the job's user is not "probe"
    And it matches the pattern of a real pseudonym

  Scenario: The probe pseudonym differs per station, like a real user's
    Given station "s2" is on air for "m"
    When the probe sends its canary to "s1" and to "s2"
    Then the two jobs carry different pseudonyms

  # founder ruling 2026-10-06: the canary identity rotates per probe, so a station sees no stable
  # probe user to recognise
  Scenario: Two canaries to the same station carry different pseudonyms
    When the probe sends 2 canaries to "s1"
    Then every canary carried a different pseudonym

  # founder ruling 2026-10-06
  Scenario: A canary pseudonym never repeats across stations
    Given station "s2" is on air for "m"
    When the probe sends 5 canaries to "s1" and 5 canaries to "s2"
    Then every canary carried a different pseudonym
    And every canary pseudonym matches the pattern of a real pseudonym

  Scenario: Canary prompts rotate and contain no fixed sentinel text
    When the probe sends 20 canaries to "s1"
    Then at least 5 distinct prompts were used
    And no prompt contains a fixed marker string shared by all of them

  Scenario: A probe for a model whose traffic streams is sent as a stream
    Given 90% of recent organic traffic for "m" was stream:true
    When the probe sends its canary to "s1"
    Then the canary job's body has stream true

  # founder ruling 2026-10-05: a streamed canary is read through the stream and graded like a
  # plain one, so a station whose traffic streams is never failed for streaming its answer.
  Scenario: A station that answers a streamed canary earns verified from it
    Given 90% of recent organic traffic for "m" was stream:true
    And "s1" has not been verified yet
    When the probe sends its canary to "s1"
    Then the canary job's body has stream true
    And "s1" earned verified from that canary

  # slice-6 audit 2026-10-06: no fixed instruction sentence marks a canary
  Scenario: Canaries phrase their challenge many ways
    When the probe sends 20 canaries to "s1"
    Then at least 8 distinct instruction phrasings were used

  # slice-6 audit 2026-10-06: a canary's sampling parameters follow the model's traffic
  Scenario: Canaries take temperature and max_tokens from organic traffic
    Given recent organic traffic for "m" uses temperature 0.7 and max_tokens 1000
    When the probe sends 20 canaries to "s1"
    Then some canaries carry temperature 0.7 and max_tokens 1000

  Scenario: Shadow canaries mirror the shape of organic traffic
    Given recent organic traffic for "m" carries tools in 60% of requests with prompts of 2000 to 8000 tokens
    When the probe sends 50 canaries to "s1"
    Then some canaries carry a tools array
    And some canary prompts fall in the 2000 to 8000 token band

  Scenario: A station that serves probes well but customers badly loses verified
    Given "s1" passes every canary
    And "s1" accrues 3 recount strikes from organic relays within 1 hour
    When routing evaluates trust_min "verified" for "s1"
    Then "s1" is not verified
    And /discover shows "s1" verified false

  Scenario: Organic success below the Tier-A bar withdraws verified despite passing probes
    Given "s1" passes every canary
    And "s1"'s organic success rate over the last hour is 0.30
    When routing evaluates trust_min "verified" for "s1"
    Then "s1" is not verified

  Scenario: Verified returns once organic evidence recovers
    Given "s1" lost verified because of organic contradictions
    And the contradiction window passes with clean organic relays
    When the next canary passes
    Then "s1" is verified again

  Scenario: Probe identities never get billed or appear in a consumer's lineage
    When the probe sends its canary to "s1"
    Then no wallet was debited
    And no consumer's /console lineage shows the canary

  # --- #12 bounded work per request ----------------------------------------------------

  Scenario: A per-request cap that drops every station is answered within the pick budget
    Given 40 stations are on air for "m"
    When "u-1" relays for "m" with provider.max_price.request 0.000000000001
    Then the response is 503
    And the routing pass made at most 64 pickFor calls

  Scenario: Exceeding the pick budget is 503 routing_budget_exceeded with no dispatch and no hold
    Given ROGERAI_PICK_BUDGET is "4"
    And 10 stations are on air for "m", each excluded by the per-request cap only after being picked
    When "u-1" relays for "m" with a per-request cap and models ["m", "m2", "m3", "m4", "m5"]
    Then the response is 503 with error code "routing_budget_exceeded"
    And no hold was placed
    And no station received anything

  Scenario: Cap drops are evaluated in the pick, not by re-picking once per dropped station
    Given 40 stations are on air for "m" and 39 are dropped by the per-request cap
    When "u-1" relays for "m" with that per-request cap
    Then the response is 200 from the one station the cap affords
    And the routing pass made at most 5 pickFor calls

  Scenario: The body is parsed once per request regardless of plan size
    Given 40 stations are on air for "m"
    When "u-1" relays for "m" with a 2 MiB body
    Then the request body was JSON-decoded at most twice (routing object and token estimate)

  Scenario: A normal request never comes near the pick budget
    When "u-1" relays for "m"
    Then the routing pass made at most 3 pickFor calls

  Scenario Outline: The pick budget is a broker knob
    Given ROGERAI_PICK_BUDGET is "<n>"
    When "u-1" relays for "m"
    Then the response is 200

    Examples:
      | n   |
      | 8   |
      | 64  |
      | 256 |

  # --- #13 unlinkable ids and a lean owner view -----------------------------------------

  Scenario: A station never receives the broker request id
    When "u-1" relays for "m" and the response carries X-RogerAI-Request-Id "R"
    Then the job id "s1" received is not "R" and does not contain "R"

  # corrected 2026-10-05 (founder-approved): random ids share a hex char 1 in 16
  Scenario: Two stations on one failover chain cannot link their job ids
    Given station "s2" is on air for "m"
    And "s1" answers the next request with an upstream 429
    When "u-1" relays for "m" and "s2" serves
    Then the job ids received by "s1" and "s2" share no common prefix longer than "att_" plus 4 hex characters
    And neither job id is derivable from the other without the broker secret

  # founder ruling 2026-10-05: the receipt names the attempt, and the response says which
  # attempt that is, so a consumer can tie its receipt to its X-RogerAI-Request-Id.
  Scenario: The response names the attempt that served in X-RogerAI-Attempt-Id
    When "u-1" relays for "m" and the response carries X-RogerAI-Request-Id "R"
    Then the response's X-RogerAI-Attempt-Id is the job id "s1" received
    And the receipt in X-RogerAI-Receipt names that attempt id

  # founder ruling 2026-10-05
  Scenario: After a failover the response names the attempt that served
    Given station "s2" is on air for "m"
    And "s1" answers the next request with an upstream 429
    When "u-1" relays for "m" and "s2" serves
    Then the response's X-RogerAI-Attempt-Id is the job id "s2" received

  # founder ruling 2026-10-05
  Scenario: A streamed response names the attempt that served
    When "u-1" streams for "m"
    Then the response's X-RogerAI-Attempt-Id is the job id "s1" received

  # founder ruling 2026-10-05: the consumer's ledger ties each attempt back to its request; a
  # station owner's never does (that would re-link the attempts the per-attempt id separates).
  Scenario: The consumer's lineage row carries the request id beside the attempt id
    When "u-1" relays for "m" and the response carries X-RogerAI-Request-Id "R"
    Then the consumer's lineage row for the job id "s1" received carries request id "R"
    And the owner's lineage row for that job id carries no request id

  Scenario: The per-attempt id is stable for the same request and attempt
    When the broker derives the job id for request "R" attempt 2 twice
    Then both derivations are equal

  Scenario: Receipts still bind to the dispatched job and settle normally
    When "u-1" relays for "m"
    Then the receipt binds to the per-attempt job id "s1" received
    And the settle records the request id "R" for the consumer

  Scenario: The /generation owner view carries no key id, no model list and no moderation verdict
    Given "u-1" relayed for "m" through key "k1" and "s1" served
    When the owner of "s1" GETs /generation for that request
    Then the record has no "key_id", no "models" and no "moderation"
    And it shows only the owner's own attempt

  Scenario: The /generation consumer view is unchanged
    Given "u-1" relayed for "m" and "s1" served
    When "u-1" GETs /generation for that request
    Then the record carries "models" and "moderation"

  Scenario: /generation has its own rate bucket
    Given "u-1" has exhausted its relay rate bucket
    When "u-1" GETs /generation for an earlier request
    Then the response is 200

  Scenario: Exhausting the /generation bucket does not block relays
    Given "u-1" has exhausted its /generation rate bucket
    When "u-1" relays for "m"
    Then the response is 200

  # --- #19 Tower streams --------------------------------------------------------------

  Scenario: A bridged stream sends keepalive comments while waiting for the Tower
    Given an approved Tower "t1" serves "m" and takes 25 seconds to answer
    When "u-1" streams for "m" with provider.order ["t1"]
    Then at least 2 ": rogerai keepalive" comments arrive before the answer
    And the answer, the usage chunk and "[DONE]" follow in order

  # slice-6 audit 2026-10-06: the broker writes a bridged answer whole, so the stream is complete
  # without a station [DONE] and carries no error frame
  Scenario: A bridged stream that answered carries no error frame
    Given an approved Tower "t1" serves "m" and takes 0 seconds to answer
    When "u-1" streams for "m" with provider.order ["t1"]
    Then the stream carries no error frame
    And the answer, the usage chunk and "[DONE]" follow in order

  # slice-6 audit 2026-10-06: a bridged attempt that fails after a keepalive committed the headers
  # still ends the stream the broker's way
  Scenario: A bridged stream that fails after a keepalive ends with an error frame, a usage chunk and [DONE]
    Given an approved Tower "t1" serves "m", sends keepalives, then fails with 503
    When "u-1" streams for "m" with provider.order ["t1"] and no fallbacks
    Then the stream ends with an error frame, a usage chunk and "[DONE]"

  Scenario: Keepalives do not count as content for failover purposes
    Given an approved Tower "t1" serves "m", sends keepalives, then fails with 503
    And direct station "s1" is on air for "m"
    When "u-1" streams for "m" with provider.order ["t1", "s1"]
    Then the stream fails over to "s1" before any content frame

  Scenario: sort latency ranks a bridged row on its total latency
    Given an approved Tower "t1" serves "m" with TTFT 100 ms and total latency 9 s
    And direct station "s1" has TTFT 400 ms and total latency 2 s
    When "u-1" relays for "m" with provider.sort "latency"
    Then the plan head is "s1"

  # slice-6 review 2026-10-06 (M3): only an answer that passes the quality gate is a latency
  # sample, so instant junk cannot buy the head of a latency sort
  Scenario: A station answering instantly with output it claims no tokens for never earns a total latency
    Given station "s2" is on air for "m"
    And "s2" answers every request instantly with text but claims no completion tokens
    When "u-1" relays for "m" pinned to "s2" 5 times
    Then "s2" has no measured total latency

  # founder ruling 2026-10-05: sort:latency means total latency for every row, direct ones too
  Scenario: sort latency ranks direct stations on total latency, not first token
    Given station "s2" is on air for "m"
    And direct station "s1" has TTFT 100 ms and total latency 9 s
    And direct station "s2" has TTFT 400 ms and total latency 2 s
    When "u-1" relays for "m" with provider.sort "latency"
    Then the plan head is "s2"

  # founder ruling 2026-10-05: with no total latency measured yet, TTFT stands in
  Scenario: sort latency falls back to first token where no total latency is measured
    Given station "s2" is on air for "m"
    And direct station "s1" has TTFT 100 ms and no total latency
    And direct station "s2" has TTFT 400 ms and no total latency
    When "u-1" relays for "m" with provider.sort "latency"
    Then the plan head is "s1"

  Scenario: A bridged stream attempt gets the same deadline as a direct stream attempt
    Given an approved Tower "t1" serves "m" and answers in 30 seconds
    When "u-1" streams for "m" with provider.order ["t1"]
    Then the stream is served through "t1"
    And the bridged attempt's deadline equals a direct stream attempt's

  # --- #21 declared attributes ---------------------------------------------------------

  Scenario: /discover labels where each attribute comes from
    Given "s1" declares region "eu", quant "Q8_0", params_b 32 and a context window of 32768
    When a consumer GETs /discover
    Then the "s1" offer has attribute_sources region "declared", quant "declared", params_b "declared", ctx "declared", tps "measured", ttft "measured"

  Scenario: An estimated parameter count is labeled estimated
    Given station "s2" is on air for "qwen3-32b" with no declared params_b
    When a consumer GETs /discover
    Then the "s2" offer has attribute_sources params_b "estimated"

  Scenario: Tools is labeled verified, vision declared
    Given "s1" earned verified "tools" for "m" and declares "vision"
    When a consumer GETs /discover
    Then the "s1" offer has attribute_sources tools "verified" and vision "declared"

  Scenario: The /v1/models rogerai block carries the same labels
    When a consumer GETs /v1/models
    Then the "m" entry's rogerai block carries attribute_sources

  Scenario: A region contradicted by the station's network makes it ineligible under region
    Given "s1" declares region "eu"
    And "s1"'s address falls in a network bucket that maps to North America
    When "u-1" relays for "m" with roger.region ["eu"]
    Then "s1" is not a candidate
    And /admin/live region_mismatch lists "s1"
    And /discover marks "s1"'s region "contradicted"

  # founder ruling 2026-10-05: the network table is operator-supplied; with none configured the
  # check fails open and region stays declared only.
  Scenario: With no network table configured a declared region is never contradicted
    Given no network table is configured
    And "s1" declares region "eu"
    And "s1"'s address falls in a network bucket that maps to North America
    When "u-1" relays for "m" with roger.region ["eu"]
    Then "s1" is a candidate
    And "s1"'s region is not marked "contradicted"
    And /admin/live region_mismatch lists no station

  Scenario: A region consistent with the network stays eligible
    Given "s1" declares region "eu" and its address maps to Europe
    When "u-1" relays for "m" with roger.region ["eu"]
    Then "s1" is a candidate

  Scenario: An address with no continent mapping does not contradict any region
    Given "s1" declares region "eu" and its address maps to no known continent
    When "u-1" relays for "m" with roger.region ["eu"]
    Then "s1" is a candidate

  Scenario: A contradicted region does not affect requests without a region filter
    Given "s1"'s declared region is contradicted by its network
    When "u-1" relays for "m"
    Then "s1" is a candidate

  Scenario: A curated station's region is its provider name and is never contradicted
    Given curated station "c1" whose region is its provider "upstream-co"
    When a consumer GETs /discover
    Then "c1"'s region is not marked "contradicted"

  Scenario: The station's address itself is never exposed by the region check
    Given "s1"'s declared region is contradicted by its network
    When a consumer GETs /discover and /admin/live
    Then neither shows "s1"'s IP address or network bucket

  @docs
  Scenario: The docs say region is not a data-residency guarantee
    Then the manual's routing section states that roger.region is self-declared and not a data-residency guarantee
