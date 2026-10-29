# STATUS: APPROVED 2026-10-06 (founder)
#
# LAN MODE - ADVERSARIAL AND KNOWN-VULNERABLE CORNERS.
#
# Within the trust-the-LAN ruling, these are the ways LAN mode could still be made to send a
# turn somewhere that is not local, or to fall over. Each is a permanent regression scenario.
# The ruling accepts that a device ON the LAN can impersonate a Tower; it does not accept a
# path that sends a turn OFF the LAN.
#
# Tags: @client, @cli, @tui, @edge, @tower.

Feature: Nothing, configured or discovered, makes LAN mode send a turn off the local network

  Background:
    Given LAN mode is on

  @client
  Scenario: a public address written into lan_towers by hand is never dialed
    Given the config file's lan_towers was edited by hand to contain "203.0.113.7:8787"
    When the dial refreshes
    Then 203.0.113.7 is not contacted
    And `roger lan list` shows the entry as refused, naming the rule

  @client
  Scenario: a hostname that flips from local to public between refresh and turn is not dialed
    Given "tower.lan" resolved to 192.168.1.20 at the last dial refresh
    And it resolves to 203.0.113.7 when the turn is dispatched
    When a turn is dispatched to the Tower "tower.lan"
    Then the connection is not made
    And the turn is refused naming "resolves to a public address"
    # The check is at connect time on the resolved IP, so DNS rebinding cannot move a turn off
    # the LAN between the check and the dial.

  @client
  Scenario: a LAN Tower that redirects is not followed off the LAN
    Given the LAN Tower at 192.168.1.20:8787 answers a completion with a 307 to "https://203.0.113.7/v1/chat/completions"
    When a turn is dispatched to it
    Then the redirect is not followed
    And the turn is refused naming the redirect

  @client
  Scenario: a LAN Tower that redirects to another LAN address is not followed either
    Given the LAN Tower at 192.168.1.20:8787 answers with a 307 to "http://192.168.1.21:8787/v1/chat/completions"
    When a turn is dispatched to it
    Then the redirect is not followed
    # A source is an address the owner chose or added; a redirect is not an addition.

  @client
  Scenario: a mixed config with a public broker and --lan never uses the broker for inference
    Given the config's broker is the public broker and lan_towers contains "192.168.1.20:8787"
    When the TUI is opened with --lan, a band is tuned, chat is sent, an agent turn runs and a guest operator sends a completion
    Then no request is made to the public broker's /discover, /v1/chat/completions, /v1/models, /bands/resolve or /balance

  @client
  Scenario: ROGER_BROKER pointing at the public broker does not leak inference in LAN mode
    Given ROGER_BROKER is the public broker and ROGER_LAN is "1"
    When a turn is dispatched for a band only the market serves
    Then it is refused
    And no request is made to the public broker

  @tui
  Scenario: a flood of Tower advertisements is bounded
    Given 10000 distinct mDNS Tower advertisements arrive within a minute
    When the dial renders
    Then at most the candidate cap is shown
    And the line notes how many more were heard
    And memory held for candidates is bounded

  @edge
  Scenario: a flood of Edge advertisements is bounded as features/edge/discovery.feature already requires
    Given a hostile LAN floods Edge advertisements
    Then the Edge discovery ceiling applies unchanged in LAN mode

  @client
  Scenario: a LAN device answering the standalone fingerprint is a Tower under trust-the-LAN, and is shown as unverified
    Given a device at 192.168.1.66:8787 answers the standalone fingerprint but is not the owner's Tower
    And the owner added it
    When the dial renders
    Then its bands are listed with the source marked "unverified"
    # Accepted by the ruling and stated in trust_and_honesty.feature. The scenario pins that it
    # is never drawn as verified.

  @client
  Scenario: a Tower /discover listing an absurd or hostile offer cannot break the dial
    Given the LAN Tower answers /discover with an offer whose model name is 100000 characters and contains control characters
    When the dial renders
    Then the model name is truncated and control characters are stripped
    And the other rows render normally

  @client
  Scenario: a Tower answering with non-JSON is drawn as an error, not as empty
    Given the LAN Tower answers /discover with "<html>"
    When the dial refreshes
    Then its line reads "bad answer"
    And it is not drawn as "0 on air"

  @client
  Scenario: an IPv6 link-local Tower without a zone is refused with a reason
    When they run "roger lan add [fe80::1]:8787"
    Then it exits non-zero, saying a link-local IPv6 address needs its interface, for example "[fe80::1%eth0]:8787"

  @client
  Scenario: an IPv4-mapped IPv6 address is classified by the IPv4 it maps
    Then "[::ffff:203.0.113.7]:8787" is not local
    And "[::ffff:192.168.1.20]:8787" is local

  @client
  Scenario: an unspecified or broadcast address is never a Tower
    When they run "roger lan add 0.0.0.0:8787"
    Then it exits non-zero naming the rule

  @tower
  Scenario: a standalone Tower's advertisement never reaches a public interface
    Given a standalone Tower started with --advertise on a machine with a public and a private interface
    Then the advertisement is sent on the private interface only
