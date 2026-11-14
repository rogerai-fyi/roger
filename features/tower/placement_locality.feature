# APPROVED SPEC - written and founder approved 2026-11-13 ("Approve as written"), with the
# proposed numbers accepted as written. Changes to an approved scenario need re-approval.
# BUILD STATUS: BUILT. internal/towercore/placement/locality.go (the class), cmd/rogerai-broker/
# towerlocality.go (hub resolution, conflict check, sweep retry, admin view), internal/towercore/link
# (locality on the session and the mirror), station locality in internal/towercore/mobility.
# One wording difference: a hub that answers with someone else's identity fails the hub check and is
# moved off by the "tower_failing" trigger, which is how a Tower that cannot prove itself is caught.
# Added after review: every passing hub check re-measures the hub from the address that just proved
# the Tower's identity, so a name pointed somewhere else once does not keep its locality. A link/hub
# conflict drops the bucket as well as the continent (conservative). Residual, stated plainly: a
# Tower that answers Core's resolver differently from everyone else's, and proves its identity at
# the address it gives Core, is measured where Core reached it.
#
# Founder request 2026-11-13: "1, 2, 3 and 4 are good ideas and we should implement them with
# rehoming and so on, lets make sure we trully and completely implement these". Idea 1 was
# "pick the Tower by distance". This file is the input that idea needs: where a Tower and a
# Station are, as Core measures it. It is M2 in docs/relay-selection-design.md.
#
# Builds on what exists: coarseNetBucket (tunnel.go, collected and never read) and the
# operator-supplied network-to-continent table (netcontinent.go, ROGERAI_NET_CONTINENTS,
# founder ruling 2026-10-05). Neither reaches an outside service and neither is shown publicly.
#
# Ruling this file depends on (design doc section 4.1, already decided): location is never
# self-declared. A typed region stays decorative.

Feature: Core knows roughly where each Tower and each Station is, from addresses it observes
  Placement (placement_ranking.feature) and mobility (station_mobility.feature) need one
  input that does not exist today: how close a Station is to a Tower. Core derives it from
  network addresses it sees itself, at two grains. The fine grain is the network bucket (an
  IPv4 /24 or an IPv6 /32, as coarseNetBucket already computes). The coarse grain is the
  continent, from the operator's table. Neither is ever taken from anything a Tower or a
  Station says about itself.

  The locality class between a Station and a Tower is one of, best first:
    | class          | meaning                                                   |
    | same_network   | the Station and the Tower's hub share a network bucket    |
    | same_continent | different buckets, same continent in the table            |
    | unknown        | either side has no continent (no table, no match, local)  |
    | far            | both sides have a continent and they differ               |

  # --- where a Tower is ------------------------------------------------------

  Scenario: A Tower's location comes from its hub address, resolved by Core
    Given a Tower opens a link session advertising hub endpoint "hub.example:7443"
    When Core accepts the session
    Then Core resolves the hub host itself and computes the bucket and continent of that address
    And stores both on the Tower's live link record, so every broker instance reads the same values
    And the Tower's typed region, display name and any Hello field play no part

  Scenario: The hub address is what counts, not the link's source address
    Given a Tower's link connects from an address in one continent
    And its hub endpoint resolves to an address in another continent
    When Core computes the Tower's location
    Then the Tower's continent is unknown
    And Core logs one "tower-locality conflict" line naming the Tower and both continents, never the addresses
    And the Tower is still eligible for placement, ranked as unknown

  Scenario: A hub host that resolves to several addresses uses them all
    Given a Tower's hub host resolves to several addresses
    When every address maps to the same bucket
    Then that bucket is the Tower's
    When the addresses span buckets in one continent
    Then the Tower has that continent and no bucket
    When the addresses span continents
    Then the Tower's continent is unknown

  Scenario Outline: A hub address that says nothing about place gives no location
    Given a Tower's hub endpoint is "<endpoint>"
    When Core computes the Tower's location
    Then the Tower's bucket and continent are both unknown

    Examples:
      | endpoint                        |
      | a host that does not resolve    |
      | a loopback address              |
      | a private RFC 1918 address      |
      | an IPv6 unique local address    |
      | a link-local address            |
      | the unspecified address         |
      | an address in no table range    |

  Scenario: A Tower's location is recomputed on every new link session and not in between
    Given a Tower's location was computed when its session opened
    When the Tower's hub DNS changes while the session stays open
    Then the stored location is unchanged until the next session opens
    And a new session recomputes it from scratch, leaving no stale value behind

  Scenario: A DNS lookup cannot stall a link session
    Given resolving a Tower's hub host takes longer than 2 seconds
    When Core accepts the session
    Then the session opens on time with an unknown location
    And Core retries the lookup on the next governance sweep, at most once per sweep

  # --- where a Station is -----------------------------------------------------

  Scenario: A Station's location comes from the address its attach arrives from
    Given a node self-attaches from a public address
    When Core places the Station
    Then the Station's bucket and continent are computed from that request's own address
    And the node's typed region plays no part

  Scenario: The address behind a trusted proxy is the client's, never a header from the internet
    Given the broker runs behind the host proxy that overwrites the client address header
    When a node self-attaches and sends its own copy of that header
    Then Core uses the address the trusted proxy supplied
    And the node's copy is ignored

  Scenario: A Station that attaches from a private address has an unknown location
    Given a node self-attaches from a loopback or private address
    When Core places the Station
    Then the Station's bucket and continent are unknown

  Scenario: A Station's location is refreshed whenever it attaches again
    Given a Station attached from one network and later re-attaches from another
    When Core handles the re-attach
    Then the Station's location is the new network's
    And no value from the old network survives

  # --- the class --------------------------------------------------------------

  Scenario Outline: The locality class between a Station and a Tower
    Given a Station with bucket "<s_bucket>" and continent "<s_cont>"
    And a Tower with bucket "<t_bucket>" and continent "<t_cont>"
    When Core computes their locality class
    Then the class is "<class>"

    Examples:
      | s_bucket        | s_cont | t_bucket        | t_cont | class          |
      | 203.0.113.0/24  | NA     | 203.0.113.0/24  | NA     | same_network   |
      | 203.0.113.0/24  |        | 203.0.113.0/24  |        | same_network   |
      | 203.0.113.0/24  | NA     | 198.51.100.0/24 | NA     | same_continent |
      | 203.0.113.0/24  | NA     | 198.51.100.0/24 | AF     | far            |
      | 203.0.113.0/24  |        | 198.51.100.0/24 | AF     | unknown        |
      | 203.0.113.0/24  | NA     | 198.51.100.0/24 |        | unknown        |
      |                 |        |                 |        | unknown        |
      | 2001:db8::/32   | EU     | 2001:db8::/32   | EU     | same_network   |
      | 2001:db8::/32   | EU     | 203.0.113.0/24  | EU     | same_continent |

  Scenario: No table configured means locality never helps or hurts anyone
    Given no network-to-continent table is configured
    When Core computes locality classes
    Then every pair is "unknown" unless the two share a network bucket
    And /admin/live keeps reporting the table as not configured, as it does today

  Scenario: A table that fails to load is ignored, never fatal
    Given the configured table cannot be read or parsed
    When the broker starts
    Then placement runs with no continents, exactly as with no table
    And /admin/live shows the table's error, as it does today

  # --- what is kept and who sees it --------------------------------------------

  Scenario: Locality is kept coarse and never published
    When Core stores or uses a Tower's or a Station's location
    Then it keeps only the bucket and the continent, never a full address
    And no public endpoint, market listing, receipt or discovery answer carries either value
    And the admin Tower detail shows the Tower's continent and whether it has a bucket, never the bucket itself
    And log lines name continents and classes, never buckets or addresses

  Scenario: A typed region is still shown as declared and is never labeled measured
    Given an operator typed a region for their Tower or Station
    When the region is displayed anywhere
    Then it is labeled as declared by the operator
    And no screen presents Core's measured continent as the operator's region or the other way round

  # --- adversarial -------------------------------------------------------------

  Scenario: A Tower cannot claim to be near by what it says
    Given a Tower sends a region, a continent, coordinates or a bucket in any message it controls
    When Core computes the Tower's location
    Then none of those values are read

  Scenario: Pointing a hub name at someone else's network does not attract Stations there
    Given a Tower advertises a hub host that resolves into a network the operator does not run
    When Stations are placed on that Tower and dial the hub
    Then the hub's certificate pin and identity proof fail for any host that is not the admitted Tower
    And those Stations fail their re-attach checks and are moved off by the "relay gone" trigger
    And the Tower's canary failures count against it as they do today

  Scenario: A Station steering its own attach through a VPN only changes its own placement
    Given a node self-attaches through a VPN exit far from where it runs
    When Core places the Station
    Then the Station is placed as if it were at the exit
    And no other Station's placement and no Tower's location changes
