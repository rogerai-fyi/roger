# STATUS: APPROVED 2026-10-10 (founder)
# BUILD STATUS: PARTIAL. Built: minting, client and Station joins over pinned TLS, wrong-kind, list/void (TestJoinInviteBDD).
#
# ONE INVITE, ONE PASTE: joining a Tower (as a client or a Station) or an Edge (as a device) takes
# one owner action and one pasted command. Founder rulings 2026-10-10:
# - ONE verb, `roger join <invite>`, for Edge devices and Towers alike; the invite names its kind.
#   `roger lan join` and `roger edge enroll --invite` stay as aliases.
# - The owner mints the invite while the Tower keeps serving (see admin_socket.feature), from the
#   CLI, the TUI or the console. It prints `roger join <invite>` and a terminal QR.
# - The invite carries the address(es), the identity fingerprint (the Tower's TLS key, or the Edge
#   root) and a one-time code: bearer, single use, 15 minutes, guess-limited.
# - The Tower admits on the code: there is no separate `admit` step. Stations use the same invite
#   and report their own models.
# - Everything works with no internet and never contacts Roger Core.
#
# REUSE, NOT A PARALLEL PATH: the code format, the stretched proof and the answer check are the
# Edge enrollment primitives (internal/edgeauth/code.go: NewCode, CanonicalCode, RootProof,
# VerifyRootProof, VerifyAnswer, ParsePin, ShortFingerprint), generalized with a domain string so a
# Tower proof can never be replayed as an Edge proof or the reverse. The Tower side reuses the
# existing durable invitation machinery in internal/tower/bootstrap.go (CreateInvitation,
# ConsumeInvitation, IsAdmitted, the per-invitation and global attempt budgets) with the client
# key bound at consume time instead of at mint time.
#
# Design: docs/simple-lan-join-design.md
Feature: One invite, one paste, for Towers and Edge devices

  Background:
    Given a standalone Tower "lab" is serving at 192.168.1.20:8787 over TLS with its own key
    And the Tower's identity fingerprint is "sha256:9f3c...e1"
    And the owner is signed in to nothing (no Roger Core account is needed)

  # =====================================================================================
  # 1. MINTING AN INVITE
  # =====================================================================================

  Scenario: The owner mints a client invite with one command while the Tower keeps serving
    When the owner runs "roger tower invite"
    Then it prints one line starting "roger join " followed by the invite
    And it prints a terminal QR code of the same invite
    And it says the invite is valid for 15 minutes and works once
    And `roger tower serve` is still running and still answering clients
    And no restart and no client id were needed

  Scenario: The invite names its kind, its addresses, the identity fingerprint and the code
    When the owner runs "roger tower invite"
    Then the invite decodes to kind "tower-client"
    And it carries every local address the Tower is serving on, in the Tower's preference order
    And it carries the full identity fingerprint "sha256:9f3c...e1", never the short form
    And it carries a one-time code

  # RULED 2026-10-10 (the recommendation below was adopted): the invite scheme. Recommend one scheme for every kind:
  #   roger://join?kind=<tower-client|tower-station|edge>&at=<host:port>[&at=...]&fp=sha256:<hex>&code=<code>
  # with the existing "rogerai-edge://join?..." Edge invites still accepted as an alias.
  Scenario Outline: Every invite kind uses one scheme
    When the owner mints a "<kind>" invite
    Then it starts "roger://join?kind=<kind>"
    And it parses back to the same kind, addresses, fingerprint and code

    Examples:
      | kind          |
      | tower-client  |
      | tower-station |
      | edge          |

  Scenario: An Edge invite from before this change is still accepted
    Given an invite of the form "rogerai-edge://join?at=192.168.1.69:39009&root=sha256:...&code=..."
    When a device runs "roger join" with it
    Then it is read as kind "edge" and the join proceeds as an Edge enrollment

  # RULED 2026-10-10 (the recommendation below was adopted): code length for Tower invites. The approved modes.feature:194 requires a Tower
  # bootstrap code to carry at least 128 bits. Edge codes are 50 bits because people TYPE them.
  # A Tower invite is pasted or scanned, never typed. Recommend: Tower invite codes are 128-bit
  # (26 Crockford base32 symbols), Edge codes stay 50-bit, and both use the same proof package.
  Scenario: A Tower invite code carries at least 128 bits from the system's secure random source
    When the owner mints a Tower invite
    Then its code carries at least 128 bits from the operating system's cryptographic random source
    And the Tower stores only a verifier of the code, never the code itself
    And the code is printed once and never logged, traced or returned again

  Scenario: An Edge invite code keeps the approved 10-symbol format
    When the owner mints an Edge invite
    Then its code is 10 Crockford base32 symbols, shown as XXXXX-XXXXX, as in enrollment_root_trust.feature

  Scenario: The TUI mints the same invite with one key
    Given the owner is on the Tower's row in the TUI
    When they press the invite key
    Then the TUI shows the same "roger join ..." line and the QR
    And the line can be copied with the TUI's existing copy action

  # RULED 2026-10-10 (the recommendation below was adopted): the TUI key. Recommend "i" on the Tower row and on [3] EDGE, unless the existing
  # keymap already binds "i" on that screen; then "+".

  Scenario: The console mints the same invite with one button
    Given the owner has the web console open
    When they press "Invite a device"
    Then the console shows the "roger join ..." line, a copy button and the QR

  Scenario: Minting a new client invite does not void an unused earlier one
    Given an unused client invite minted 5 minutes ago
    When the owner mints another client invite
    Then both work until each is used once or expires

  # RULED 2026-10-10 (the recommendation below was adopted): whether a fresh invite voids older unused ones. Edge's D6 voids the previous code
  # for the SAME key; a bearer invite has no key. Recommend: invites are independent, and
  # `roger tower invite --list` / `--void <id>` manage them.

  Scenario: The owner can list and void outstanding invites
    Given two unused client invites
    When the owner runs "roger tower invite --list"
    Then it lists each by a short id, its kind, and the minutes left, never the code
    When the owner runs "roger tower invite --void <id>"
    Then that invite no longer works and the other still does

  Scenario: An invite for a named machine only
    When the owner runs "roger tower invite --for laptop-x"
    Then the invite works only for a joiner that names itself "laptop-x"
    And a joiner with any other name is refused without spending the invite

  # =====================================================================================
  # 2. JOINING WITH AN INVITE: A CLIENT
  # =====================================================================================

  Scenario: A client joins a Tower with one pasted command
    Given the owner minted a client invite
    When the client runs "roger join <invite>"
    Then it checks the Tower's identity against the invite's fingerprint before sending anything
    And it proves the code without sending the code itself
    And the Tower admits the client's key on the code
    And the client records the Tower in lan_towers WITH its pinned fingerprint
    And the client prints "joined the Tower lab at 192.168.1.20:8787" and the bands it serves
    And it prints one next step: "try: roger --lan use <band>"

  Scenario: The client proof binds the code to this client and this Tower
    When the client joins with a valid invite
    Then the proof covers the domain "tower-join", the client's public key, the Tower's fingerprint and a fresh nonce
    And it is the Edge proof construction (PBKDF2 of the code, then HMAC) with that domain string

  Scenario: The owner sees the join as it happens
    Given "roger tower invite" is still showing its invite
    When a client joins with it
    Then the owner's terminal prints "laptop-x joined (key 4be1...a9)"
    And it prints how to undo it with the new tower command: revoke-client laptop-x
    And the TUI and console show the same line

  Scenario: A joined client is admitted exactly as before
    When the client has joined
    Then the Tower's existing admission check (IsAdmitted) accepts its key
    And every request it sends is still signed and carries the replay nonce

  Scenario: Joining a Tower already joined is a no-op that says so
    Given the client already joined "lab"
    When it runs "roger join" with a fresh invite for "lab"
    Then it says it is already a member, keeps its pin, and does not spend the invite

  Scenario: The client tries the invite's addresses in order
    Given the invite lists 192.168.1.20:8787 and [fe80::1%en0]:8787
    And 192.168.1.20 does not answer
    When the client joins
    Then it joins through the next address whose identity matches the fingerprint
    And it records the address that worked first in lan_towers

  Scenario: An invite whose addresses are not local is refused before any connection
    Given an invite whose only address is 203.0.113.9:8787
    When the client runs "roger join" with it
    Then it is refused with the local-address rule (lanmode), naming the address
    And no connection is made

  # =====================================================================================
  # 3. JOINING WITH AN INVITE: A STATION
  # =====================================================================================

  Scenario: A station joins with a station invite and reports its own models
    Given the owner ran "roger tower invite --station"
    And the station machine has "qwen3-8b" and "smollm2-135m-instruct" on air
    When the station runs "roger join <invite>"
    Then the Tower attaches it under the station's own name
    And the station's models are taken from its live offers, not typed by hand
    And the station starts serving the Tower right away
    And the owner sees "station kitchen-pi joined, serving: qwen3-8b, smollm2-135m-instruct"

  Scenario: A station's model list follows what it actually serves
    Given a joined station that later stops "qwen3-8b" and starts "gpt-oss-20b"
    When the station's next poll reaches the Tower
    Then the Tower routes "gpt-oss-20b" to it and no longer routes "qwen3-8b" to it

  Scenario: The owner can limit what a station may serve
    Given the owner ran "roger tower invite --station --models qwen3-8b"
    When a station serving "qwen3-8b" and "gpt-oss-20b" joins
    Then the Tower routes only "qwen3-8b" to it
    And the station is told which of its models were not accepted

  Scenario: `roger share --tower <invite>` is the same join for a station
    When the station runs "roger share --tower <station invite>"
    Then it joins exactly as "roger join <station invite>" and then shares

  Scenario: A client invite cannot attach a station, and a station invite cannot admit a client
    When a station joins with a client invite
    Then it is refused with "this invite is for a client, not a station"
    And the invite is not spent
    When a client joins with a station invite
    Then it is refused with "this invite is for a station, not a client"
    And the invite is not spent

  # =====================================================================================
  # 4. JOINING AN EDGE WITH THE SAME VERB
  # =====================================================================================

  Scenario: A device joins an Edge with "roger join"
    Given the Edge authority's owner ran "roger edge invite"
    When the device runs "roger join <invite>"
    Then it enrolls exactly as "roger edge enroll --invite <invite>" does today
    And it verifies the root through the code's proof before keeping anything (enrollment_root_trust.feature)

  # RULED 2026-10-10 (the recommendation below was adopted): a bearer Edge invite. enrollment_root_trust.feature D15 says the code does not
  # replace the allow-list. A key-less "roger edge invite" needs the code to admit the joiner's key
  # on use. Recommend: the Edge code admits on use exactly like the Tower (the allow step becomes
  # optional), superseding D15. Keep "roger edge authority allow <key>" for owners who want it.
  Scenario: "roger edge invite" needs no joiner key first
    When the Edge owner runs "roger edge invite"
    Then it prints "roger join <invite>" and a QR without asking for a key
    And the joiner's key is admitted when it presents the code

  Scenario Outline: The old join commands are aliases of "roger join"
    When the user runs "<old>"
    Then it behaves exactly as "roger join <invite>"

    Examples:
      | old                                        |
      | roger lan join <invite>                    |
      | roger edge enroll <name> --invite <invite> |

  # =====================================================================================
  # 5. OFFLINE
  # =====================================================================================

  Scenario: Minting and joining work with no internet
    Given neither machine can reach the internet
    When the owner mints an invite and the client joins with it
    Then the join succeeds
    And no request goes to Roger Core or any public address
