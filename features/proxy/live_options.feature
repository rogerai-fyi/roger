# GUEST OPERATORS — Phase 1 proxy hardening (money-path, spec-first).
#
# LIVE PROXYOPTIONS — the TUI binds the proxy ONCE, on the first tune-in, and NEVER rebinds:
# openChannel does `if !m.proxyUp { ... go http.Serve(ln, client.ProxyHandler(opts)) }`
# (tui.go:3636-3658), and disconnect leaves the listener up (tui.go:3752-3754, "left bound").
# The `opts` captured at first bind FREEZE band A's caps + routing. So this sequence:
#   tune into band A (open market) -> disconnect -> tune into band B (a PRIVATE freq)
# leaves the proxy serving with band A's ProxyOptions: band A's MaxPriceOut/MinTPS/Confidential
# and — critically — band A's X-Roger-Freq (empty), so a guest agent pointed at the "band B"
# endpoint is actually routed to band A's OPEN-MARKET stations at band A's price caps. Wrong
# money, wrong routing, wrong model (once model_rewrite lands). Fix: the handler reads its
# options LIVE per request from a shared, updatable snapshot, so a re-tune re-points the SAME
# endpoint atomically.
#
# GROUND TRUTH SEAM: this behavior lives at the client.ProxyOptions boundary. Proposed:
# ProxyHandler takes a live source instead of a frozen value — e.g. ProxyHandler(func() ProxyOptions)
# or an exported *ProxyOptionsHolder{ Set(ProxyOptions); Get() ProxyOptions } read per request.
# The TUI hook: openChannel updates the holder on EVERY tune-in (not just the first bind), and
# disconnect either clears routing or leaves the last snapshot — founder's call (below). The
# per-session bearer key (auth.feature) stays STABLE across the re-point so the running guest's
# config keeps working.
#
# FOUNDER RULINGS NEEDED (see report):
#   - On disconnect with the endpoint left bound and NO band tuned: should the proxy (a) refuse
#     all relays with a clean 409/503 "no band tuned" OpenAI error, or (b) keep serving the last
#     band until a new tune-in? Proposed (a): a disconnected proxy refuses to spend — safest.
#   - Whether the bearer key rotates on re-tune (proposed: stays stable for the session).
#
# EXECUTABLE: RED evidence DEFERRED to the live-source seam (today's ProxyHandler takes a frozen
# value; the live behavior cannot be exercised without the new API, which is gated behind
# approval). This feature is the founder-approvable spec; the table test TestProxyLiveOptions
# lands with the seam. See report.

Feature: Local proxy serves the live tuned band, not stale bind-time options

  Scenario: After A -> disconnect -> B, relays use band B's routing
    Given the proxy is bound while tuned to band A (open market)
    When the user disconnects and re-tunes to band B on private freq "SEVENTX"
    And a chat request is made
    Then the relay carries X-Roger-Freq "SEVENTX"
    And it does NOT carry band A's empty/open-market routing

  Scenario: After A -> disconnect -> B, relays use band B's price caps
    Given the proxy is bound while tuned to band A with max-out $10
    When the user re-tunes to band B with max-out $2
    And a chat request is made
    Then the relay carries X-Roger-Max-Price-Out "2"
    And it does NOT carry band A's "10"

  Scenario: After A -> disconnect -> B, /v1/models reports band B's model
    Given the proxy is bound while tuned to band A model "qwen3-32b-fp8"
    When the user re-tunes to band B model "llama-3.3-70b"
    And an agent probes GET "/v1/models"
    Then data[0].id is "llama-3.3-70b"

  Scenario: After A -> disconnect -> B, the confidential flag follows band B
    Given the proxy is bound while tuned to band A (confidential off)
    When the user re-tunes to band B (confidential on)
    And a chat request is made
    Then the relay carries X-Roger-Confidential "1"

  Scenario: The endpoint URL and bearer key are stable across the re-point
    Given the proxy is bound while tuned to band A
    When the user re-tunes to band B
    Then the local endpoint address is unchanged
    And the per-session bearer key is unchanged
    # so a running guest agent's generated config keeps working across a re-tune

  Scenario: A disconnected proxy with no band tuned refuses to spend
    Given the proxy is bound while tuned to band A
    When the user disconnects and no band is tuned
    And a chat request is made
    Then the request is refused with an OpenAI-shaped error (no band tuned)
    And the broker was never called (no spend)

  # Concurrency corner: a re-tune while a request is in flight must not tear the options.
  Scenario: A re-tune concurrent with an in-flight request reads a consistent snapshot
    Given a chat request is in flight against band A
    When the user re-tunes to band B mid-flight
    Then the in-flight request completes against a consistent (A) snapshot
    And the NEXT request uses band B
    And no request ever sees a half-updated mix of A and B options
