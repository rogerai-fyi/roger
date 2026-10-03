# THE REVOCATION LIST A MEMBER FETCHES IS SIGNED BY THE EDGE'S ROOT.
#
# STATUS: APPROVED by the founder 2026-10-03 ("approve both, headless first").
# BUILD STATUS (2026-10-03, wt/edge-limoni): BUILT - the authority signs with its root at
# /edge/revocations/signed; members verify before using a serial, refuse a bad list whole, and take an
# old authority's unsigned list add-only, saying so.
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

  Scenario: an authority from before signing is still heard, and can still only add
    Given an authority that answers only the unsigned list
    Then a member uses it as before, adding to its list and never removing from it
    And it says the list it took was unsigned
