# The Playbox chat path: the relay becomes browser-callable for allowlisted web
# origins, using the SAME session cookie the dashboard uses and the SAME wallet,
# receipts, moderation, and rate limits the CLI path uses. Nothing about routing,
# settlement, or lineage changes - only who is allowed to knock on the door.
#
# Threat model pinned here: credentialed CORS + a cookie is the classic CSRF
# surface, so the Origin header is load-bearing - a cookie WITHOUT a verified
# allowlisted Origin never authenticates. The signed-request and grant paths are
# untouched and remain the only way to spend from a non-browser client.
Feature: Browser-session relay access for the Playbox
  In order to chat with a station from the Playbox page
  As a visitor with (or without) a web session
  I want /v1/chat/completions to accept credentialed browser calls from our origins only

  Background:
    Given the broker's credentialed web-origin allowlist contains "https://rogerai.fm"
    And a station is on air sharing the free model "wave-nano-chat"
    And a station is on air sharing the paid model "big-model" at a nonzero price

  # ---------- CORS surface ----------------------------------------------------

  Scenario: preflight from an allowlisted origin is granted with credentials
    When a browser sends OPTIONS /v1/chat/completions with Origin "https://rogerai.fm"
    Then the response allows origin "https://rogerai.fm" exactly (never "*")
    And the response allows credentials
    And the response allows methods "POST, OPTIONS"
    And the response allows the "Content-Type" header

  Scenario: preflight from a foreign origin is not granted
    When a browser sends OPTIONS /v1/chat/completions with Origin "https://evil.example"
    Then the response carries no Access-Control-Allow-Origin header

  Scenario: the wildcard and credentialed grants never mix
    When any /v1/chat/completions response carries Access-Control-Allow-Credentials "true"
    Then its Access-Control-Allow-Origin is a single allowlisted origin and never "*"

  # ---------- identity: the session cookie ------------------------------------

  Scenario: a logged-in browser chats on a free model
    Given a browser holds a valid web session for github user "octocat"
    When it POSTs a chat completion for "wave-nano-chat" with Origin "https://rogerai.fm"
    Then the request is relayed to the station and a completion returns
    And the lineage receipt is verified and co-signed exactly as on the CLI path

  Scenario: a logged-in browser spends the same wallet the CLI spends
    Given a browser holds a valid web session for github user "octocat"
    And the wallet "u_gh_octocat" holds sufficient balance
    When it POSTs a chat completion for "big-model" with Origin "https://rogerai.fm"
    Then the spend settles against wallet "u_gh_octocat"
    And no second wallet is created for the browser identity

  Scenario: a session cookie without an allowlisted Origin never authenticates
    Given a browser holds a valid web session for github user "octocat"
    When it POSTs a chat completion for "big-model" with Origin "https://evil.example"
    Then the session cookie is ignored and the request is treated as unsigned
    And the response is 401

  Scenario: a session cookie with no Origin header never authenticates
    Given a request carries a valid web session cookie but no Origin header
    When it POSTs a chat completion for "big-model"
    Then the session cookie is ignored and the request is treated as unsigned
    And the response is 401

  Scenario: an expired or forged session cookie is rejected, not downgraded to anon spend
    Given a browser presents an invalid session cookie
    When it POSTs a chat completion for "big-model" with Origin "https://rogerai.fm"
    Then the response is 401 and no wallet is touched

  # ---------- the anonymous visitor -------------------------------------------

  Scenario: a spoofed Origin cannot rotate legacy ids to dodge the per-IP limiter
    # Found by the 2026-08-01 push audit: outside a browser the Origin header is
    # trivially spoofable, and a legacy X-Roger-User / Bearer id would otherwise
    # mint a fresh per-id rate bucket on every rotation. On the browser path any
    # identity that is not a valid session cookie IS the anonymous identity.
    Given a request with Origin "https://rogerai.fm", no cookie, and a legacy "X-Roger-User: rotating-id" header
    When it POSTs chat completions for "wave-nano-chat" repeatedly with a fresh id each time
    Then every request draws from the ONE per-IP anonymous bucket
    And rotating the legacy id never yields a fresh rate bucket

  Scenario: a signed-out browser may chat on a free model under the per-IP discipline
    Given a browser holds no session
    When it POSTs a chat completion for "wave-nano-chat" with Origin "https://rogerai.fm"
    Then the request is relayed as the anonymous identity
    And the per-IP anonymous rate limit applies before any station is picked

  Scenario: a signed-out browser on a paid model is told to sign in
    Given a browser holds no session
    When it POSTs a chat completion for "big-model" with Origin "https://rogerai.fm"
    Then the response is 401 and the error tells the caller to sign in
    And no wallet is touched and no station receives the request

  # ---------- the audio relay: the Playbox speaks and listens -------------------
  # /v1/audio/speech (TTS) and /v1/audio/transcriptions (STT) share ONE spine
  # (audioRelayCore) with the chat relay, so they inherit the same door: an
  # allowlisted Origin, the session cookie as identity, cookieless = anonymous.
  # This is what lets a Playbox voice card be SPOKEN by a real station on the
  # network instead of a browser's built-in synthesizer.

  Scenario Outline: the audio relay opens to the browser on the same terms as chat
    When a browser sends OPTIONS <path> with Origin "https://rogerai.fm"
    Then the response allows origin "https://rogerai.fm" exactly (never "*")
    And the response allows credentials

    Examples:
      | path                       |
      | /v1/audio/speech           |
      | /v1/audio/transcriptions   |

  Scenario: a logged-in browser synthesizes speech on a free voice station
    Given a station is on air sharing a free TTS voice
    And a browser holds a valid web session
    When it POSTs /v1/audio/speech with Origin "https://rogerai.fm"
    Then the audio is relayed from that station and returned to the page
    And the credentialed CORS headers are present on the audio response

  Scenario: a signed-out browser may use a free voice under the per-IP discipline
    Given a station is on air sharing a free TTS voice
    And a browser holds no session
    When it POSTs /v1/audio/speech with Origin "https://rogerai.fm"
    Then the request is relayed as the anonymous identity
    And the per-IP anonymous rate limit applies

  Scenario: a paid voice signed out is told to sign in, and no wallet is touched
    Given a station is on air sharing a PAID TTS voice
    And a browser holds no session
    When it POSTs /v1/audio/speech with Origin "https://rogerai.fm"
    Then the response refuses with the audio relay's own paid gate (403) and says sign in
    And no wallet is touched and no station is paid

  Scenario: an audio session cookie without an allowlisted Origin never authenticates
    Given a browser holds a valid web session
    When it POSTs /v1/audio/speech with Origin "https://evil.example"
    Then the session cookie is ignored and the request is treated as unsigned
    And the response is 401

  Scenario: a spoofed Origin cannot rotate legacy ids on the audio path either
    Given a request with Origin "https://rogerai.fm", no cookie, and a legacy "X-Roger-User" header
    When it POSTs /v1/audio/speech repeatedly with a fresh id each time
    Then every request draws from the ONE per-IP anonymous bucket

  # ---------- unchanged invariants (regression pins) ---------------------------

  Scenario: moderation runs on browser-session requests exactly as on signed requests
    Given a browser holds a valid web session
    When it POSTs a chat completion whose content the moderator rejects
    Then the request is refused before any station receives it

  Scenario: browser-session requests share the per-identity rate bucket with the CLI
    Given a browser session and a signed CLI keypair resolve to the same github identity
    When both send chat completions concurrently
    Then they draw from one rate bucket, not two

  Scenario: streaming completions work cross-origin
    Given a logged-in browser on an allowlisted origin
    When it POSTs a chat completion for "wave-nano-chat" with stream true
    Then the SSE stream is delivered with the credentialed CORS headers intact

  Scenario: the signed-request path is unchanged
    When a CLI sends a correctly signed chat completion with no Origin header
    Then it authenticates and relays exactly as before this feature

  Scenario: the grant path is unchanged
    When a caller presents a valid "rog-grant_" bearer token with no Origin header
    Then it authenticates and relays exactly as before this feature
