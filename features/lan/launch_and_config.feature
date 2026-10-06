# STATUS: PROPOSED 2026-10-06 (awaiting founder approval)
#
# LAN MODE - LAUNCH AND CONFIG: one switch, the same everywhere, and you can always tell it is on.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-10-06: "some way of launching roger where it will only pick up bands
# from the local network or edge private network". Rulings the same day:
#   - LAN mode is a BAND FILTER, not an internet lockdown. "this mode is not about restricting
#     internet, it's more about just filtering out the towers and bands not local".
#   - Sources: standalone Towers on the LAN, the owner's Edge members, and this machine's own
#     `roger model serve`. One dial.
#   - Launch: `roger --lan` on any command, ROGER_LAN=1, or `roger config set mode lan`.
#
# THE ONE RULE OF THIS FILE: the mode a command runs in is decided ONCE, at start, from one
# precedence (flag, then environment, then config), and every surface reads that one answer.
# A mode two surfaces disagree about is the bug that makes the whole feature a lie.
#
# GROUND TRUTH: cmd/rogerai/main.go (config, loadConfig, ROGER_BROKER precedence at :256,
# cmdUse, cmdShare, runTUI), internal/tui (header), internal/webui (topbar).
#
# Tags: @cli in cmd/rogerai, @tui in internal/tui, @console in internal/webui.

Feature: LAN mode is one switch, set by flag, environment or config, and reported back the same everywhere

  # =========================================================================
  # 1. TURNING IT ON
  # =========================================================================

  @cli
  Scenario: the --lan flag turns LAN mode on for one command
    Given a machine with no mode set in config and no ROGER_LAN in the environment
    When they run "roger --lan use qwen3-8b"
    Then the command runs in LAN mode
    And the next "roger use qwen3-8b" without the flag runs in the default mode

  @cli
  Scenario Outline: the flag is accepted before or after the subcommand
    When they run "<command>"
    Then the command runs in LAN mode

    Examples:
      | command                      |
      | roger --lan use qwen3-8b     |
      | roger use qwen3-8b --lan     |
      | roger --lan share            |
      | roger share --lan            |
      | roger --lan search           |
      | roger --lan                  |

  @cli @tui
  Scenario: `roger --lan` with no subcommand opens the TUI in LAN mode
    When they run "roger --lan"
    Then the TUI opens
    And its header carries the LAN badge

  @cli
  Scenario Outline: ROGER_LAN turns LAN mode on for every command in that environment
    Given ROGER_LAN is set to "<value>"
    When they run "roger use qwen3-8b"
    Then the command runs in LAN mode

    Examples:
      | value |
      | 1     |
      | true  |
      | TRUE  |
      | yes   |
      | on    |

  @cli
  Scenario Outline: ROGER_LAN can also turn it off
    Given the config has mode "lan"
    And ROGER_LAN is set to "<value>"
    When they run "roger use qwen3-8b"
    Then the command runs in the default mode

    Examples:
      | value |
      | 0     |
      | false |
      | no    |
      | off   |

  @cli
  Scenario: an unreadable ROGER_LAN value is an error, never a guess
    Given ROGER_LAN is set to "sometimes"
    When they run "roger use qwen3-8b"
    Then it exits non-zero
    And the error names ROGER_LAN and the accepted values
    And nothing was dialed

  @cli
  Scenario: `roger config set mode lan` makes LAN mode sticky
    When they run "roger config set mode lan"
    Then the config file records mode "lan"
    And every later command runs in LAN mode until it is changed

  @cli
  Scenario: an unknown mode value is a usage error and the config is unchanged
    Given the config has no mode set
    When they run "roger config set mode offline"
    Then it exits non-zero naming the accepted values
    And the config still has no mode set

  # =========================================================================
  # 2. PRECEDENCE: FLAG, THEN ENVIRONMENT, THEN CONFIG
  # =========================================================================

  @cli
  Scenario Outline: the mode is decided by one precedence
    Given the config has mode "<config>"
    And ROGER_LAN is "<env>"
    When they run "roger use qwen3-8b <flag>"
    Then the command runs in "<result>" mode
    And `roger status` in that same environment and flags reports "<result>" from "<source>"

    Examples:
      | config  | env   | flag     | result  | source |
      | unset   | unset |          | default | default |
      | lan     | unset |          | lan     | config |
      | unset   | 1     |          | lan     | env    |
      | lan     | 0     |          | default | env    |
      | unset   | unset | --lan    | lan     | flag   |
      | lan     | unset | --no-lan | default | flag   |
      | unset   | 0     | --lan    | lan     | flag   |
      | lan     | 1     | --no-lan | default | flag   |

  @cli
  Scenario: --lan and --no-lan together are a usage error
    When they run "roger use qwen3-8b --lan --no-lan"
    Then it exits non-zero naming the conflict
    And nothing was dialed

  # =========================================================================
  # 3. TURNING IT OFF
  # =========================================================================

  @cli
  Scenario: `roger config unset mode` turns sticky LAN mode off
    Given the config has mode "lan"
    When they run "roger config unset mode"
    Then the config has no mode set
    And the next command runs in the default mode

  @cli
  Scenario: `roger config set mode default` is the same as unsetting it
    Given the config has mode "lan"
    When they run "roger config set mode default"
    Then the config has no mode set

  @cli
  Scenario: an old binary that does not know the mode key never silently drops it
    Given a config written by this version with mode "lan"
    When a field-preserving older-version reader rewrites the config
    Then the mode key is still present
    # The existing voice-station lesson: config keys dropped by old binaries.

  # =========================================================================
  # 4. REPORTING IT BACK
  # =========================================================================

  @cli
  Scenario: `roger status` says the mode and where it came from
    Given the config has mode "lan"
    When they run "roger status"
    Then the output has a line reading "mode: LAN (from config)"
    And it lists the local sources it will use: Towers, Edge members, this machine

  @cli
  Scenario: `roger status --json` carries the mode as data
    Given ROGER_LAN is "1"
    When they run "roger status --json"
    Then the JSON has "mode" equal to "lan" and "mode_source" equal to "env"

  @cli
  Scenario: a sticky LAN mode is announced on every interactive launch, so it is never forgotten
    Given the config has mode "lan"
    When they run "roger use qwen3-8b" in a terminal
    Then the first line printed names LAN mode and how to turn it off
    When the output is piped
    Then that note goes to stderr and stdout carries only the command's output

  @tui
  Scenario: the TUI header carries the mode for the whole session
    Given the TUI was opened in LAN mode
    When any section renders
    Then the header carries the LAN badge
    And switching sections never removes it

  @console
  Scenario: the console reads the same mode the CLI decided
    Given "roger --lan webui" was run
    When the console's status is read
    Then it reports mode "lan" from "flag"

  @tui
  Scenario: the mode does not change mid-session
    Given the TUI was opened in LAN mode
    When the config's mode is changed by another process
    Then this session stays in LAN mode
    And the header does not change until the TUI is restarted
    # One answer per process. A session that silently switched to the market mid-conversation
    # would send the next prompt somewhere the owner did not choose.
