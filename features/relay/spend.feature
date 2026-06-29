# RELAY SPEND PATH: what a priced /v1 relay does end-to-end. The broker verifies the SIGNED
# request (see relay/auth.feature), resolves the wallet FROM THE SIGNATURE (never a header),
# HOLDs a pre-authorization (no overdraft), dispatches to a routed node, and on success SETTLEs
# (debit consumer, mint the operator share, write a receipt). A request that never served is not
# charged. This re-homes the "X-Roger-User cannot spend" guard where it actually applies.
#
# GROUND TRUTH (cmd/rogerai-broker/tunnel.go relay + internal/store Hold/ReleaseHold/Settle;
#   internal/protocol/auth.go VerifyRequest/UserIDFromPubkey). See also features/money/holds.feature
#   + settle.feature for the money math this path drives.
#
# Enforced by: the broker relay tests + the money/* suites. (Doc spec; convertible to godog.)

Feature: Relay spend path

  Scenario: The spend debits the wallet derived from the SIGNATURE, not a header
    Given a consumer signs a priced relay request with their key
    When the relay settles
    Then the debit lands on UserIDFromPubkey(signer), regardless of any X-Roger-User header

  Scenario: Setting X-Roger-User to a victim spends nothing
    Given a request that sets X-Roger-User to a victim's id but carries NO valid signature
    When the broker processes the relay
    Then it is rejected (a spend ALWAYS requires a signature) and the victim's wallet is untouched

  Scenario: A hold pre-authorizes before dispatch (no overdraft)
    Given a wallet with balance $1.00
    When a relay needs to reserve $0.40
    Then a hold reserves it before dispatch, and the wallet can never go negative through this path

  Scenario: A served request settles: debit consumer, mint operator share, receipt
    Given a held request that the node serves successfully
    When it settles for cost C with owner share S
    Then the consumer wallet is debited C, the operator earns S, and a receipt is recorded

  Scenario: A request that never served is not charged
    Given a hold was placed but dispatch found no station / failed before serving
    When the relay returns its error
    Then the hold is released and no spend is recorded (you pay only for served tokens)

  Scenario: Moderation gates the spend path before any node is paid
    Given moderation is required and a prompt is flagged
    When the relay runs
    Then it is rejected before dispatch (no hold settles, no node serves, no charge)
