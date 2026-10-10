# STATUS: PROPOSED 2026-10-10 (awaiting founder approval)
# BUILD STATUS: NOT BUILT.
#
# SUPER SIMPLE COPY. Founder 2026-10-10: "make this tool super stupid simple to use". Every
# screen in the join flow says what happened and what to do next in ONE line. No ids to copy, no
# jargon (no "keyhash", "credential", "bootstrap"). The CLI, TUI and console say the same thing.
Feature: What each screen says during a join

  Scenario Outline: The CLI says what happened and the next step in one line each
    When "<situation>"
    Then the CLI prints "<says>"
    And it prints exactly one next-step line "<next>"

    Examples:
      | situation                          | says                                                    | next                                              |
      | the owner mints a client invite    | invite for one device, valid 15 min:                    | on the other machine run: roger join <invite>     |
      | the owner mints a station invite   | invite for one station, valid 15 min:                   | on the station run: roger join <invite>           |
      | a client joins                     | joined the Tower lab                                    | try: roger --lan use <band>                       |
      | a station joins                    | joined lab as a station, serving: <bands>               | nothing else to do; it is serving                 |
      | a device joins an Edge             | joined the Edge home as <name>                          | open roger and press 3 to see it                  |
      | the invite expired or was used     | that invite has expired or was already used             | ask the owner for a new one: roger tower invite   |
      | the Tower's key does not match     | this is not the Tower your invite names                 | check you are on the right network                |
      | the Tower can't be reached         | can't reach 192.168.1.20:8787                           | is roger tower serve running there?               |
      | the owner refused a request        | lab refused the request                                 | ask the owner, or use an invite                   |
      | nobody answered a request          | lab did not answer in 2 minutes                         | ask the owner to open roger, or use an invite     |

  Scenario: The not-admitted hint names the simple flow
    # Supersedes features/packaging/one_binary.feature:600 (see the report's superseded list).
    Given a LAN Tower that answers this machine with the uniform 401
    When the user runs "roger --lan use" against it
    Then the hint says "this Tower hasn't let you in yet. Ask its owner for an invite (roger tower invite), then run: roger join <invite>"

  Scenario: No screen asks the user to copy a key or an id
    Then no join-flow message asks the user to copy a key hash, client id or invitation id between machines

  Scenario: The TUI invite panel is one screen
    Given the owner pressed the invite key on the Tower row
    Then one panel shows the "roger join ..." line, a copy hint, the QR and the minutes left
    And when the invite is used it shows "<name> joined" in place

  Scenario: The TUI request panel is one line plus y and n
    When a join request arrives
    Then the TUI shows "<name> wants to join · <phrase> · y approves, n refuses"
    And it says "approve only if <name> shows the same phrase"

  Scenario: The console shows the same wording as the TUI
    Then the console's invite panel and request card use the same sentences as the TUI

  Scenario: The QR fits a normal terminal
    When the invite is shown in an 80x24 terminal
    Then the QR fits without wrapping, drawn with half-block characters
    And with NO_COLOR or a narrow terminal it says "terminal too small for the QR; paste the line instead"

  # DECISION: the QR encoder. Recommend vendoring a small pure-Go QR encoder (no network, no cgo)
  # under internal/, with a golden test; no new module dependency.

  Scenario: Help text for "roger join" is three lines
    When the user runs "roger join --help"
    Then it explains: join with an invite (roger join <invite>), or ask by address (roger join <address>), and that the owner approves
