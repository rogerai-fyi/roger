# PROPOSED SPEC - no step definitions or production code yet: approve this spec first
# (spec-first workflow, CLAUDE.md step 3). Written from the 2026-07-01 launch audit.
#
# Spec-first behavior contract for CSAM report SUBMISSION (draining the queue) - the
# 2026-07-01 launch audit's legal blocker: preservation works (report.go encrypts +
# queues into rogerai.csam_incidents with report_state='queued'), but NOTHING drains
# the queue to NCMEC's CyberTipline, and 18 USC 2258A obligates REPORTING, not just
# preservation, "as soon as reasonably possible".
#
# Ground truth today: cmd/rogerai-broker/report.go (preserveCSAM, encrypt-at-rest),
# internal/store PendingCSAMReports (store.go), moderation S4 path in tunnel.go /
# concierge.go / audio.go. The drain ships in two rungs; THIS spec covers rung 1:
#
#   RUNG 1 (this spec): an ADMIN-ONLY drain surface - list queued incidents and mark
#     them submitted with the CyberTipline report id the founder obtained by filing
#     (manually via NCMEC's portal, or later rung-2's API client). This makes the
#     legal workflow OPERATIONAL now: nothing sits silently in `queued`.
#   RUNG 2 (follow-up, NOT this spec): the automated CyberTipline API client that
#     drains without a human. Same state machine, so rung 1's specs keep holding.
#
# State machine: queued -> submitted (with tipline id + timestamp)
#                queued -> failed (transient submit error; stays claimable)
# Invariants:
#   C1 ADMIN ONLY: every endpoint here requires requireAdmin (constant-time key or
#      admin GitHub session); unauthenticated/non-admin gets 403 with NO existence hint.
#   C2 NO CONTENT LEAK: list/mark responses NEVER include the preserved content or
#      decryption material - only ids, timestamps, category, state.
#   C3 DURABLE AUDIT: every state transition appends who/when/what (audit trail);
#      submitted rows retain the tipline report id permanently (retention window).
#   C4 IDEMPOTENT: marking an already-submitted incident again is a no-op that
#      returns the existing tipline id (never a second report row).
#   C5 MONOTONIC: an incident never leaves `submitted` (no un-report).
#   C6 VISIBLE BACKLOG: queue depth + oldest-queued age are exposed on the admin
#      surface (and the broker logs a WARNING on boot + daily while depth > 0), so
#      a growing legal backlog can never be silent.

Feature: CSAM incident queue drain - admin listing and submission marking

  Background:
    Given a fresh store with an admin key configured
    And a preserved CSAM incident "inc_1" in state "queued"

  # ===========================================================================
  # 1. Authorization (C1)
  # ===========================================================================

  Scenario: listing the queue requires admin
    When an unauthenticated request lists the CSAM queue
    Then it is rejected with 403
    And the response body reveals nothing about the queue

  Scenario: a non-admin session cannot list or mark
    Given a logged-in NON-admin web session
    When it lists the CSAM queue
    Then it is rejected with 403

  Scenario: the admin key is compared constant-time
    Then the CSAM admin surface uses the same requireAdmin gate as recourse (no second path)

  # ===========================================================================
  # 2. Listing (C2, C6)
  # ===========================================================================

  Scenario: the admin list shows metadata only - never content
    When the admin lists the CSAM queue
    Then incident "inc_1" appears with its id, category, created_at, and state
    And the response contains NO preserved content, ciphertext, or key material

  Scenario: queue depth and oldest age are reported
    Given 3 queued incidents, the oldest 40 hours old
    When the admin lists the CSAM queue
    Then the response reports depth 3 and the oldest-queued age

  Scenario: a non-empty queue is loudly logged
    Given 1 queued incident
    When the broker boots
    Then a WARNING naming the queue depth is logged (never the content)

  # ===========================================================================
  # 3. Marking submitted (C3, C4, C5)
  # ===========================================================================

  Scenario: marking an incident submitted records the tipline id and timestamp
    When the admin marks "inc_1" submitted with CyberTipline report id "CT-12345"
    Then incident "inc_1" is in state "submitted"
    And it permanently records report id "CT-12345" and the submission time
    And an audit row records the admin identity and the transition

  Scenario: re-marking a submitted incident is an idempotent no-op
    Given incident "inc_1" already submitted with report id "CT-12345"
    When the admin marks "inc_1" submitted with report id "CT-99999"
    Then the stored report id remains "CT-12345"
    And no second audit submission row is minted

  Scenario: a submitted incident never returns to queued
    Given incident "inc_1" already submitted
    Then no admin operation can move it back to "queued"

  Scenario: marking an unknown incident id is a clean 404
    When the admin marks "inc_ghost" submitted with report id "CT-1"
    Then it is rejected with 404 and no state changes

  Scenario: marking with an empty tipline id is rejected
    When the admin marks "inc_1" submitted with report id ""
    Then it is rejected with 400 and "inc_1" stays "queued"

  # ===========================================================================
  # 4. Retention (C3) - preserved evidence outlives the report
  # ===========================================================================

  Scenario: submitted incidents retain their preserved content for the legal window
    Given incident "inc_1" submitted 89 days ago
    Then its preserved (encrypted) content is still retrievable by the retention job
    # 18 USC 2258A(h): preserve 90 days from report (extendable); deletion only after.
