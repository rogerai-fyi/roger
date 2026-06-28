# Verified serving — the active probe/canary that proves a node is ACTUALLY answering
# right now (distinct from the TEE-confidential ◆ and from mere heartbeat liveness). The
# broker periodically sends a deterministic, broker-originated canary to each online node;
# a node that heartbeats but fails a sustained probe streak is surfaced OFFLINE so a
# consumer never tunes into a dead channel. Third trust pillar; permanent regression net.
#
# GROUND TRUTH (cmd/rogerai-broker/probe.go + market.go):
#   - proberLoop enqueues a broker-ORIGINATED canary job to each online node; the canary
#     (canaryFingerprint) is a deterministic challenge checked for liveness + a coarse
#     model-size sniff + TTFT - a node can't pass by echoing garbage.
#   - online := live && tq.probeFails < probeDeadStreak (market.go:96): a heartbeat-fresh
#     node that has FAILED a sustained streak of liveness probes is shown OFFLINE; ONE OK
#     probe resets the streak and it flips back online.
#   - Verified (offerView.Verified) = a recent PASSED canary (tq.verifiedServing()),
#     surfaced separately from Confidential (◆); it is hard positive evidence feeding the
#     signal's verified-serving term + successFor (a probed-OK-no-traffic node scores ~0.9,
#     NOT the old constant 1.0, and an unproven node sits at a neutral 0.6).
#   - Real served traffic is a FREE measurement: recordServed + markMeasured reset the
#     probe backoff, so an actively-used node is barely probed and reads as freshly verified.
#   - Demand probing: a /discover, /market browse, or a pick on a STALE node schedules a
#     near-term probe (demandProbeSoon). Measurement staleness (0.7..1.0) gives a modest
#     confidence haircut to the MEASURED terms (speed/latency/verified) the longer a node
#     goes unmeasured; a fresh measurement restores full confidence at once.
#   - An OFFLINE node scores 0 signal (no supply / dead channel).

Feature: A node is shown serving only while it passes the active canary, and reads OFFLINE once it fails a sustained probe streak

  Background:
    Given an online node that heartbeats fresh

  # --- the probe-dead streak gates "online" -------------------------------

  Scenario: A heartbeat-fresh node that fails a sustained probe streak reads OFFLINE
    When the node fails liveness probes up to the dead-streak threshold
    Then the node is surfaced OFFLINE in the market even though it still heartbeats

  Scenario: One OK probe resets the streak and the node flips back online
    Given the node is offline from a failed probe streak
    When a single probe succeeds
    Then the node is surfaced online again

  Scenario: An offline node scores zero signal
    Given the node is offline
    Then its market signal is 0

  # --- the Verified (probe-canary) bit ------------------------------------

  Scenario: A recent passed canary sets the Verified bit, separate from confidential
    When the node passes a serving canary
    Then its offer carries Verified=true
    And Verified is independent of the confidential (◆) badge

  Scenario: A probed-OK node with no organic traffic scores positive (not perfect) success
    Given the node has no organic traffic but a recent passed canary
    Then its success evidence is strongly positive but below a proven-perfect rate

  Scenario: A node with no evidence at all sits at a neutral success rate
    Given the node has neither organic traffic nor a passed canary
    Then its success evidence is the neutral no-data value, not an assumed-perfect rate

  # --- the canary is deterministic + broker-originated ---------------------

  Scenario: A node cannot pass the canary by returning garbage
    Given the broker sends its deterministic canary challenge
    When the node returns an answer that fails the fingerprint check
    Then the probe is recorded as failed

  # --- real traffic is a free measurement ---------------------------------

  Scenario: A served request counts as a fresh measurement and defers probing
    When the node serves a quality-validated request
    Then its probe backoff is reset and it reads as freshly measured

  # --- demand probing + measurement staleness -----------------------------

  Scenario: Browsing a stale node schedules a near-term demand probe
    Given the node's measurement is stale
    When a consumer browses the market for its model
    Then a near-term probe is scheduled for that node

  Scenario: A long-unmeasured node takes a confidence haircut on its measured terms
    Given the node has gone long without a fresh measurement
    Then only its measured terms (speed, latency, verified) are discounted, not supply or liveness
    And a fresh measurement restores full confidence at once
