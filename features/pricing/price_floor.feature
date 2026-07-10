# GLOBAL PRICE FLOOR: the symmetric twin of the price CEILING. Every price and every token
# count that feeds the cost multiplication must be bounded BELOW (>= 0, and finite), because
# settle computes `cost = tokens * price / 1e6` and Finalize does `wallet += held - cost` -
# so a NEGATIVE cost (from a negative price OR a negative token count) MINTS spendable credit
# into a wallet. The codebase already bounds these values ABOVE (registerPriceCeiling, the
# cost > maxCost clamp, the recount that only LOWERS a token claim); this file pins the
# missing lower bound.
#
# WHY THIS FILE EXISTS (2026-07-08 adversarial sweep, siblings of the grant-price mint):
#   GRANT price (fixed): features/grants/grant_price_safety.feature.
#   #1 REGISTER price (this file): cmd/rogerai-broker/tunnel.go register runs ONLY
#     registerPriceCeiling (upper). A negative price passes; worse, offersPriced tests `> 0`
#     so a negative price is not even "priced" -> the login-to-monetize gate (tunnel.go)
#     does not fire -> an ANONYMOUS node can register it. pickFor prefers the "cheapest",
#     routing a consumer to it; CostWith2 (internal/protocol/protocol.go) returns a negative
#     cost; the cost > maxCost clamp is upper-only; Finalize (internal/store/store.go) mints
#     to the CONSUMER's wallet. This is the public, consumer-facing, anonymous variant.
#   #2 TOKEN counts (this file): billedTokens (internal/store/store.go) sets
#     promptTok = rec.PromptTokens (node-supplied int) and the broker recount only replaces
#     it when strictly LOWER - never floors a negative. A node-signed receipt claiming a
#     negative completion/prompt count yields a negative cost and mints the same way. The
#     void gate (recount.go) checks completion TEXT, not the token sign, so it does not stop it.
#
# GROUND TRUTH: cmd/rogerai-broker/tunnel.go (register, settle), pricesafety.go
#   (validateOfferInput = the non-negative guard; registerPriceCeiling = the upper one),
#   internal/protocol/protocol.go (ActivePrice, CostWith2), internal/store/store.go
#   (billedTokens, Finalize: wallet += held - cost).

Feature: Global price floor — no price or token count can be negative (never mint)

  # ---- #1: node registration rejects a negative price (the public sibling of the ceiling) ----

  Rule: A public node registration rejects a negative price

    Scenario: A node registering with a negative output price is rejected
      Given an owner registers a node with output price -1000 per 1M
      When the node is registered as a public station
      Then the registration is rejected with "price cannot be negative"
      And the node does not appear on the market

    Scenario: A node registering with a negative input price is rejected
      Given an owner registers a node with input price -50 per 1M
      When the node is registered as a public station
      Then the registration is rejected with "price cannot be negative"
      And the node does not appear on the market

    Scenario: A negative scheduled-window price is rejected
      Given an owner registers a node whose scheduled window price is -10 per 1M
      When the node is registered as a public station
      Then the registration is rejected with "price cannot be negative"

    Scenario: A negative price is not treated as free and cannot skip the login gate
      Given an anonymous (unsigned) node registers with output price -1000 per 1M
      When the node is registered as a public station
      Then the registration is rejected with "price cannot be negative"
      And no anonymous negative-priced node is on the market

    Scenario: A free (0/0) public registration is still accepted with no login
      Given an anonymous (unsigned) node registers free at 0 per 1M
      When the node is registered as a public station
      Then the registration is accepted

    Scenario: A within-bounds positive price is accepted
      Given an owner registers a node with output price 5 per 1M
      When the node is registered as a public station
      Then the registration is accepted

    # A restart re-hydrates persisted registrations; the same floor must gate that ingest so a
    # pre-fix negative-price row cannot rejoin the market and win cheapest-first routing.
    Scenario: A persisted node with a negative price is dropped on re-hydrate
      Given a persisted node registration carrying a negative output price
      When the broker re-hydrates its node registry
      Then the node does not appear on the market

  # ---- #2: a node-signed receipt with a negative token count never mints ----

  Rule: A settled receipt never bills a negative token count

    Scenario: A receipt claiming negative completion tokens does not mint
      Given a priced offer and a consumer with a starting balance of 10 credits
      When the node returns a receipt claiming -1000000 completion tokens
      Then the settled cost is not negative
      And the consumer balance is not increased by the settle
      And the recorded completion tokens are not negative

    Scenario: A receipt claiming negative prompt tokens does not mint
      Given a priced offer and a consumer with a starting balance of 10 credits
      When the node returns a receipt claiming -1000000 prompt tokens
      Then the settled cost is not negative
      And the consumer balance is not increased by the settle
      And the recorded prompt tokens are not negative

    Scenario: A positive over-report is still recounted DOWN, never up (regression pin)
      Given a priced offer and a broker re-count of 100 completion tokens
      When the node claims 5000 completion tokens
      Then the billed completion tokens are 100

    Scenario: A negative broker re-count is ignored and the claim is used (then floored)
      Given a priced offer and a broker re-count of -5 completion tokens
      When the node claims 100 completion tokens
      Then the billed completion tokens are 100

    Scenario: A receipt with BOTH token axes negative does not mint
      Given a priced offer and a consumer with a starting balance of 10 credits
      When the node returns a receipt claiming -500 prompt and -500 completion tokens
      Then the settled cost is not negative
      And the consumer balance is not increased by the settle
      And the recorded prompt tokens are not negative
      And the recorded completion tokens are not negative

    # The floor is applied at the re-count source, so a negative claim can never leak into the
    # response headers, the logs, or the broker-SIGNED token counts either - not just the ledger.
    Scenario: A negative claimed token count is floored before headers, logs, and the broker signature
      When the relay re-counts a claim of -1000000 completion tokens
      And the relay re-counts a claim of -1000000 prompt tokens
      Then the billed completion count is 0
      And the billed prompt count is 0

  # ---- Defense in depth: the settled cost is bounded below by zero ----

  Rule: Settlement cost is floored at zero

    Scenario: A negative computed cost is floored to zero before it debits a wallet
      Given a consumer with a starting balance of 10 credits
      When a request settles at a computed cost of -500 credits
      Then the settled cost applied is 0
      And the consumer balance is not increased by the settle

    Scenario: A non-finite price never reaches a wallet
      Given a request whose computed cost is not finite
      When the cost is prepared for settlement
      Then the cost applied is 0

    Scenario: A cost above the authorized maximum is still capped at the maximum (upper bound intact)
      Given a consumer with a starting balance of 10 credits
      When a request settles at a computed cost of 5000 credits
      Then the settled cost applied is 1000
