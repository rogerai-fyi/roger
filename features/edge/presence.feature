# ROGER EDGE - DIAL-OUT PRESENCE: a node that cannot serve still appears, verified (PROPOSED).
#
# STATUS: PROPOSED 2026-09-22, founder-greenlit as "the small Go-side dial-out fallback endpoint"
# for the phone. Implemented alongside this spec.
#
# WHY: the Edge's normal membership presence is PULL - discovery dials a node's LAN face, reads its
# certificate and its /edge/describe, and records it as a verified member (discovery.feature,
# topology_view.feature). That assumes the node can SERVE TLS on an inbound port. A phone cannot
# assume that: it sleeps, roams between networks, and iOS may not serve an Ed25519 TLS identity.
# So a member needs a second, equivalent way to be present: PUSH. The node dials OUT to the
# authority and says "I am here", and the authority records it as the same verified member it would
# have observed by dialing.
#
# THE TRUST IS UNCHANGED. Presence by push is authenticated by exactly what presence by pull is: the
# node's authority-ISSUED certificate. The authority verifies the certificate chains to its own root
# (it issued it), that the node id is the one the certificate's key derives, and that the request is
# freshly SIGNED by that key - so only a node this authority really enrolled, holding its private
# key, can present, and a captured request cannot be replayed. Nothing new is trusted; only the
# direction of the dial changes.
#
# HONESTY: a pushed member is a real verified member, drawn like any other, and it goes DARK if the
# pushes stop, exactly as a dialed member goes dark when it stops answering. The endpoint records a
# self-reported describe (kind, caps) - the same self-report /edge/describe carries - never more.
#
# GROUND TRUTH: internal/edge/presence.go (PresenceHandler, PresenceRequest), internal/edge/
# fleet.go (Observe - the same sink discovery uses), internal/towercore/cert (Authenticate),
# internal/edge/verify.go (the pull-side check this mirrors), cmd/rogerai/edgehost.go (mounts it on
# the authority server beside /edge/enroll).
#
# Tags: @edge (the endpoint + verification), @security (the trust checks).

Feature: A node can present itself to the authority by dialing out, and be recorded as a verified member without serving an inbound face

  @edge
  Scenario: a node with a valid issued certificate presents and becomes a verified member
    Given a node enrolled on this Edge, holding the certificate the authority issued it
    When it POSTs a freshly signed presence to /edge/present with its describe
    Then the authority records it as a VERIFIED member with its kind and capabilities
    And the member is drawn on the map exactly like one discovery had dialed

  @edge
  Scenario: presence refreshes last-seen, and a member that stops presenting goes dark
    Given a member that presented a moment ago
    When it presents again
    Then its last-seen advances
    When it stops presenting past the dark threshold
    Then it goes dark and is kept, like any member that stopped answering

  @security
  Scenario: a presence whose certificate was not issued by this authority is refused
    Given a presence carrying a certificate signed by some other authority
    When it is POSTed to /edge/present
    Then it is refused, and nothing is written to the fleet

  @security
  Scenario: a presence whose node id does not match its certificate key is refused
    Given a presence whose node_id is not the id the certificate's key derives
    When it is POSTed
    Then it is refused as an identity mismatch, and nothing is written

  @security
  Scenario: a presence with a bad or missing signature is refused
    Given a presence not signed by the certificate's key
    When it is POSTed
    Then it is refused, and nothing is written

  @security
  Scenario: a stale presence is refused
    Given a presence whose timestamp is far outside the freshness window
    When it is POSTed
    Then it is refused as stale

  @security
  Scenario: a replayed presence is refused
    Given a valid presence that was already accepted
    When the exact same signed presence is POSTed again
    Then it is refused as a replay, and the member is not double-written

  @security
  Scenario: a revoked node's presence is refused
    Given a node whose certificate the authority has revoked
    When it presents
    Then it is refused, so a node taken off the Edge cannot re-appear by pushing
