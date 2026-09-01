# DRAFT SPEC - NOT YET APPROVED. Written for the founder approval gate.
#
# BUILD STATUS: PARTIAL. Approval is not implementation - this line says which.
# Built (the SEALED-HUB generation, which superseded the TLS-splice relay + roger-station +
# Core-issued edge TLS certificates - all three now REMOVED): the tower-hosted hub
# (internal/towerhub) carrying sealed submits from consumers to self-attached
# `roger share` nodes, the edge grant with signed token usage in the receipt, Core's
# authorize/ack/settle endpoints with one-use settlement and 70/10/20 splits at the node's
# own pinned per-token price, the settle courier (retrying, spooled across restarts),
# endpoint advertisement on the link, a first-party sealed consumer
# (internal/edgeclient AuthorizeSealed/DoSealed/AckSealed, `roger-tower probe`), CANARIES
# (Core probes a Tower by using it, sealed, marker-free, acknowledged), SAMPLED TRANSCRIPT
# AUDIT (Station-signed transcripts, sealed to Core's envelope key so the tower stays blind,
# checked against the receipt digests) with the ADAPTIVE elevation (new stations and
# anomalous recent history raise the selection odds by an unpredictable coin), the Tower
# WIRE-COUNT attestation (sealed-byte counts as settlement EVIDENCE - dispute + forced
# audit, never money; the audit attributes an impossible count to the Tower), and a
# REPUTATION ledger that suspends on repeated canary failures or an audit mismatch.
# Built: COMPENSATION, end to end. A settled attempt mints a real earning lot for the Tower
# operator (5% of gross) and the serving node's owner (90%) on the SAME ledger a direct
# node's serving share uses - so a relay share holds, releases, and cashes out through the
# ordinary /payouts/request rail (Stripe Connect transfer, the policy hold (30 days since
# the 2026-09-01 ruling; 120 at first writing), $25 minimum, KYC
# gate), with clawback on dispute. internal/towercore/earnings additionally records a
# read-only per-attempt accrual (/tower/earnings/owed) as substrate for the wider program.
# Not built: the compensated-Tower revenue-share PROGRAM around it (eligibility, funded-work
# verification, maturity, payout authority, program-level clawback) - its own
# approved-but-unbuilt corpus: operator_revenue_share / compensation_state_machines /
# payment_authority. (Certificate revocation and mutual TLS are now enforced.)
# Enforced by internal/towercore/featurestatus_test.go against the "Contract:"
# references in the code. Changing the status without changing the code fails.
#
# Supersedes four scenarios in job_and_settlement.feature, listed at the end of this file
# and in docs/tower-edge-design.md. Those four rest on Roger Core seeing the request before
# dispatch and observing the bytes in transit. On the edge path it does neither, which is
# the entire point: the load Core stops carrying is the load an operator is paid for.
#
# Scope: what a grant authorizes when Core has not seen the request, how a Station is
# reached through a Tower that cannot read the session, and what settlement rests on once
# Core's own byte counts are gone.

Feature: A Tower carries the data plane and Roger Core keeps the control plane
  Roger Core authorizes and settles. A Tower relays a session it cannot read to a Station
  that terminates it. Per request Core handles two small messages instead of the payload,
  and every claim about what happened is signed by a party with something to lose.

  Background:
    Given an active joined Tower whose data plane is reachable by consumers
    And an attached admitted Station holding a Roger Core-issued certificate for its relay name
    And a funded client whose account passed central authentication

  # --- what a grant is, now that Core has not seen the request -------------

  # THE CHANGE THAT MAKES EVERYTHING ELSE POSSIBLE. On the Core-relayed path the grant
  # commits to a digest of the request, because Core had the request. Here it cannot: the
  # request goes straight from the consumer to the Station. So a grant authorizes a BOUNDED
  # SCOPE and the digest travels the other way, in the evidence.
  Scenario: An edge grant authorizes a bounded scope rather than one exact request
    When Roger Core authorizes an edge attempt for a funded client
    Then the grant names exactly one attempt, one Station, one Tower, and one model
    And the grant carries a one-use nonce, a maximum input size, a maximum output size, and a deadline
    And the grant does not commit to a request digest, because Roger Core has not seen the request
    And Roger Core records the attempt as issued before the grant becomes usable

  Scenario: The consumer is told where to go and nothing more
    When Roger Core authorizes an edge attempt
    Then the consumer receives the grant, the Station relay name, and the attempt deadline
    And the consumer is not told the Station's address, because reachability is what the Tower provides
    And the consumer is not told which Tower carries it

  # --- reaching the Station ------------------------------------------------

  Scenario: The consumer's session terminates at the Station and not before
    When the consumer opens TLS to the Tower's data plane for the Station's relay name
    Then the Tower routes by server name without terminating the session
    And the certificate the consumer verifies is the Station's, issued by Roger Core for that name
    And the Tower holds no private key for that name
    And the Tower observes only the server name, the addresses, the byte counts, and the timings

  Scenario: A Station refuses work the grant does not cover
    Given an edge grant naming another Station
    When the consumer presents it to this Station
    Then the Station refuses before reaching the model
    And the refusal names the mismatch rather than the grant's contents

  Scenario Outline: A Station refuses a grant it cannot accept
    Given an edge grant that is "<defect>"
    When the consumer presents it to the Station
    Then the Station refuses before reaching the model
    And no receipt is produced, because a refusal is not a result

    Examples:
      | defect |
      | not signed by the pinned Roger Core key |
      | past its deadline |
      | for a different public network |
      | for an attempt already settled |
      | asking for more output than its maximum |
      | carrying a request larger than its maximum input size |

  # --- the evidence --------------------------------------------------------

  Scenario: The Station signs for exactly what it received and returned
    When the Station serves an edge attempt
    Then the receipt commits to the digest of the request bytes it actually received
    And the receipt commits to the digest of the response bytes it actually returned
    And the receipt carries the observed input and output usage
    And the receipt is signed with the assertion key recorded at attachment

  # THE OPPOSING INTEREST. The Station's own claim about its own usage is the claim it is
  # paid on. The consumer's is the only independent one available once Core is out of the
  # path, and the Tower sits between the two and can forge neither.
  Scenario: The consumer acknowledges what it actually received
    When the consumer finishes reading the response
    Then it may send Roger Core a signed acknowledgement for that attempt
    And the acknowledgement carries the digest of the response bytes it received
    And the acknowledgement carries its observed output usage and its first-byte and completion times

  Scenario: Settlement takes the lower of two independent claims
    Given a Station receipt and a matching consumer acknowledgement for one attempt
    When Roger Core settles
    Then the response digests must match, or the attempt is refused and attributed to the relay
    And the billed usage is the lower of the two observed usages
    And the attempt is recorded as corroborated

  # A CONSUMER THAT NEVER ACKS IS NOT A FAULT. Customers close laptops mid-stream and
  # third-party clients will never ack at all. An operator who loses money for that is an
  # operator who leaves, so the signal is in the RATE and not in the single attempt.
  Scenario: An attempt with no acknowledgement still settles, and says so
    Given a Station receipt with no consumer acknowledgement before the deadline
    When Roger Core settles
    Then the attempt settles on the receipt alone
    And the attempt is recorded as uncorroborated
    And the Tower's uncorroborated rate is updated

  Scenario: An unusual uncorroborated rate is a finding rather than a refusal
    Given a Tower whose uncorroborated rate is far above the fleet's
    When Roger Core evaluates that Tower
    Then the Tower is flagged for investigation
    And individual attempts already settled are not reversed by the rate alone

  # --- catching a dishonest Tower without reading the traffic --------------

  Scenario Outline: What a Tower cannot get away with
    Given a Tower that attempts "<attack>"
    When Roger Core settles the attempt
    Then the attempt is refused or flagged, by "<mechanism>"

    Examples:
      | attack | mechanism |
      | altering the request | the receipt's request digest against the consumer's own record |
      | altering the response | the receipt's response digest against the acknowledgement's |
      | replaying a settled attempt | the grant's one-use nonce |
      | routing to a Station Core did not select | only the named Station's key can sign an acceptable receipt |
      | inflating the usage it reports | settlement uses the receipt and the acknowledgement, never the Tower's word |
      | serving nothing at all | a canary attempt whose correct answer Roger Core already knows |

  Scenario: Canary attempts are indistinguishable from customer traffic
    When Roger Core issues a canary attempt through a Tower
    Then it is authorized, carried, and settled exactly as a customer attempt is
    And nothing in the grant, the relay name, or the timing marks it as a canary
    And a Tower that fails canaries repeatedly is quarantined

  # WHAT REPLACES PRE-DISPATCH SCREENING. Core cannot moderate what it never sees, and both
  # ends have signed a digest of the exact bytes - so neither can produce a different
  # transcript afterwards. This is the only route by which Tower-served content is reviewed.
  # LENIENCY THAT RETIRES ITSELF. A capability that did not exist cannot be a finding - but a
  # blanket exemption is a permanent hole, and lifting one on a flag day punishes whoever
  # upgrades last. So the leniency is keyed to PROOF, per Station, and ends the moment the
  # Station demonstrates it can answer.
  Scenario: A Station that has never produced a transcript is not yet held to the standard
    Given a Station that has never answered a content audit
    When Roger Core asks it for a transcript and it cannot produce one
    Then the miss is recorded as a soft signal rather than a quarantine-grade finding
    And the same miss from a Station that HAS produced one before is a finding

  Scenario: A sampled transcript is checked against what both ends signed
    Given a settled attempt selected for audit
    When Roger Core asks the Station for the full transcript
    Then the transcript must hash to the digests in the receipt and the acknowledgement
    And a transcript that does not match is attributed to the Station and not to the consumer
    And a Station that cannot produce a transcript for a sampled attempt is quarantined

  Scenario: Content screening for edge traffic is post-hoc and says so
    When a request is served on the edge path
    Then Roger Core does not inspect the content before dispatch
    And no user-facing surface describes edge traffic as pre-screened
    And a policy violation found in audit is enforced against the account afterwards

  # THE RATE ADAPTS TO THE EVIDENCE. The deterministic baseline keeps Core and the Station's
  # retention in agreement; the adaptive layer selects RECENT attempts (which a Station holds
  # regardless of the long-term sample) when the evidence says look closer.
  Scenario: The audit rate adapts to the evidence, not only a fixed dice roll
    Given the sampled-transcript baseline selects a deterministic fraction of settled attempts
    When a Station is newly attached, or its Tower's recent settlements are disputed or persistently uncorroborated
    Then Roger Core additionally selects that settlement for audit at an elevated probability
    And the elevation decays back toward the baseline as corroborated history accumulates
    And a failure to enqueue an adaptive selection under-samples and never gates the money

  # THE TOWER'S OWN WIRE COUNT - EVIDENCE, NEVER MONEY. The Tower cannot read the session,
  # but it can weigh it, and sealed bytes bound the plaintext they carry. A security review
  # killed the clamp version of this scenario: letting the Tower's number move the bill let a
  # consumer running its own tower attest tiny counts and buy near-free inference at an honest
  # node's expense. So the count flags; the money continues to rest on the receipt and the
  # acknowledgement, and the AUDIT arbitrates - the transcript proves the true byte lengths,
  # and a wire count below them is a physical impossibility attributable to the Tower.
  Scenario: The Tower's wire count is settlement evidence the audit arbitrates
    Given a Tower forwards a Station's receipt for settlement
    When the forward carries the byte sizes of the sealed request and sealed result the Tower actually relayed
    Then a Station claim above the attested wire count marks the settlement disputed and forces an audit
    And the billable figures still come from the receipt and the acknowledgement - the Tower's word moves no money in either direction
    And at audit, an attested wire count below the transcript's proven byte length is attributed to the Tower, not the Station
    And an absent or zero wire count changes nothing

  # --- what the operator is paid for ---------------------------------------

  Scenario: Compensation rests on evidence Roger Core can verify
    When Roger Core compensates a Tower operator
    Then the amount is computed from corroborated usage, not from the Tower's own counts
    And a Tower's earnings can be withheld on the same evidence that detects it misbehaving

  # --- overflow becomes paid relay (the consumer wire) ---------------------

  # The load-bearing wire: until now the paid edge path had every part built and no
  # first-party consumer driving it, so real overflow traffic (a model no direct node
  # serves) took a separate FREE relayed path and no operator ever earned. Roger Core now
  # drives the edge path itself for that overflow (Option A, broker-as-edge-consumer): it
  # authorizes an attempt bound to the real consumer, carries the request to the Station,
  # and settles from the receipt inline. On this path Core sees the content (it is the
  # consumer end), which is what lets it price at the Station's OWN token rate exactly as a
  # direct request - the blind, client-driven path is a later phase.
  #
  # MONEY MODEL (founder decision 2026-08-13; rates amended by the 2026-09-01 fee ruling,
  # "10% approved ... 90/5/5"): a relayed request is priced at the Station's
  # per-token rate (Core re-counts tokens, min(claim, recount)), and gross splits three ways
  # of the metered cost - Station 90%, Tower 5%, Platform 5%. The tower's 5% comes out of
  # the platform's margin (its 10% fee drops to 5%), so a Station is paid no less because
  # its traffic was relayed. Billed to the consumer's ACCOUNT wallet, the same balance a
  # direct request draws.

  Scenario: A real request to a tower-only model bills the consumer and pays the operator
    Given a model that only a Station attached to a Tower serves
    And edge pricing is on
    And a signed-in consumer whose account accepted the terms and holds funds
    When the consumer makes an ordinary request the direct fleet cannot serve
    Then Roger Core authorizes an edge attempt bound to that consumer and holds the ceiling on the account wallet
    And the request is carried to the Station and a signed receipt returns
    And the consumer's account wallet is charged at the Station's token rate for the recounted usage
    And the serving Station's owner earns 90 percent of the metered cost
    And the relaying Tower's operator earns 5 percent of the metered cost
    And the platform keeps the remaining 5 percent
    And the answer is returned with its real cost, not marked free

  Scenario: The serving Station is paid no less because its traffic was relayed
    Given the same request served directly versus through a Tower
    Then the Station owner earns the same 90 percent either way
    And the Tower's 5 percent is taken from the platform's share, never the Station's

  Scenario: A failed overflow attempt charges the consumer nothing
    Given a signed-in funded consumer and edge pricing on
    When an overflow attempt is authorized but the Station or Tower dies before a valid receipt
    Then no settlement captures the hold
    And the consumer's reserved funds are released by the pending-hold sweep
    And neither the Station owner nor the Tower operator earns anything

  Scenario: A free or unpriced tower stays free
    Given edge pricing is off
    When an overflow request is served through a Tower
    Then no hold is placed and no wallet is charged
    And the request is carried on the existing free relayed path
    And no relay earning is minted

  Scenario: Only a signed-in, non-anonymized account can be relay-billed
    Given a caller that is not a signed-in, non-anonymized account
    When its request would overflow to a tower-only model
    Then Roger Core does not authorize a paid edge attempt for it
    And it is never billed for relayed traffic

  # --- superseded ----------------------------------------------------------

  # These four scenarios in job_and_settlement.feature describe the Core-relayed path and
  # are contradicted here. They remain correct for that path, which is still in the tree.
  #
  #   "Roger Core still sees content under the v1 policy contract"
  #   "Roger Core records channel-bound transit from its own observation"
  #   "A joined settlement requires a complete matching Core transit observation"
  #   "The inner TLS session authenticates Roger Core and the selected Station end to end"
  #
  # Everything else there survives unchanged: the attempt ledger, one-use grants, receipts
  # bound to digests, attachment-recorded keys, quarantine, and the lifecycle.
