# ROGER EDGE - FABRIC TOOLS: the agent you are talking to can SEE and REACH the rest of your Edge,
# through tools, scoped to your Edge and gated by grants. Every boundary is specified before the use.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-24: "I should be able to ... ask about any of the other agents assuming
# the agent I'm talking to has some access to a model in the network." Talking to one agent should let
# you learn about, and act on, the whole Edge - because the agent has TOOLS that read the fleet and
# reach peers. This is the tool layer that sits on top of two things that already have their own
# specs: addressed inference (features/edge/addressed_inference.feature - `edge_ask`) and the invoke
# fabric (features/edge/control.feature - `edge_invoke`, the grant, the refusals).
#
# THE TOOLS, and where each one's rules live:
#   edge_list                    -> read the fleet: nodes, kinds, caps, on-air, DARK. Read-only.
#   edge_describe <node>         -> read one node's record: what it is, what it can do, what it serves.
#   edge_ask <node> [--model m]  -> run a PROMPT on that node's model (addressed_inference.feature).
#   edge_invoke <node> <task>    -> DELEGATE a task to that node's agent (control.feature, under a grant).
#
# THE BOUNDARIES, stated before any scenario:
#   1. Tools see ONLY this owner's Edge. The fleet is per-account; a tool never lists, describes,
#      reaches, or leaks a node of a different account or a different Edge.
#   2. Reading is not reaching. edge_list/edge_describe read records the fleet already holds; they
#      never touch the remote node. edge_ask/edge_invoke DO reach it and inherit that path's rules.
#   3. edge_invoke without a valid, authority-signed grant is refused, whatever the prompt says
#      (control.feature). The model cannot talk itself into a permission.
#   4. Every tool that reaches a peer is receipted and drawn as a session; refusals included.
#   5. A grant's budget, expiry, revocation, node scope, capability scope and hop count bind the tool
#      exactly as they bind `roger edge invoke` - the tool is not a second way in.
#   6. The tools exist only for an instance that is an AGENT (declares operate) on an Edge. A booth
#      that is not an agent does not get them.
#
# GROUND TRUTH: internal/harness (the loop tools run in), internal/edge/fleet.go (List, Resolve,
# ServersOf, AuthorizeInvoke - the built, unwired gate), internal/edge/node.go (Operate cap), and the
# two specs this composes: addressed_inference.feature, control.feature.
#
# Tags: @edge (scope + refusals), @harness (tools in the loop), @tools, @tui.

Feature: The agent you talk to can read and reach the rest of your Edge, scoped and grant-gated

  # =========================================================================
  # 1. WHEN THE TOOLS EXIST
  # =========================================================================

  @edge
  Scenario: an agent on an Edge gains the fabric tools
    Given a roger that is an AGENT (declares operate) on the owner's Edge
    Then its tool set includes edge_list, edge_describe, edge_ask and edge_invoke
    And a roger that is NOT an agent, or not on an Edge, has none of them

  @edge
  Scenario: the tools are scoped to this owner's Edge only
    Given the agent is on the account's Edge with peers "jetson" and "pi"
    And another account has a node "someone-else"
    When the agent calls edge_list
    Then it sees "jetson" and "pi"
    And it never sees "someone-else"
    And edge_describe, edge_ask and edge_invoke all refuse an address that is not on this Edge

  # =========================================================================
  # 2. READING THE FABRIC (read-only, never touches the remote node)
  # =========================================================================

  @tools
  Scenario: edge_list returns the fleet the way the owner sees it
    Given peers "jetson" (host, serve VERIFIED, on air qwen3-8b), "pi" (board, sense) and a DARK "attic"
    When the agent calls edge_list
    Then it returns each node's name, kind, capabilities with state, what it serves, and DARK where dark
    And it reached no remote node to do it

  @tools
  Scenario: edge_describe reads one node's record without contacting it
    Given a member "gentle-ibex-14"
    When the agent calls edge_describe "gentle-ibex-14"
    Then it returns what that node IS and what it CAN do, from the record the fleet holds
    And "gentle-ibex-14" was not contacted

  @tools
  Scenario Outline: a read tool on an address it cannot resolve refuses, and leaks nothing
    Given the address <address>
    When the agent calls edge_describe <address>
    Then it is refused, naming <reason>
    And it discloses nothing about any other account's nodes

    Examples:
      | address       | reason                                             |
      | no-such-node  | no node on this Edge answers to that name          |
      | someone-else  | that node is not on this Edge                       |
      | iPhone        | the name is on more than one node - name one exactly |

  # =========================================================================
  # 3. ASK ONE NODE ABOUT ANOTHER (read + prompt, the founder's scenario)
  # =========================================================================

  @tools
  Scenario: the owner asks agent A about device B, and A uses tools to answer
    Given the owner is prompting agent "workstation/agent"
    And a device "gentle-ibex-14" is a member of the Edge
    When the owner asks "what is gentle-ibex-14 and what can it do"
    Then the agent calls edge_describe "gentle-ibex-14"
    And answers from what it read, not from a guess
    And the tool call and its result are drawn as a session on the graph

  @tools
  Scenario: an agent runs a prompt on a peer's model to answer, when it has none itself
    Given agent "pi/agent" that serves no model of its own
    And a peer "jetson" that serves "qwen3-8b"
    When the owner asks "pi/agent" a question that needs a model
    Then "pi/agent" may call edge_ask "jetson" to run the prompt on "jetson"'s model
    And the answer returns through "pi/agent" to the owner
    And both hops - owner->pi, pi->jetson - are drawn and receipted

  @tools
  Scenario: edge_ask inherits every refusal of addressed inference
    Given a peer "gentle-ibex-14" that serves no model
    When the agent calls edge_ask "gentle-ibex-14"
    Then it is refused for the same reason `roger use gentle-ibex-14` would be
    And the agent reports the refusal, it does not fabricate an answer

  # =========================================================================
  # 4. ACTING ON A NODE (edge_invoke - grant-gated, control.feature)
  # =========================================================================

  @tools
  Scenario: edge_invoke delegates a task only under a grant the authority signed
    Given a grant allowing "laptop/agent" to operate "workstation/agent"
    When the agent "laptop/agent" calls edge_invoke "workstation/agent" with a task
    Then AuthorizeInvoke checks the grant before anything runs
    And the task runs in "workstation/agent"'s own harness, with its own tools and permissions
    And the result and the receipt return to "laptop/agent"

  @tools
  Scenario: edge_invoke with no grant is refused, whatever the prompt says
    Given no grant allowing "laptop/agent" to operate "workstation/agent"
    When the model, told to "just do it", calls edge_invoke "workstation/agent"
    Then it is refused for the missing grant
    And nothing ran on "workstation/agent"
    And the refusal is drawn, not hidden from the owner

  @tools
  Scenario Outline: edge_invoke fails closed on every bad grant, exactly as the CLI arrow does
    Given a grant that is <defect>
    When the agent calls edge_invoke "workstation/agent" under it
    Then it is refused, naming <reason>
    And nothing ran

    Examples:
      | defect                       | reason          |
      | expired                      | the expiry      |
      | revoked                      | the revocation  |
      | for a different callee       | the callee      |
      | for a different capability   | the capability  |
      | for a different account      | the account     |

  @tools
  Scenario: edge_invoke to actuate still needs the device owner's standing confirmation
    Given a grant allowing "laptop/agent" to actuate "gate/controller"
    But the owner has NOT confirmed actuate on "gate/controller"
    When the agent calls edge_invoke "gate/controller" to actuate
    Then it is refused for the missing owner confirmation
    And a grant did not substitute for it (control.feature rule 2)

  # =========================================================================
  # 5. THE FABRIC CANNOT BECOME A BOTNET THROUGH ITS TOOLS
  # =========================================================================

  @harness
  Scenario: a delegated task's tools carry the origin and hop count, and a loop is refused
    Given a task delegated laptop -> workstation, and workstation's agent calls edge_invoke back to laptop
    When laptop appears in the task's own chain
    Then the invoke is refused for looping (control.feature rule 4)
    And every hop is on the receipt

  @harness
  Scenario: a tool call cannot exceed the grant's budget
    Given a grant with a spend ceiling
    When edge_ask / edge_invoke under it would exceed the ceiling
    Then the call is refused before it runs, naming the ceiling

  @harness
  Scenario: a mobile agent's tools are bounded by what the phone can run
    Given a mobile agent on iOS that cannot spawn processes
    Then it may call edge_list, edge_describe and edge_ask (which reach peers, not the phone)
    But a task delegated TO the phone runs only what the phone's harness permits
    And the ceiling is stated, not silently exceeded

  # =========================================================================
  # 6. EVIDENCE AND THE VIEW
  # =========================================================================

  @tui
  Scenario: every fabric tool that reaches a peer is drawn as a session, refusals included
    Given the agent used edge_ask and edge_invoke, and one was refused
    Then each reaching call is a session on the graph, attributed to the caller
    And the refused one is drawn as a refusal, never as nothing

  @edge
  Scenario: on a locally-rooted Edge, the tools contact Core for nothing
    Given a locally-rooted Edge with no internet
    When the agent uses edge_list, edge_describe, edge_ask and edge_invoke
    Then all of it works over the LAN with the Edge's own root
    And Core was not contacted
