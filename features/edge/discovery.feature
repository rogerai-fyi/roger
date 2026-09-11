# ROGER EDGE - LAN DISCOVERY ("the advertisement is a hint, the certificate is the proof").
#
# WHY THIS EXISTS
#
# Nothing in the Go tree discovers another roger instance today. A `roger share` node dials OUT
# to a relay and is reachable through it, which is how a Station behind NAT serves the market at
# all - but two roger instances on one desk have no idea the other is there, and a device with no
# internet is invisible to its own owner. That is the gap this closes: the owner's machines find
# each other on the network they share, with no cloud round trip.
#
# THE TRAP THIS SPEC IS BUILT AROUND
#
# mDNS is UNAUTHENTICATED. Anyone on the LAN can advertise `_rogerai._tcp` claiming any node id,
# any account and any capability. So an advertisement may only ever be a HINT ABOUT WHERE TO
# LOOK. Authority comes from the TLS certificate the peer presents when we connect to it, pinned
# against the fingerprint the advertisement carried and, for an enrolled node, against the
# account the owner actually enrolled. A spoofed record must be able to waste a dial and nothing
# else: no fleet membership, no capability, no trust, no traffic.
#
# The prevailing implementation in this market pins the peer certificate on the LAN dial for
# exactly this reason, and documents the MITM concern in its own code comments. We adopt the
# pinning and go one step further: a record whose fingerprint does not match is not merely
# refused, it is REPORTED, because on a home or plant network a mismatch is either a
# misconfiguration the owner must see or an attack the owner must see.
#
# SCOPE. Discovery only: find candidates, verify identity, produce node records. Enrollment
# (features/edge/enrollment.feature), the message set (protocol.feature) and control
# (control.feature) are separate and come later. Discovery NEVER grants membership by itself:
# an unenrolled peer is a CANDIDATE the owner may adopt, never a member.
#
# GROUND TRUTH: internal/localplane (the existing LAN-bindable plane and its private-range
# rules), internal/towercore/cert (the certificate machinery a node's identity reuses),
# cmd/rogerai-broker/tunnel.go (the dial-out path that remains the always-available fallback).
# Step definitions drive a REAL mDNS responder and a REAL TLS listener on loopback - no mocks.
#
# KNOBS: ROGERAI_EDGE_DISCOVERY=1|0 (default 1), ROGERAI_EDGE_MDNS_SERVICE=_rogerai._tcp,
# ROGERAI_EDGE_DISCOVERY_INTERVAL=30s, ROGERAI_EDGE_CANDIDATE_TTL=5m.

Feature: A roger instance finds the owner's other instances on the LAN, and trusts none of them on their word

  Background:
    Given a roger instance "alpha" enrolled to account "acct-1"
    And LAN discovery is enabled

  # =========================================================================
  # 1. ADVERTISING
  # =========================================================================

  Scenario: an instance advertises itself with the fields a peer needs to verify it
    When "alpha" starts
    Then it advertises the mDNS service "_rogerai._tcp"
    And the advertisement carries the node id, the account id, the declared capabilities, the port, and the SHA-256 fingerprint of the certificate it will present
    And the advertisement carries no secret, no token and no private key

  Scenario: the advertised fingerprint is the certificate actually served
    When a peer dials "alpha" on the advertised port
    Then the certificate "alpha" presents hashes to the fingerprint it advertised

  Scenario Outline: discovery can be turned off and then nothing is advertised at all
    Given ROGERAI_EDGE_DISCOVERY is "<value>"
    When "alpha" starts
    Then it advertises <advertises>
    And it browses for peers <browses>

    Examples:
      | value | advertises | browses |
      | 1     | yes        | yes     |
      | 0     | no         | no      |

  Scenario: an instance that is not enrolled advertises no account
    Given a roger instance "fresh" with no account
    When "fresh" starts
    Then its advertisement carries an empty account field
    And a peer treats it as a candidate, never as a member

  Scenario: advertising binds only to private networks, never to a public interface
    When "alpha" advertises
    Then it advertises only on loopback and RFC1918 or IPv6-ULA interfaces
    And it never advertises on a link-local 169.254 address

  # =========================================================================
  # 2. BROWSING AND VERIFICATION - the security core
  # =========================================================================

  Scenario: a peer of the same account with a matching certificate becomes a verified node
    Given a peer "beta" enrolled to account "acct-1" advertising a truthful record
    When "alpha" browses
    Then "beta" appears as a VERIFIED node of the fleet
    And its capabilities are the ones its certificate-backed describe reported, not the ones the advertisement claimed

  Scenario: a record whose fingerprint does not match the served certificate is refused and reported
    Given a peer advertising a fingerprint that does not match its certificate
    When "alpha" browses and dials it
    Then the peer does NOT appear in the fleet
    And it appears in the discovery report as "fingerprint mismatch" with the observed and advertised fingerprints
    And one warning line names the address and both fingerprints

  Scenario: a spoofed advertisement claiming another node's identity cannot take its place
    Given a verified node "beta" at one address
    And an attacker advertising the same node id and account at a different address
    When "alpha" browses and dials the attacker
    Then the attacker fails certificate verification
    And "beta" keeps its place in the fleet, at its original address
    And the attempt is reported as "identity collision"

  Scenario: a peer enrolled to a DIFFERENT account is never a member, however truthful its record
    Given a peer "stranger" enrolled to account "acct-2" with a valid certificate
    When "alpha" browses
    Then "stranger" does not appear in the fleet
    And it is not offered as a candidate
    And nothing about "stranger" is written to the fleet store

  Scenario Outline: every certificate defect refuses the peer
    Given a peer advertising a record whose certificate is <defect>
    When "alpha" dials it
    Then the peer is refused with reason "<reason>"
    And it does not appear in the fleet

    Examples:
      | defect                                  | reason              |
      | expired                                 | certificate expired |
      | not yet valid                           | certificate expired |
      | signed by an unknown authority          | unknown authority   |
      | valid for a different node id           | identity mismatch   |
      | self-signed while claiming an account   | unknown authority   |

  Scenario: a peer that advertises but refuses the connection is reported, not retried forever
    Given a peer advertising a record whose port accepts nothing
    When "alpha" browses
    Then the dial fails once and is reported as "unreachable"
    And it is retried on the next discovery interval, not in a tight loop

  Scenario: an unenrolled peer on the same LAN is a CANDIDATE the owner may adopt
    Given a peer "fresh" with no account advertising truthfully
    When "alpha" browses
    Then "fresh" appears as a CANDIDATE, clearly separate from the fleet
    And adopting it requires the owner's explicit action
    And until adopted it has no capabilities and receives no traffic

  # =========================================================================
  # 3. LIVENESS AND THE FLEET RECORD
  # =========================================================================

  Scenario: a node that stops advertising goes dark but is not forgotten
    Given a verified node "beta" in the fleet
    When "beta" stops advertising and stops answering
    Then after the candidate TTL "beta" is shown DARK with its last-seen time
    And it is still listed, because a fleet that silently drops members hides the problem

  Scenario: a node that returns is the same node, not a new one
    Given a DARK node "beta"
    When "beta" advertises again with the same node id and a matching certificate
    Then it returns to VERIFIED
    And its name, its history and its place in the fleet are unchanged
    And no duplicate row is created

  Scenario: a node that returns with a DIFFERENT certificate does not silently take its own place
    Given a DARK node "beta"
    When a peer advertises node id "beta" with a certificate that does not match the pin
    Then it is refused as "identity mismatch"
    And "beta" stays DARK
    And the owner is told the pin can be cleared deliberately

  Scenario: the same node reached two ways appears once
    Given a verified node "beta" reachable both on the LAN and through a relay
    When the fleet is listed
    Then "beta" appears exactly once
    And its transports list LAN first and relay second

  Scenario: discovery and the relay agree on one fleet
    Given a node "gamma" that is connected to a relay but not on this LAN
    When the fleet is listed
    Then "gamma" appears with transport "relay" only
    And nothing about it differs from a LAN-discovered node except its transports

  # =========================================================================
  # 4. IT NEVER GETS IN THE WAY
  # =========================================================================

  Scenario: discovery failing entirely does not affect serving or relaying
    Given the network blocks multicast
    When "alpha" browses and finds nothing
    Then discovery reports "no peers found"
    And "alpha" still serves and relays exactly as before
    And one line says discovery is unavailable, once, not per interval

  Scenario: discovery is not on the request path
    Given a discovery browse that takes 5 seconds
    When a consumer relays a request through "alpha"
    Then the relay completes at its normal speed
    And nothing about the relay waited on discovery

  Scenario: a hostile LAN flooding advertisements cannot exhaust the instance
    Given 10000 advertised records arrive in one interval
    Then at most the configured candidate ceiling is retained
    And the excess is counted and reported, not stored
    And memory and dial attempts stay bounded

  Scenario: discovery on a machine with no network does nothing and says so once
    Given the machine has no non-loopback interface
    When "alpha" starts
    Then discovery reports unavailable once
    And startup is not delayed
