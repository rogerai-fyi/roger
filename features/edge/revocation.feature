# THE REVOCATION LIST A MEMBER FETCHES IS SIGNED BY THE EDGE'S ROOT.
#
# STATUS: APPROVED by the founder 2026-10-03 ("approve both, headless first").
# BUILD STATUS (2026-10-03, wt/edge-limoni): BUILT - the authority signs with its root at
# /edge/revocations/signed; members verify before using a serial, refuse a bad list whole, and take an
# old authority's unsigned list add-only, saying so.
# AMENDMENT APPROVED by the founder 2026-10-26 (pre-push audit of wt/edge-limoni): the signed time was verified and
# then ignored, so a captured list could be replayed forever, hiding every later revocation while the
# member believed its list was fresh; and a 404 dropped a member to the unsigned list, letting anyone
# who answers in the authority's place revoke every good device. No released roger has ever served an
# unsigned list (the endpoint is not on main), so the unsigned fallback is removed, not kept.
#
# Found preparing the Pi test (2026-10-02): a member now refreshes its revocation list from its
# authority while it runs (enrollment.feature: "a peer that has the current revocation list"), but the
# list travelled unsigned over the LAN. Members only ever ADD to their list, so a forged list could
# not un-revoke a device - but it could add serials (revoking good devices), and a member could not
# tell a real list from a forged one. The authority now signs the list with the Edge's root key, which
# every member already trusts.

@edge
Feature: A member trusts a revocation list only when the Edge's root signed it

  Scenario: the authority's revocation list carries the root's signature
    Given a locally-rooted Edge
    When a member asks its authority for the revocation list
    Then the list comes with when it was signed and the root's signature over both

  Scenario: a member verifies the list against the root it holds before using a single serial
    Given a member refreshing its revocation list
    When the list's signature verifies against the root this member enrolled under
    Then its serials are added to the member's list
    When the signature does not verify, or the list was signed by a different root
    Then not one serial is used, the member's list stays exactly as it was, and it says so

  # APPROVED 2026-10-26: replaces "an authority from before signing is still heard, and can still only add".
  Scenario: a member never uses an unsigned revocation list
    Given a member refreshing its revocation list
    When its authority does not answer with a signed list, for any reason
    Then not one serial is used, the member's list stays exactly as it was
    And it says the list could not be checked, so its list may be stale

  # APPROVED 2026-10-26
  Scenario: a list signed earlier than the last one the member took is refused
    Given a member that took a list signed at 10:00
    When it is offered a validly signed list signed at 09:00
    Then not one serial is used, its list and its freshness stay as they were, and it says the list was older
    When it is offered a validly signed list signed at 10:00 again
    Then it is taken, and changes nothing

  # APPROVED 2026-10-26
  Scenario: a member's list is as fresh as when it was signed, not when it was fetched
    Given a member that takes a validly signed list signed 20 minutes ago
    Then its list is 20 minutes old, and the staleness note counts from the signing

  # APPROVED 2026-10-26
  Scenario: a list signed in the future is refused
    When a member is offered a validly signed list signed more than 5 minutes ahead of its own clock
    Then not one serial is used, and it says the authority's clock or the list is wrong
