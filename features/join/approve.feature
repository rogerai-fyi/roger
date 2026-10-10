# STATUS: APPROVED 2026-10-10 (founder)
# BUILD STATUS: NOT BUILT.
#
# REQUEST AND APPROVE: no copy-paste at all. The client asks to join by address; the owner's TUI or
# console shows "laptop-x wants to join" with a short phrase; the client shows the same phrase; the
# owner compares and presses y. Founder ruling 2026-10-10: built NOW, beside the invite.
#
# THE PHRASE IS A REAL SAS (short authentication string), not decoration. It is derived from a
# transcript that covers BOTH parties' long-term public keys (the client's signing key K and the
# Tower's TLS key T as actually presented on this connection), both parties' ephemeral X25519 keys,
# and both parties' fresh nonces, with a commit-then-reveal order (the client commits to its nonce
# before it sees the Tower's), as in Bluetooth numeric comparison. A party in the middle that swaps
# any key cannot make the two screens show the same phrase except by guessing, and it gets exactly
# one guess per pairing.
#
# It only protects when the owner compares. The screens say so in one line.
Feature: Request and approve, with a matching phrase

  Background:
    Given a standalone Tower "lab" is serving at 192.168.1.20:8787 over TLS
    And the owner has the Tower's TUI open

  # =====================================================================================
  # 1. THE HAPPY PATH
  # =====================================================================================

  Scenario: A client asks to join by address and the owner approves
    When the client runs "roger join 192.168.1.20:8787"
    Then the client prints "asking lab to let you in. Check this phrase matches the owner's screen: amber-river-71"
    And the owner's TUI shows "laptop-x wants to join · amber-river-71 · y approves, n refuses"
    When the owner presses y
    Then the Tower admits the client's key
    And the client pins the Tower's TLS fingerprint as seen during the pairing
    And the client prints "joined the Tower lab" and one next step

  Scenario: The console can approve the same request
    Given the owner has the web console open and no TUI
    When the client asks to join
    Then the console shows the request with its phrase and Approve and Refuse buttons
    When the owner presses Approve
    Then the client joins

  Scenario: A station can ask to join the same way
    When a station runs "roger join 192.168.1.20:8787 --station"
    Then the owner sees "kitchen-pi (a station) wants to join · <phrase> · serving: qwen3-8b"
    When the owner presses y
    Then the station is attached with its self-reported models

  Scenario: An Edge authority can approve a device the same way
    Given an Edge authority with its TUI open
    When a device runs "roger join <authority address>"
    Then the authority shows the device's request with a phrase
    When the owner presses y
    Then the device enrolls, and its root is the one the phrase covered

  Scenario: The owner refuses
    When the client asks to join and the owner presses n
    Then the client prints "lab refused the request"
    And the client's key is not admitted and nothing is pinned

  # =====================================================================================
  # 2. THE PHRASE IS A REAL SAS
  # =====================================================================================

  Scenario: The phrase covers both long-term keys, both ephemeral keys and both nonces
    When a pairing runs
    Then the phrase is derived from a hash of the transcript (domain "join-sas/v1", K, T, both ephemeral X25519 public keys, the client's committed nonce, the Tower's nonce)
    And the client's nonce commitment is sent before the Tower's nonce is received

  # RULED 2026-10-10 (the recommendation below was adopted): phrase size. Recommend three words from a fixed 2048-word list plus two digits
  # (about 40 bits), shown the same on both screens. A middle party gets one guess per pairing.
  Scenario: The phrase has a fixed, readable form
    When a pairing runs
    Then both screens show the phrase as three words and two digits joined by hyphens
    And the words come from a fixed list shipped with roger, the same on every platform

  Scenario: A party in the middle makes the phrases differ
    Given a device between the client and the Tower terminates both connections with its own keys
    When the client asks to join
    Then the phrase on the client differs from the phrase on the owner's screen
    And if the owner presses y anyway, the client's pin is the middle device's key and the pin is refused on the next direct contact with the real Tower

  Scenario: The nonce commitment stops a middle party from steering the phrase
    Given a middle party that waits to see the client's nonce before choosing its own
    When the pairing runs
    Then the client refuses because the reveal does not match the commitment, or the phrases differ

  Scenario: A phrase is never reused
    When the same client asks to join twice
    Then the two pairings show different phrases

  # =====================================================================================
  # 3. LIMITS: TIMEOUTS, ONE PENDING, RATE LIMITS, NO OWNER
  # =====================================================================================

  # RULED 2026-10-10 (the recommendation below was adopted): request lifetime. Recommend 2 minutes.
  Scenario: An unanswered request expires
    When a client asks to join and nobody answers for 2 minutes
    Then the client prints "lab did not answer in 2 minutes. Ask the owner to open roger, or use an invite"
    And the request disappears from the owner's screen

  Scenario: A request while no owner screen is open waits, then expires
    Given the Tower runs headless with no TUI or console open
    When a client asks to join
    Then the request waits as pending
    And the new tower command requests, run on the Tower, lists it with its phrase
    And the new tower command approve <id> approves it from the CLI
    And if nobody acts within 2 minutes it expires

  Scenario: One pending request per client
    Given laptop-x has a pending request
    When laptop-x asks again
    Then the earlier request is replaced, not added, and the new one gets a new phrase

  # RULED 2026-10-10 (the recommendation below was adopted): pending cap and rate. Recommend at most 8 pending requests per Tower and at most
  # 5 requests per source address per minute.
  Scenario: The Tower caps how many requests can wait
    Given 8 requests are already pending
    When a ninth device asks to join
    Then it is told "lab is busy with other join requests, try again in a minute"
    And it is not shown to the owner

  Scenario: Requests from one address are rate limited
    When one address sends 6 join requests within a minute
    Then the sixth is refused with a retry-after
    And none beyond the cap reach the owner's screen

  Scenario: A request carries no secret and grants nothing until approved
    When a client asks to join
    Then nothing is admitted and nothing is pinned until the owner approves
    And a refused or expired request leaves no trace beyond a log line

  # =====================================================================================
  # 4. HONEST SCREENS
  # =====================================================================================

  Scenario: Both screens say the phrase must match
    When a request is shown
    Then the owner's line says "approve only if laptop-x shows the same phrase"
    And the client's line says "the owner should see the same phrase"

  Scenario: Approving works offline
    Given neither machine can reach the internet
    When a client asks and the owner approves
    Then the client joins and nothing contacts Roger Core
