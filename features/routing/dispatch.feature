# Multi-instance DISPATCH: once pickFor has chosen a node, the relay must get the job to
# a poller that is actually attached to THAT node — which, at the 2-instance cap, may be a
# DIFFERENT broker instance than the one holding the request. Dispatch is local when a
# local poller is attached, else it crosses the rendezvous bus to the peer instance. Each
# outcome is counted (telemetry) and surfaces read-only on /admin/live.
#
# GROUND TRUTH (cmd/rogerai-broker/instmetrics.go counters + the relay dispatch path):
#   localDispatch++   a local poller took the job (single-instance ALWAYS takes this path)
#   busDispatch++     no local poller, but a peer instance's poller took it over the bus
#   busNoPoller++     no poller on ANY instance -> 503 (cross-instance "queue full")
#   busDispatchErr++  the bus itself failed (e.g. Valkey down) -> 503, counted distinctly
#   The counters are monotonic since process start, a single atomic add on the hot path
#   (no mutex/alloc), PURE TELEMETRY (they change no request behavior), and appear under
#   health.dispatch in /admin/live ONLY in multi-instance mode (single-instance is unchanged).
#
# Enforced by: cmd/rogerai-broker/instmetrics_test.go, multiinstance_test.go.

Feature: Multi-instance dispatch + its telemetry

  # --- single instance --------------------------------------------------------
  Scenario: A single-instance broker always dispatches locally
    Given a single-instance broker with a poller attached to node "n1"
    When a relay for "n1" dispatches
    Then localDispatch increments by 1
    And no bus counter (busDispatch/busNoPoller/busDispatchErr) moves

  # --- cross-instance success -------------------------------------------------
  Scenario: With the poller on the peer instance, the job crosses the bus
    Given instance A holds the request and instance B has the poller for node "n1"
    When instance A dispatches the relay for "n1"
    Then busDispatch increments by 1 on instance A
    And localDispatch stays 0 on instance A
    And the no-poller / bus-error counters stay 0

  # --- no poller anywhere -----------------------------------------------------
  Scenario: No poller on any instance returns 503 and counts a no-poller
    Given a multi-instance broker with NO poller attached to node "n1" on any instance
    When a relay for "n1" dispatches
    Then busNoPoller increments by 1
    And busDispatch stays 0
    And the relay responds 503 Service Unavailable

  # --- bus failure ------------------------------------------------------------
  Scenario: A dead rendezvous bus fails cleanly and counts a bus error
    Given a multi-instance broker whose rendezvous bus (Valkey) is down
    When a relay that needs cross-instance dispatch runs
    Then busDispatchErr increments by at least 1
    And the relay responds 503 (never a hang or a silent drop)

  # --- telemetry exposure -----------------------------------------------------
  Scenario: The dispatch counters surface read-only on /admin/live in multi-instance mode
    Given a multi-instance broker that has handled some dispatches
    When the founder reads GET /admin/live
    Then health.dispatch carries the local/bus/no-poller/bus-error snapshot
    And health.instance_id identifies which instance answered

  Scenario: A single-instance /admin/live omits the multi-instance keys (shape unchanged)
    Given a single-instance broker
    When the founder reads GET /admin/live
    Then health has neither instance_id nor a dispatch snapshot
