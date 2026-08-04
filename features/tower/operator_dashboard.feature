# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# Scope: what a TOWER operator sees about the Tower they host — its identity and
# admission state, its certificate and lease lifecycle, the Stations attached to it, the
# traffic it actually relayed, and its compensation. This is the operator-facing twin of
# the enrollment, lifecycle, attribution and compensation contracts already specified in
# public_enrollment.feature, trust_tiers.feature and operator_revenue_share.feature.
#
# A TOWER IS NOT A STATION. A Station is a machine serving inference (`roger share`),
# and its owner view is features/stations/stations_dashboard.feature, which is SHIPPED.
# A Tower is a broker-like relay that routes work to Stations and, in the compensated
# tier, earns a share of net platform revenue on the traffic settled through it. The two
# dashboards report different objects, different money, and different lifecycles, and
# neither may be presented as the other.
#
# SEQUENCING: nothing here is implementable until the joined-Tower protocol exists
# (Phase 2) and compensation exists (Phase 3). Until then this page must not ship, and
# must not be stubbed with placeholder numbers — a dashboard that invents state is worse
# than no dashboard.

Feature: A Tower operator can see their Tower's identity, state, traffic and earnings
  Everything shown is Roger Core's own record, scoped to the authenticated Tower owner.
  Nothing a Tower asserted about itself is displayed as though RogerAI verified it.

  Background:
    Given an authenticated operator account that owns at least one joined Tower

  # --- authorization and scope ---------------------------------------------

  Scenario: The Tower view requires an authenticated owner
    Given the caller presents no session and no valid request signature
    When they read the Tower view
    Then the request is rejected as unauthorized
    And no Tower identity, lease, inventory, or compensation data is returned

  Scenario: An operator sees only the Towers they own
    Given the operator owns Tower "T1"
    And another operator owns Tower "T2"
    When the operator reads their Tower list
    Then it contains "T1"
    And it does not contain "T2", its Stations, its traffic, or its earnings

  Scenario: A Tower ID alone does not authorize a read
    Given the operator supplies another operator's Tower ID directly
    When the view is assembled
    Then it is assembled from the operator's own account bindings only
    And the supplied ID grants no access

  # --- identity and admission ----------------------------------------------

  Scenario: A Tower reports its identity and admission state
    Given the operator owns an admitted Tower
    When they read it
    Then it reports its Tower ID, owner, enrolled-at time, and current lifecycle state
    And the lifecycle state is one of pending, quarantine, active, draining, suspended, revoked, or expired
    And the state shown is Roger Core's recorded state, never a state the Tower claimed

  Scenario Outline: Each lifecycle state explains what it means for the operator
    Given the Tower is in state "<state>"
    When the operator reads it
    Then the view explains "<meaning>" in the operator's terms

    Examples:
      | state      | meaning                                                        |
      | pending    | enrollment is incomplete; no traffic and no earning             |
      | quarantine | admitted but limited to probes or bounded beta traffic          |
      | active     | eligible for ordinary work within its assigned limits           |
      | draining   | finishing existing work; no new jobs will be assigned           |
      | suspended  | excluded pending review; existing accrual is held, not lost     |
      | revoked    | credential denied; re-enrollment requires a policy decision     |
      | expired    | the lease or certificate lapsed; renew to return to service     |

  Scenario: A quarantined Tower is told what promotion depends on
    Given a newly enrolled Tower is in quarantine
    When the operator reads it
    Then the view names the centrally observed evidence promotion depends on
    And it does not promise a promotion date

  # --- certificate and lease lifecycle -------------------------------------

  Scenario: A Tower reports its certificate and lease validity
    Given the Tower holds a current short-lived certificate and admission lease
    When the operator reads it
    Then it reports the certificate serial, not-before, not-after, and next rotation window
    And it reports the admission lease expiry
    And it never displays or exports any private key material

  Scenario: An approaching expiry is surfaced before it bites
    Given the Tower's certificate or lease expires within the warning window
    When the operator reads it
    Then the view warns that service will stop at expiry
    And it states the exact action required to renew

  Scenario: A revoked credential is shown with its reason and appeal route
    Given Roger Core revoked the Tower's credential
    When the operator reads it
    Then the view reports the revocation time and the recorded reason class
    And it offers the documented appeal route
    And it does not expose another operator's evidence or any third-party identity

  # --- attached Stations and inventory -------------------------------------

  Scenario: A Tower reports the Stations attached to it
    Given Stations are attached to the Tower and admitted by Roger Core
    When the operator reads it
    Then each attached Station reports its ID, admission state, and offered models
    And declared capacity, region and hardware are labelled DECLARED
    And centrally observed latency and availability are labelled OBSERVED
    And the two are never merged into one number

  Scenario: A Station rejected by Roger Core is shown as rejected, with the reason class
    Given the Tower submitted an inventory offer that Roger Core did not admit
    When the operator reads it
    Then that Station is listed as not admitted with its reason class
    And the Tower's own inventory claim is not shown as though it were admitted

  Scenario: A local Station bridge credential is never returned
    Given attached Stations authenticate to the Tower with local bridge credentials
    When the operator reads it
    Then no bridge token, local credential, or Station private key appears in the response

  # --- traffic actually relayed --------------------------------------------

  Scenario: A Tower reports the work Roger Core observed on its session
    Given the Tower relayed jobs that settled
    When the operator reads its traffic
    Then it reports attempt counts, byte volume, and error classes from Roger Core's own observations
    And the figures derive from CoreTransitObservationV1, not from the Tower's self-report
    And a divergence between the Tower's statement and Core's observation is shown as a divergence

  Scenario: Traffic reporting exposes no content and no consumer identity
    Given the Tower relayed many jobs
    When the operator reads its traffic
    Then no prompt, completion, request body, model input, or consumer identity appears
    And per-attempt rows carry opaque identifiers only

  Scenario: Availability is measured centrally, not claimed
    Given the Tower's own uptime claim differs from Roger Core's observations
    When the operator reads its health
    Then the health figure shown is the centrally observed one
    And the Tower's claim does not overwrite measured history

  # --- monetization: what the Tower earned ---------------------------------

  Scenario: An uncompensated Tower is told plainly that it earns nothing
    Given the Tower is admitted but not enrolled in the compensated tier
    When the operator reads its earnings
    Then the view states that admission alone earns nothing
    And it explains what the compensated tier requires
    And it shows no accrual, balance, or projection

  Scenario: A compensated Tower reports its compensation by lifecycle stage
    Given the Tower is enrolled in the compensated tier and has relayed settled work
    When the operator reads its earnings
    Then it reports, separately and without double counting: immature accrual, reserve held, mature payable, reserved for a payout in flight, paid, and outstanding debt
    And each figure names the currency, unit and scale it is denominated in
    And the sum relationship between the stages is shown so the operator can reconcile it

  Scenario: Each compensation figure names the policy that produced it
    Given compensation accrued under a signed revenue-share policy
    When the operator reads its earnings
    Then the view names the rate and the policy version in force at grant issue
    And a later policy change does not restate historical accrual

  Scenario Outline: An ineligible settlement explains itself
    Given a settlement through the Tower produced no compensation because "<reason>"
    When the operator reads that settlement
    Then the view states the deterministic reason
    And it distinguishes a reason the operator can act on from one they cannot

    Examples:
      | reason                                             |
      | the Tower was not enrolled in the compensated tier |
      | the job was funded entirely by platform grant credit |
      | the consumer payment has not matured                |
      | the consumer payment was refunded or charged back   |
      | transit evidence was missing or inconsistent        |
      | the settlement's net platform revenue was zero      |
      | accrual was withheld pending a self-dealing review   |
      | accrual was withheld because the exposure cap was reached |

  Scenario: Withheld and forfeited amounts are distinguished
    Given some of the Tower's accrual is withheld and some was forfeited by a decision
    When the operator reads its earnings
    Then withheld amounts are shown as held pending an outcome, with the review deadline
    And forfeited amounts are shown as final, with the decision reference
    And the two are never presented as one number

  Scenario: Payout state is reported honestly, including what is not yet payable
    Given the operator has mature payable compensation below the minimum payout
    When they read their payout state
    Then the view states that the balance is below the minimum and will carry forward
    And it does not present it as an imminent payment

  Scenario: Debt is shown to the operator who owes it, with its origin
    Given a reversal created outstanding debt against the operator
    When they read their earnings
    Then the debt is shown with the settlements that produced it
    And the view explains that future compensation offsets it before paying out

  # --- honest labelling -----------------------------------------------------

  Scenario: Attribution is described as identity, not physical proof
    Given the view reports traffic attributed to this Tower
    When that attribution is explained
    Then it states that the bound envelopes crossed a Core session authenticated to the Tower key
    And it makes no claim about physical path, geography, unmodified runtime, or non-collusion

  Scenario: A Tower is never described as a peer of Roger Core
    When the trust relationship is described on this page
    Then RogerAI is identified as the admission, routing, settlement and revocation authority
    And the Tower is described as an untrusted relay whose statements are corroborated, not trusted

  # --- private and standalone ----------------------------------------------

  Scenario: A standalone Tower has no entry in the public dashboard
    Given an operator runs a Tower in standalone mode
    When they read the public Tower view
    Then no standalone Tower appears
    And the view explains that a standalone Tower is governed entirely locally

  # --- the two dashboards are distinct -------------------------------------

  Scenario: Stations and Towers are never conflated
    Given the operator runs both Stations and a Tower
    When they read either view
    Then Stations report serving state, model offers and provider earnings
    And Towers report admission state, relay attribution and revenue-share compensation
    And neither view presents one kind of object as the other
