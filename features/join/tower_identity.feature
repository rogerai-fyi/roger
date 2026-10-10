# STATUS: APPROVED 2026-10-10 (founder)
# BUILD STATUS: NOT BUILT.
#
# A CLIENT KNOWS IT IS TALKING TO ITS TOWER. Founder ruling 2026-10-10: the Tower serves TLS with
# its own key; the client pins that key's fingerprint from the invite or from an approved pairing;
# a changed key is refused loudly with a recovery hint. This replaces "trust the LAN" for Towers
# (features/lan/trust_and_honesty.feature, ruled 2026-10-06), which narrows to plain-http Towers
# during one deprecation release.
#
# The Tower already holds a TLS key (internal/tower: tls.key, State.TLSKey). The Tower's TLS
# certificate is self-signed by that key; trust comes from the pinned fingerprint, never a CA.
Feature: The client pins the Tower's TLS key

  Background:
    Given a standalone Tower "lab" serves at 192.168.1.20:8787 over TLS with key fingerprint "sha256:9f3c...e1"

  # =====================================================================================
  # 1. THE TOWER SERVES TLS
  # =====================================================================================

  Scenario: The Tower serves TLS with a certificate from its own key
    When `roger tower serve` starts in a standalone directory
    Then it serves TLS using a certificate self-signed by the Tower's tls.key
    And `roger tower serve` prints the fingerprint it serves under

  Scenario: The Tower's fingerprint is shown where the owner looks
    Then `roger tower status`, the Tower row in the TUI and the console all show the short fingerprint
    And `roger tower status --full` shows the full fingerprint

  Scenario: Every Tower route is served over the same TLS listener
    Then /discover, /v1/models, /v1/chat/completions, /local/poll, /local/complete, /local/chunk and the join routes all answer only over that TLS listener (plain http aside, see section 4)

  # =====================================================================================
  # 2. THE CLIENT PINS AND CHECKS
  # =====================================================================================

  Scenario: A client joined by invite pins the invite's fingerprint
    When the client joins with an invite naming "sha256:9f3c...e1"
    Then lan_towers records the Tower with pin "sha256:9f3c...e1"

  Scenario: A client joined by approval pins the key it saw during the pairing
    When the client joins by request and approve
    Then lan_towers records the TLS fingerprint the SAS covered

  Scenario: Every later request checks the pin
    Given the client pinned "sha256:9f3c...e1"
    When it sends any request to the Tower
    Then the TLS handshake is accepted only if the Tower's certificate key hashes to the pin
    And no CA, hostname or expiry check replaces the pin

  Scenario: A changed key is refused loudly with a recovery hint
    Given the client pinned "sha256:9f3c...e1"
    And the device now answering at 192.168.1.20:8787 presents a different key
    When the client sends a request
    Then the request is refused before any prompt is sent
    And it prints "the Tower at 192.168.1.20:8787 is not the one you joined (its key changed). If the owner reset it, re-join with a new invite: roger join <invite>"
    And the pin is not changed

  Scenario: The pin follows the Tower, not its address
    Given the Tower moves to 192.168.1.21:8787 with the same key
    When the client re-adds it or is given a new invite
    Then the existing pin is kept and nothing is re-trusted

  Scenario: The dial marks pinned Towers as verified
    Given a pinned Tower and an Edge member are on the dial
    Then the Tower row's source carries "verified", like the Edge row

  Scenario: Station polls check the pin too
    Given a station joined with a station invite
    Then its /local/poll, /local/complete and /local/chunk calls check the Tower's pin

  # =====================================================================================
  # 3. A RESET TOWER
  # =====================================================================================

  Scenario: The owner rotates or resets the Tower's key
    When the owner runs the new tower command rotate-key
    Then the Tower serves under a new fingerprint
    And it prints that every client and station must re-join with a new invite
    And `roger tower invite` mints invites naming the new fingerprint

  # =====================================================================================
  # 4. PLAIN HTTP FOR ONE RELEASE
  # =====================================================================================

  # RULED 2026-10-10 (the recommendation below was adopted): plain http during the transition. Recommend: for ONE release `roger tower serve`
  # also answers plain http on the old port, with a warning logged per client that uses it; a
  # client that reaches a Tower over plain http shows "unverified" and the existing trust note.
  # The release after, plain http is refused unless the owner passes --allow-plain-http.
  Scenario: A plain-http Tower still works for one release, with a warning
    Given a Tower that answers only plain http (an older roger)
    When a client uses it
    Then it works as today, marked "unverified", with the one-time trust note
    And the client prints "this Tower uses plain http; ask its owner to upgrade roger so it can be verified"

  Scenario: A Tower serving both logs the clients still on plain http
    Given the Tower serves TLS and plain http
    When a client uses plain http
    Then the Tower logs once per client "laptop-x used plain http; upgrade it"

  Scenario: A pinned client never falls back to plain http
    Given the client pinned the Tower's key
    When the TLS handshake fails
    Then the client refuses and does not retry over plain http
