# AWAITING FOUNDER APPROVAL (spec-first workflow step 3) — do NOT write step definitions or
# implementation until approved. RELEASE BLOCKER, audit finding #2 (money/auth).
#
# THE HOLE: a grant is authenticated on the voice money paths (/v1/audio/speech and
# /v1/audio/transcriptions, resolveGrant at audio.go), but audioRelayCore routes with
#   b.pickFor(routeModel, false, 0,0,0, pinNode, nil, nil, nil, ...)   // audio.go:269
# — the `allow` arg is nil and gc.modelDenied is NEVER called. pickFor treats allow==nil as
# "no node filter" (tunnel.go: `if allow != nil && !allow[n.NodeID] { continue }`), so a
# grant can be served by ANY on-air node of ANY operator, and by ANY model, on the voice path.
#
# The chat relay (tunnel.go:1425-1435) does the exact opposite and states the invariant:
#   var allow = gc.nodeAllow            // owner's nodes ∩ grant.Nodes
#   if len(allow)==0 { 503 }            // "no node of this grant's owner is serving right now"
#   if gc.modelDenied(req.Model) { 503 } // uniform, non-oracle
#   "A grant confines routing to the issuing owner's nodes — it can never reach another
#    owner's hardware."
#
# MONEY IMPACT (the reason this is a blocker, not a cleanup):
#   - Sponsored (custom-priced) grant: resolvePricing bills ownerSponsorWallet(A) at A's
#     grant price while settleRequest mints the earning lot for the SERVING node's owner B —
#     grant owner A pays a third-party operator B at A's own (wrong) price.
#   - Free grant: owner B's node is consumed for $0 with no earning — theft of B's voice
#     service. Either way both the node scope AND the model allow-list are bypassed.
#
# GROUND TRUTH: cmd/rogerai-broker/audio.go audioRelayCore (resolveGrant → gok/gc; pickFor
# with allow=nil at :269), grant.go grantContext{nodeAllow, modelDenied}, tunnel.go relay()
# chat path :1425-1435. The fix mirrors the chat path onto the voice path.
#
# DECISIONS PINNED BY THIS SPEC:
#   V1  NODE SCOPE: when the request carries a grant, audioRelayCore passes allow=gc.nodeAllow
#       to pickFor (owner's nodes ∩ grant.Nodes). A voice node NOT in that set is never
#       selected — a grant can never reach another owner's hardware on the voice path.
#   V2  EMPTY SCOPE: if gc.nodeAllow is empty (owner has no voice node of this modality on
#       air), refuse with the SAME 503 the chat path uses — never fall through to a foreign
#       node. No charge, no dispatch.
#   V3  MODEL ALLOW-LIST: if gc.modelDenied(routeModel) (the resolved raw voice model — the
#       namespaced "@station/voice" resolves to its raw id first), refuse with the uniform
#       503 (no oracle on the grant's model list). Applied to BOTH tts and stt.
#   V4  NAMESPACED + GRANT: a namespaced voice id resolves to a specific node AND that node
#       must still be in gc.nodeAllow — resolution never widens grant scope.
#   V5  NO REGRESSION for a non-grant caller (signed request / anon): routing is unchanged
#       (allow stays nil = the whole eligible pool), and for a grant whose scope legitimately
#       includes the serving node + model, the request succeeds and bills exactly as today.

# ENFORCED BY: cmd/rogerai-broker/grant_node_isolation_test.go (real /v1/audio/speech money
# path over store.NewMem(), no mocks: V1 cross-owner escape, V3 model-denied-before-dispatch,
# V5 in-scope no-regression). The fix lives in audio.go audioRelayCore (allow=gc.nodeAllow +
# gc.modelDenied, mirroring the chat relay).

Feature: A voice grant is confined to its owner's nodes and model allow-list
  As an operator who mints a voice grant scoped to my own nodes and voices
  I want that grant to reach only my hardware and only the models I allowed
  So that my sponsored wallet never pays a stranger and no one free-rides another operator

  Background:
    Given operator A has an on-air TTS node "a-tts" offering voice "af_heart"
    And operator B has an on-air TTS node "b-tts" offering voice "af_heart"
    And a grant issued by owner A scoped to nodes ["a-tts"] and models ["af_heart"]

  # ── V1/V2: node scope ──────────────────────────────────────────────────────────────────

  Scenario: a grant's TTS request is served only by the owner's node
    When the grant calls POST /v1/audio/speech for "af_heart"
    Then the request is routed to node "a-tts"
    And node "b-tts" is never selected

  Scenario: a grant whose owner has no matching node on air is refused, not rerouted
    Given owner A's node "a-tts" is off air
    When the grant calls POST /v1/audio/speech for "af_heart"
    Then the response is 503 "no node of this grant's owner is serving right now"
    And no node is dispatched to and no hold is placed

  Scenario: the same isolation holds for STT
    Given operator A has an on-air STT node "a-stt" and operator B an on-air STT node "b-stt"
    And a grant issued by owner A scoped to nodes ["a-stt"]
    When the grant calls POST /v1/audio/transcriptions
    Then the request is routed to node "a-stt"
    And node "b-stt" is never selected

  # ── V3: model allow-list ───────────────────────────────────────────────────────────────

  Scenario: a grant is refused a voice model outside its allow-list
    Given owner A's node "a-tts" also offers voice "am_onyx"
    When the grant calls POST /v1/audio/speech for "am_onyx"
    Then the response is 503 "no station on air for am_onyx"
    And no node is dispatched to and no hold is placed

  # ── V4: namespaced resolution never widens scope ───────────────────────────────────────

  Scenario: a namespaced voice on a foreign node is not reachable through the grant
    When the grant calls POST /v1/audio/speech for "@operator-b/af_heart"
    Then the response is 503
    And node "b-tts" is never selected

  # ── V5: no regression ──────────────────────────────────────────────────────────────────

  Scenario: a signed non-grant caller still routes across the whole eligible pool
    Given no grant is presented, only a signed request
    When the caller calls POST /v1/audio/speech for "af_heart"
    Then either node "a-tts" or node "b-tts" may serve it
    And it bills the serving node's owner exactly as today

  Scenario: a grant legitimately scoped to the serving node and model bills unchanged
    When the grant calls POST /v1/audio/speech for "af_heart"
    Then node "a-tts" serves it
    And the sponsor wallet is billed at the grant price and owner A's node earns the lot
