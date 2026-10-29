# ROGER EDGE ON A MACHINE WITH NO SCREEN - running headless, and at boot.
#
# STATUS: APPROVED by the founder 2026-10-03.
# BUILD STATUS (2026-10-03, wt/edge-limoni): BUILT - `roger edge run`, `roger edge service [install|remove]` (systemd;
# shown and asked first; as a normal user it stages the unit and prints the sudo commands; carries a
# non-default config location). Reboot recovery is systemd's Restart + WantedBy, first proven on the Pi.
#
# Found preparing the Pi test (2026-10-02): a member answers on its Edge only while roger runs, and
# the only way to run roger without a terminal is a hidden `__edge-agent` argument the launcher uses.
# A Raspberry Pi, a Jetson or a NAS has no screen; the owner should not need tmux to keep it on the
# Edge, and it should come back on its own after a power cut.
#
# Linux first (systemd), because every board roger targets runs it. macOS (launchd) and Windows
# (a scheduled task or a service) are named as not built, each saying so in one sentence.

@edge @headless
Feature: A member with no screen stays on its Edge, and comes back after a reboot

  Scenario: roger runs headless, in the open
    Given a machine enrolled on an Edge
    When the owner runs `roger edge run`
    Then roger stays running with no screen: its face, its presence, discovery, and what it serves
    And it writes what it is doing to its output, one line per event, as the app's log does
    And it stops cleanly on an interrupt or a stop signal, stopping what it serves
    And a machine that is not enrolled is refused in one sentence, naming `roger edge enroll`

  Scenario: roger can be set to start at boot, and the owner sees what it will write first
    Given a Linux machine with systemd, enrolled on an Edge
    When the owner runs `roger edge service install`
    Then roger shows the service it will install - what runs, as which user, and that it restarts
      on failure and starts at boot
    And it asks before writing it
    And it never raises its own privileges: where the service needs root, it prints the exact
      command to run with sudo, rather than running sudo itself

  Scenario: the service is said, and removable
    When the owner runs `roger edge service`
    Then it says whether roger starts at boot, whether it is running now, and since when
    When the owner runs `roger edge service remove`
    Then it is asked first, and afterwards roger no longer starts at boot

  Scenario: after a reboot, the member is back on its Edge without anyone touching it
    Given a member whose service is installed
    When the machine restarts
    Then it answers on its Edge again within a minute of the network coming up
    And the fleet window shows it went dark and came back, with when

  Scenario: a platform without a service manager roger knows says so
    Given a machine without systemd (macOS, Windows, or a Linux without it)
    When the owner runs `roger edge service install`
    Then it says in one sentence that starting at boot is not built for this platform yet
    And it says `roger edge run` still works there
