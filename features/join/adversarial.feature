# STATUS: PROPOSED 2026-10-10 (awaiting founder approval)
# BUILD STATUS: NOT BUILT.
#
# Every way the simple join could be abused, each a permanent scenario. Grounded in the existing
# guards this change reuses: the Edge proof (internal/edgeauth/code.go), the durable attempt
# budgets and global limiter (internal/tower/bootstrap.go ConsumeInvitation), the uniform 401 and
# replay nonce (internal/localplane authClient), and the local-address rule (internal/lanmode).
Feature: The simple join under attack

  Background:
    Given a standalone Tower "lab" at 192.168.1.20:8787 with fingerprint "sha256:9f3c...e1"

  # ----- impostor Tower -----

  Scenario: A device answering first at the Tower's address is refused
    Given a different device answers at 192.168.1.20:8787 with its own key
    When a client runs "roger join" with a valid invite for "lab"
    Then the client refuses because the key does not match the invite's fingerprint
    And it sends no code proof, no prompt and nothing else
    And the invite is not spent

  Scenario: An impostor relaying to the real Tower cannot complete the join
    Given an impostor terminates the client's TLS with its own key and forwards to the real Tower
    When the client joins with a valid invite
    Then the client refuses on the fingerprint before the proof is sent

  Scenario: A spoofed mDNS advertisement for "lab" is only a candidate
    Given a device advertises itself as Tower "lab" on the LAN
    Then it appears only as a candidate on the dial and is never used until joined
    And joining it by invite fails on the fingerprint

  # ----- impostor client -----

  Scenario: Joining without an invite or an approval is refused uniformly
    When a device posts a join request with no code and no pending approval
    Then it gets the uniform 401 and its key is not admitted

  Scenario: A wrong code does not reveal what was wrong
    When a device presents a wrong code for a real invite
    Then the answer is the same as for an unknown invite
    And the attempt counts against that invite's budget and the global limiter

  # ----- replay -----

  Scenario: Replaying a captured join request is refused
    Given a captured successful join request
    When it is sent again
    Then it is refused because the code is spent and the nonce was used
    And no second admission happens

  Scenario: A proof made for one Tower cannot be used at another
    Given a proof made with a valid invite for "lab"
    When it is presented to another Tower
    Then it does not verify because it covers "lab"'s fingerprint

  Scenario: A Tower join proof cannot be replayed as an Edge enrollment, or the reverse
    Then the Tower proof domain differs from the Edge proof domain
    And a proof from one never verifies as the other

  # ----- brute force -----

  Scenario: Guessing codes exhausts the invite and then the global limiter
    When a device sends wrong codes for one invite
    Then after the per-invite budget the invite is dead, even for the right code
    And continued guessing across invites hits the durable global limiter
    And the limits hold across restarts and across source addresses

  Scenario: A Tower invite code is not guessable within the budget
    Then a Tower invite code carries at least 128 bits

  # ----- stolen or expired invites -----

  Scenario: An invite used once cannot be used again
    Given laptop-x joined with an invite
    When someone else presents the same invite
    Then it is refused as spent and the owner is told "a used invite was presented again from 192.168.1.44"

  Scenario: A stolen invite used first is visible and revocable
    Given an attacker uses an invite before the intended client
    Then the owner sees "<attacker-name> joined" immediately
    And the new tower command revoke-client <name> removes it
    And the intended client is told the invite was already used and to ask for a new one

  Scenario: An expired invite is refused
    Given an invite minted 16 minutes ago
    When a client presents it
    Then it is refused with "that invite has expired. Ask the owner for a new one"

  Scenario: Two clients racing one invite: exactly one wins
    When two clients present the same valid invite at the same moment
    Then exactly one is admitted and the other is told the invite was already used

  # ----- MITM on approve -----

  Scenario: A middle party during request-and-approve yields different phrases
    Given a middle party substitutes its own keys during a pairing
    Then the client's phrase and the owner's phrase differ

  Scenario: Approving a mismatched pairing does not give the middle party the real Tower
    Given the owner approved despite different phrases
    Then the client's pin is the middle party's key
    And the client refuses the real Tower's different key on the next direct contact, telling the user to re-join

  # ----- flood -----

  Scenario: A flood of join requests does not reach the owner's screen
    When thousands of join requests arrive from many addresses
    Then at most the pending cap is shown to the owner
    And per-address rate limits refuse the rest with a retry-after
    And serving clients is not slowed

  Scenario: Oversized or malformed join requests are refused cheaply
    When a join request is oversized, malformed or has unknown fields
    Then it is refused before any proof is computed

  # ----- downgrade -----

  Scenario: A pinned client refuses a downgrade to plain http
    Given the client pinned "lab"
    When an attacker makes the TLS port fail so only plain http answers
    Then the client refuses and does not send over plain http

  Scenario: An invite never joins over plain http
    When the client joins with an invite and only plain http answers
    Then the join is refused, because an invite join always verifies the fingerprint over TLS

  Scenario: The admin socket is not reachable from the network
    Then admin.sock is a local socket in the data directory only, never a TCP listener

  # ----- addresses -----

  Scenario: An invite naming a public address is refused before connecting
    Given an invite whose address is public
    When the client runs "roger join" with it
    Then it is refused by the local-address rule with no connection made

  Scenario: An invite naming a hostname that resolves to a public address is refused
    Given an invite whose address is a hostname resolving to a public address
    Then it is refused, re-checked at connect time, as lanmode requires
