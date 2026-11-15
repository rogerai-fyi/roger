# APPROVED SPEC - written and founder approved 2026-11-14 ("Approve as written"): every upgraded Tower pulls,
# "same network" is the same public IPv4 or the same IPv6 /64, numbers accepted (3 s pickup, 4 held
# pulls, 25 s hold). Changes to an approved scenario need re-approval.
# BUILD STATUS: PARTIAL. Code is in; the executable runners are being written.
#
# Companion to tower_pull_dispatch.feature (change 1 of three, founder request 2026-11-14). That
# file moves the Core-to-Tower leg onto the Tower's outbound link. This file covers the other
# leg for a Tower with no public address: which Stations can reach its hub, and how placement,
# locality, health and mobility treat it.
#
# Two defects found reading the code on 2026-11-14 (main 18bc8da1), each now a scenario below:
#   - a Tower advertising a private hub address fails Core's hub check every interval, because
#     the hub check refuses to dial a private address and records that as a failure
#     (towerhubcheck.go:63, towerhub/pin.go ReachVetted). Two in a row writes health_failing.
#   - placement checks only that a hub address exists (towerplacement.go:171), so an internet
#     Station can be handed a private address it can never reach.
#
# One deliberate exception to an approved scenario in placement_locality.feature ("The hub
# address is what counts, not the link's source address"): for a hub that advertises a private
# address, the hub address says nothing about where the Tower is, so its location comes from the
# address its outbound link is observed from. Public-address hubs are unchanged.
#
# Proposed rule for "the same network" (founder to accept or change): the Station's observed
# address and the Tower link's observed address are the SAME public IPv4 address (one household
# behind one NAT), or are in the same IPv6 /64 (one home LAN). The existing same_network class
# (an IPv4 /24) is too loose for this purpose: a neighbour on the same ISP /24 cannot reach your
# LAN. Mixed families (one side seen over IPv4, the other over IPv6) are not the same network.

Feature: A Tower with no public address serves the Stations on its own network
  A home Tower behind NAT cannot be dialed from the internet, but every machine on its own
  network can reach its hub at a private address. With work arriving by pull over the
  Tower's link, that is enough to carry real traffic: Core places the Stations it can see
  are on the Tower's network there, and nowhere else hands that private address out.

  Background:
    Given Roger Core runs two instances sharing one store
    And a Tower whose session carries the "pull" capability

  # --- what counts as a private hub ----------------------------------------------------

  Scenario Outline: How Core classes the hub address a Tower advertises
    Given the Tower advertises hub address "<address>"
    Then Core classes the hub as "<class>"

    Examples:
      | address              | class    |
      | 203.0.113.7:8444     | public   |
      | hub.example:8444     | public   |
      | 192.168.1.20:8444    | private  |
      | 10.0.0.5:8444        | private  |
      | 172.16.4.2:8444      | private  |
      | [fd12:3456::20]:8444 | private  |
      | 100.64.1.9:8444      | private  |
      | 127.0.0.1:8444       | loopback |
      | [::1]:8444           | loopback |
      | (none)               | none     |

  Scenario: A name that resolves to a private address is a private hub
    Given the Tower advertises hub address "hub.lan:8444"
    And Core resolves "hub.lan" to 192.168.1.20
    Then Core classes the hub as "private"

  Scenario: A loopback hub is never handed to any Station
    Given the Tower advertises a loopback hub address
    Then no Station is placed on it, because Core cannot tell the same machine from the same network
    And `roger tower serve` tells the operator to advertise the machine's LAN address instead

  Scenario: A Tower with no hub address at all carries no Stations
    Given the Tower advertises no hub address
    Then no Station is placed on it, and `roger tower serve` says the hub must be running to carry Stations

  # --- where a private-hub Tower is -----------------------------------------------------

  Scenario: A private-hub Tower's location comes from its link's observed address
    Given the Tower advertises a private hub address
    When Core accepts its link session from public address 198.51.100.40
    Then the Tower's network bucket and continent are computed from 198.51.100.40
    And the private hub address plays no part in its location

  Scenario: The observed address is the one the front proxy recorded, never a header the Tower sets
    When a private-hub Tower's request carries a forwarded-for header naming another address
    Then Core uses the address its trusted front proxy recorded, as it does for every caller

  Scenario: A private-hub Tower whose link address changes is re-measured
    Given a private-hub Tower measured at 198.51.100.40
    When its next link request is observed from 198.51.100.77
    Then its location is re-measured from 198.51.100.77, on every instance
    And Stations placed on it under the old address are re-checked against the new one

  # --- which Stations may be placed on a private hub ------------------------------------

  Scenario Outline: Only a Station on the same network is a candidate for a private hub
    Given the Tower's link is observed from "<tower>"
    And a Station attaches observed from "<station>"
    Then the private-hub Tower <is> a placement candidate for that Station

    Examples:
      | tower                  | station                | is     |
      | 198.51.100.40          | 198.51.100.40          | is     |
      | 198.51.100.40          | 198.51.100.41          | is not |
      | 2001:db8:1:2::40       | 2001:db8:1:2::99       | is     |
      | 2001:db8:1:2::40       | 2001:db8:1:3::99       | is not |
      | 198.51.100.40          | 2001:db8:1:2::99       | is not |
      | 2001:db8:1:2::40       | 198.51.100.40          | is not |

  Scenario: An internet Station is never handed a private hub address
    Given a private-hub Tower and a Station on a different network
    When the Station attaches
    Then the attach answer never names the private hub
    And the Station is placed on its best eligible public-hub Tower, or stays direct

  Scenario: A same-network Station ranks the private-hub Tower as same_network
    Given a Station on the same network as a private-hub Tower in the same health tier as every other Tower
    When Core ranks Towers for the Station
    Then the private-hub Tower is in the same_network class, ahead of every other class

  Scenario: Health still comes first
    Given a same-network private-hub Tower in health tier B and a public-hub Tower in tier A
    When Core ranks Towers for the Station
    Then the tier A Tower wins, as placement_ranking.feature already says

  Scenario: The trust ramp applies to private-hub Towers unchanged
    Given a community private-hub Tower at stage "new" already holding 10 community Stations
    When an eleventh same-network community Station attaches
    Then it is not placed there, exactly as the stage cap says

  Scenario: House and community rules apply unchanged
    Given a house private-hub Tower and a community Station on its network
    When the community Station attaches
    Then the house and community preferences in placement_ranking.feature decide, unchanged

  # --- the Station's side ---------------------------------------------------------------

  Scenario: A private hub is handed out only with a TLS pin
    Given a private-hub Tower that serves its hub without TLS
    Then no Station is placed on it
    And `roger tower serve` tells the operator a private hub needs --hub-tls

  Scenario: The Station proves the hub before it sends anything
    Given a Station handed a private hub address with its pin
    When it connects
    Then it checks the hub's TLS key against the pin before it sends its node credentials or any request
    And a hub that fails the pin is never sent anything, and the Station reports the failure to Core

  Scenario: A Station never dials a private address Core did not hand it
    Then a private hub address reaches a Station only in Core's signed attach answer
    And a Station ignores any private hub address from any other source

  # --- health -------------------------------------------------------------------------

  Scenario: Core never dials a private hub address, and never counts that as a failure
    Given a Tower advertises a private hub address
    When its hub check is due
    Then Core does not dial the private address
    And no failed hub check is recorded for it
    And the Tower is checked by pull, as tower_pull_dispatch.feature says

  Scenario: An older Tower with a private hub address is neither checked nor failed
    Given a Tower without the "pull" capability advertising a private hub address
    When its hub check is due
    Then Core records nothing for it, no pass and no failure, and places no Station on it

  Scenario: A private-hub Tower with no Stations still builds a health record
    Given a private-hub Tower with no Stations attached
    When a day of hub check sweeps runs
    Then its pull hub checks are recorded, so it can be promoted on the same terms as any Tower

  Scenario: The canary reaches a private hub's Stations by pull
    Given a private-hub Tower with a same-network Station attached
    When Core runs a canary against that Station
    Then the canary is delivered by pull and counts exactly as any canary does

  # --- mobility -----------------------------------------------------------------------

  Scenario: A Station that leaves the Tower's network is moved off, without waiting for idle
    Given a Station placed on a private-hub Tower while on the same network
    When the Station's next request to Core is observed from a different network
    Then Core records the move trigger "left_network"
    And moves the Station to its best eligible Tower without proving it idle, because it can no longer reach the hub
    And the move follows the existing pacing, nudge and epoch rules in station_mobility.feature

  Scenario: A Tower that leaves its network takes its private hub with it
    Given a private-hub Tower with three same-network Stations
    When the Tower's link is observed from a different network and the Stations' addresses do not change
    Then all three Stations get the move trigger "left_network"

  Scenario: better_tower can move a same-network Station onto a private hub
    Given a Station on a public-hub Tower in the same_continent class
    And a private-hub Tower on the Station's own network, eligible and in the same health tier
    When three fresh observations agree
    Then the Station is moved to the private-hub Tower under the existing better_tower rules

  Scenario: A private-hub Tower is never a move target for a Station on another network
    Given a Station on a different network from a private-hub Tower
    Then no trigger ever names that Tower as the Station's destination

  # --- the operator ---------------------------------------------------------------------

  Scenario: Serve says plainly who this Tower can serve
    When an operator runs `roger tower serve --hub :8444 --hub-tls --relay-public 192.168.1.20:8444`
    Then it prints that Stations on this network will be placed here
    And that Stations elsewhere need a public address for now
    And it does not say the Tower carries nothing

  Scenario: Admins see the hub class and the network Core measured
    When an admin opens the Tower detail
    Then it shows the hub class (public, private, loopback or none) and the network bucket Core measured
    And for a private hub, how many Stations are on its network and placed there
