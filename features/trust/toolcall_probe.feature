# TOOL-CALL CAPABILITY PROBE — the broker follow-on that turns an INFERRED "agent-ready"
# reading into a VERIFIED one. This is the fix band_gate.feature flagged (FOUNDER FLAG G1:
# "fund the probe gap — a 'tools' capability label + a broker tool-call canary — as a
# follow-up"). It is the FOURTH trust pillar next to verified-serving (the liveness canary),
# confidential (◆), and lineage receipts.
#
# THE GAP TODAY (grounded in the merged code, origin/main 76d4a24):
#   · protocol.knownCapabilities is a CLOSED node-DECLARED set; its only member is "vision"
#     (protocol.go:105-109, commit 74b109f). There is NO "tools" member, and no probe backs
#     any capability — a node simply ASSERTS "vision" and the broker canonicalizes it.
#   · The broker probe (cmd/rogerai-broker/probe.go) sends a deterministic /v1/chat/completions
#     CANARY and measures liveness + TTFT + tok/s (evalCanary). It does NOT send a tool
#     definition and NEVER inspects a tool_calls response.
#   · /discover + /market emit per-offer Capabilities canonicalized at read (market.go:174),
#     so whatever the CLOSED set recognizes flows to consumers exactly like "vision".
#
# THE RULE THIS SPEC PINS:
#   "tools" is a VERIFIED capability, NOT a declared one. A model earns "tools" only when the
#   broker's own tool-call canary confirms the provider HONORS an OpenAI tool-call request
#   (a well-formed tool_calls response to a "call this function" prompt). A node CANNOT earn
#   it by declaring it — unlike "vision" (declared-not-probed; the asymmetry is called out in
#   the "Adversarial" section and left as FOUNDER FLAG T3). This is the same discipline as
#   confidential ◆ (verified, not asserted) rather than vision (asserted, not verified).
#
# WIRING (reuse, don't rebuild — the minimization rung):
#   · The tool canary rides the EXISTING probe schedule/backoff/jitter/per-owner cap
#     (probeOnce): same adaptive floor->ceiling cadence, so it never hammers a node. It is a
#     SECOND assertion folded into the SAME canary round, not a new loop.
#   · The pass/fail verdict is a PURE function over the response body (toolCallOK), the twin of
#     evalCanary's fingerprint check — table-tested with no live node.
#   · The earned bit is stamped on the node's trust/metrics state (like probeOK / verifiedServing)
#     and materialized into the offer's Capabilities as "tools" on the /discover + /market read.
#   · Multi-instance: the earned bit mirrors to the shared store and is read as a UNION, exactly
#     like the registry/liveness pattern (persistent-state / no per-instance memory), so two broker
#     instances neither double-probe nor split the verdict.
#
# FOUNDER FLAGS (approval gate):
#   T1 (cadence): the tool canary rides the EXISTING performance-probe cadence (adaptive 30s
#      floor -> 15m ceiling). Confirm it is NOT a separate, more-frequent loop.
#   T2 (cost): the canary is an UNBILLED probe job (User="probe", result discarded) with a
#      trivial tool + a tiny max_tokens. Confirm the free/negligible-spend tolerance and whether
#      the tool canary should ride ONLY free-band / already-scheduled rounds.
#   T3 (vision asymmetry): "vision" stays DECLARED-not-probed while "tools" is probed. Confirm we
#      do NOT (yet) probe vision, and that the honesty copy never implies vision is verified.
#   T4 (strictness): does a well-formed tool_calls to the WRONG function name still count as
#      "honors tool-calling" (lenient: structure proves it), or must the name match the canary's
#      trivial function (strict)? Default in this spec: STRUCTURE proves it (lenient) — see the
#      canary table test.

Feature: A model earns the verified "tools" capability only when the broker's tool-call canary confirms the provider honors an OpenAI tool call
  The broker probes tool-calling with its own canary; "tools" is emitted on the public
  feed only for a model that returned a well-formed tool_calls response, and a model that
  stops honoring tool calls loses the capability. A node can never earn it by declaring it.

  Background:
    Given an online node that heartbeats fresh
    And the node offers a chat model with a context window of 131072 tokens

  # --- the signal: "tools" is a KNOWN, canonicalized capability -----------------------

  Scenario: "tools" is a recognized capability, canonicalized like "vision"
    When the broker canonicalizes the capability list "tools"
    Then the capability "tools" survives canonicalization
    And it is lowercased, trimmed, and deduped exactly like "vision"

  Scenario: A node CANNOT earn "tools" merely by declaring it on registration
    Given the node declares the capability "tools" in its offer
    And the broker has never run a passing tool-call canary against that model
    Then the model's public capabilities do NOT include "tools"
    # Declared-not-probed is the vision discipline; "tools" is verified-not-declared.

  # --- the probe: the tool-call canary --------------------------------------------------

  Scenario: A passing tool-call canary earns the model "tools"
    When the broker sends its tool-call canary to the model
    And the provider returns a well-formed tool_calls response
    Then the model earns the verified "tools" capability
    And "tools" appears in that model's public capabilities

  Scenario: A malformed or absent tool_calls response does NOT earn "tools" (unproven stays unproven)
    When the broker sends its tool-call canary to the model
    And the provider answers in plain text with no tool_calls
    Then the model does NOT earn "tools"
    And its public capabilities omit "tools" rather than claiming an unproven one

  Scenario: A provider that emits an empty tool_calls array does not earn "tools"
    When the broker sends its tool-call canary to the model
    And the provider returns finish_reason "tool_calls" but an empty tool_calls array
    Then the model does NOT earn "tools"

  Scenario: A provider that returns garbage/unparseable body does not earn "tools"
    When the broker sends its tool-call canary to the model
    And the provider returns an unparseable response body
    Then the model does NOT earn "tools"

  # --- honest regression: a model that regresses LOSES the cap --------------------------

  Scenario: A model that stops honoring tool calls loses "tools"
    Given the model previously earned the verified "tools" capability
    When a later tool-call canary fails to produce a well-formed tool_calls response
    Then the model loses "tools"
    And its public capabilities no longer include "tools"

  Scenario: The verified "tools" bit is per-MODEL, not per-node
    Given the node offers two chat models
    And only the first model returns a well-formed tool_calls response to the canary
    Then only the first model's public capabilities include "tools"
    And the second model's capabilities omit "tools"

  # --- cadence / caching: reuse the existing probe schedule, never hammer ---------------

  Scenario: The tool-call canary rides the existing adaptive probe cadence (FOUNDER FLAG T1)
    Then the tool-call canary is dispatched on the same probe round as the liveness canary
    And an idle model's tool-call re-probe backs off floor->ceiling like the performance probe
    And real served traffic and demand-probing reset that backoff the same way

  Scenario: The tool-call canary is unbilled and negligible-cost (FOUNDER FLAG T2)
    When the broker sends its tool-call canary to the model
    Then the canary job is marked unbilled and its result is discarded
    And no wallet is touched and no receipt is settled
    And the canary carries a trivial single-parameter tool and a tiny token budget

  Scenario: A voice-only node is never tool-call probed
    Given the node offers only a tts voice model
    Then no tool-call canary is dispatched to it
    And its capabilities never claim "tools"

  # --- emission: "tools" flows through /discover + /market like "vision" -----------------

  Scenario: A verified model surfaces "tools" on /discover
    Given the model has earned the verified "tools" capability
    When a consumer reads /discover
    Then the model's offer lists "tools" in its capabilities
    And the value is canonicalized at read, never trusted raw from the wire

  Scenario: A verified vision+tools model lists both, sorted and deduped
    Given the model accepts images and has earned the verified "tools" capability
    When a consumer reads /market
    Then the model's capabilities are exactly "tools" and "vision" in canonical order

  Scenario: Absence still claims nothing (undetermined, never a false "no tools")
    Given the model has never been tool-call probed
    When a consumer reads /discover
    Then the capabilities key is absent for that offer
    And absence is read as UNDETERMINED, never as a positive "text only / no tools"

  # --- adversarial / edge ----------------------------------------------------------------

  Scenario: A node lying that it supports tools is caught by the canary, not trusted
    Given the node declares "tools" but its upstream ignores tool definitions
    When the broker's tool-call canary runs
    Then the model does NOT earn "tools"
    # The probe IS the check for tools. "vision" remains DECLARED-not-probed (FOUNDER FLAG T3):
    # the vision/tools asymmetry is deliberate and flagged, not silently equated.

  Scenario: A provider that supports tools but RATE-LIMITS the canary is not falsely regressed
    Given the model previously earned the verified "tools" capability
    When the tool-call canary comes back as a transport error or a 429 rate-limit
    Then the earned "tools" bit is NOT cleared on a transient non-verdict
    And the probe is retried on a later round rather than recorded as a regression
    # Mirrors the liveness probe's "a dispatch that never reached the node is not evidence".

  Scenario: A wrong-function but well-formed tool_calls still proves tool-calling (FOUNDER FLAG T4, lenient default)
    When the provider returns a well-formed tool_calls entry for a DIFFERENT function name
    Then the model earns "tools" under the lenient rule
    # Structure (a valid tool_calls array with a function name + parseable arguments) proves the
    # provider HONORS tool-calling; exact-name matching is the strict alternative left to T4.

  Scenario: A "tools" that slips into a STORED offer (mirror / re-hydrate) is still not emitted unprobed
    Given the model's stored offer already lists "tools" from a mixed-version mirror
    And the broker has never run a passing tool-call canary against that model
    Then the model's public capabilities do NOT include "tools"
    # REGRESSION GUARD (pre-push audit, major): register strips a node-declared "tools", but the
    # shared-registry mirror, the lazy tunnel learn, and the DB re-hydrate ingest raw regs that
    # never passed that door (a mixed-version rolling deploy can mirror a pre-strip "tools").
    # Emission (withVerifiedTools) strips a stored "tools" and re-adds it ONLY from the probe
    # verdict, so no ingestion path can leak an unproven "tools" to the public feed.

  # --- multi-instance: two brokers must not double-probe or split the verdict ------------

  Scenario: Two broker instances share the verified "tools" verdict (no per-instance split)
    Given the broker runs two instances behind the shared store
    And instance A ran a passing tool-call canary against the model
    When a consumer reads /discover from instance B
    Then instance B also surfaces "tools" for that model
    # The earned bit mirrors to the shared store and is read as a UNION, like registry/liveness
    # (persistent-state / no per-instance memory). Neither instance re-probes what the other proved.

  Scenario: A non-authoritative peer does not clear "tools" on its own failed cross-instance probe
    Given instance A hosts the node's live poll and proved "tools"
    And instance B is a peer that merely mirrors the node
    When instance B's cross-instance tool-call canary times out
    Then "tools" is NOT cleared for the model
    # Same authoritative-poll-host gate the /discover flicker fix uses (market.go authoritative).

  Scenario: An authoritative host regression clears "tools" for every peer (no stale peer bit)
    Given the broker runs two instances behind the shared store
    And instance A ran a passing tool-call canary against the model
    And a consumer on instance B surfaces "tools" for that model
    When instance A's authoritative canary later regresses the model
    Then instance B no longer surfaces "tools" for that model
    # REGRESSION GUARD (pre-push audit, major): the verdict is FIRST-CLASS shared state, not a
    # per-instance monotonic map, so the host's clear propagates to the peer on its next sync -
    # a peer can never keep surfacing (or re-stamp/resurrect) a verdict the host retracted.
