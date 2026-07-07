# GUEST OPERATORS — Phase 1 proxy hardening (money-path, spec-first).
#
# STREAM TIMEOUT — today the proxy's relay client is `&http.Client{Timeout: 120*time.Second}`
# (client.go:391). http.Client.Timeout is a BLANKET deadline covering the ENTIRE exchange
# INCLUDING reading the streamed body. A legitimate long generation (a big agent turn, a slow
# CPU-MoE node) that streams SSE past 120s is CUT MID-STREAM — the client sees a truncated
# response and a transport error. The broker itself keeps SSE open to ~300s (chatTimeout is
# already 300s for the in-channel path, client.go:946), so the 120s proxy bound is the weak
# link. Fix: REMOVE the blanket Timeout; bound only the parts that should be bounded —
# TCP DIAL and RESPONSE-HEADER wait — via a Transport (DialContext / ResponseHeaderTimeout /
# ExpectContinueTimeout) plus a request Context, so a healthy stream can trickle indefinitely
# (up to the broker's own 300s) but a dead connection still fails fast.
#
# GROUND TRUTH: relayWithFailover uses httpClient.Do(req) then copyRelayResponse streams the
# body chunk-by-chunk with a Flusher (client.go:547-560). The 120s blanket kills exactly the
# streaming read loop. The in-channel ChatDetailed already uses a 300s client (client.go:1006)
# — the proxy must at least match, and ideally use header/dial bounds instead of a blanket.
#
# TEST SEAM (design requirement): the timeout bound MUST be injectable so the test runs fast.
# Proposed: a package seam like the existing useStdin/useServe vars (client.go:784-787) — e.g.
# a `proxyDialTimeout` / `proxyResponseHeaderTimeout` var or a ProxyOptions field — that the
# test sets to a small value, plus a stub broker that emits an early response header then
# trickles body bytes past the OLD 120s bound on a compressed clock. Without the blanket
# Timeout, the trickle completes; with it, the trickle is cut.
#
# FOUNDER RULINGS NEEDED: the response-header timeout value (proposed 30s — a node that hasn't
# sent a single header byte in 30s is dead); the dial timeout (proposed 10s); whether to keep a
# hard overall ceiling matching the broker's 300s or rely purely on the request Context.
#
# EXECUTABLE: RED evidence is DEFERRED to the injectable-seam landing (the blanket 120s can't be
# exercised fast without the seam, and the seam is a production change gated behind approval).
# This feature is the founder-approvable spec; the table test TestProxyStreamNotCutByBlanket
# lands with the seam. See report.

Feature: Local proxy does not cut legitimate long streams

  Background:
    Given a tuned band whose model is "qwen3-32b-fp8"
    And the proxy stream bound is injected as a small test value

  Scenario: A slow stream that trickles past the old 120s blanket completes
    Given a stub broker that sends response headers immediately
    And then trickles SSE chunks for longer than the OLD 120s blanket (compressed clock)
    When a streaming chat request is made
    Then the full stream is delivered without being cut
    And the client sees a clean stream end, not a transport timeout

  Scenario: The blanket http.Client.Timeout is gone from the relay client
    Then the relay client has no blanket Timeout that covers the body read
    And bounds are applied via dial + response-header timeouts and a request context

  Scenario: A dead connection that never sends a header still fails fast
    Given a stub broker that accepts the connection but never sends a response header
    When a chat request is made
    Then it fails within the response-header timeout, not after 120s
    And the failure is an OpenAI-shaped 502 (see errors.feature)

  Scenario: A node that dials but stalls before the first byte is bounded by the header timeout
    Given a stub broker that stalls before sending any header
    When a chat request is made
    Then the request is aborted at the response-header timeout
    And failover is attempted against another station

  Scenario: A healthy fast response is unaffected
    Given a stub broker that responds promptly
    When a chat request is made
    Then it returns 200 well within all bounds

  # Regression corner: the exact bug — a >120s stream — must never be cut again.
  Scenario: A generation longer than 120s of wall-clock streaming is NOT truncated
    Given a stub broker that streams a valid completion whose wall-clock exceeds 120s (compressed clock)
    When a streaming chat request is made
    Then the completion arrives whole
    And no partial/truncated body is delivered to the agent
