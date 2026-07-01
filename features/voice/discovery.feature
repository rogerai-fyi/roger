# Layer 3/4 — voice discovery. GET /voices is the anonymous endpoint the BUILT iOS picker calls
# (roger-ios docs/BROKER-VOICE-API.md). It aggregates the live TTS stations into the app's shape.
#
# SECURITY (founder): /voices NEVER exposes a node's hostname, bridge URL, or IP — the broker
# proxies all node traffic (like the chat bridge; NodeRegistration.BridgeToken secures the tunnel),
# so a voice is reachable ONLY through the broker. /voices carries voice metadata, not addresses.
#
# DECISIONS PINNED (founder 2026-06-30):
#   - our first voice is named "roger-operator-voice".
#   - /voices exposes metadata only (id, name, provider LABEL, price_per_1k_chars, free, latency_ms,
#     language, sample_url) — no node hostname/bridge/IP is ever in the response.
#   - price_per_1k_chars is USD, derived from the offer's per-1M-char credit price (price_in / 1000
#     at credit=$1); free = a price-0 offer.
#   - voice metadata (name/language/sample_url/latency_ms) rides as OPTIONAL fields on ModelOffer.
#
# Step definitions come AFTER approval (RED-first).

@voice @discovery
Feature: GET /voices lists live voice stations for the app, leaking no node addresses

  Rule: /voices is anonymous and returns the app's voice shape

    Scenario: The picker fetches the on-air voices without auth
      Given a live TTS node offering "roger-operator-voice" at price_in 20 per 1M chars
      When an anonymous GET /voices arrives
      Then the response is 200
      And it lists a voice with id "roger-operator-voice"
      And that voice's price_per_1k_chars is 0.02
      And that voice's free is false

    Scenario: A price-0 voice is marked free (usable anonymously)
      Given a live TTS node offering "front-desk-voice" at price_in 0 per 1M chars
      When an anonymous GET /voices arrives
      Then the listed voice "front-desk-voice" has free true
      And its price_per_1k_chars is 0

    Scenario: Optional metadata is passed through; absent fields are null
      Given a TTS node offering "roger-operator-voice" advertising name "1950s Operator", language "en-US", latency_ms 400
      When an anonymous GET /voices arrives
      Then the voice carries name "1950s Operator", language "en-US", latency_ms 400
      And a voice that advertised no sample_url has sample_url null

  Rule: no node hostname, bridge URL, or IP is EVER exposed (security)

    Scenario: The /voices response leaks no node address
      Given a live TTS node "roger-operator-voice" whose bridge URL is "https://abc123.trycloudflare.com"
      And whose local address is "192.168.1.50:8790"
      When an anonymous GET /voices arrives
      Then the response body contains neither the bridge URL nor the local address
      And no field exposes a node hostname or IP
      And a sample_url, if present, is a broker- or CDN-hosted URL, never the node's

    Scenario: Only TTS stations appear (a chat or stt node is not a "voice")
      Given a chat node offering "llama3.2"
      And an STT node offering "whisper-large-v3"
      And a TTS node offering "roger-operator-voice"
      When an anonymous GET /voices arrives
      Then only "roger-operator-voice" is listed
      And the chat and stt models are not in /voices

  Rule: the app degrades gracefully

    Scenario: No voice on air returns an empty list, not an error
      Given no TTS node is registered
      When an anonymous GET /voices arrives
      Then the response is 200 with an empty voices list
