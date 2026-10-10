# STATUS: APPROVED 2026-10-10 (founder)
# BUILD STATUS: PARTIAL. Built: the owner-only admin socket, invite, attach, admit and reads while serving, stale and second-serve handling (TestJoinAdminSocketBDD). Not yet: the request-and-approve rows, station revoke, the Windows named pipe.
#
# OWNER COMMANDS WORK WHILE THE TOWER SERVES. Founder ruling 2026-10-10: invite, attach, admit,
# approve and the other administration commands reach a running `roger tower serve` through an
# owner-only local admin socket (mode 0600, the same user), instead of refusing because serve
# holds the data directory's exclusive lock. With no serve running they keep using the directory
# directly. No new network surface; works offline.
Feature: Owner commands reach a running Tower through a local admin socket

  Background:
    Given a standalone Tower directory /srv/lab-tower owned by the user "rt"
    And "roger tower serve --dir /srv/lab-tower" is running as "rt"

  Scenario: Serve opens an owner-only admin socket in the data directory
    Then the directory holds "admin.sock"
    And the socket's mode is 0600 and its owner is "rt"
    And serve removes it on a clean stop

  Scenario Outline: Administration commands work while serving
    When "rt" runs "<command>"
    Then it succeeds through the admin socket without stopping serve
    And clients keep being served while it runs

    # split 2026-10-10 (structural, no change in meaning): the rows built so far run; the rest wait
    # on request-and-approve (requests, approve) and on the Station kill switch
    # reaching a standalone Tower through the socket (station revoke).
    Examples:
      | command                                   |
      | roger tower invite                        |
      | roger tower invite --station              |
      | roger tower invite --list                 |
      | roger tower stations                      |
      | roger tower attach --station s --key K --models m |
      | roger tower revoke-client laptop-x        |

    @blocked-on-request-approve
    Examples:
      | command                                   |
      | roger tower requests                      |
      | roger tower approve <id>                  |
      | roger tower station revoke --station-id x |

  Scenario: With no serve running, the same commands use the directory directly
    Given serve is not running
    When "rt" runs "roger tower invite"
    Then it takes the directory lock and works as before

  Scenario: Another user cannot use the socket
    When a different local user connects to admin.sock
    Then the connection is refused by the file permission
    And on Linux the Tower also checks the peer's user id and refuses a mismatch

  Scenario: A stale socket from a crashed serve does not block the next start
    Given serve crashed and left admin.sock behind
    When serve starts again
    Then it detects nothing is listening, replaces the socket and starts

  Scenario: Two serves on one directory are still refused
    When a second "roger tower serve" starts on the same directory
    Then it is refused because the first holds the lock, exactly as today

  Scenario: The socket speaks only the administration commands
    When anything other than a known administration request arrives on the socket
    Then it is refused and logged, and the socket stays up

  # RULED 2026-10-10 (the recommendation below was adopted): Windows. Recommend a named pipe whose ACL grants only the serving user; same
  # commands. Joined Towers stay Linux-only (one_binary ruling), so this applies to standalone.
  Scenario: Windows uses an owner-only named pipe
    Given a standalone Tower serving on Windows
    Then administration commands reach it through a named pipe that only the serving user can open

  Scenario: The socket works with no network at all
    Given the machine has no network interfaces up except loopback
    When "rt" runs "roger tower invite"
    Then it succeeds
