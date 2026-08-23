# APPROVED SPEC - founder-dictated 2026-08-23 ("I want the roger-tower cli to clearly have
# in a log displaying the STATE... as admin/approver, i want to do this on the website...
# i would also like to get an email... once it's approved the tower cli should
# automatically say it's approved"). Changes to an approved scenario need re-approval.
# BUILD STATUS: PARTIAL. CLI + heartbeat state + canary vet + admin email are built;
# the dashboard Towers panel lives in the private roger-admin repo.

Feature: A Tower's approval is visible to the operator and workable for the admin
  The waiting room works but is invisible: the operator cannot tell they are waiting,
  the admin is not told anyone arrived, and approval needs a curl with a pasted secret.
  Every gap here was found by the first real operator - the founder - standing in both
  roles at once.

  # --- the operator sees where they stand -----------------------------------

  Scenario: Serve says plainly that the Tower awaits approval
    Given a registered Tower in quarantine starts serve
    Then the link banner names the state and that approval is pending
    And it says the flip is automatic and where to learn more
    And it does not read as a fault

  Scenario: Serve announces the approval the moment it lands
    Given a Tower is serving in quarantine
    When the admin approves it
    Then within one heartbeat serve prints that the Tower is approved and ready to carry traffic
    And no restart is required

  Scenario: The heartbeat answer carries the admission state
    Given a Tower heartbeats its live session
    Then the response names the Tower's current admission state
    And a state the CLI does not recognise is shown verbatim rather than hidden

  Scenario: Serve announces a suspension or drain the same way
    Given a Tower is serving while active
    When the admin suspends it
    Then within one heartbeat serve says it no longer takes work and why that can be

  # --- the admin approves on the website ------------------------------------

  Scenario: The admin dashboard lists Towers pending approval
    Given a Tower enrolled today and another was approved last week
    When the admin opens the Towers panel
    Then the pending Tower is listed with its id, owner, enrollment time, and advertised endpoint
    And the approved Tower shows its state and last-seen time

  Scenario: Approval is one click and is attributed
    Given the admin dashboard shows a pending Tower
    When the admin clicks approve
    Then the dashboard's backend forwards the lifecycle change with its own broker credential
    And the admin pastes no secret into a terminal
    And the broker log records the transition

  Scenario: The dashboard can also suspend, drain, and revoke
    Given an approved Tower is listed
    Then the same panel offers suspend and revoke, each requiring a confirmation
    And revoke states plainly that it is permanent

  # --- the admin hears about arrivals ----------------------------------------

  Scenario: A new enrollment emails the admin
    Given admin notification email is configured
    When a Tower completes enrollment into quarantine
    Then one email is sent naming the tower id, the owner, and where to approve it
    And a failure to send is logged and never fails the enrollment itself

  Scenario: Enrollment email is rate-limited per owner
    Given an owner enrolls five Towers in one hour
    Then the admin receives at most one email per owner per hour naming the count

  # --- honest relay endpoints ------------------------------------------------

  Scenario: An empty relay-public host resolves to the machine's own address
    Given serve is started with --relay-public :8444
    Then the advertised endpoint uses this machine's outbound LAN address
    And serve prints the address it chose so a wrong guess is visible immediately

  Scenario: A loopback relay endpoint is accepted and named for what it is
    Given serve advertises a loopback relay endpoint
    Then serve says only this machine can reach the hub and that the public network cannot
    And the link proceeds: loopback is a legitimate test rig, not an error

  Scenario: Roger Core never dials a non-public relay endpoint
    Given a Tower advertises a loopback, private-range, or link-local relay endpoint
    When Core's canary or any Core-side dialer selects a Tower to probe
    Then endpoints that are not publicly routable are skipped
    And the skip is recorded so the Tower's canary status says unreachable-by-design
    # Without this, a hostile Tower advertising localhost or the metadata address turns
    # Core's own canary into a probe of Core's host - server-side request forgery.

  Scenario: Approval is never inferred from the advertised endpoint
    Given a Tower advertises a loopback endpoint at enrollment
    Then its approval requirement is unchanged
    # The advert can change on any reconnect; approval keyed to it would be bypassed by
    # approve-then-swap. Local-only use without approval is what standalone mode is.
