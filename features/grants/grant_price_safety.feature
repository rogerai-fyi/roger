# GRANT PRICE SAFETY: a grant can NEVER carry a negative price. This is a money invariant.
#
# WHY THIS FILE EXISTS (known-vulnerable regression, 2026-07-08 pre-launch audit):
#   The public-market registration path already rejects negative prices via
#   validateOfferInput ("price cannot be negative", cmd/rogerai-broker/pricesafety.go), but the
#   GRANT paths did NOT. grantCreate (cmd/rogerai-broker/grants_http.go) and UpdateGrant
#   (internal/store/grant.go applyPatch, via PATCH /grants/{id}) stored price_in/price_out with
#   no sign check. A custom-priced grant is billed to the OWNER's own unified wallet
#   (resolvePricing -> ownerSponsorWallet, cmd/rogerai-broker/grant.go). At settle, cost =
#   tokens * price and balance += held - cost, so a NEGATIVE price yields a NEGATIVE cost and
#   CREDITS the owner's wallet - minting spendable balance the owner can then spend on OTHER
#   operators' real nodes. This file pins the fix at both the boundary (mint + edit reject) and
#   the billing chokepoint (Grant.GrantPrice never returns a negative), forever.
#
# GROUND TRUTH:
#   - grantCreateReq.PriceIn/PriceOut (grants_http.go): create body.
#   - store.GrantPatch.PriceIn/PriceOut (*float64, nil = leave unchanged): edit body.
#   - store.Grant.GrantPrice() -> (in, out): the single price source every settle path reads.
#   - The public ceiling ($50/$100 per 1M) is enforced SEPARATELY by registerPriceCeiling;
#     this file is only about the negative-price floor.

Feature: Grant price safety — a grant can never carry a negative price

  # ---- Minting (POST /grants) rejects a negative price at the boundary ----

  Rule: Minting a grant rejects a negative price and stores nothing

    Scenario: Mint with a negative output price is rejected
      Given an owner authenticated to manage grants
      When the owner mints a grant with price_in 1 and price_out -5
      Then the request is rejected with "price cannot be negative"
      And no grant is created for that owner

    Scenario: Mint with a negative input price is rejected
      Given an owner authenticated to manage grants
      When the owner mints a grant with price_in -1 and price_out 5
      Then the request is rejected with "price cannot be negative"
      And no grant is created for that owner

    Scenario: Mint with both prices negative is rejected
      Given an owner authenticated to manage grants
      When the owner mints a grant with price_in -2 and price_out -3
      Then the request is rejected with "price cannot be negative"
      And no grant is created for that owner

    Scenario Outline: A non-negative mint price is accepted
      Given an owner authenticated to manage grants
      When the owner mints a grant with price_in <in> and price_out <out>
      Then the grant is created
      And the stored grant has price_in <in> and price_out <out>

      Examples:
        | in | out |
        | 0  | 0   |
        | 0  | 10  |
        | 2  | 3   |

  # ---- Editing (PATCH /grants/{id}) rejects a negative price at the boundary ----

  Rule: Editing a grant rejects a negative price and leaves the stored price unchanged

    Scenario: Patching the output price negative is rejected
      Given an existing grant with price_in 2 and price_out 3
      When the owner patches price_out to -4
      Then the request is rejected with "price cannot be negative"
      And the stored grant still has price_in 2 and price_out 3

    Scenario: Patching the input price negative is rejected
      Given an existing grant with price_in 2 and price_out 3
      When the owner patches price_in to -1
      Then the request is rejected with "price cannot be negative"
      And the stored grant still has price_in 2 and price_out 3

    Scenario: Patching a price to a non-negative value is accepted
      Given an existing grant with price_in 2 and price_out 3
      When the owner patches price_out to 7
      Then the grant is updated
      And the stored grant has price_in 2 and price_out 7

    Scenario: A patch that omits the price fields leaves the price unchanged
      Given an existing grant with price_in 2 and price_out 3
      When the owner patches only revoked to true
      Then the grant is updated
      And the stored grant still has price_in 2 and price_out 3

  # ---- Billing chokepoint: even a negative stored price never bills negative ----

  Rule: Billing never reads a negative grant price (defense in depth)

    Scenario: A grant carrying a negative stored price resolves to a non-negative billing price
      Given a grant whose stored price_in is -2 and price_out is -3
      When the billing price of the grant is read
      Then the billing input price is 0
      And the billing output price is 0

    # Free/Self grants short-circuit to 0/0 in GrantPrice BEFORE the clamp, so a stale negative
    # price on one can never bill negative either - pinned so a refactor of that early return
    # can't quietly reintroduce the mint.
    Scenario: A free grant with a stale negative stored price still bills nothing
      Given a free grant whose stored price_in is -2 and price_out is -3
      When the billing price of the grant is read
      Then the billing input price is 0
      And the billing output price is 0

    Scenario: A self grant with a stale negative stored price still bills nothing
      Given a self grant whose stored price_in is -2 and price_out is -3
      When the billing price of the grant is read
      Then the billing input price is 0
      And the billing output price is 0

    Scenario: A negative-priced grant cannot mint spendable credits for its owner
      Given an owner with a starting balance of 10 credits
      And that owner runs a negative-priced grant against their own node
      When a request settles through that grant
      Then the owner's balance is not increased by the settle
      And the settled cost is never negative
