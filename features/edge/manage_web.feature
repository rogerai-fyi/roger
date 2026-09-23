# ROGER EDGE - THE MANAGE CONSOLE: an offline-first fleet dashboard in the WebUI (PROPOSED).
#
# STATUS: PROPOSED, awaiting founder approval. NOT IMPLEMENTED. Spec for the browser half of
# fleet management the founder asked for 2026-09-21: "our website and dashboard section we can
# introduce a management web portion that you can access from the webui ... since this is meant to
# be offline." It is the console counterpart to the TUI patch bay and the `roger edge` CLI, over
# the SAME Edge fabric and the SAME health layer (features/edge/health.feature).
#
# WHAT IT IS
#
# The console already has an EDGE tab (features/edge/console_view.feature: GET /api/edge snapshot,
# POST /api/edge/adopt). This adds a MANAGE view: a fleet HEALTH dashboard (each node with its
# temperature, memory, disk, uptime, presence and any firing alerts, drawn as bars the same way
# the patch bay draws them) plus the safe management actions the CLI already exposes (adopt, name,
# forget, set an alert rule, read sessions). It reuses the EdgeHooks the console is already built
# from, so it is one source of truth with the TUI, not a second implementation.
#
# OFFLINE-FIRST, BY CONSTRUCTION. The console is served by the local `roger webui` on the owner's
# own machine; the browser talks only to it, over the same localhost + token guard the rest of the
# console uses. Nothing here needs the internet: the page's assets are the shipped console assets,
# the data is the local fleet, and an airgapped Edge serves the whole dashboard. No external fonts,
# no CDN, no cloud.
#
# HONESTY, carried from the fabric. It draws only what it has: a node that reports no metric shows
# a dash, never a 0; a dark node is drawn dark and KEPT; a stale reading shows its age. Enrollment
# and destructive actions stay explicit and confirmed, exactly as the CLI and TUI require.
#
# WHAT IT IS NOT. It is not remote administration over the internet, and it does not run arbitrary
# commands on nodes. It manages the fleet the console's host already has hooks for. Anything that
# would open a new remote-control surface is out of scope and belongs to control.feature, gated
# separately.
#
# GROUND TRUTH once approved: internal/webui/edge.go (the /api/edge handlers, the localhost+token
# guard, edgeSnapshot), internal/webui/assets/console.{html,js,css} (the EDGE tab this extends),
# features/edge/health.feature (the health + alert layer it draws), features/edge/console_view.
# feature (the read model), features/edge/patchbay.feature (the shared drawing rules).
#
# Tags: @web runs in internal/webui (the real Server over httptest, the real localhost+token
# guard, the shipped assets, a real fleet + health hook; a fake alert sink records deliveries).

Feature: The console has an offline-first Manage view that shows fleet health and performs the same safe management the CLI and TUI do

  # =========================================================================
  # 1. THE DASHBOARD READS FLEET HEALTH
  # =========================================================================

  @web
  Scenario: the manage view reads the fleet's health in one snapshot
    Given a console over an Edge with three members reporting health
    When the manage data is read
    Then each node carries its temperature, memory, disk, uptime and presence
    And a node reporting no metric carries a dash for it, never a zero
    And it is a single snapshot, like every other console read

  @web
  Scenario: a resource with a real total draws as a bar, one without draws as a figure
    Given a node reporting 8 GB of 32 GB memory used
    And a node reporting a memory figure with no total
    When the manage data is read
    Then the first node's memory is a level of a real total
    And the second node's memory is a plain figure, not a bar

  @web
  Scenario: an offline node is drawn dark and kept, with how long it has been dark
    Given a member that has been offline past the liveness window
    When the manage data is read
    Then the node is present in the dashboard, marked offline, with its dark duration
    And it is never dropped from the list

  @web
  Scenario: a firing alert shows on its node
    Given a CPU-temperature rule firing for "pi"
    When the manage data is read
    Then "pi" carries the firing alert with the metric, the value and the threshold
    And a node with no firing alert carries none

  # =========================================================================
  # 2. OFFLINE-FIRST AND PRIVATE
  # =========================================================================

  @web
  Scenario: the dashboard is served entirely by the local console, no internet
    Given a console on an airgapped machine
    When the manage view is opened and the data is read
    Then the page and its data come only from the local host
    And no external asset, font, or service is fetched

  @web
  Scenario: manage is behind the same localhost and token guard as the rest of the console
    When a manage request arrives without the console token
    Then it is refused
    And no fleet state changed

  @web
  Scenario: a sink secret is never sent to the browser
    Given a Discord sink configured with a secret URL
    When the manage data is read
    Then the dashboard can say a Discord sink is configured
    And the secret URL is never sent to the browser

  # =========================================================================
  # 3. THE SAFE MANAGEMENT ACTIONS (the same the CLI and TUI expose)
  # =========================================================================

  @web
  Scenario: adopt takes a candidate into the fleet, POST-only and confirmed
    Given a discovered candidate at the edge of the fleet
    When the owner adopts it from the dashboard
    Then it is enrolled through the host's adopt hook, the same the TUI uses
    And a GET to the adopt path is refused

  @web
  Scenario: naming a node updates its name through the host, not a browser-only label
    Given a member "n_ab12" the owner wants to call "greenhouse"
    When the owner sets its name from the dashboard
    Then the node's real name becomes "greenhouse" through the host
    And every surface then shows the new name

  @web
  Scenario: forgetting a node is confirmed and revokes, never a silent delete
    Given a member "oldpi" the owner wants to remove
    When the owner forgets it from the dashboard
    Then the dashboard requires an explicit confirmation first
    And on confirmation the node leaves the fleet and its certificate is revoked, as the CLI does

  @web
  Scenario: setting an alert rule from the dashboard stores it and takes effect
    When the owner sets a rule that disk over 90 percent is a warning, fleet-wide
    Then the rule is stored through the host
    And the next health read evaluates it

  @web
  Scenario: a test alert can be sent from the dashboard to prove a sink
    Given a configured sink
    When the owner sends a test alert from the dashboard
    Then a clearly-marked test reaches the sink
    And it raises no real alarm and changes no rule state

  @web
  Scenario: enrollment is never performed from the browser
    Given a machine that is not yet a member
    When the manage view is opened
    Then it points at the terminal wizard for setup, as onboard_web.feature specifies
    And it does not enroll this machine from the browser

  # =========================================================================
  # 4. IT HOLDS UP
  # =========================================================================

  @web
  Scenario: the dashboard reads correctly with a large fleet and says how many it shows
    Given a fleet larger than the dashboard draws at once
    When the manage data is read
    Then it shows a bounded page of nodes and says how many of how many
    And every shown node still carries its health and presence

  @web
  Scenario: a fleet that cannot be read is an error, never an empty dashboard
    Given the fleet store cannot be read
    When the manage data is read
    Then the dashboard shows the read error
    And it never draws an empty, healthy-looking fleet over a failure
