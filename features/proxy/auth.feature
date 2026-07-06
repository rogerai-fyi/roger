# GUEST OPERATORS — Phase 1 proxy hardening (money-path, spec-first).
#
# PER-SESSION BEARER AUTH — today the local endpoint is UNAUTHENTICATED: ProxyHandler
# (client.go:390) accepts any request on 127.0.0.1 and the advertised key is the decorative
# constant "roger-local" (client.go:766, tui.go:3661) that nothing checks. Any other local
# process, a browser tab hitting 127.0.0.1, or a casual prompt-injected fetch can spend the
# user's wallet. Phase 1 generates a PER-SESSION random secret at bind time and enforces
# `Authorization: Bearer <key>` on every proxy route, with a CONSTANT-TIME compare.
#
# GROUND TRUTH: the key is generated when the proxy is bound (Use()/openChannel) and handed to
# the agent via its generated scoped config (Phase 2) and the on-screen plate. Proposed seam:
# ProxyOptions.SessionKey (random 256-bit, hex/base64), or ProxyHandler returns (handler, key).
# The compare MUST be crypto/subtle.ConstantTimeCompare — never ==, never a prefix check — so a
# local attacker can't time-oracle the key byte by byte.
#
# SCOPE NOTE (documented, NOT closed by this gate): a launched guest agent runs as the user and
# CAN read the wallet key off disk ($UserConfigDir/rogerai/user.key, identity.go:29) or the
# generated proxy config. The per-session bearer bounds OTHER local processes and casual
# injection, NOT the agent itself. The spend budget (budget.feature) is the backstop for the
# agent-itself case. This is design doc §8 "Key exfil" — residual, accepted, out of scope here.
#
# FOUNDER RULINGS NEEDED: exact 401 error `type` ("authentication_error" proposed, matching
# OpenAI); key length/encoding; whether the key rotates on re-tune (proposed: STABLE for the
# session so the running guest's config keeps working across a re-tune — see live_options).
#
# EXECUTABLE: step defs deferred. RED evidence: TestProxyRequiresBearer in proxy_hardening_test.go
# (the positive right-key path needs the SessionKey seam and lands with it; the negative
# missing/wrong-key paths are RED against today's no-auth handler now).

Feature: Local proxy per-session bearer auth

  Background:
    Given a tuned band whose model is "qwen3-32b-fp8"
    And the local proxy is bound with a per-session bearer key

  Scenario: A chat request with the correct session key is admitted
    When a chat request arrives with the correct session key
    Then the request is relayed to the broker
    And the status is 200

  Scenario: A chat request with no Authorization header is refused 401
    When a chat request arrives with no Authorization header
    Then the status is 401
    And the body is an OpenAI error with type "authentication_error"
    And the broker was never called (no hold, no spend)

  Scenario: A chat request with the wrong bearer key is refused 401
    When a chat request arrives with the bearer key "wrong-key"
    Then the status is 401
    And the body is an OpenAI error with type "authentication_error"
    And the broker was never called (no hold, no spend)

  Scenario: The decorative legacy "roger-local" is NOT accepted as a key
    When a chat request arrives with the bearer key "roger-local"
    Then the status is 401
    And the body is an OpenAI error with type "authentication_error"

  Scenario Outline: Malformed / spoofing Authorization headers are refused
    When a chat request arrives with the raw Authorization header "<header>"
    Then the status is 401
    And the body is an OpenAI error with type "authentication_error"

    Examples:
      | header                    |
      |                           |
      | Bearer                    |
      | Bearer                    |
      | Basic <sessionkey>        |
      | <sessionkey>              |
      | bearer <sessionkey>xtra   |
      | Bearer <sessionkey>       |

  # Note: the "<sessionkey>" placeholder above is substituted with the REAL key by the step
  # def; rows that still fail carry a deliberately-broken scheme/prefix/suffix around it.

  Scenario: /v1/models is auth-gated with the same key
    When an agent sends GET "/v1/models" with no Authorization header
    Then the status is 401
    And the body is an OpenAI error with type "authentication_error"

  Scenario: The compare is constant-time (no early-exit prefix oracle)
    Given a session key of 32 bytes
    When a request arrives with a key that shares the first 16 bytes then differs
    Then it is refused 401 exactly like a fully-wrong key
    And the comparison used crypto/subtle.ConstantTimeCompare, not "==" or a prefix match

  Scenario: A wrong key never triggers a broker hold (auth precedes spend)
    When a chat request arrives with the bearer key "wrong-key"
    Then the broker was never called (no hold, no spend)
    And no cost is accumulated against the session budget

  # Adversarial: a same-origin browser page can send a Bearer header but not read the
  # per-session key, so it is bounded exactly like any wrong-key caller.
  Scenario: A local cross-process caller without the key cannot spend
    Given a second local process that does not know the session key
    When it POSTs a chat request to the proxy guessing "roger-local"
    Then the status is 401
    And the wallet is untouched
