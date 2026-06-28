# L1 independent token re-count — the broker re-tokenizes the served text with the
# canonical tokenizer for the model and BILLS the lesser of (node claim, broker re-count)
# on BOTH axes, so an over-reporting node cannot inflate its earnings; a sustained
# discrepancy freezes the node's earnings via a recount hold (auto-expiring operator
# recourse). Fourth trust pillar; permanent regression net.
#
# GROUND TRUTH (cmd/rogerai-broker/recount.go + recourse.go + the store):
#   - settleRecount(node, req, model, completion, claimed) -> billed completion tokens:
#     re-tokenize via the sidecar; bill min(claimed, brokerRecount); fold the sample into
#     trust + the promotion-hold flag (observeRecount).
#   - settleRecountPrompt(...) is the input twin PLUS a HARD, fail-closed BYTE FLOOR
#     independent of the sidecar: claimed prompt tokens > body bytes is impossible, so it
#     clamps + strikes (no tokenizer can emit more tokens than there are bytes).
#   - Only an EXACT re-count (TokenizerExact) can flag a DISCREPANCY; a heuristic re-count
#     (no exact tokenizer for the model) is an OUTLIER GATE only, never a strike trigger.
#   - A small discrepancy within the billing tolerance is honest tokenizer variance
#     (special tokens / model families) -> clamp to tolerance, NOT a strike.
#   - A discrepancy past tolerance records a discrepancy (tq.discrepancies++) and HOLDS the
#     node's lots from promotion (a recount hold freezes the earnings).
#   - Sidecar down / re-count disabled (no url): bill the claim unchanged - the input byte
#     floor remains the fail-closed backstop.
#   - recountHoldSweep auto-expires a hold past ROGERAI_RECOUNT_HOLD_DAYS (operator
#     recourse); adminUnhold clears a hold after review so held lots promote again.
#   - Settlement bills the re-counted (verified) counts via UsageReceipt.CostWith2; the
#     node-signed receipt is left intact (only the BILLED counts change). See lineage_receipts.

Feature: The broker re-counts tokens and bills the verified lesser count, holding earnings on a sustained discrepancy

  Background:
    Given a served request with a node-signed receipt
    And the L1 re-count sidecar is available

  # --- bill the verified lesser count, both axes --------------------------

  Scenario: An over-reported completion count is billed at the broker re-count
    Given the node claims more completion tokens than the broker's exact re-count
    Then the request is billed on the broker re-count, not the claim

  Scenario: An over-reported prompt count is billed at the broker re-count
    Given the node claims more prompt tokens than the broker's exact re-count
    Then the input is billed on the broker re-count, not the claim

  Scenario: The input byte floor clamps an impossible prompt claim even with no sidecar
    Given the node claims more prompt tokens than the request body has bytes
    Then the prompt is clamped to the byte floor and the node is struck
    And this holds even when the sidecar is unavailable

  # --- exact vs heuristic: only exact flags a discrepancy -----------------

  Scenario: Only an exact re-count can flag a discrepancy
    Given the model has no exact tokenizer so the re-count is heuristic
    When the heuristic re-count differs from the claim
    Then no discrepancy is recorded (the heuristic is an outlier gate only)

  Scenario: A small discrepancy within tolerance is honest variance, not a strike
    Given an exact re-count that differs from the claim within the billing tolerance
    Then billing is clamped to tolerance and no discrepancy strike is recorded

  # --- a sustained discrepancy holds earnings -----------------------------

  Scenario: A discrepancy past tolerance holds the node's lots from promotion
    Given an exact re-count below the claim by more than the tolerance
    Then a discrepancy is recorded and the node's earning lots are held from promotion

  # --- fail-open billing when the re-count is unavailable -----------------

  Scenario: With the sidecar down, completion billing falls back to the claim
    Given the re-count sidecar is unavailable
    Then the completion is billed at the node's claim
    And the input byte floor still applies as the fail-closed backstop

  Scenario: With re-count disabled, billing uses the claim
    Given L1 re-count is not configured
    Then the request is billed on the node's claimed counts
    And the input byte floor still applies

  # --- recount hold is recoverable (operator recourse) --------------------

  Scenario: A recount hold auto-expires past the review window
    Given a node under a recount hold
    When more than the recount-hold window elapses with no fresh discrepancy
    Then the hold auto-expires and the held lots promote again

  Scenario: An admin can clear a recount hold after review
    Given a node under a recount hold
    When an admin clears the hold
    Then the held lots promote again on the next sweep
