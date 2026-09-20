# ROGER EDGE - LOCAL INFERENCE: your fleet is one inference pool.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-19: "say i have the jetson orin machines and roger is running there, i
# should be able to use other edge inference from any of them".
#
# Today a turn has two possible fates: a model loaded in this very process, or the market through
# the broker. There is nothing in between. A Pi and a Jetson on the same switch talk to each other
# by way of the internet, and on an airgapped Edge they cannot talk at all. That is the single
# biggest gap between what Roger Edge draws and what it is for.
#
# THE LADDER. A turn is dispatched in order:
#   1. this instance's own loaded model (exists: harness.LocalCompleter)
#   2. an Edge peer whose instance serves that band - VERIFIED, reachable LAN-direct, dialled over
#      its own certificate and checked against its pin. No broker, no internet, no account lookup.
#      Failover across peers before the rung is given up.
#   3. the market through the broker, exactly as today - only when the ROOT is Core-linked and the
#      preference is not local-only (features/edge/mode.feature).
#
# RUNG 2 MOVES NO MONEY. The owner owns both ends: no counterparty, no hold, no fee, no ledger
# entry. But the Edge's honesty rule still binds ("everything drawn was already receipted",
# features/edge/sessions.feature), so a local turn produces a LOCAL RECEIPT: the same shape as a
# broker receipt, signed by the SERVING NODE's key rather than the broker's, cost zero, marked
# local. It exists for the view and for audit, never for settlement. This is a second issuer of
# receipts and this spec says so plainly. It cannot become a fee dodge by construction: rung 2 can
# only reach nodes enrolled under this Edge's own root - machines the same owner already owns.
#
# THE WIRE. A peer serves at its own LAN face (the same listener that answers describe) on a new
# path, over mutual TLS: the caller presents ITS node certificate, the server checks it chains to
# the Edge's root and names a member of the same account; the caller checks the server's
# certificate against the pin the fleet recorded. Nothing else authenticates a peer turn - no
# bearer token, no session key on the LAN - because a certificate is the one credential this
# layer already has and already refuses to duplicate.
#
# GROUND TRUTH: internal/client/client.go (relayWithFailover, ProxyOptions - where rung 2 slots
# in ahead of the broker), internal/harness/local.go (rung 1), internal/edge/server.go (the face,
# gaining the infer path), internal/edge/verify.go (VerifyPeer, the pin check), internal/
# edgeauth/identity.go (Store.Trust - the verify-only authority a node checks peers with),
# internal/protocol/protocol.go (UsageReceipt, SignNode), internal/edge/session.go (Record).
#
# Tags: @edge (internal/edge: the face and the local receipt), @client (internal/client: the
# ladder), @cli, @tui.

Feature: A turn is served by the nearest thing that can serve it: this instance, then a peer on this Edge over the LAN, then the market

  # =========================================================================
  # 1. THE LADDER
  # =========================================================================

  @client
  Scenario: rung 1 - this instance's own model serves before anything is dialled
    Given this instance has "gpt-oss-20b" loaded
    And a peer on this Edge also serves "gpt-oss-20b"
    When a turn is dispatched for "gpt-oss-20b"
    Then it is served by this instance's own model
    And no peer and no broker was contacted
    And the session's route is local, naming this instance

  @client
  Scenario: rung 2 - a peer on this Edge serves what this instance does not have
    Given this instance has no model loaded
    And the instance "jetson/serve" on this Edge serves "qwen-3.8-27b", VERIFIED, LAN-direct
    When a turn is dispatched for "qwen-3.8-27b"
    Then it is served by "jetson/serve" over the LAN
    And no broker was contacted
    And the session's route is local, naming "jetson/serve"

  @client
  Scenario: rung 3 - the market, only when nothing on the Edge serves it
    Given no instance on this Edge serves "gpt-oss-120b"
    And the preference is local
    When a turn is dispatched for "gpt-oss-120b"
    Then it is served through the broker
    And the session's route is market

  @client
  Scenario: a CLAIMED serve is not a rung
    Given the instance "bench/lab" claims serve for "qwen-3.8-27b" but is not VERIFIED
    And no other instance serves it
    When a turn is dispatched for "qwen-3.8-27b" under the preference local
    Then "bench/lab" is not dialled
    And the turn falls out to the market
    And the reason names that the peer's serve is unverified

  @client
  Scenario: a dark peer is not a rung
    Given the instance "jetson/serve" serves "qwen-3.8-27b" but its node is DARK
    When a turn is dispatched for "qwen-3.8-27b"
    Then "jetson/serve" is not dialled
    And the ladder continues

  @client
  Scenario: a relayed-only peer is not a rung 2 peer
    Given the instance "shed/serve" serves "qwen-3.8-27b" but is reachable only through a relay
    When a turn is dispatched for "qwen-3.8-27b"
    Then it is not served LAN-direct by "shed/serve"
    And the ladder continues to the market

  @client
  Scenario: failover across peers before giving the rung up
    Given the instances "jetson/serve" and "bench/serve" both serve "qwen-3.8-27b", VERIFIED
    And "jetson/serve" refuses the connection
    When a turn is dispatched for "qwen-3.8-27b"
    Then it is served by "bench/serve"
    And the session names "jetson/serve" as the peer it left
    And no broker was contacted

  @client
  Scenario: every peer failing falls out to the market under local, and refuses under local-only
    Given two peers serve "qwen-3.8-27b" and both fail
    When a turn is dispatched under the preference local
    Then it is served through the broker and the route is market
    When a turn is dispatched under the preference local-only
    Then it is refused, naming both peers and how each failed

  @client
  Scenario: the preferred peer is the one that answered fastest last time
    Given two peers serve "qwen-3.8-27b" and one has measured faster on this Edge
    When a turn is dispatched for "qwen-3.8-27b"
    Then the faster peer is tried first
    And the choice is drawn from measurement, never from the order the fleet lists them in

  # =========================================================================
  # 2. THE WIRE
  # =========================================================================

  @edge
  Scenario: a peer turn is served at the instance's own LAN face
    Given the instance "jetson/serve" serving "qwen-3.8-27b" with its LAN face up
    When a member dials its infer path with a completion request for "qwen-3.8-27b"
    Then it is answered with a completion
    And it was answered by the model that instance has loaded, not relayed anywhere

  @edge
  Scenario: the caller must present a certificate under this Edge's root
    Given the instance "jetson/serve" with its LAN face up
    When a dial arrives with no client certificate
    Then it is refused before any request body is read
    When a dial arrives with a certificate under a different root
    Then it is refused the same way

  @edge
  Scenario: a certificate under the root but of another account is refused
    Given two Edges on one LAN under one designated authority, accounts "acct-1" and "acct-2"
    When an "acct-2" member dials an "acct-1" instance's infer path
    Then it is refused
    And nothing about the "acct-1" instance's bands is disclosed in the refusal

  @edge
  Scenario: a revoked member is refused
    Given the instance "bench/desk" whose node's certificate this Edge has revoked
    When it dials a peer's infer path
    Then it is refused
    And the refusal is drawn on the serving instance's Edge as a refused session

  @edge
  Scenario: the caller checks the server against the pin, never the other way round
    Given a peer whose served certificate no longer matches the pin the fleet recorded
    When a turn would be dispatched to it
    Then it is not dialled for inference
    And the node is marked as needing re-verification, as discovery already does

  @edge
  Scenario: a band the instance does not serve is refused, naming what it does serve
    Given the instance "jetson/serve" serving "qwen-3.8-27b"
    When a member asks it for "gpt-oss-120b"
    Then it is refused
    And the refusal names the bands it serves

  @edge
  Scenario: the infer path speaks the OpenAI shape, streaming included
    Given the instance "jetson/serve" serving "qwen-3.8-27b"
    When a member sends a streaming completion request
    Then the answer streams as server-sent events, exactly as a market station's would
    And the local receipt rides the stream's end as the same comment the broker uses

  @edge
  Scenario: a peer turn is bounded like a market turn
    Given the instance "jetson/serve" serving "qwen-3.8-27b"
    When a member sends a body over the size cap
    Then it is refused with the same shaped error the local proxy gives
    And a request that stalls past the header timeout is dropped, never held open

  # =========================================================================
  # 3. THE LOCAL RECEIPT
  # =========================================================================

  @edge
  Scenario: a peer turn produces a local receipt signed by the serving node
    Given a peer turn served by "jetson/serve"
    Then a receipt is returned with the request id, the band, the serving node and instance, and the token counts
    And it is signed by the serving NODE's key and verifies against that node's certificate
    And its cost is zero
    And it is marked local

  @edge
  Scenario: a local receipt is never sent to the broker and never enters the ledger
    Given a peer turn served on this Edge
    Then no receipt reached the broker
    And no ledger, hold or grant usage changed anywhere

  @edge
  Scenario: the view draws a local receipt exactly as it draws a broker receipt
    Given a session recorded from a local receipt
    When the Edge view reads it
    Then it names the band, the station and the outcome as any other session does
    And its route reads local

  @edge
  Scenario: a local receipt that does not verify is not a session
    Given a peer returns a receipt whose signature does not match its certificate
    Then the turn's answer is still delivered to the caller
    And no session is recorded from that receipt
    And the peer is marked as needing re-verification

  @edge
  Scenario: a local receipt cannot be replayed as a market receipt
    Given a local receipt from "jetson/serve"
    When it is presented to the broker as if it were a market receipt
    Then the broker refuses it, because it carries no broker signature
    And nothing settles

  # =========================================================================
  # 4. NO MONEY MOVES, BY CONSTRUCTION
  # =========================================================================

  @edge
  Scenario: rung 2 can only reach nodes under this Edge's own root
    Given a station on the open market that also happens to be on this LAN
    When the ladder looks for a rung 2 peer
    Then that station is not a candidate unless it is an enrolled member
    And a stranger's machine is never dialled as a peer

  @client
  Scenario: a rung 2 turn debits nothing and charges nothing
    Given a wallet with a known balance
    When a turn is served by a peer on this Edge
    Then the balance is unchanged
    And no hold was placed and none released

  @client
  Scenario: the spend limits still bound what falls out to the market
    Given a per-band spend limit
    And no peer serves the band
    When a turn falls out to the market
    Then the existing limit applies exactly as it does today

  # =========================================================================
  # 5. THE SURFACES
  # =========================================================================

  @tui
  Scenario: a peer serving a band is drawn as a band you can use
    Given the instance "jetson/serve" serving "qwen-3.8-27b" on this Edge
    When the Edge screen renders at 100 columns
    Then "jetson/serve" shows the band it serves
    And the band is marked as reachable from here

  @tui
  Scenario: a turn that went to a peer pulses along that peer's wire
    Given a turn served by "jetson/serve"
    When the Edge screen renders the next frame
    Then a pulse travels the wire to "jetson/serve"
    And the session row reads local and names "jetson/serve"

  @cli
  Scenario: `roger edge bands` lists what this Edge can serve locally
    Given instances on this Edge serving "qwen-3.8-27b" and "gpt-oss-20b"
    When they run "roger edge bands"
    Then it lists each band with the instances that serve it and whether each is reachable from here
    And it says which bands would have to go to the market

  @cli
  Scenario: `roger use` says which rung served each turn
    Given `roger use` open for "qwen-3.8-27b" and a peer serving it
    When a turn is relayed
    Then the endpoint's log line names the peer and the route local
