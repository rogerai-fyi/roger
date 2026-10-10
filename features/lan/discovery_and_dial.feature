# STATUS: APPROVED 2026-10-06 (founder)
#
# LAN MODE - DISCOVERY AND THE DIAL: three local sources, one dial, and every row says where it is.
#
# THE THREE SOURCES (founder ruling 2026-10-06):
#   1. THIS MACHINE    `roger model serve` (internal/localmodel), loopback only.
#   2. EDGE MEMBERS    nodes enrolled under this Edge's root, found by mDNS and verified by
#                      certificate (features/edge/discovery.feature). Rung 2 of the ladder
#                      (features/edge/local_inference.feature).
#   3. LAN TOWERS      standalone Towers (`roger tower serve`, internal/localplane) at a LOCAL
#                      address. Their /discover requires a signed, admitted client
#                      (features/tower/standalone_consumer_plane.feature).
#
# WHAT "LOCAL" MEANS FOR A TOWER ADDRESS (the single predicate every scenario below uses):
#   An address is LOCAL when the IP the connection actually goes to is one of:
#     - loopback          127.0.0.0/8, ::1
#     - RFC 1918          10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
#     - IPv6 ULA          fc00::/7
#     - link-local        169.254.0.0/16, fe80::/10 (with its zone)
#   It is the SAME classification internal/localplane already uses for bind and adverts
#   (IsPrivateAddr), plus link-local; one predicate, not a second copy.
#   # corrected 2026-10-06 (founder-approved): IsPrivateAddr already covers loopback; the
#   # client's addition is link-local, which the plane deliberately does not bind to.
#   A HOSTNAME is local when EVERY address it resolves to is local, checked again at EVERY
#   connection (a name that resolved local yesterday and public today is public today).
#   CGNAT 100.64.0.0/10 is NOT local by default.
#   # RULED 2026-10-06: CGNAT is not local by default (it is ISP shared space, and a
#   # carrier-NATed neighbour is not your LAN), but an address the owner adds EXPLICITLY with
#   # `roger lan add` is accepted with a one-line note that it is a carrier/overlay range (mesh
#   # VPN overlays commonly use it). Discovery never yields a CGNAT Tower on its own.
#
# HOW A TOWER IS FOUND IN LAN MODE:
#   (a) the `lan_towers` list in config, managed by `roger lan add|rm|list`;
#   (b) the existing `broker` setting, when it is a local address that answers the standalone
#       fingerprint (today's isLocalTowerBroker probe: /local/poll -> exactly the uniform 401);
#   (c) mDNS: a standalone Tower may advertise itself on the LAN, and LAN mode lists what it
#       hears as CANDIDATES the owner adds with one action. It is never used until added.
#   # RULED 2026-10-06 (command superseded 2026-10-07, one binary): a Tower may advertise, OFF by default on
#   # the Tower (`roger tower serve --advertise`), private interfaces only, and candidates
#   # are never dialed for inference until the owner adds them (one keystroke in the TUI, one
#   # command in the CLI). This keeps the approved standalone scenario "the Tower's own
#   # advertisements never leave the machine" true by default; it would need a superseded marker
#   # ONLY for the opt-in case, and only for the LAN (never the public network).
#
# GROUND TRUTH: internal/client/failover.go (client.discover with the signed retry on 401),
# internal/tui/tui.go (fetchOffers: unsigned, no retry), cmd/rogerai/localshare.go
# (isLocalTowerBroker), internal/localplane/bind.go (IsPrivateAddr), internal/edge (mDNS, the
# fleet, `roger edge bands`), internal/localmodel (serving records).
#
# Tags: @client in internal/client, @cli in cmd/rogerai, @tui in internal/tui, @console in
# internal/webui, @tower in internal/localplane (+ `roger tower serve`), @edge in internal/edge.

Feature: In LAN mode the dial lists only bands served on this machine, by this Edge, or by a Tower at a local address, and every row names its source

  Background:
    Given LAN mode is on

  # =========================================================================
  # 1. THE PREDICATE: WHAT COUNTS AS LOCAL
  # =========================================================================

  @client
  Scenario Outline: an address is local or not by one rule
    When the address "<address>" is classified
    Then it is "<verdict>"

    Examples:
      | address              | verdict   |
      | 127.0.0.1:8787       | local     |
      | [::1]:8787           | local     |
      | 10.0.0.5:8787        | local     |
      | 172.16.0.9:8787      | local     |
      | 172.31.255.254:8787  | local     |
      | 172.32.0.1:8787      | not local |
      | 192.168.1.69:8787    | local     |
      | [fd12:3456::1]:8787  | local     |
      | 169.254.10.10:8787   | local     |
      | [fe80::1%eth0]:8787  | local     |
      | 100.64.0.1:8787      | not local |
      | 8.8.8.8:8787         | not local |
      | 0.0.0.0:8787         | not local |
      | [::]:8787            | not local |
      | 255.255.255.255:8787 | not local |
      | 224.0.0.251:5353     | not local |

  @client
  Scenario: a hostname is local only when every address it resolves to is local
    Given "tower.lan" resolves to 192.168.1.20 and 10.0.0.4
    Then "tower.lan" is local
    Given "tower.lan" resolves to 192.168.1.20 and 203.0.113.7
    Then "tower.lan" is not local

  @client
  Scenario: a hostname is re-checked at every connection, never cached as local
    Given "tower.lan" resolved to 192.168.1.20 at the last refresh
    And "tower.lan" now resolves to 203.0.113.7
    When the dial refreshes
    Then "tower.lan" is not dialed
    And its row is shown as refused, naming "resolves to a public address"

  # corrected 2026-10-06 (founder-approved); superseded 2026-10-07 by founder ruling (one binary):
  # the plane's private-address set now lives in internal/netpriv (IsHome), plus link-local.
  @client
  Scenario: the predicate is the one the plane already uses, not a second copy
    Then the client's check is the private-address check (netpriv.IsHome) plus link-local, for every address in the classification table

  # =========================================================================
  # 2. FINDING TOWERS
  # =========================================================================

  @cli
  Scenario: `roger lan add` records a Tower at a local address
    When they run "roger lan add 192.168.1.20:8787"
    Then the config's lan_towers list contains "192.168.1.20:8787"
    And the output says it was added and whether it answered as a standalone Tower

  @cli
  Scenario: `roger lan add` refuses a public address, naming the rule
    When they run "roger lan add 203.0.113.7:8787"
    Then it exits non-zero
    And the error says LAN mode only uses local addresses and lists the local ranges
    And lan_towers is unchanged

  @cli
  Scenario: `roger lan add` of a CGNAT address is accepted with a note
    When they run "roger lan add 100.64.3.9:8787"
    Then lan_towers contains "100.64.3.9:8787"
    And the output notes it is a carrier/overlay range that LAN mode uses only because it was added by hand

  @cli
  Scenario: `roger lan add` of an address that answers but is not a standalone Tower is refused
    Given 192.168.1.30:8787 answers HTTP but /local/poll does not return the standalone fingerprint
    When they run "roger lan add 192.168.1.30:8787"
    Then it exits non-zero naming that the address is not a standalone Tower
    And lan_towers is unchanged

  @cli
  Scenario: `roger lan add --offline` records an address that does not answer yet
    Given nothing is listening at 192.168.1.40:8787
    When they run "roger lan add 192.168.1.40:8787 --offline"
    Then lan_towers contains "192.168.1.40:8787"
    And the output says it did not answer and will be tried at each refresh

  @cli
  Scenario: `roger lan list` and `roger lan rm`
    Given lan_towers contains "192.168.1.20:8787" and "192.168.1.40:8787"
    When they run "roger lan list"
    Then each Tower is listed with whether it answered and how many bands it serves
    When they run "roger lan rm 192.168.1.40:8787"
    Then lan_towers contains only "192.168.1.20:8787"

  @cli
  Scenario: `roger lan add` of a duplicate is a no-op, not a second entry
    Given lan_towers contains "192.168.1.20:8787"
    When they run "roger lan add 192.168.1.20:8787"
    Then lan_towers contains "192.168.1.20:8787" once

  @client
  Scenario: the broker setting counts as a LAN Tower when it is local and answers the fingerprint
    Given the config's broker is "http://192.168.1.20:8787"
    And that address answers the standalone fingerprint
    And lan_towers is empty
    When the dial is read
    Then the bands of the Tower at 192.168.1.20:8787 are listed

  @client
  Scenario: a public broker setting is never a band source in LAN mode
    Given the config's broker is the public broker
    When the dial is read
    Then no request is made to the public broker's /discover
    And no market band is listed

  @tower
  Scenario: a standalone Tower advertises on the LAN only when asked to
    Given a standalone Tower started without --advertise
    Then it sends no mDNS advertisement
    Given a standalone Tower started with --advertise
    Then it advertises on private interfaces only
    And the advertisement carries its address and a label, and no account, key or client list

  @client @tui
  Scenario: an advertised Tower is a candidate, never used until it is added
    Given a standalone Tower at 192.168.1.50:8787 advertises on the LAN
    And it is not in lan_towers
    When the dial is read
    Then its bands are not listed as usable
    And the dial shows one candidate line naming it and how to add it
    And no inference is ever sent to 192.168.1.50:8787

  @tui
  Scenario: adding a candidate Tower from the TUI is one keystroke, and asked first
    Given the dial shows the candidate Tower at 192.168.1.50:8787
    When the owner selects it and presses the add key
    Then the TUI asks to confirm, showing the address and the trust note
    When the owner confirms
    Then lan_towers contains "192.168.1.50:8787"
    And its bands appear on the dial

  @client
  Scenario: a candidate advertising a public address is not offered at all
    Given an mDNS advertisement names a Tower at 203.0.113.7:8787
    When the dial is read
    Then no candidate is shown for it

  # =========================================================================
  # 3. THE MERGED DIAL
  # =========================================================================

  @client @tui
  Scenario: the dial merges the three sources and nothing else
    Given this machine serves "qwen3-8b"
    And the Edge member "jetson" serves "gpt-oss-20b", VERIFIED, LAN-direct
    And the LAN Tower at 192.168.1.20:8787 serves "llama-3.3-70b"
    And the market has 40 bands on air
    When the dial is read
    Then it lists exactly "qwen3-8b", "gpt-oss-20b" and "llama-3.3-70b"
    And it lists no market band

  @tui
  Scenario: every row names its source
    Given the same three sources
    When the dial renders at 100 columns
    Then the "qwen3-8b" row's source reads "this machine"
    And the "gpt-oss-20b" row's source reads "edge: jetson"
    And the "llama-3.3-70b" row's source reads "tower: 192.168.1.20"

  @tui
  Scenario: the dial in LAN mode carries no price or tier columns that mean nothing here
    When the dial renders at 100 columns
    Then no row shows a $/1M price
    And each row shows "free"

  @client
  Scenario: one band served by two sources is one row listing both
    Given this machine and the Edge member "jetson" both serve "qwen3-8b"
    When the dial is read
    Then there is one "qwen3-8b" row
    And its sources read "this machine, edge: jetson"

  @client
  Scenario: the same Tower reached through two configured spellings is one source
    Given lan_towers contains "tower.lan:8787" and "192.168.1.20:8787"
    And "tower.lan" resolves to 192.168.1.20
    When the dial is read
    Then the Tower's bands appear once
    And `roger lan list` notes the two entries name the same Tower

  @client
  Scenario: an Edge member that also runs a standalone Tower is not double-counted
    Given the Edge member "shed" serves "qwen3-8b" and also runs a standalone Tower at its LAN address serving "qwen3-8b"
    When the dial is read
    Then there is one "qwen3-8b" row
    And its sources read "edge: shed, tower: <shed's address>"

  @client
  Scenario: a CLAIMED Edge serve is not a dial row
    Given the Edge member "bench" claims serve for "qwen3-8b" but is not VERIFIED
    And nothing else serves it
    When the dial is read
    Then "qwen3-8b" is not listed
    # The same rule as features/edge/local_inference.feature: a claim is not a rung.

  @client
  Scenario: a DARK Edge member's bands are not listed as usable
    Given the Edge member "jetson" serves "gpt-oss-20b" but is DARK
    When the dial is read
    Then "gpt-oss-20b" is not listed as usable

  @client
  Scenario: a relayed-only Edge member is not a LAN source
    Given the Edge member "shed" is reachable only through a relay
    When the dial is read
    Then none of its bands are listed
    # LAN mode means LAN-direct; a relay is the network, not the LAN.

  # =========================================================================
  # 4. TOWER REFRESH, FAILURE AND DEPARTURE
  # =========================================================================

  @client
  Scenario: the dial reads a LAN Tower's /discover signed, as the CLI already does
    Given the LAN Tower at 192.168.1.20:8787 admits this machine's key
    When the dial refreshes
    Then the request to /discover carries this machine's signature
    And the Tower's bands are listed
    # Regression: TUI fetchOffers fetched unsigned with no retry and drew a Tower as "0 on air".

  @client
  Scenario: a Tower that has not admitted this machine is shown as refusing, not as empty
    Given the LAN Tower at 192.168.1.20:8787 does not admit this machine's key
    When the dial refreshes
    Then the Tower's line reads "not admitted"
    And it names the simple way in, an invite then roger join, with no key hash (superseded 2026-10-10 by features/join/surfaces.feature)
    And it is not drawn as "0 on air"

  @client
  Scenario: a Tower that stops answering is drawn as gone, not silently removed
    Given the LAN Tower at 192.168.1.20:8787 served "llama-3.3-70b" at the last refresh
    And it now refuses the connection
    When the dial refreshes
    Then "llama-3.3-70b" is no longer listed as usable
    And the Tower's line reads "not answering" with when it was last seen

  @client
  Scenario: a Tower that comes back is the same source
    Given the LAN Tower at 192.168.1.20:8787 was "not answering"
    When it answers again at the next refresh
    Then its bands are listed again under the same source

  @client
  Scenario: a slow Tower does not hold the dial hostage
    Given the LAN Tower at 192.168.1.20:8787 takes 30 seconds to answer /discover
    When the dial refreshes
    Then the other sources' bands are listed without waiting for it
    And the slow Tower's line reads "waiting" until it answers or times out

  @client
  Scenario: a Tower's band list is bounded
    Given the LAN Tower at 192.168.1.20:8787 answers /discover with 100000 offers
    When the dial refreshes
    Then at most the dial's band cap is read from that Tower
    And the line notes the Tower listed more than LAN mode shows
    And the response body read is capped

  # =========================================================================
  # 5. THE EMPTY LAN IS A STATUS
  # =========================================================================

  @tui
  Scenario: an empty LAN says what was looked at, never just "nothing"
    Given this machine serves nothing
    And this Edge has no member serving a band
    And lan_towers is empty and the broker setting is not local
    When the dial renders
    Then it says no local band is on air
    And it says what it checked: this machine, the Edge, and Towers
    And it names the three ways to get one: serve a model here, add an Edge member, add a Tower
    And it does not show market bands as a fallback

  @tui
  Scenario: an empty LAN whose sources could not be read says so
    Given the Edge record cannot be read
    When the dial renders
    Then it says the Edge could not be read, with the reason
    And it does not claim the Edge serves nothing
    # features/edge/empty_edge.feature: unread is unread.

  @cli
  Scenario: `roger --lan search` lists only local bands with their source
    Given the same three sources as the merged-dial scenario
    When they run "roger --lan search"
    Then the table lists the three local bands and a source column
    And no request is made to the public broker

  @cli
  Scenario: `roger --lan search --json` carries the source per band
    When they run "roger --lan search --json"
    Then each band carries a "sources" list whose entries have a "kind" of "self", "edge" or "tower" and an "id"

  @console
  Scenario: the console's band list in LAN mode is the same merged list
    Given "roger --lan webui" was run with the same three sources
    When the console's band list is read
    Then it is the same three bands with the same sources the TUI dial shows

  @tui
  Scenario: Ping World in LAN mode shows the LAN, not the world
    Given LAN mode is on
    When the Ping World screen opens
    Then no request is made to the public broker's /discover
    And it draws the local sources only, or says it has nothing local to draw
    # RULED 2026-10-06: Ping World in LAN mode draws only local sources; it is a
    # discovery view and the ruling is that LAN mode filters what is discovered.
