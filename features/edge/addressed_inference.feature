# ROGER EDGE - ADDRESSED INFERENCE: run a turn on a SPECIFIC node's model, not just "the nearest peer
# that serves this band". Every refusal is specified before the happy path.
#
# STATUS: built 2026-09-24. Not godog-bound: pinned by Go tests - internal/edge/addressed_test.go
# (ServingPeer and every refusal), cmd/rogerai/edgeladder_test.go (`roger use <node>`), and
# internal/tui/edge_network_test.go (the Ask action). VERIFIED serve is earned by the serving probe
# (features/edge/serving_probe.feature); until 2026-09-27 nothing in production ran it.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-24: "I should be able to open any agent and prompt to find information
# about that specific edge device ... assuming the agent I'm talking to has some access to a model in
# the network." Today `roger use <model>` runs on WHICHEVER peer serves that band (the ladder auto-
# picks: features/edge/local_inference.feature). You cannot say "use gentle-ibex-14's model" or "ask
# THIS node." This feature adds NODE addressing to the existing, working infer arrow - it does not add
# a new transport, only a new way to CHOOSE the peer.
#
# ONE ADDRESS. The address is the same node/instance address the fleet already resolves for
# `roger edge describe` and `roger edge sessions` (fleet.Resolve / SplitAddress). A bare name that is
# on more than one node is ambiguous and refused with the choices listed, exactly as edgeResolve does
# for forget. Adding a node address never widens what may run: it still goes over /edge/infer, still
# mutually authenticated, still receipted, still bound by `roger edge prefer`.
#
# WHAT A NODE MUST BE to be addressable for a turn: a MEMBER of this Edge, with a VERIFIED `serve`
# instance (Routable), reachable on a LAN transport, not DARK. A node that merely CLAIMS serve does
# not route - the whole point of CLAIMED vs VERIFIED (node_identity.feature). A phone with no inbound
# serving face never earns VERIFIED serve, so it is not addressable for a turn until it can serve
# (features/edge/presence.feature dial-out, or a future mobile serve path) - and the refusal says so.
#
# GROUND TRUTH: internal/edge/fleet.go (Resolve, Located, ServersOf, Located.Serves requiring
# Verified), cmd/rogerai/edgeladder.go (applyEdgeLadder, edgePeersFor - VERIFIED-serve non-dark LAN
# peers), internal/client/client.go (dialEdgePeer, Prefer), cmd/rogerai/main.go (cmdUse), internal/
# edge/infer.go (/edge/infer, signed receipt), internal/tui/edge_view.go and edge_enrolled_look.go
# (edgeActions/edgeLookActions - the member "nothing to do" branch this feature replaces with Ask).
#
# Tags: @edge (routing + refusals), @cli (`roger use <node>`), @tui (the Ask action).

Feature: A turn can be addressed to a specific node's model, and every reason it cannot is drawn

  # =========================================================================
  # 1. ADDRESSING A TURN TO A NODE (CLI)
  # =========================================================================

  @cli
  Scenario: use a node that serves exactly one band by its name
    Given a member "jetson" with a VERIFIED serve instance on air with model "qwen3-8b"
    When the owner runs `roger use jetson`
    Then the turn runs on "jetson"'s model "qwen3-8b"
    And the reply carries a signed local receipt naming node "jetson"
    And nothing was asked of the market

  @cli
  Scenario: address a specific instance on a node with node/instance
    Given "workstation" runs two instances, "big" serving "qwen3-72b" and "small" serving "qwen3-8b"
    When the owner runs `roger use workstation/big`
    Then the turn runs on the "big" instance's model
    And the receipt names node "workstation" and instance "big"

  @cli
  Scenario: pin a model on a node when the node serves more than one
    Given "workstation" serves both "qwen3-72b" and "qwen3-8b" across its instances
    When the owner runs `roger use workstation --model qwen3-8b`
    Then the turn runs on the instance of "workstation" that serves "qwen3-8b"

  @cli
  Scenario: a node that serves more than one band, addressed with no model, lists the choices
    Given "workstation" serves both "qwen3-72b" and "qwen3-8b"
    When the owner runs `roger use workstation`
    Then it is refused, listing the models "workstation" serves
    And it asks for `roger use workstation --model <model>` or an instance address
    And nothing ran

  # =========================================================================
  # 2. REFUSALS - every reason a node address will not run, before anything runs
  # =========================================================================

  @edge
  Scenario Outline: an address that cannot carry a turn is refused, naming why, and nothing runs
    Given the node address <address> on this Edge
    When the owner runs `roger use <address>`
    Then it is refused, naming <reason>
    And nothing ran
    And the market was not asked

    Examples:
      | address              | reason                                                        |
      | no-such-node         | no node on this Edge answers to that name                     |
      | gentle-ibex-14       | that node serves no model (it declares no VERIFIED serve)     |
      | jetson               | that node is DARK - it has not checked in within the window   |
      | workstation/ghost    | that node has no instance by that name                        |
      | jetson --model gpt-5 | that node does not serve that model                           |
      | far-pi               | that node has no LAN transport this machine can reach         |
      | stranger             | that node is not a member of this Edge                        |

  @cli
  Scenario: node names are unique, so a bare name is never ambiguous for a node
    Given the Edge enrolls at most one node under any given name
    When the owner runs `roger use <a name that is not a node>`
    Then it is treated as a MODEL for the band ladder, not as a node address
    And a name that IS a node routes to that node

  @edge
  Scenario: a node that only CLAIMS serve (never probed) is not addressable for a turn
    Given a member "pi" whose serve capability is CLAIMED but not VERIFIED
    When the owner runs `roger use pi`
    Then it is refused, saying "pi" cannot yet serve, only that it has claimed it
    And nothing ran

  @edge
  Scenario: a mobile member with no serving face says so plainly
    Given a mobile member "gentle-ibex-14" that advertises but serves no inbound face
    When the owner runs `roger use gentle-ibex-14`
    Then it is refused, saying a phone cannot yet serve a model to the Edge
    And it does not pretend the phone is unreachable or unknown
  # A phone CONSUMING a band (the other direction) is features/edge/phone_consume.feature: it asks
  # the authority, which relays to the member that serves it (ruling 2026-10-13).

  # =========================================================================
  # 3. THE TUI: an Ask action on a node that can serve, replacing "nothing to do"
  # =========================================================================

  @tui
  Scenario: a member that serves a model offers an Ask action on the [3] EDGE screen
    Given the EDGE screen with a member "jetson" that has a VERIFIED serve instance
    When the owner highlights "jetson"
    Then WHAT YOU CAN DO shows an "Ask" action for "jetson"
    And choosing it opens a prompt pinned to "jetson"'s model

  @tui
  Scenario: a member that serves nothing explains why it cannot be asked, instead of "nothing to do"
    Given the EDGE screen with a member "gentle-ibex-14" that serves no model
    When the owner highlights "gentle-ibex-14"
    Then it is NOT drawn as "nothing to do on this node here"
    And it says the device serves no model yet, so there is nothing to ask it
    And it names what would make it askable (a served, verified model)

  @tui
  Scenario: opening a node's detail shows whether it can be asked
    Given a member "jetson" that serves "qwen3-8b"
    When the owner opens "jetson"'s detail with Enter
    Then the detail shows it can be asked, and the model it would answer with
    And Enter or the Ask action starts the prompt

  @tui
  Scenario: a prompt pinned to a node in the TUI is the same turn as the CLI address
    Given the owner is prompting "jetson" from the EDGE screen
    When they send a message
    Then it runs on "jetson"'s model over /edge/infer, exactly as `roger use jetson` would
    And the session is drawn on the graph from this machine to "jetson"

  # =========================================================================
  # 4. RELATION TO `roger edge prefer` AND THE LADDER
  #
  # corrected 2026-10-06 (founder-approved): in LAN mode a standalone Tower can be addressed the
  # same way, by its address (`roger use 192.168.1.20:8787 [model]`); see
  # features/lan/routing_and_inference.feature.
  # =========================================================================

  @edge
  Scenario: a node address pins the peer - the band ladder does not second-guess it
    Given peers "jetson" and "workstation" both serve "qwen3-8b"
    When the owner runs `roger use jetson`
    Then the turn runs on "jetson" even though "workstation" also serves that band
    And the ladder does not fall through to "workstation" or the market

  @edge
  Scenario: local-only refuses a node address that is not on this Edge, never silently uses the market
    Given `roger edge prefer local-only`
    And an address that resolves to no Edge peer
    When the owner runs `roger use <that address>`
    Then it is refused for having no local peer
    And the market is not used as a fallback

  @edge
  Scenario: an addressed node that has gone DARK does not silently fall to the market under prefer local
    Given `roger edge prefer local`
    And "jetson" was serving but has since gone DARK
    When the owner runs `roger use jetson`
    Then it is refused, naming "jetson" as DARK
    And the owner is told to drop the address to use the band ladder instead

  # =========================================================================
  # 5. EVIDENCE
  # =========================================================================

  @edge
  Scenario: an addressed turn is receipted at the serving node, attributed to the caller
    Given the owner ran `roger use jetson` for one turn
    Then "jetson" signed a receipt for the turn with its node key
    And the receipt names the caller's node id
    And a Self grant (owner's own boxes) prices it at zero

  @edge
  Scenario: a refused address is still drawn, never nothing
    Given the owner addressed a node that could not serve
    Then the refusal is surfaced with its reason
    And it is not shown as an empty or successful turn
