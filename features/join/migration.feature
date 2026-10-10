# STATUS: APPROVED 2026-10-10 (founder)
# BUILD STATUS: BUILT (TestJoinMigrationBDD; TestFirstUsePinsAnAddedTowerOnce, TestLanAddAsksBeforePinning, TestTheOldCommandsNameTheNewWay). "The release after removes the old Tower admission commands" describes the next release and is not run in this one.
#
# NOTHING BREAKS ON UPGRADE. Founder ruling 2026-10-10: clients already admitted keep working and
# pin the Tower the first time they talk to it after upgrading (trust on first use, ONCE, logged).
# The old commands stay for one release with deprecation notes.
Feature: Upgrading to one-invite joins

  Scenario: An already-admitted client keeps working after both sides upgrade
    Given a client admitted by "roger tower invite --client" and "roger tower admit" before this change
    And lan_towers lists the Tower with no pin
    When both upgrade and the client sends its next request over TLS
    Then the request is served
    And the client pins the Tower's fingerprint as seen on that first TLS contact
    And it prints once "pinned the Tower lab now (first use after upgrade): sha256:9f3c...e1"
    And the config records the pin and that it came from first use

  Scenario: First-use pinning happens once only
    Given the client pinned the Tower on first use after upgrading
    When the key later changes
    Then the client refuses exactly as tower_identity.feature says, and does not re-pin

  Scenario: First-use pinning applies only to Towers the user already added
    Given an address that is not in lan_towers
    When the client contacts it
    Then nothing is pinned by first use; joining needs an invite or an approval

  Scenario: An already-attached station keeps serving after upgrade
    Given a station attached with "roger tower attach --station s --key K --models m" before this change
    When both upgrade
    Then the station keeps serving its listed models
    And it pins the Tower on its first TLS poll, once, logged

  Scenario Outline: The old commands still work for one release, with a deprecation note
    When the owner runs "<old>"
    Then it works as before
    And it prints one line naming the new way: "<new>"

    Examples:
      | old                                                   | new                                      |
      | roger tower invite --client <id>                      | roger tower invite (one paste: roger join) |
      | roger tower admit --client <id> --id <inv> --code <c> | roger tower invite, then roger join       |
      | roger tower attach --station s --key K --models m     | roger tower invite --station              |
      | roger lan add 192.168.1.20:8787                       | roger join <invite>                       |

  # RULED 2026-10-10 (the recommendation below was adopted): `roger lan add` after the release. Recommend it stays for adding a Tower you are
  # already admitted to; for a TLS Tower it shows the fingerprint and asks "pin this Tower? [y/N]".
  Scenario: `roger lan add` of a TLS Tower asks before pinning
    Given the client is already admitted to a TLS Tower but has no pin for it
    When the client runs "roger lan add 192.168.1.20:8787"
    Then it shows the Tower's short fingerprint and asks "pin this Tower? [y/N]"
    And only y records the pin

  Scenario: The release after removes the old Tower admission commands
    Given the release after this one
    When the owner runs "roger tower admit"
    Then it prints the new way and exits 2

  Scenario: The manual explains the change once
    When the manual's LAN section is read
    Then it says Towers are now joined with "roger join" and verified by their key
    And it says older clients pin their Tower on first contact after upgrading
