# Ping (the homepage concierge) fails FAST past a registered-but-dead station instead
# of burning the full relay wait on it. The concierge dogfoods FREE supply to keep the
# public widget lively, but a node that is heartbeat-fresh yet NOT proven-live (never
# answered a canary, or its last proof is stale) must be SKIPPED AT THE PICK so Ping
# falls through to Groq in seconds — not after the ~30s relay ceiling on each dogfood
# path (measured: ~61s live, via:groq). A genuinely slow-but-LIVE flagship still gets
# its full relay headroom, because it is proven-live.
#
# PROVEN-LIVE (the pick gate, active probe ENABLED) = the node has a COMPLETION-PROVEN
# canary — a PASSED canary that ran to COMPLETION (returned counted output tokens) with a
# clean failure streak (verifiedServing) — OR a real COMPLETED served request
# (successCount>0), AND that proof is FRESH (within the probe ceiling; measurementStale=
# false). Proven-LIVE (answered once) is not proven-COMPLETE (finished once): a reasoning
# model (gpt-oss-120b) can return a 2xx reasoning channel and STALL without a countable
# answer — that is TTFT-alive but never COMPLETED, so it must NOT read verified and must be
# skipped from the FIRST pick (a passed-but-uncompleted canary used to leak through and
# cost Ping the full ~30s relay wait; measured cold /concierge = 61s live). When the active
# probe is DISABLED there is no proven-liveness signal, so the gate is inert and the
# legacy heartbeat-only pick is preserved (Ping never goes dark just because probing is
# off). This is the FREE public surface only — the paid relay/billing pick is untouched,
# and the graceful-degrade contract (dogfood miss -> Groq -> canned, never an error) and
# the moderation screen + per-IP rate limit + daily cap ordering are all unchanged.

Feature: Concierge dogfood fails fast past a registered-but-dead station
  As the operator of the public Ping concierge
  I want the free-station and grant dogfood PICK to skip a node that is not proven-live
  So that Ping falls through to Groq in seconds instead of burning the ~30s relay wait

  Background:
    Given the active liveness probe is enabled

  # --- free-station dogfood pick (pickFreeStation) --------------------------------

  Scenario: A heartbeat-fresh but never-probed free station is skipped at the pick
    Given a free on-air station that heartbeats fresh
    And that station has never passed a liveness probe
    When the concierge picks a free station
    Then no free station is picked

  Scenario: A free station whose last passed probe is stale is skipped at the pick
    Given a free on-air station that heartbeats fresh
    And that station passed a canary but its last measurement is older than the probe ceiling
    When the concierge picks a free station
    Then no free station is picked

  Scenario: A free station with a failing probe streak is skipped at the pick
    Given a free on-air station that heartbeats fresh
    And that station has a recent passed probe but a non-zero failure streak
    When the concierge picks a free station
    Then no free station is picked

  Scenario: A proven-live free station IS picked
    Given a free on-air station that heartbeats fresh
    And that station passed a canary within the probe ceiling
    When the concierge picks a free station
    Then that free station is picked

  Scenario: A free station proven-live by a recent real relay IS picked
    Given a free on-air station that heartbeats fresh
    And that station passed a canary within the probe ceiling
    And that station served a real request just now
    When the concierge picks a free station
    Then that free station is picked

  # --- grant dogfood pick (pickGrantStation) --------------------------------------

  Scenario: A heartbeat-fresh but never-probed grant node is skipped at the pick
    Given the grant owner has an on-air node offering the granted model that heartbeats fresh
    And that node has never passed a liveness probe
    When the concierge picks a grant station
    Then no grant station is picked

  Scenario: A proven-live grant node IS picked
    Given the grant owner has an on-air node offering the granted model that heartbeats fresh
    And that node passed a canary within the probe ceiling
    When the concierge picks a grant station
    Then that grant station is picked

  # --- completion-proven gate: a TTFT-alive-but-stall-after-first-token node -------
  # A node can PASS the first-token/liveness canary (verifiedServing) yet never FINISH
  # a real generation (it stalls after the first token). Proven-LIVE (answered once) is
  # not proven-COMPLETE (finished once). The concierge pick must require completion
  # evidence — a canary that ran to completion, OR a real completed relay — so a
  # stall-after-first-token node is skipped from the FIRST hit, not only after the first
  # 30s relay timeout.

  Scenario: A canary-verified free station that never COMPLETED a generation is skipped
    Given a free on-air station that heartbeats fresh
    And that station passed a liveness canary but the canary did not complete a generation
    When the concierge picks a free station
    Then no free station is picked

  Scenario: A free station whose canary ran to COMPLETION is picked
    Given a free on-air station that heartbeats fresh
    And that station passed a canary that ran to completion within the probe ceiling
    When the concierge picks a free station
    Then that free station is picked

  Scenario: A free station proven by a real COMPLETED relay is picked
    Given a free on-air station that heartbeats fresh
    And that station has a stall-only canary but completed a real relay just now
    When the concierge picks a free station
    Then that free station is picked

  Scenario: A completion-proven free station that later goes stale is skipped
    Given a free on-air station that heartbeats fresh
    And that station passed a completing canary but its last measurement is older than the probe ceiling
    When the concierge picks a free station
    Then no free station is picked

  Scenario: A canary-verified grant node that never COMPLETED a generation is skipped
    Given the grant owner has an on-air node offering the granted model that heartbeats fresh
    And that node passed a liveness canary but the canary did not complete a generation
    When the concierge picks a grant station
    Then no grant station is picked

  Scenario: A grant node whose canary ran to COMPLETION is picked
    Given the grant owner has an on-air node offering the granted model that heartbeats fresh
    And that node passed a canary that ran to completion within the probe ceiling
    When the concierge picks a grant station
    Then that grant station is picked

  # --- end-to-end: the exact prod symptom (cold /concierge = 61s) -----------------

  Scenario: A stall-after-first-token free station makes the dogfood relay miss FAST
    Given a free on-air station that heartbeats fresh
    And that station passed a liveness canary but the canary did not complete a generation
    And the concierge relay wait is 30 seconds
    When the concierge runs the free-station dogfood relay
    Then the relay reports not served
    And the relay returned in well under the relay wait

  # --- BOOTSTRAP: a genuinely healthy node still becomes pickable ------------------

  Scenario: A fresh node becomes pickable the moment its FIRST completing canary lands
    Given a free on-air station that heartbeats fresh
    And that station has never passed a liveness probe
    When a completing canary probe records for that station
    And the concierge picks a free station
    Then that free station is picked

  # --- the end-to-end fail-fast contract through the relay ------------------------

  Scenario: A dead free station makes the dogfood relay miss FAST, not after the relay wait
    Given a free on-air station that heartbeats fresh
    And that station has never passed a liveness probe
    And the concierge relay wait is 30 seconds
    When the concierge runs the free-station dogfood relay
    Then the relay reports not served
    And the relay returned in well under the relay wait

  Scenario: A proven-live-but-slow free station still gets the full relay headroom
    Given a free on-air station that heartbeats fresh
    And that station passed a canary within the probe ceiling
    When the concierge runs the free-station dogfood relay against a node that answers
    Then the relay serves the station reply

  # --- the graceful-degrade contract is preserved --------------------------------

  Scenario: With the probe disabled the legacy heartbeat-only pick still serves
    Given the active liveness probe is disabled
    And a free on-air station that heartbeats fresh
    And that station has never passed a liveness probe
    When the concierge picks a free station
    Then that free station is picked

  Scenario: A dead dogfood station degrades Ping to Groq, never an error
    Given the free-station dogfood misses because no station is proven-live
    And Groq is available
    When a visitor asks Ping a question
    Then Ping answers via Groq
