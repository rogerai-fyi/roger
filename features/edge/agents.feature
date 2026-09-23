# ROGER EDGE - AGENTS ON THE EDGE: see them, adopt them, resume them, remove them (PROPOSED).
#
# STATUS: PROPOSED, awaiting founder approval. NOT IMPLEMENTED. This is the design for the vision
# the founder described 2026-09-21 after seeing that a second roger instance running an agent did
# not appear on [3]:
#
#   "if the edge/machine is RogGentooEdged, and I make another instance of roger, I should be able
#    to adopt it on my network ... if I'm on another instance and I open Edge [3] it's like
#    discovery is on, and I should see them and adopt them immediately, like a UniFi device-
#    adoption experience but in ASCII ... if I make a roger edge addition as an agent, I should see
#    it as an agent; if that makes new agents I should see them; we have a network for agents to
#    talk to each other and monitor each other through Roger Edge. Then if they're gone, and it was
#    a persistent agent that was adopted, there should be a way to resume it or remove it."
#
# WHAT ALREADY EXISTS to build on (not re-invented here):
#   - INSTANCES: a roger process registers in the local instance registry (features/edge/
#     instances.feature, internal/edge/instance.go): node = machine, instance = process, each with
#     a name, its bands, and its capabilities. [3] now shows them as RUNNING HERE.
#   - The `operate` CAPABILITY already means "runs an agent that can act on other nodes"
#     (internal/edge/node.go). An agent IS an instance that carries `operate`.
#   - PRESENCE: a node/instance that stops being heard goes DARK and is KEPT (topology_view).
#   - ADOPTION of a discovered machine already exists (`a`, EdgeAdopt) for NODES.
#   - The agent-to-agent MESH (one agent invoking/acting on another) is control.feature; this spec
#     is about SEEING, ADOPTING, and the LIFECYCLE, and defers the invoke/act mechanics to it.
#
# THE MODEL THIS PROPOSES (the "figure out how we properly do this"):
#   1. AN INSTANCE BECOMES AN AGENT by registering `operate` - i.e. it is running the agent loop
#      and offers to act on the Edge. It then draws as ◆ agent, on its node, everywhere.
#   2. SAME-MACHINE instances/agents are already yours (under your node) - they appear under RUNNING
#      HERE with no adoption needed; they are members the moment they register.
#   3. A DIFFERENT-MACHINE instance/agent that is not yet a member appears as a CANDIDATE when
#      discovered, and is adopted with `a` - the UniFi moment - which enrolls its NODE and brings
#      its agents onto your Edge.
#   4. A PERSISTENT agent (one the owner marked to keep) that goes away is drawn DARK and KEPT, with
#      two owner actions: RESUME it (relaunch on its machine) or REMOVE it from the Edge (forget).
#   5. Agents MONITOR each other through the Edge: each agent's presence and health is on [3]
#      (health.feature), and one agent can see another's status - the read side of the mesh.
#
# HONESTY, as everywhere: an instance is only an agent if it really carries `operate`; a dark agent
# is drawn dark and kept, never quietly dropped; resume/remove change real state and are confirmed;
# nothing claims an agent is running that is not registered live.
#
# GROUND TRUTH once approved: internal/edge/instance.go (Register, Household, the operate cap),
# internal/edge/node.go (Operate), internal/tui/edge_view.go (RUNNING HERE, the graph, adopt),
# cmd/rogerai/edgehost.go + edgeinstance.go (registerInstance, the household hook), features/edge/
# instances.feature, discovery.feature, control.feature (the invoke/act mesh), health.feature.
#
# Tags: @edge (the registry + lifecycle rules), @tui ([3] shows/adopts/resumes), @cli (the
# `roger edge agent` verbs).

Feature: A roger instance can be an agent on the Edge, seen and adopted like a device, and resumed or removed when it is meant to persist

  # =========================================================================
  # 1. AN INSTANCE BECOMES AN AGENT
  # =========================================================================

  # An instance declares itself an agent in ANY of three ways (founder ruling 2026-09-21, "all of
  # the above"). All three set one honest bit on the instance record - Agent - which is what the
  # surfaces draw as ◆; the operate CAPABILITY (which every instance can claim for the mesh) is not
  # by itself the "this is an agent" signal, so a plain serving instance is never mis-drawn.

  @cli
  Scenario: (1) the command - roger edge agent on marks this instance an agent
    When they run "roger edge agent on"
    Then this instance is recorded as an agent, persisted in the config role
    And it re-registers at once so it shows as an agent without a restart
    When they run "roger edge agent off"
    Then it is no longer an agent, and drops the mark

  @edge
  Scenario: (2) the config role - a roger configured as an agent registers as one
    Given a roger whose config role marks it an agent
    When it registers on this machine
    Then its instance record carries Agent true
    And it is drawn as an agent with the ◆ mark

  @edge
  Scenario: (3) automatic - a roger launched as an agent job registers as one
    Given a roger launched with ROGER_EDGE_AGENT set, as an agent launcher does
    When it registers
    Then it registers as an agent for that run, without any saved config
    And the env wins for that run over an absent or off config role

  @edge
  Scenario: an instance that only serves or uses is NOT drawn as an agent
    Given a roger instance serving a model, declared an agent by none of the three ways
    When it registers
    Then its record has Agent false
    And it is drawn as a plain instance, never with the ◆ mark, even though it could claim operate

  @edge
  Scenario: the agent mark follows the declaration, live
    Given a serving instance that is not an agent
    Then RUNNING HERE draws it plainly
    When the owner declares it an agent by any of the three ways and it re-registers
    Then RUNNING HERE draws it as an agent
    And nothing else about it changed but the one honest bit

  @tui
  Scenario: RUNNING HERE shows this machine's instances and marks the agents
    Given this machine runs a serving instance "desk" and an agent "scout"
    When the Edge screen renders
    Then RUNNING HERE lists both, this instance among them
    And "scout" carries the agent mark and says it can operate on other nodes
    And "desk" says what it serves, not that it is an agent

  # =========================================================================
  # 2. ADOPTION - the UniFi moment
  # =========================================================================

  @tui
  Scenario: opening the Edge screen is discovery - unadopted machines appear as candidates
    Given another machine on this LAN running roger that is not on this Edge
    When the owner opens the Edge screen
    Then that machine appears as a CANDIDATE, outside the fleet, with no wire to the desk
    And it is marked adoptable with a

  @tui
  Scenario: adopting a candidate brings its node and its agents onto the Edge
    Given a candidate machine that is running an agent
    When the owner adopts it
    Then its node joins the Edge
    And its agent appears under it, marked ◆, on the next read
    And the board shows it move from the candidate edge into the fleet

  @edge
  Scenario: a same-machine instance needs no adoption - it is already yours
    Given a second roger instance started on this machine
    When it registers
    Then it is a member at once, under this node, in RUNNING HERE
    And it is never shown as a candidate to adopt, because it is already on your Edge

  # =========================================================================
  # 3. PERSISTENCE, RESUME, REMOVE
  # =========================================================================

  @edge
  Scenario: marking a roger a persistent agent records it durably, not just for this run
    Given a roger the owner declares a persistent agent with the command
    When it registers
    Then a DURABLE persistent-agent record is written for it, keyed by its name and node
    And that record outlives the process, unlike the ephemeral instance registration that ages out

  @edge
  Scenario: an env-declared agent is ephemeral, never durable
    Given a roger declared an agent only by the ROGER_EDGE_AGENT env
    When it registers
    Then no durable persistent-agent record is written
    And when it stops it simply ages out, offering no resume

  @tui
  Scenario: a persistent agent that stops is drawn dark and kept, with resume and remove
    Given a persistent agent "scout" whose process has stopped
    When the Edge screen renders
    Then "scout" is drawn dark and kept, marked a persistent agent that is not running
    And the owner is offered to resume it or remove it

  @cli
  Scenario: roger edge agent resume relaunches a dark persistent agent as a plain roger
    Given a persistent agent "scout" that is not running
    When they run "roger edge agent resume scout"
    Then a plain roger is launched for it, with the agent role and its own name
    And it returns to RUNNING HERE as a live agent on its next registration
    # "Plain roger invocation" is the chosen mechanism for now (founder ruling 2026-09-21): resume
    # spawns `roger` locally with ROGER_EDGE_AGENT and its instance name, detached, and iterates.

  @cli
  Scenario: roger edge agent resume declines when it cannot find the agent
    Given no persistent agent named "ghost"
    When they run "roger edge agent resume ghost"
    Then it says there is no such persistent agent, and launches nothing

  @cli
  Scenario: roger edge agent remove drops a persistent agent's record, offering no more resume
    Given a persistent agent "scout" the owner no longer wants
    When they run "roger edge agent remove scout"
    Then its durable record is deleted
    And it is no longer drawn as a persistent agent, nor offered to resume
    And a running "scout" is left alone - remove stops the persistence, it does not kill a live process

  @edge
  Scenario: an ephemeral instance that stops simply ages out, never nagging for resume
    Given a non-persistent instance that stops running
    When it ages out
    Then it leaves RUNNING HERE quietly
    And nothing offers to resume it, because it was never meant to persist

  # =========================================================================
  # 4. THE AGENT MESH - see and monitor each other (invoke/act is control.feature)
  # =========================================================================

  @tui
  Scenario: an agent sees the other agents on the Edge and their state
    Given two agents on the Edge, "scout" and "scribe"
    When either one's owner opens the Edge screen
    Then both agents are drawn, each on its node, with its presence and what it can do
    And an agent that is serving a model shows the band it serves

  @edge
  Scenario: one agent can read another's status through the Edge, receipted
    Given agent "scout" wants to know if agent "scribe" is available
    When it reads "scribe"'s status over the Edge
    Then it learns "scribe"'s presence and capabilities
    And the read is authenticated as an Edge member, not open to the world
    # The ACT side - "scout" asking "scribe" to do work - is control.feature, gated there.

  # =========================================================================
  # 5. WHAT THIS DOES NOT DO (kept honest)
  # =========================================================================

  @edge
  Scenario: nothing is drawn as an agent that is not really running one
    Given an instance with no operate capability
    Then it is never drawn with the agent mark
    And a dark agent is drawn dark and kept, never as live, and never silently removed

  # =========================================================================
  # 6. THE [3] NETWORK MAP + CONTROL PANEL - a navigable UniFi-style topology
  # =========================================================================
  #
  # FOUNDER DIRECTION 2026-09-21 (Image #15): "allow the keyboard to move like it does on the agent
  # ... control the highlighted box ... I connected an agent and clicked add agent but I don't see
  # how add agent got represented ... a way better, more colorful ASCII chart of our network and
  # nodes - imagine how UniFi looks on the web console but on ASCII ... rework this entire view."
  #
  # So [3] on a set-up machine is a MAP: the authority machine drawn as a card at the top, the
  # rogers and agents running on it hung under it on a branch as cards, the SELECTED card glowing,
  # moved with the arrow keys. The control panel below acts on WHATEVER is highlighted - the buttons
  # follow the selection, so the owner "controls the highlighted box" instead of one fixed set.

  @tui
  Scenario: the set-up screen is a topology - the machine, then the rogers on it as cards
    Given a set-up machine running a serving roger and an agent
    When the Edge screen renders
    Then the machine is drawn as a card at the top, marked the authority
    And each roger running on it is drawn as its own card, hung under the machine on a branch
    And the agent card carries the agent mark, the serving card shows the band it serves
    And it states the purpose: a network of authorized rogers and agents the authority controls

  @tui
  Scenario: the arrow keys move the highlight across the nodes
    Given a set-up machine with the machine, this roger, and a dark agent on the map
    When the owner presses the arrow keys
    Then the highlight moves from node to node, glowing the selected card
    And it never runs past the ends - it clamps at the first and last node
    And the highlight starts on THIS roger, so its actions are to hand without moving

  @tui
  Scenario: the control panel offers the actions that fit the SELECTED node
    Given a set-up machine
    When THIS roger is highlighted
    Then the panel offers Make Agent and Rename, naming this roger
    When the machine is highlighted
    Then the panel offers Add Agent, Add Device and Allow Machine, naming this machine
    When a dark persistent agent is highlighted
    Then the panel offers Resume and Remove, naming that agent
    And each button carries the letter to press

  @tui
  Scenario: pressing the Make Agent button makes this roger an agent in place
    Given a set-up machine whose roger is highlighted and is not an agent
    When the owner presses the Make Agent button
    Then this roger becomes an agent at once, drawn ◆, without leaving the screen
    And the button now offers to Stop Agent
    When the owner presses it again
    Then this roger is no longer an agent

  @tui
  Scenario: the Add Agent button launches a new agent, and it appears on the map
    Given a set-up machine with the machine highlighted
    When the owner presses the Add Agent button
    Then a new agent roger is launched here
    And it joins the map as its own card under the machine on its next registration

  @tui
  Scenario: the Rename button renames this roger in place
    Given a set-up machine
    When the owner presses Rename, clears the name, types a new one and confirms
    Then this roger is renamed at once
    And esc cancels the rename without changing anything

  @tui
  Scenario: resuming a highlighted dark agent relaunches it, in place
    Given a set-up machine with a dark persistent agent highlighted
    When the owner presses the Resume button
    Then a plain roger is launched for that agent, with the agent role and its name
    And it returns to the map as a live agent on its next registration

  @tui
  Scenario: removing a highlighted dark agent forgets it, in place
    Given a set-up machine with a dark persistent agent highlighted
    When the owner presses the Remove button
    Then that agent's durable record is dropped and it leaves the map
    And a running agent is never touched - remove ends the persistence, not a live process
    And the highlight steps back onto a node that is still there

  @tui
  Scenario: a build that cannot perform an action does not show a dead button
    Given a set-up machine whose host wired no agent action
    When THIS roger is highlighted
    Then it shows no Make Agent button
    And nothing on the panel is a button that does nothing when pressed
