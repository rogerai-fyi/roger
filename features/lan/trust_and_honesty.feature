# STATUS: APPROVED 2026-10-06 (founder)
#
# LAN MODE - TRUST AND HONESTY: trust the LAN, and say plainly what that means.
#
# FOUNDER RULING 2026-10-06: Tower trust is "trust the LAN". A client does not pin a standalone
# Tower's identity. The consequence, stated honestly because a user turning on LAN mode is often
# doing it because the data matters: a standalone Tower speaks plain http with no server identity
# (the client never authenticates it). Any device on the same network that can answer at the
# Tower's address, or redirect traffic to itself, can impersonate the Tower, list fake bands, and
# read and alter every prompt and answer sent to it.
#
# What is NOT weakened: Edge members keep mutual TLS with fingerprint pinning, membership under
# this Edge's root, and revocation (features/edge/discovery.feature, local_inference.feature).
# The client's request signature and per-request nonce to a Tower stay (they protect the Tower
# from replay; they do not authenticate the Tower to the client).
#
# GROUND TRUTH: internal/client/identity.go (signRequest, targetsLANTower, the nonce),
# internal/edge/verify.go (VerifyPeer), cmd/rogerai/localshare.go (the fingerprint probe),
# web docs (the manual's LAN section, new).
#
# Tags: @cli, @tui, @client, @edge, @docs (web/src manual pages + their fixtures).

Feature: LAN mode trusts the local network for Towers, says so once where it matters, and keeps the Edge's verification

  Background:
    Given LAN mode is on

  @cli
  Scenario: the first use of a plain-http LAN Tower states the exposure once
    Given lan_towers contains "192.168.1.20:8787"
    And this machine has never been told the LAN trust note
    When they run "roger --lan use llama-3.3-70b" and the Tower serves it
    Then the output states that a Tower on plain http can be impersonated by any device on this network and can read what is sent to it
    And it says Edge members are verified by certificate and are not affected
    And the config records that the note was shown

  @cli
  Scenario: the trust note is not repeated on every command
    Given the config records that the LAN trust note was shown
    When they run "roger --lan use llama-3.3-70b"
    Then the trust note is not printed

  @tui
  Scenario: adding a Tower in the TUI shows the trust note in the confirm
    When the owner adds a candidate Tower from the dial
    Then the confirm states the plain-http exposure in one sentence

  @tui
  Scenario: a Tower row is marked as unverified, an Edge row as verified
    Given the dial lists a band from the LAN Tower at 192.168.1.20:8787 and one from the Edge member "jetson"
    When the dial renders
    Then the Tower row's source carries "unverified"
    And the Edge row's source carries "verified"
    And the difference is readable with NO_COLOR

  @docs
  Scenario: the manual's LAN section states the trust model in plain words
    When the manual's LAN mode section is read
    Then it says LAN mode filters bands to local sources and is not an offline mode
    And it says Towers are trusted because they are on the LAN, with the plain-http exposure
    And it says Edge members are verified by certificate
    And it says how to keep sensitive work on Edge members only
    # RULED 2026-10-06: yes, offer `roger --lan --edge-only` (Edge members and this machine, no Towers) for
    # sensitive work. Recommendation: yes; it is the strict subset that needs no trust in the LAN.

  @client
  Scenario: the client still signs and nonces every request to a LAN Tower
    When a turn is sent to the LAN Tower at 192.168.1.20:8787
    Then the request carries this machine's signature
    And it carries a fresh X-Roger-Nonce
    And a captured copy replayed to the Tower is refused by the Tower

  @edge
  Scenario: Edge members are verified exactly as without LAN mode
    Given the Edge member "jetson" presents a certificate whose fingerprint does not match its advertisement
    When the dial is read
    Then "jetson" is not listed as a source
    And the refusal reason is the same as features/edge/discovery.feature gives

  @edge
  Scenario: a spoofed Edge advertisement is still refused in LAN mode
    Given an advertisement claims the node id of "jetson" with a different certificate
    When the dial is read
    Then the spoofed record is not a source
    And "jetson"'s real record is unaffected

  @edge
  Scenario: a peer enrolled to a different account is never a source in LAN mode
    Given a peer on the LAN is enrolled to a different account and serves "qwen3-8b"
    When the dial is read
    Then it is not listed as a source

  @client
  Scenario: LAN mode never falls back silently, and says so
    Given nothing local serves "gpt-oss-120b"
    When a turn is dispatched for "gpt-oss-120b"
    Then the refusal says LAN mode is on and the market was not tried
    And nothing in any surface shows the turn as served
