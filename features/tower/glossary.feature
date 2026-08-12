# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# BUILD STATUS: REFERENCE. Approval is not implementation - this line says which.
# Enforced by internal/towercore/featurestatus_test.go against the "Contract:"
# references in the code. Changing the status without changing the code fails.
#
# Scope: the normative definitions of the load-bearing vocabulary used by every other
# features/tower/*.feature file. This file is the SOURCE OF TRUTH for what these terms mean.
# Where another file uses one of these terms, it inherits the meaning pinned here and must
# not redefine it.
#
# Why this file exists: terms such as "complete hash" (600+ uses), "canonical" (500+ uses),
# and "atom" (300+ uses) were previously used as if precise but were defined nowhere. A
# reader could not tell whether a complete hash covers the signature or not, nor whether
# canonical absence means an explicit sentinel or an omitted key. Both readings produce
# different, incompatible implementations. These scenarios exist to make the intended
# reading executable rather than inferred.

Feature: The Tower specification vocabulary has one exact meaning
  Every term defined here is pinned by a scenario a step definition can assert, so that the
  remaining Tower specifications inherit one reading instead of a plausible one.

  # --- complete hash --------------------------------------------------------

  Scenario: A complete hash covers the signed preimage and never the signature
    Given a signed Tower protocol object with a strict signed preimage and a detached signature
    When its complete hash is computed
    Then the hash input is exactly the RFC 8785 JCS canonical bytes of the strict signed preimage
    And the signature bytes, key material, and transport framing are excluded from that input
    And the same preimage produces a byte-identical complete hash under any signer or key rotation
    And re-signing the identical preimage with a different key changes the signature but not the complete hash

  Scenario: A complete hash is domain-separated by schema so two object kinds cannot collide
    Given two different object kinds whose remaining payload fields are byte-identical
    When each complete hash is computed
    Then the schema/network/protocol identifiers inside each preimage make the two hashes differ
    And no object kind's complete hash can be presented where another kind's hash is required

  Scenario: A complete hash over an unparseable or non-canonical preimage is not computed
    Given supplied bytes that are not strict RFC 8785 JCS canonical form
    When a complete hash is requested
    Then no hash value is produced and the object is rejected before any authority reads it

  # --- canonical absence ----------------------------------------------------

  Scenario: Canonical absence is an explicit encoded sentinel, not a missing key
    Given an optional field that does not apply to this object instance
    When the strict signed preimage is encoded
    Then the field is present with its defined canonical-absence sentinel value
    And a preimage that instead omits the key entirely is rejected as malformed
    And a preimage that supplies an empty object, empty string, or zero in place of the sentinel is rejected

  Scenario: Canonical absence and a real value are not interchangeable
    Given a field encoded as canonical absence in one object and as a real value in another
    When either object is verified against a context requiring the other form
    Then verification fails closed rather than coercing the sentinel to a value or a value to absent

  # --- strict decoding ------------------------------------------------------

  Scenario Outline: Strict decoding rejects every ambiguity a permissive decoder would accept
    Given supplied preimage bytes containing "<defect>"
    When strict decoding runs
    Then the bytes are rejected before signature verification, hashing, or any authority read

    Examples:
      | defect                                              |
      | a duplicate object key                              |
      | an unknown field not in the object's schema         |
      | object keys not in JCS code-point order             |
      | a number encoded with an exponent or a leading zero |
      | a number outside the signed 64-bit integer range    |
      | a non-integer where the schema requires an integer  |
      | a string containing unpaired surrogates             |
      | non-UTF-8 bytes                                     |
      | trailing bytes after the top-level value            |
      | a null in place of canonical absence                |

  Scenario: Strict decoding is total, so acceptance implies one unique interpretation
    Given any byte sequence accepted by strict decoding
    When it is re-encoded from the decoded value
    Then the re-encoded bytes are byte-identical to the accepted input

  # --- atom and rail minor unit --------------------------------------------

  Scenario: An atom is the indivisible accounting quantum, distinct from a rail minor unit
    Given signed currency policy declares positive integer Q accounting quanta per rail minor unit
    When compensation amounts are represented
    Then every entitlement, lot, debt, and journal amount is an integer count of atoms
    And no atom is ever divided, and no amount is represented as a fraction, float, or decimal string
    And a rail minor unit equals Q atoms exactly, so only whole multiples of Q can leave the platform
    And atoms held below one rail minor unit remain on the ledger rather than being rounded away

  Scenario: Atoms of different currency, unit, or scale are never combined
    Given amounts recorded under different currency, unit, or scale triples
    When any sum, comparison, offset, or conservation check runs
    Then it is performed within one exact triple only
    And a cross-triple operation is rejected rather than converted

  # --- Core tuple -----------------------------------------------------------

  Scenario: A Core tuple is an independently assigned time and total-ordered sequence
    Given Roger Core stamps an event with a Core tuple
    Then the tuple is exactly the Core-assigned time and the Core-assigned global sequence for that ledger
    And both elements are assigned by Roger Core, never supplied or influenced by a Tower, Station, or client
    And the sequence is strictly increasing within its ledger with no reuse, gap-filling, or backdating
    And ordering comparisons use the sequence, so equal or skewed times never reorder two events

  Scenario: A supplied Core tuple is evidence of a claim, not of a time
    Given a Tower or Station presents an object carrying a Core tuple it did not receive from Core
    When Roger Core verifies it
    Then the tuple is checked against the authoritative Core-assigned value and any mismatch is rejected

  # --- freshness and fail-closed -------------------------------------------

  Scenario: Fresh means inside the authority's own signed freshness window
    Given an authority object declares a signed freshness window and an expiry Core tuple
    When a use transaction reads it at a use Core tuple
    Then it is fresh only when the use tuple is at or after its effective tuple and strictly inside both the freshness window and the expiry
    And a stale, expired, not-yet-effective, or window-less authority is treated as unavailable

  Scenario: Fail closed means the operation commits nothing and grants nothing
    Given a required authority, head, or store is unavailable, stale, or inconsistent
    When an operation that depends on it runs
    Then no partial state, no reservation, no accrual, no payout, and no capability is created
    And the operation may be retried only by rereading authoritative durable state

  # --- fact head ------------------------------------------------------------

  Scenario: There are exactly six compensation fact heads
    Given compensated-tier eligibility is evaluated
    When the fact heads are enumerated
    Then the closed set is exactly:
      | fact head           | subject                                                    |
      | payout_identity     | the operator's verified payout identity version            |
      | operator_account    | the operator account's current standing                    |
      | accepted_terms      | the operator's acceptance of the current revenue-share terms |
      | sanctions_screening | the operator's current sanctions-screening result           |
      | program_jurisdiction| the operator's admitted program jurisdiction                |
      | tax_profile         | the operator's current TaxProfileFactV1                     |
    And no other fact is a fact head, and no fact head may be omitted from a six-head check
    And any enumeration elsewhere in this specification set naming fewer than six heads is a defect in that file, not a different rule

  # --- dust -----------------------------------------------------------------

  Scenario: Dust is mature payable value too small to leave the platform
    Given an operator's mature payable balance in one currency is below one rail minor unit or below the signed minimum payout
    When payout preparation runs
    Then the balance is dust and no rail instruction is prepared for it
    And the dust remains mature_payable and countable in conservation totals rather than being discarded
    And dust is resolved only through its signed dust-cycle lifecycle or by later accrual lifting it above the threshold

  # --- closed set -----------------------------------------------------------

  Scenario: A closed set admits no value outside its enumeration
    Given a field whose specification declares a closed set of permitted values
    When any other value is supplied, including a plausible synonym or a new value from a later protocol version
    Then the object is rejected rather than treated as unknown-but-acceptable

  # --- typed set and complete hash of a set --------------------------------

  Scenario: A typed set is owner-bound, order-canonical, and duplicate-free
    Given a canonical typed set of member hashes
    When its complete hash is computed
    Then the preimage binds the set's declared member kind, its owner object identity, the exact member count, and the members in ascending byte order
    And a duplicate member, an out-of-order member, a member of another kind, or a count mismatch is rejected
    And an empty set has one deterministic canonical encoding distinct from canonical absence
