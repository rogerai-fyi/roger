# SHARING / THE ON-AIR LOCK: one live broadcaster per node id, ACROSS EVERY FRONT-END.
#
# THE INCIDENT (2026-07-02, eager-puma-54-voice): the per-node-id on-air lock was only
# taken by the headless `roger share` path (cmd/rogerai/main.go acquireOnAirLock), so an
# abandoned TUI/web-console share and a headless systemd unit could BOTH broadcast the
# SAME node id. The broker then saw one station flapping between two upstreams, each
# side's re-register rotating the other's bridge token (the alternating-401 ping-pong),
# breaking routing and earnings attribution. The controller's share toggle (the TUI and
# the browser console both drive internal/node startLocked) never consulted the lock.
#
# THE CONTRACT: EVERY path that puts a node id on air holds that node id's on-air lock
# for the life of the session - the headless daemon AND the controller toggle alike.
# A toggle against a node id already live in ANOTHER process refuses with the SAME
# clear error the headless path prints (naming the holder + the lock path); stopping a
# share releases its lock; a stale lock left by a crashed/killed process is reclaimed.
#
# GROUND TRUTH:
#   internal/onair: Acquire/LockPath - the cooperative per-node-id lockfile (PID-keyed
#     liveness; advisory, reclaimable, error names the lock path).
#   cmd/rogerai/onair_lock.go: acquireOnAirLock - the headless daemon's wrapper (adds
#     the SIGINT/SIGTERM lock-clearing exit hook).
#   internal/node/controller.go: startLocked acquires the lock BEFORE agent.Start and
#     surfaces a refusal via ToggleResult.Err/PrivateResult.Err (both front-ends render
#     it verbatim); ToggleOnAir(off)/TogglePrivate/StopAll release.
#
# Enforced by: cmd/rogerai/onair_lock_bdd_test.go (REAL cross-process legs via a
# re-exec'd helper process; REAL node.Controller + agent.Start against an httptest
# broker; no mocks) + internal/node/onair_lock_test.go + cmd/rogerai/onair_lock_test.go.

Feature: Sharing - the on-air lock guards every front-end

  # --- the incident: daemon live, console toggles the same node id -------------
  Scenario: A console toggle refuses while a headless daemon broadcasts the same node id
    Given a headless daemon in another process is on air as station "eager-puma-54" model "voice"
    When the operator toggles "voice" on air from the console for station "eager-puma-54"
    Then the toggle is refused with the on-air error naming the live broadcaster
    And no share session is started
    And the other process still holds the on-air lock

  Scenario: The private flip also refuses while another process holds the node id
    Given a headless daemon in another process is on air as station "eager-puma-54" model "voice"
    And the operator is logged in at the console
    When the operator flips "voice" private from the console for station "eager-puma-54"
    Then the flip is refused with the on-air error naming the live broadcaster
    And no share session is started

  # --- the other direction: console live, a second daemon tries ----------------
  Scenario: A live console share blocks a headless daemon for the same node id
    Given the operator toggles "voice" on air from the console for station "eager-puma-54"
    When another process runs the headless on-air acquire for node id "eager-puma-54-voice"
    Then that process is refused with the on-air error naming the live broadcaster

  # --- release on stop ----------------------------------------------------------
  Scenario: Toggling off air releases the lock for the next broadcaster
    Given the operator toggles "voice" on air from the console for station "eager-puma-54"
    When the operator toggles "voice" off air
    Then the on-air lock for node id "eager-puma-54-voice" is released
    And another process can now acquire the headless on-air lock for node id "eager-puma-54-voice"

  Scenario: Stopping all shares releases every held lock
    Given the operator toggles "voice" on air from the console for station "eager-puma-54"
    And the operator toggles "chat" on air from the console for station "eager-puma-54"
    When the operator stops all shares
    Then the on-air lock for node id "eager-puma-54-voice" is released
    And the on-air lock for node id "eager-puma-54-chat" is released

  # --- stale-lock reclaim still works -------------------------------------------
  Scenario: A stale lock from a crashed daemon is reclaimed by the console toggle
    Given a headless daemon in another process is on air as station "eager-puma-54" model "voice"
    But that process is killed without releasing its lock
    When the operator toggles "voice" on air from the console for station "eager-puma-54"
    Then the share goes on air
    And the console's process owns the on-air lock for node id "eager-puma-54-voice"

  # --- no false collisions --------------------------------------------------------
  Scenario: Distinct node ids never collide across processes
    Given a headless daemon in another process is on air as station "eager-puma-54" model "voice"
    When the operator toggles "chat" on air from the console for station "eager-puma-54"
    Then the share goes on air

  # --- the same-process private flip must not self-collide ------------------------
  Scenario: Flipping a live share private keeps the node id under the console's own lock
    Given the operator is logged in at the console
    And the operator toggles "voice" on air from the console for station "eager-puma-54"
    When the operator flips "voice" private from the console for station "eager-puma-54"
    Then the flip succeeds
    And the console's process owns the on-air lock for node id "eager-puma-54-voice"

  # --- a failed start must not leak the lock ---------------------------------------
  Scenario: A share that fails to register never leaks its lock
    Given the console's broker refuses registrations
    When the operator toggles "voice" on air from the console for station "eager-puma-54"
    Then the toggle fails with a registration error
    And the on-air lock for node id "eager-puma-54-voice" is released
