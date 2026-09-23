# ROGER EDGE - WHAT THE AGENTS DO: jobs, models, use vs share, device routing (PROPOSED).
#
# STATUS: PARTIALLY IMPLEMENTED (2026-09-21). BUILT: the AgentJob record (internal/edge/binding.go),
# `roger edge model`/`roger edge job`, the ⏎ detail's DOES/JOB/SHARES/USES, the in-place Bind Model
# (b), the card job labels, and phone/LAN adoption on the map (§E). NOT YET BUILT: the actual route
# side of share/use across the network (control.feature) and device->preferred-agent routing (§D).
# This is the design for the founder's 2026-09-21 direction, after seeing two rogers "doing nothing":
#
#   "I would have assumed the agents could be doing some sort of job, but right now they are doing
#    nothing. How do we monitor that, and what job? I can imagine we connect a specific model - say
#    agent-1 uses qwen and agent-2 uses pico. Maybe agents do other things, so the detail should
#    show capability. If a device is connected we should show which agent or model it would use, if
#    not its own. And maybe it doesn't use, it can either USE or SHARE - an agent has local
#    inference we need to use across the network. And if I run roger on my phone on the local
#    network I should see it and add it to the RogGentooEdged."
#
# THE PIECES THIS PROPOSES (each honest, each drawn on [3] and readable in the ⏎ node detail that
# already exists - the detail shows DOES / JOB / SHARES / USES / CAN DO / resources today, with JOB
# and USES honestly saying "not assigned yet"; this spec is what fills those in):
#
#   A. WHAT AN AGENT DOES - its JOB. An agent is an instance carrying `operate`; a job is the bound
#      work it runs. This spec defines a small, honest job vocabulary to start:
#        - serve   : keep a local model on air for the Edge (it SHARES local inference).
#        - watch   : monitor named nodes/agents and raise an alert when one goes dark (health.feature).
#        - relay   : carry requests from one node to a model on another (the mesh, control.feature).
#      "none" is a real, shown state: an agent with no job is drawn and its detail says so - never
#      faked into looking busy.
#
#   B. A MODEL BOUND TO AN AGENT. The owner binds a model to an agent (agent-1 -> qwen, agent-2 ->
#      pico). The binding is a durable role, like the persistent-agent record. The bound model is
#      what the agent SERVES (shares) and/or what it USES for its own inference.
#
#   C. USE vs SHARE - the two postures on a local model, the founder's distinction:
#        - SHARE : the model is on air to the Edge; other authorized nodes may route to it.
#        - USE   : the agent draws on the model for its own work, but does not offer it out.
#      A model can be both (use AND share), one, or neither. The detail's SHARES/USES lines are
#      exactly these two postures.
#
#   D. A DEVICE'S PREFERRED AGENT/MODEL. A device that cannot run a model of its own (a board, a
#      phone) is routed: its record names the agent or model it USES when it needs inference. [3]
#      shows that preference on the device, and its detail names where its inference comes from.
#
#   E. MONITORING - what each agent is doing, live. [3] and the detail show each agent's job, its
#      bound model, its use/share posture, and its health, read from the live registry - so "what
#      are they doing" is answered on the screen, not guessed.
#
# HONESTY, as everywhere: a job is shown only if the agent really carries it; a model binding is
# shown only if it is really set; USE/SHARE reflect the real posture; an unrouted device says it is
# unrouted rather than inventing a model; nothing claims work that is not running.
#
# GROUND TRUTH once approved: internal/edge/instance.go (the instance record + bands + caps), the
# persistent-agent record (internal/edge/persist.go) as the model for a durable binding,
# internal/tui/edge_map_view.go (the ⏎ detail's DOES/JOB/SHARES/USES lines), control.feature (the
# route/relay mesh), health.feature (watch + alerts), features/edge/instances.feature.
#
# Tags: @edge (the job/binding records + rules), @tui ([3] shows jobs/models/postures), @cli (the
# `roger edge job` / `roger edge model` verbs).

Feature: An Edge agent runs a named job on a bound model, sharing or using local inference, and a device routes to the agent or model it prefers

  # =========================================================================
  # A. AN AGENT'S JOB - what it does, shown and monitored
  # =========================================================================

  @edge
  Scenario: an agent with no job is drawn honestly, never faked busy
    Given an agent on the Edge with no job assigned
    When the Edge screen renders and its detail is opened
    Then its job reads "none assigned"
    And nothing claims it is doing work it is not

  @cli
  Scenario: assigning a serve job puts a model on air for the Edge
    Given an agent "worker-1" and a local model "qwen3-30b"
    When they run "roger edge job worker-1 serve qwen3-30b"
    Then "worker-1" carries the serve job bound to "qwen3-30b", persisted durably
    And it re-registers so the job shows without a restart
    And its detail reads DOES serve, SHARES qwen3-30b

  @cli
  Scenario: assigning a watch job monitors named nodes and raises alerts
    Given an agent "sentry"
    When they run "roger edge job sentry watch greenhouse pump"
    Then "sentry" carries the watch job over "greenhouse" and "pump"
    And when a watched node goes dark, sentry raises the alert (health.feature)

  @cli
  Scenario: clearing a job leaves the agent an agent with nothing to do
    Given an agent "worker-1" running a serve job
    When they run "roger edge job worker-1 none"
    Then it drops the job durably
    And it remains an agent, its detail reading "none assigned", never a lie

  @tui
  Scenario: the map and detail monitor what each agent is doing, live
    Given two agents, one serving "qwen3-30b" and one watching a board
    When the owner opens the Edge screen
    Then each agent's card names its job at a glance
    And each agent's detail names its job, its bound model, and its health
    And a job that stops updates the screen on the next read, not on a timer

  # =========================================================================
  # B + C. A BOUND MODEL, and USE vs SHARE
  # =========================================================================

  @edge
  Scenario Outline: a model bound to an agent has a use/share posture
    Given agent "<agent>" with model "<model>" bound as "<posture>"
    When its detail is opened
    Then SHARES shows "<shares>" and USES shows "<uses>"

    Examples:
      | agent   | model     | posture     | shares    | uses      |
      | share-1 | qwen3-30b | share       | qwen3-30b | not used  |
      | use-1   | pico      | use         | not shared| pico      |
      | both-1  | qwen3-30b | use, share  | qwen3-30b | qwen3-30b |
      | idle-1  |           | none        | nothing   | not assigned |

  @edge
  Scenario: a shared model is reachable by other authorized nodes, a used-only model is not
    Given agent "share-1" sharing "qwen3-30b" and agent "use-1" using "pico" without sharing
    When another Edge node asks the Edge for a model
    Then "qwen3-30b" on share-1 is offered as a route
    And "pico" on use-1 is NOT offered - use without share keeps it private to that agent
    And every route is authenticated as an Edge member, never open to the world

  @edge
  Scenario: two agents bind different models, side by side
    Given agent "agent-1" bound to "qwen3-30b" and agent "agent-2" bound to "pico"
    When the Edge screen renders
    Then agent-1 shows it serves qwen3-30b and agent-2 shows it serves pico
    And the authority sees both models available on its Edge

  # =========================================================================
  # D. A DEVICE ROUTES TO A PREFERRED AGENT OR MODEL
  # =========================================================================

  @edge
  Scenario: a device that cannot run a model routes to a preferred agent
    Given a board "greenhouse" that runs no model of its own
    And a preferred agent "worker-1" set for it
    When the board's detail is opened
    Then it names "worker-1" as where its inference comes from
    And a request the board raises is carried to worker-1's model

  @tui
  Scenario: a device with no route says so, rather than inventing a model
    Given a phone "pixel-8" on the Edge with no preferred agent or model
    When its detail is opened
    Then USES reads "not assigned"
    And the owner is shown how to route it to an agent or model

  # =========================================================================
  # E. A PHONE (OR ANY NEW ROGER) ON THE LAN IS SEEN AND ADDED
  # =========================================================================
  #
  # This is discovery + adoption (agents.feature §2, discovery.feature) applied to a roger on a
  # phone: it appears as a CANDIDATE on the owner's Edge, and adopting it brings it onto the
  # RogGentooEdged authority. The job/model pieces above then apply to it like any node.

  # IMPLEMENTED 2026-09-21 on the network map: candidates draw in a DISCOVERED band, navigable with
  # the arrows, adopted with a; adopted members draw in an ON THE EDGE band. The map is the home view
  # of a set-up machine up to edgeMaxGraphNodes; a larger fleet falls back to the graph until the
  # manual grid/list switch lands (features/edge/map_scale.feature).
  @tui
  Scenario: a roger running on a phone on the LAN appears as an adoptable candidate
    Given roger running on a phone on this LAN, not yet on this Edge
    When the owner opens the Edge screen on the authority
    Then the phone appears in the DISCOVERED band on the map, marked adopt with a
    And pressing a brings the phone onto the RogGentooEdged Edge
    And it then appears in the ON THE EDGE band as a member
    And once on, the phone can be routed to an agent/model like any device
