# Layer 3 (broker) — STATION-NAMESPACED public voices: attribution, register-time uniqueness,
# and NAMESPACED VOICE-ID ROUTING (the money/routing path). This REPLACES the merged @login
# namespacing (commit 2cd4a33): a public voice is now keyed on the owner's STATION CALLSIGN, not
# their GitHub login.
#
# WHY STATION, NOT @login (founder-approved 2026-07-01):
#   - the STATION (internal/tui/tui.go:47, internal/agent/agent.go:GenerateStation) is the owner's
#     friendly, NON-SENSITIVE broadcast callsign (e.g. `brave-otter-37`), auto-generated from the
#     adjective-animal wordlist, renameable in SHARE, ALREADY carried into /discover as the node id
#     prefix (`<station>-<model>` via agent.ShareNodeID). It is anonymous, AUTH-AGNOSTIC, and
#     non-sensitive by design — safe in /voices.
#   - the @login scheme LEAKED the GitHub identity AND structurally BROKE for Apple-only accounts:
#     apple.go binds store.Owner{AppleSub,Pubkey} with an EMPTY Login, so operatorLogin (which
#     requires o.Login!="") excluded every Apple operator's voice from /voices and made it
#     unroutable. The station comes from the node id, present for EVERY owner regardless of auth.
#
# RECON FINDING (surfaced for the approval gate — station SOURCE OF TRUTH is a design decision):
#   The node id is `slugify(station) + "-" + slugify(model)` [+ "-<instance>" when instance>=2]
#   (agent.go:ShareNodeID). NEITHER NodeRegistration NOR ModelOffer carries an explicit station
#   field today, so recovering the station from (node id, o.Model) alone is AMBIGUOUS/FRAGILE:
#     (a) the suffix in the id is slugify(model) (e.g. "af-heart"), NOT the stored raw o.Model
#         ("af_heart"), so a naive strip of "-<o.Model>" fails;
#     (b) the station itself is multi-segment with a trailing number ("brave-otter-37"), so the
#         station/model boundary can't be found unambiguously from the string;
#     (c) an instance>=2 node id has a trailing "-2"/"-3" that a prefix-strip would fold into the
#         station.
#   => This spec is written against an AUTHORITATIVE station the broker can read reliably. The
#      recommended implementation is a SIGNED Station field on NodeRegistration (regSigningBytes
#      marshals the whole struct, so it can't be forged/stripped) threaded from the SAME persisted
#      station the node id is derived from. A prefix-strip fallback is NOT sound and this spec's
#      "station of an on-air node" step must resolve to the authoritative station, not a guess.
#      The scenarios below are agnostic to that choice; the step wiring pins it.
#
# WHAT THIS COVERS (three layers, all keyed on STATION):
#   1. ATTRIBUTION — computeVoices dual-emits raw id + `@<station>/<slug(Name)>`; operator=station.
#   2. REGISTER-TIME UNIQUENESS — a PUBLIC voice's station must not collide with ANOTHER on-air
#      owner's public station (the station is RENAMEABLE, so two owners could pick the same one).
#      The existing chat-model impersonation prefix guard on the slug + moderation (fail-closed)
#      STILL apply.
#   3. ROUTABLE — `@<station>/<slug>` resolves to the SPECIFIC on-air node (station + voice-slug
#      match) and routes to THAT node, so THE COLLISION (two owners both offering raw "af_heart")
#      is safe: each station's namespaced id routes to its OWN node and bills the RIGHT owner. The
#      credit side keys off node.NodeID (settleRequest -> Finalize -> earnings[node]), so resolving
#      to the specific node is what makes the bill land on the right operator's price — NOT a bare
#      pickFor(rawModel), which would re-collide (selectP2C over the RNG picks either node).
#
# KEEP (unchanged): dual-emit, the no-address-leak invariant (station is non-sensitive; the node
# id / bridge URL / pubkey / hostname / IP still NEVER appear in /voices), and modality isolation.
# Step definitions come AFTER approval (RED-first), against a REAL in-process broker + REAL store
# (the in-memory reference store, as every voice/money BDD suite uses; the attribution path is
# identical to Postgres). Nodes answer real signed receipts through a real tunnel — NO mocks.

@money @voice @routing @namespacing @station
Feature: Public voices are namespaced by STATION, attributed, unique per station, and routable

  Background:
    Given a broker with content screening disabled
    And an operator broadcasting as station "brave-otter" who has logged in
    And an operator broadcasting as station "swift-lynx" who has logged in
    And a signed-in consumer with a funded wallet

  # ===========================================================================
  Rule: attribution — a public voice dual-emits raw id + @<station>/<slug(Name)>, operator=station

    Scenario: A public voice is dual-emitted (raw id + station-namespaced id), attributed to the station
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "1950s Operator" at 15 credits per 1M chars
      When an anonymous GET /voices arrives
      Then a voice with raw id "af_heart" is listed
      And that voice's namespaced_id is "@brave-otter/1950s-operator"
      And that voice's operator is "brave-otter"
      And no pubkey, node id, bridge URL, or IP appears anywhere in the response

    Scenario: The operator field carries the bare station, no "@" prefix, and no GitHub login leaks
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And the operator's GitHub login is "octocat"
      When an anonymous GET /voices arrives
      Then a voice with operator "brave-otter" is listed
      And it carries no "@" prefix in the operator field
      And "octocat" never appears anywhere in the response

    Scenario: An APPLE-only operator (no GitHub login) still gets a station-namespaced public voice
      # the @login scheme excluded these entirely; the station scheme MUST list them.
      Given station "brave-otter" is an Apple-only operator with no GitHub login
      And station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@brave-otter/operator" is listed
      And that voice's operator is "brave-otter"

    Scenario: The voice-name slug is normalized (lowercase, spaces->dash) under the station
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "front-desk" named "  Front  Desk  VOICE  " at 15 credits per 1M chars
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@brave-otter/front-desk-voice" is listed

    Scenario: The raw id is preserved verbatim for routing/back-compat
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      When an anonymous GET /voices arrives
      Then a voice with raw id "af_heart" is listed
      And that voice's namespaced_id is "@brave-otter/operator"

    Scenario: An anonymous (unbound, station-less earning) node is still excluded from public /voices
      Given an anonymous free tts node "nfree" offering raw model "kokoro" named "Kiosk"
      When an anonymous GET /voices arrives
      Then no voice with raw id "kokoro" is listed
      And the voices list is empty

    Scenario: The full /voices body leaks no node address with station attribution added
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And that node's bridge URL is "https://abc123.trycloudflare.com"
      And its local address is "192.168.1.50:8790"
      When an anonymous GET /voices arrives
      Then the response body contains neither the bridge URL nor the local address
      And no field named for a host, ip, bridge, url-to-node, or pubkey exists

  # ===========================================================================
  Rule: register-time station uniqueness for PUBLIC voices (the station is renameable)

    Scenario: Two owners with DISTINCT stations both list a same-named voice without collision
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And station "swift-lynx" has an on-air tts node "nl" offering raw model "af_heart" named "Operator" at 30 credits per 1M chars
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@brave-otter/operator" is listed
      And a voice with namespaced_id "@swift-lynx/operator" is listed
      And the two voices are distinct entries

    Scenario: A SECOND owner registering a public voice under a station another owner already broadcasts is rejected
      # the auto-generated station is ~unique; a RENAME could collide. A colliding public station
      # from a DIFFERENT owner is refused so @station is unambiguous for routing + attribution.
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      When a DIFFERENT owner registers an on-air tts node under station "brave-otter" offering raw model "af_heart" named "Reception"
      Then the registration is rejected as a station already in use by another operator
      And only one "brave-otter" operator is listed in /voices

    Scenario: The SAME owner re-registering their OWN station is not a collision (idempotent re-register)
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      When station "brave-otter" re-registers node "nb" offering raw model "af_heart" named "Operator"
      Then the registration is accepted
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@brave-otter/operator" is listed

    Scenario: The SAME owner may serve a SECOND model under their station (one station, many voices)
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And station "brave-otter" has an on-air tts node "nb2" offering raw model "af_aoede" named "Receptionist" at 15 credits per 1M chars
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@brave-otter/operator" is listed
      And a voice with namespaced_id "@brave-otter/receptionist" is listed

    Scenario: The SAME owner still cannot serve two voices whose NAMES slug identically (deterministic ids)
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      When station "brave-otter" registers another on-air tts node offering raw model "af_aoede" named "  operator "
      Then the registration is rejected as a duplicate voice name for this station
      And only one "@brave-otter/operator" is listed in /voices

    Scenario: A colliding station on a DIFFERENT modality is not a public-voice collision (only TTS voices are public)
      # a chat-only node under a station never forms a public voice, so it does not reserve the
      # station against a tts voice; the guard scopes uniqueness to PUBLIC (tts) voices.
      Given station "brave-otter" runs a chat-only node offering raw model "llama3.2"
      When station "swift-lynx" registers an on-air tts node offering raw model "af_heart" named "Operator"
      Then the registration is accepted

    Scenario: The chat-model impersonation guard on the slug still applies under a station
      When station "brave-otter" registers an on-air tts node offering raw model "af_heart" named "gpt-oss-120b"
      Then the registration is rejected as chat-model impersonation

    Scenario: A voice NAME that fails moderation is still rejected under a station (fail-closed)
      Given content screening is required but unreachable
      When station "brave-otter" registers an on-air tts node offering raw model "af_heart" named "Operator"
      Then the registration is rejected 503 (fail closed), not served unscreened

  # ===========================================================================
  Rule: a station-namespaced id routes to the resolved node (routable)

    Scenario: A station-namespaced id routes to the operator whose voice it names
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "1950s Operator" at 15 credits per 1M chars
      When the consumer says "hello" with voice "@brave-otter/1950s-operator"
      Then the request is served by node "nb"
      And the response is 200
      And node "nb" is credited for the request

    Scenario: The advertised namespaced id from /voices is the id that resolves
      # /voices renders namespaced_id from the station + slugVoiceName; the relay resolves via the
      # SAME station + slug, so the exact string a caller reads from /voices routes here.
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "roger-operator-voice" named "1950s Operator" at 15 credits per 1M chars
      When the consumer reads the namespaced_id for raw model "roger-operator-voice" from /voices
      And the consumer says "hello" with that exact namespaced id
      Then the request is served by node "nb"
      And the response is 200

  # ===========================================================================
  Rule: THE COLLISION — two stations, same raw model, each id bills its OWN node

    Scenario: Two stations both offer raw "af_heart"; the namespaced id routes + bills its own node
      # pickFor("af_heart") would pick EITHER node (RNG) and could bill the wrong owner. The
      # station-namespaced id must pin to the SPECIFIC resolved node.
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And station "swift-lynx" has an on-air tts node "nl" offering raw model "af_heart" named "Reception" at 30 credits per 1M chars
      When the consumer says "hello" with voice "@brave-otter/operator"
      Then the request is served by node "nb"
      And node "nb" is credited for the request
      And node "nl" is NOT credited for the request

    Scenario: The peer namespaced id routes to the OTHER node (collision, mirror direction)
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And station "swift-lynx" has an on-air tts node "nl" offering raw model "af_heart" named "Reception" at 30 credits per 1M chars
      When the consumer says "hello" with voice "@swift-lynx/reception"
      Then the request is served by node "nl"
      And node "nl" is credited for the request
      And node "nb" is NOT credited for the request

    Scenario: The bill uses the RESOLVED node's price, not the other station's
      # nb charges 15/1M, nl charges 30/1M for the SAME raw "af_heart". A 100-char say at
      # @swift-lynx/reception must bill swift-lynx's 30/1M price (0.003), proving the RESOLVED
      # offer's price is used, not nb's.
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And station "swift-lynx" has an on-air tts node "nl" offering raw model "af_heart" named "Reception" at 30 credits per 1M chars
      When the consumer says a 100-character line with voice "@swift-lynx/reception"
      Then the consumer's wallet is debited 0.003000 credits
      And node "nl" is credited for the request

    Scenario: The cheaper collided station bills its own lower price
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And station "swift-lynx" has an on-air tts node "nl" offering raw model "af_heart" named "Reception" at 30 credits per 1M chars
      When the consumer says a 100-character line with voice "@brave-otter/operator"
      Then the consumer's wallet is debited 0.001500 credits
      And node "nb" is credited for the request

  # ===========================================================================
  Rule: back-compat — a raw id routes exactly as today

    Scenario: A raw id with no "@" routes on the raw model unchanged
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      When the consumer says "hello" with voice "af_heart"
      Then the request is served by node "nb"
      And the response is 200

    Scenario: A raw id still works when the ONLY on-air voice is anonymous/unowned (no namespace exists)
      Given an anonymous free tts node "nfree" offering raw model "kokoro" named "Kiosk"
      When the consumer says "hello" with voice "kokoro"
      Then the request is served by node "nfree"
      And the response is 200

    Scenario: A raw id that no on-air node offers gives the uniform 503
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      When the consumer says "hello" with voice "nonexistent-model"
      Then the response is 503
      And the error names "no station"
      And no node is credited

  # ===========================================================================
  Rule: a namespaced id with no matching on-air voice -> the uniform 503

    Scenario: An unknown station gives 503, not a mis-route to a same-slug voice on another station
      # swift-lynx has a voice slugging to "operator"; a request for @ghost/operator must NOT fall
      # through to it — the STATION must match too. No @ghost station is on air -> 503.
      Given station "swift-lynx" has an on-air tts node "nl" offering raw model "af_heart" named "Operator" at 30 credits per 1M chars
      When the consumer says "hello" with voice "@ghost/operator"
      Then the response is 503
      And the error names "no station"
      And node "nl" is NOT credited for the request

    Scenario: A known station but an unknown slug gives 503, not a mis-route to their other voice
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      When the consumer says "hello" with voice "@brave-otter/reception"
      Then the response is 503
      And the error names "no station"
      And node "nb" is NOT credited for the request

    Scenario: A namespaced id whose station went OFF AIR gives 503 (no stale resolution)
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And node "nb" has gone off air
      When the consumer says "hello" with voice "@brave-otter/operator"
      Then the response is 503
      And the error names "no station"

    Scenario: A namespaced id for a BANNED operator's station gives 503 (owner-ban holds through resolution)
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And station "brave-otter" is owner-banned
      When the consumer says "hello" with voice "@brave-otter/operator"
      Then the response is 503
      And the error names "no station"
      And no node is credited

    Scenario: A namespaced id cannot cross-route to a CHAT model of the same name under the station
      # modality isolation still holds: only TTS offers form public voices, so a chat-only station
      # has no voice and its /v1/audio/speech resolution 503s.
      Given station "brave-otter" runs a chat-only node offering raw model "operator"
      When the consumer says "hello" with voice "@brave-otter/operator"
      Then the response is 503
      And the error names "no station"

  # ===========================================================================
  Rule: namespaced resolution cannot be spoofed by a crafted name (forgery guards hold)

    Scenario: A slash-folded slug resolves to the voice that actually registered it
      # a voice named "acme/official" slugs to "acme-official" (the "/" folds to "-").
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "acme/official" at 15 credits per 1M chars
      When the consumer says "hello" with voice "@brave-otter/acme-official"
      Then the request is served by node "nb"
      And the response is 200

    Scenario: A forged deeper "/"-segment does not resolve
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "acme/official" at 15 credits per 1M chars
      When the consumer says "hello" with voice "@brave-otter/acme/official"
      Then the response is 503
      And the error names "no station"

  # ===========================================================================
  Rule: the money gates still fire on the resolved node (adversarial, unchanged spine)

    Scenario: Insufficient balance on a namespaced route refuses before the node is called
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And the consumer's wallet holds 0.000100 credits
      When the consumer says a 1000-character line with voice "@brave-otter/operator"
      Then the response is 402
      And node "nb" is never called
      And node "nb" is NOT credited for the request

    Scenario: An anonymous caller on a PAID namespaced voice hits the sign-in gate
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      And the caller is anonymous and not logged in
      When the caller says "hello" with voice "@brave-otter/operator"
      Then the response is 403
      And node "nb" is never called

    Scenario: A namespaced route places a hold that equals the finalized char cost
      Given station "brave-otter" has an on-air tts node "nb" offering raw model "af_heart" named "Operator" at 15 credits per 1M chars
      When the consumer says a 1000-character line with voice "@brave-otter/operator"
      Then the response is 200
      And the consumer's wallet is debited 0.015000 credits
      And node "nb" is credited for the request

  # ===========================================================================
  Rule: transcription (STT) station-namespaced routing follows the same resolution (parity)

    Scenario: A station-namespaced STT id routes to the resolved node
      # STT offers are not public /voices entries, but the resolver is modality-scoped to the
      # request path; a namespaced id on /v1/audio/transcriptions resolves within STT offers.
      Given station "brave-otter" has an on-air stt node "ns" offering raw model "whisper-large-v3" named "Court Reporter" at 20 credits per 1M bytes
      When the consumer transcribes 100 bytes with voice "@brave-otter/court-reporter"
      Then the request is served by node "ns"
      And the response is 200
      And node "ns" is credited for the request
