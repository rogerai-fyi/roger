# ROGER EDGE - THE `roger edge` COMMAND SURFACE.
#
# WHY THIS EXISTS
#
# The `roger` CLI is a flat surface today: login, use, share, tower, grant, limits and so on.
# Roger Edge adds ONE noun with subcommands under it, the way `roger tower` already does, so the
# fleet has a home that is obvious from `roger --help` and does not collide with the market
# commands next to it.
#
# TWO TRANSPORTS, ONE COMMAND TREE
#
# The best idea worth borrowing from the incumbent product is that its cloud command tree is not
# a reimplementation of its local one: it builds the same tree and wraps every leaf to inject a
# different transport. We do the same. `roger edge <cmd>` prefers a LAN-direct path when the node
# is on this network and falls through to the relay when it is not, and the command, its flags and
# its output are identical either way. An owner never has to know which path carried their
# request, and there is exactly one implementation of each command to keep correct.
#
# THIS FILE IS THE READ-ONLY HALF. list, describe, name, forget, adopt and the discovery report
# are here because they are the founder-approved build scope. `invoke`, `read`, `classify` and
# `deploy` are control and carry grants; they live in features/edge/control.feature and wait for
# spec sign-off.
#
# GROUND TRUTH: cmd/rogerai/main.go (the flat command dispatch and its usage text),
# features/edge/node_identity.feature (the record these commands print),
# features/edge/discovery.feature (where nodes and candidates come from).
#
# EXIT CODES. 0 success; 1 a real failure; 2 usage. A command that finds nothing is NOT a
# failure: an empty fleet exits 0 and says so, because scripts should not have to tell "broken"
# from "empty".

Feature: roger edge is one command tree over the owner's fleet, whichever path reaches a node

  Background:
    Given a logged-in owner with an Edge

  # =========================================================================
  # 1. THE SURFACE
  # =========================================================================

  Scenario: the fleet has a home in the help
    When the owner runs "roger --help"
    Then "edge" is listed as a command
    And its one-line description names the fleet

  Scenario: bare `roger edge` shows the fleet, because that is what the owner wanted
    When the owner runs "roger edge"
    Then it prints the fleet
    And it does not print a usage error

  Scenario Outline: the subcommands are exactly these, and nothing is a silent no-op
    When the owner runs "roger edge <cmd> --help"
    Then it describes "<purpose>"

    Examples:
      | cmd      | purpose                                          |
      | list     | show every node on this Edge                      |
      | describe | show one node in full                             |
      | name     | give a node a name the owner chooses              |
      | forget   | remove a node from this Edge                       |
      | adopt    | take a discovered candidate into the fleet         |
      | scan     | look for nodes on this network now                 |

  Scenario: an unknown subcommand is a usage error, not a silent success
    When the owner runs "roger edge frobnicate"
    Then it exits 2
    And it names the valid subcommands

  Scenario: a stray argument is refused rather than ignored
    When the owner runs "roger edge list extra-arg"
    Then it exits 2
    And it says the command takes no arguments

  # =========================================================================
  # 2. LIST
  # =========================================================================

  Scenario: an empty Edge is a success, not a failure
    Given an Edge with only this machine
    When the owner runs "roger edge list"
    Then it exits 0
    And it says this machine is the only node
    And it names the action that would add another

  Scenario: the fleet lists name, kind, capabilities, transport and last seen
    Given nodes of several kinds and capabilities
    When the owner runs "roger edge list"
    Then each row carries the name, the kind, the verified capabilities, the transport in use and how long ago the node was seen

  Scenario: a claimed but unverified capability is marked as claimed
    Given a node claiming "serve" whose probe failed
    When the owner runs "roger edge list"
    Then that capability is shown as claimed, distinctly from a verified one

  Scenario: dark nodes are listed, not hidden
    Given a node whose heartbeat aged out
    When the owner runs "roger edge list"
    Then it is listed as dark with its last-seen age

  Scenario: candidates are listed separately from members
    Given a discovered unenrolled peer
    When the owner runs "roger edge list"
    Then it appears under candidates, not among the fleet
    And the output says how to adopt it

  Scenario: --json is stable enough to script against
    When the owner runs "roger edge list --json"
    Then the output is a JSON array
    And each element carries id, name, kind, capabilities, transports, last_seen and state
    And no field carries a secret, a token or a private key

  Scenario Outline: filters narrow the fleet without changing the shape of a row
    When the owner runs "roger edge list <flag>"
    Then only <shown> are shown

    Examples:
      | flag                | shown                               |
      | --capability serve  | nodes with the serve capability      |
      | --capability sense  | nodes with the sense capability      |
      | --dark              | nodes whose heartbeat aged out       |
      | --candidates        | discovered peers that are not members |

  # =========================================================================
  # 3. DESCRIBE, NAME, FORGET, ADOPT
  # =========================================================================

  Scenario: describe names the node by name or by id prefix
    Given a node named "bench-pi"
    When the owner runs "roger edge describe bench-pi"
    Then it prints that node
    And running it with an unambiguous id prefix prints the same node

  Scenario: an ambiguous prefix is refused with the candidates listed
    Given two nodes whose ids share a prefix
    When the owner describes that prefix
    Then it exits 1
    And it lists the matching nodes so the owner can disambiguate

  Scenario: describing a node that is not on this Edge does not confirm it exists elsewhere
    When the owner describes an id that belongs to another account
    Then it exits 1 with "no such node on this Edge"
    And the message reveals nothing about the other account

  Scenario: naming a node is idempotent and reversible
    When the owner names a node "bench-pi" twice
    Then the second run succeeds and changes nothing
    And renaming it again to "cabinet-pi" succeeds

  Scenario: a name already taken in this Edge is refused with the holder named
    Given a node named "bench-pi"
    When the owner names a different node "bench-pi"
    Then it exits 1
    And it names the node already holding it

  Scenario: forget asks before it acts, unless told not to
    Given a node in the fleet
    When the owner runs "roger edge forget bench-pi"
    Then it asks for confirmation naming the node
    And running it with --yes skips the prompt
    And after it succeeds the node is gone and its pin is cleared

  Scenario: forgetting a node that is not there is a clean no-op
    When the owner forgets a name that is not on this Edge
    Then it exits 0 and says there was nothing to forget

  Scenario: adopt takes a candidate into the fleet and requires the owner to mean it
    Given a discovered candidate
    When the owner runs "roger edge adopt <candidate>"
    Then the candidate becomes a member with no capabilities until it declares them
    And adopting something that is already a member is a clean no-op

  Scenario: adopt refuses a candidate whose certificate does not match its advertisement
    Given a candidate whose fingerprint does not match the certificate it serves
    When the owner tries to adopt it
    Then it exits 1 naming the mismatch
    And nothing is added to the fleet

  # =========================================================================
  # 4. SCAN, AND THE TWO TRANSPORTS
  # =========================================================================

  Scenario: scan reports what it found and what it refused
    When the owner runs "roger edge scan"
    Then it reports the peers it verified
    And it reports each peer it refused with the reason
    And it exits 0 even when it found nothing

  Scenario: the same command works whichever path reaches the node
    Given a node reachable LAN-direct and a node reachable only through a relay
    When the owner describes each of them
    Then the output shape is identical
    And each says which transport carried the request only when asked with --verbose

  Scenario: a LAN path is preferred, and its failure falls through to the relay silently
    Given a node reachable both ways whose LAN port stops answering
    When the owner describes it
    Then the command succeeds over the relay
    And --verbose says the LAN attempt failed first

  Scenario: with no network at all the command says so plainly
    Given no network and no relay connection
    When the owner runs "roger edge list"
    Then it exits 0
    And it prints the last known fleet marked as stale, with the age of that knowledge

  Scenario: none of these commands requires a login to read a locally discovered fleet
    Given an owner who is not logged in
    When the owner runs "roger edge scan"
    Then it reports LAN candidates
    And it says that adopting them requires a login
