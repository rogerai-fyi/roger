# GUEST OPERATORS — Phase 1 proxy hardening (money-path, spec-first).
#
# 413 INSTEAD OF SILENT TRUNCATION — today the proxy reads the request body as
# `io.ReadAll(io.LimitReader(r.Body, 4<<20))` and IGNORES the error (client.go:395). A body
# over 4MiB is SILENTLY TRUNCATED: the LimitReader cuts it at 4MiB, the truncated bytes are
# then Unmarshalled (usually failing quietly) and/or relayed as a corrupt body. A guest agent
# with a big context (long files, many messages) gets a baffling failure or a garbage
# completion, and the truncation is invisible. Fix: when the body EXCEEDS the cap, return an
# OpenAI-shaped 413 (request too large) — never truncate-and-relay.
#
# GROUND TRUTH: the 4MiB LimitReader is at client.go:395; the read error is discarded (`_`).
# Detection: read up to cap+1 bytes; if the extra byte is present the body exceeded the cap.
#
# FOUNDER RULINGS NEEDED: keep the 4MiB cap or raise it (agent contexts with big files can be
# large — but the broker/model context window is the real limit; 4MiB of JSON is already huge).
# Proposed: keep 4MiB, return 413 over it. Error type "invalid_request_error", code
# "request_too_large" (see errors.feature).
#
# EXECUTABLE: step defs deferred. RED evidence: TestProxyOversizeBody413 in
# proxy_hardening_test.go — RED today (an oversize body is truncated and relayed, not 413'd).

Feature: Local proxy rejects oversize bodies with 413, never silent truncation

  Background:
    Given a tuned band whose model is "qwen3-32b-fp8"
    And the local proxy is bound to that band
    And the request body cap is 4 MiB

  Scenario: A body over the cap returns an OpenAI-shaped 413
    When a chat request arrives with a body of 5 MiB
    Then the status is 413
    And the Content-Type is "application/json"
    And the body is an OpenAI error with type "invalid_request_error"
    And the broker was never called (no truncated relay, no spend)

  Scenario: A body exactly at the cap is accepted (boundary)
    When a chat request arrives with a valid body of exactly 4 MiB
    Then the request is relayed to the broker
    And the broker receives the FULL body (not truncated)

  Scenario: A body one byte over the cap is rejected 413 (boundary)
    When a chat request arrives with a valid body of 4 MiB plus 1 byte
    Then the status is 413
    And the broker was never called

  Scenario: A normal small body is accepted
    When a chat request arrives with a 2 KiB body
    Then the request is relayed to the broker
    And the broker receives the full body

  # Adversarial: the old bug — truncation produced a corrupt-but-relayed body — must not recur.
  Scenario: An oversize body is never truncated-and-relayed
    When a chat request arrives with a body of 8 MiB
    Then the broker never receives a truncated 4 MiB body
    And the caller gets a clear 413, not a silent garbage completion

  Scenario: An oversize body does not spend against the session budget
    When a chat request arrives with a body of 6 MiB
    Then no cost is accumulated against the session budget
