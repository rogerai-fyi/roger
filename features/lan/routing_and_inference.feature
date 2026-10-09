# STATUS: APPROVED 2026-10-06 (founder)
#
# LAN MODE - ROUTING AND INFERENCE: every turn, by every path, goes to a local source or is refused.
#
# THE PROMISE: in LAN mode no inference request reaches the market. Not as a fallback, not for
# one forgotten path. Every path that carries a turn today is named below, because the
# exploration found three that bypass the Edge ladder (TUI chat and console chat via ChatTurns,
# and the agent via BrokerCompleterRoute), and one that consults the market before the peer
# (`roger use <node>` calling BandRangeFor first). A promise with a forgotten path is not a
# promise.
#
# THE LADDER IN LAN MODE (extends features/edge/local_inference.feature):
#   rung 1  this machine's own model                (unchanged)
#   rung 2  a VERIFIED, LAN-direct Edge member      (unchanged; mTLS, pinned)
#   rung 2b a LAN Tower                             (new; plain http, trust-the-LAN)
#   rung 3  the market                              REMOVED in LAN mode
#   # RULED 2026-10-06: Edge members before LAN Towers. A member is the owner's own
#   # hardware, verified by certificate; a LAN Tower is trusted only because it is on the LAN
#   # and may be someone else's (an office Tower). When both serve a band, prefer the one with
#   # the stronger identity, then measured speed within each rung.
#
# RELATIONSHIP TO `edge.prefer`:
#   LAN mode implies `local-only` for the Edge for that process, WITHOUT writing edge_prefer.
#   It goes further than local-only: it adds LAN Towers as a rung and covers every path that
#   carries a turn. `edge.prefer local-only` without LAN mode keeps its approved meaning (Edge
#   only, no Towers). `edge.prefer market` is overridden while LAN mode is on, and the mode
#   reports that it was.
#
# GROUND TRUTH: internal/client/client.go (Use, relayWithFailover, ProxyOptions, relayEdgeFirst,
# dialEdgePeer, ProxyHandlerLive, ChatTurns), cmd/rogerai/edgeladder.go (applyEdgeLadder),
# internal/harness (BrokerCompleterRoute, LocalCompleter), internal/tui/agent.go,
# internal/operator/materialize.go (guest operators get the loopback proxy base URL),
# internal/webui/chat.go, internal/localplane (/v1/chat/completions on a Tower).
#
# Tags: @client, @cli, @tui, @agent (internal/tui/agent.go + internal/harness), @operator
# (internal/operator), @console.

Feature: In LAN mode every turn, by every path, is served by this machine, an Edge member or a LAN Tower, or it is refused, and nothing reaches the market

  Background:
    Given LAN mode is on
    And the public broker is reachable from this machine

  # =========================================================================
  # 1. THE LADDER
  # =========================================================================

  @client
  Scenario: this machine's own model serves first
    Given this machine serves "qwen3-8b"
    And the Edge member "jetson" and the LAN Tower at 192.168.1.20:8787 also serve "qwen3-8b"
    When a turn is dispatched for "qwen3-8b"
    Then it is served by this machine
    And no peer, no Tower and no broker was contacted for inference

  @client
  Scenario: an Edge member serves when this machine does not
    Given this machine serves nothing
    And the Edge member "jetson" serves "gpt-oss-20b", VERIFIED, LAN-direct
    And the LAN Tower at 192.168.1.20:8787 also serves "gpt-oss-20b"
    When a turn is dispatched for "gpt-oss-20b"
    Then it is served by "jetson" over mTLS
    And the Tower was not contacted for inference

  @client
  Scenario: a LAN Tower serves what no Edge member serves
    Given the LAN Tower at 192.168.1.20:8787 serves "llama-3.3-70b"
    And nothing else local serves it
    When a turn is dispatched for "llama-3.3-70b"
    Then the request goes to "http://192.168.1.20:8787/v1/chat/completions"
    And it carries this machine's signature and a per-request nonce
    And the answer is returned to the caller

  @client
  Scenario: a band nothing local serves is refused, never bought
    Given nothing local serves "gpt-oss-120b"
    And the market has "gpt-oss-120b" on air
    When a turn is dispatched for "gpt-oss-120b"
    Then it is refused
    And the refusal names the band and says nothing on this LAN serves it
    And it names the three ways to change that, and that turning LAN mode off would use the market
    And no request was made to the public broker
    And no money moved

  @client
  Scenario: failover stays inside the LAN
    Given the Edge member "jetson" and the LAN Tower at 192.168.1.20:8787 both serve "gpt-oss-20b"
    And "jetson" refuses the connection
    When a turn is dispatched for "gpt-oss-20b"
    Then it is served by the Tower at 192.168.1.20:8787
    And the turn's record names "jetson" as the source it left

  @client
  Scenario: every local source failing is refused with each failure named
    Given the Edge member "jetson" and the LAN Tower at 192.168.1.20:8787 both serve "gpt-oss-20b"
    And both fail
    When a turn is dispatched for "gpt-oss-20b"
    Then it is refused, naming "jetson" and the Tower and how each failed
    And no request was made to the public broker

  @client
  Scenario: a mid-stream failure is not retried somewhere the caller cannot see
    Given the LAN Tower at 192.168.1.20:8787 serves "llama-3.3-70b" as a stream
    And the stream breaks after the first tokens
    When the turn is in progress
    Then the caller receives the tokens already sent and a clear stream error
    And the turn is not silently restarted on another source
    # The same rule the market relay already follows once bytes have reached the consumer. # unblocked 2026-10-07 by features/tower/standalone_streaming.feature

  @client
  Scenario: within a rung the faster source is tried first
    Given two Edge members serve "gpt-oss-20b" and one has measured faster on this machine
    When a turn is dispatched for "gpt-oss-20b"
    Then the faster member is tried first

  @client
  Scenario: LAN mode overrides edge.prefer market, and says so
    Given edge.prefer is "market"
    When `roger status` is read
    Then it reports the Edge preference as "local-only (from LAN mode; your setting is market)"
    When a turn is dispatched for a band the market serves and nothing local serves
    Then it is refused

  @client
  Scenario: LAN mode does not rewrite the stored preference
    Given edge.prefer is "local"
    When a command runs with --lan and exits
    Then edge.prefer is still "local"

  @client
  Scenario: edge.prefer local-only without LAN mode keeps its approved meaning
    Given LAN mode is off
    And edge.prefer is "local-only"
    And only the LAN Tower at 192.168.1.20:8787 serves "llama-3.3-70b"
    When a turn is dispatched for "llama-3.3-70b"
    Then it is refused as not served on this Edge
    # LAN Towers are a LAN-mode source; local-only stays Edge-scoped (features/edge/mode.feature).

  # =========================================================================
  # 2. EVERY PATH THAT CARRIES A TURN
  # =========================================================================

  @cli
  Scenario: `roger use <band>` in LAN mode opens the proxy on a local source and never asks the market
    Given the LAN Tower at 192.168.1.20:8787 serves "llama-3.3-70b"
    When they run "roger --lan use llama-3.3-70b"
    Then the loopback proxy opens
    And no request was made to the public broker for /discover, /balance or /bands/resolve
    And the plate names the source "tower: 192.168.1.20"

  @cli
  Scenario: `roger use <band>` for a band nothing local serves exits non-zero before opening anything
    Given nothing local serves "gpt-oss-120b"
    When they run "roger --lan use gpt-oss-120b"
    Then it exits non-zero
    And the message names the band and the three local sources it checked
    And no proxy port was opened

  @cli
  Scenario: `roger use <node>` goes to the node and never consults the market first
    Given the Edge member "jetson" serves "gpt-oss-20b"
    And the market has no station for "gpt-oss-20b"
    When they run "roger --lan use jetson"
    Then the proxy opens pinned to "jetson"
    And no request was made to the public broker
    # Regression: client.Use called BandRangeFor(broker) before the peer and printed
    # "no station on air" for a band only the peer served.

  @cli
  Scenario: `roger use <tower-address>` addresses one LAN Tower
    Given the LAN Tower at 192.168.1.20:8787 serves "llama-3.3-70b" and "qwen3-8b"
    When they run "roger --lan use 192.168.1.20:8787 qwen3-8b"
    Then the proxy opens pinned to that Tower for "qwen3-8b"
    # RULED 2026-10-06: a Tower can be addressed by address in `roger use`, mirroring
    # node addressing (features/edge/addressed_inference.feature); a bare address that serves
    # one band needs no model, one that serves several lists them.

  @tui
  Scenario: tuning a band in the TUI binds the proxy to the local ladder
    Given the TUI is open in LAN mode
    And the dial lists "llama-3.3-70b" from the LAN Tower at 192.168.1.20:8787
    When the owner tunes "llama-3.3-70b" and confirms
    Then the live proxy routes to that Tower
    And no request was made to the public broker

  @tui
  Scenario: TUI chat in LAN mode goes through the local ladder, not ChatTurns to the broker
    Given the TUI is tuned to "llama-3.3-70b" from the LAN Tower at 192.168.1.20:8787
    When the owner sends a chat message
    Then the turn is served by that Tower
    And no request was made to the public broker's /v1/chat/completions
    # Regression: ChatTurns always posted to {broker}/v1/chat/completions, bypassing the ladder.

  @agent
  Scenario: an agent turn in LAN mode goes through the local ladder
    Given the TUI agent is running in LAN mode on "gpt-oss-20b" served by the Edge member "jetson"
    When the agent takes a turn
    Then the completion is served by "jetson"
    And no request was made to the public broker
    # Regression: BrokerCompleterRoute always went to the broker.

  @agent
  Scenario: an agent turn on this machine's own model still uses the direct local completer
    Given the TUI agent is running in LAN mode on "qwen3-8b" served by this machine
    When the agent takes a turn
    Then it is served by the local completer directly

  @operator
  Scenario: a guest operator in LAN mode reaches only local sources
    Given the TUI is tuned in LAN mode to "gpt-oss-20b" served by the Edge member "jetson"
    And the guest operator "opencode" is handed the mic
    When the operator sends a completion through the loopback proxy
    Then it is served by "jetson"
    And no request was made to the public broker

  @operator
  Scenario: a guest operator listing models sees only local bands
    Given the TUI is tuned in LAN mode
    When the guest operator requests GET /v1/models from the loopback proxy
    Then the list contains only bands from the local sources
    And no request was made to the public broker

  @operator
  Scenario: a guest naming a market-only model is refused locally
    Given the TUI is tuned in LAN mode
    When the guest operator sends a completion for "gpt-oss-120b" which nothing local serves
    Then the proxy answers 503 with a message that nothing on this LAN serves it
    And no request was made to the public broker

  @console
  Scenario: console chat in LAN mode goes through the local ladder
    Given "roger --lan webui" is running and tuned to "qwen3-8b" served by this machine
    When a chat message is sent from the console
    Then it is served by this machine
    And no request was made to the public broker

  @cli
  Scenario: `roger say` and voice in LAN mode use a local voice or refuse
    Given nothing local serves a TTS band
    When they run "roger --lan say hello"
    Then it is refused, saying no local voice is on air
    And no request was made to the public broker's /v1/audio/speech
    # RULED 2026-10-06: voice uses the same filter as text; a local TTS station is
    # a band like any other.

  @cli
  Scenario: remote sessions and context capsules are unchanged by LAN mode
    When they run "roger --lan remote-control"
    Then it behaves exactly as without --lan
    # They are not inference; the ruling keeps non-inference network use working.
    # RULED 2026-10-06: remote control and context capsules keep working unchanged in LAN mode.

  # =========================================================================
  # 3. VARIANTS, ALIASES AND CAPS IN LAN MODE
  # =========================================================================

  @client
  Scenario Outline: model variants keep a meaning on the LAN
    Given two local sources serve "qwen3-8b"
    When a turn is dispatched for "<model>"
    Then it is routed as "<meaning>"
    And the model the source receives is "qwen3-8b"

    Examples:
      | model           | meaning                                                   |
      | qwen3-8b:free   | any local source, every LAN turn is already free          |
      | qwen3-8b:nitro  | the measured-fastest local source first                   |
      | qwen3-8b:floor  | any local source; there is no price to floor on the LAN   |
  # unblocked 2026-10-08: the class table is shared with the broker (internal/routingrules), resolved over local bands only
  @client
  Scenario: a class alias resolves against local bands only
    Given the local sources serve "qwen3-8b" and "gpt-oss-20b"
    When a turn is dispatched for "@class/<a class both belong to>"
    Then it resolves to one of the local bands
    And never to a market band of that class

  @client
  Scenario: price caps are accepted and never refuse a LAN turn
    Given the request carries provider.max_price with prompt 0.1 and completion 0.1
    And this machine serves "qwen3-8b"
    When a turn is dispatched for "qwen3-8b"
    Then it is served
    # Every LAN source prices at 0, so any cap at or above 0 admits it.
  # unblocked 2026-10-08: local sources carry their quant label (the serve record, the Edge instance record; a Tower is unlabeled)
  @client
  Scenario: routing constraints still filter local sources
    Given the Edge member "jetson" serves "qwen3-8b" quantized "Q4_K_M" and this machine serves it quantized "Q8_0"
    When a turn is dispatched for "qwen3-8b" with provider.quantizations ["Q8_0"]
    Then it is served by this machine
  # unblocked 2026-10-08: the market's quant rule (routingrules.QuantAdmits) filters the LAN ladder
  @client
  Scenario: a routing constraint no local source meets is refused, naming the constraint
    Given only the Edge member "jetson" serves "qwen3-8b" quantized "Q4_K_M"
    When a turn is dispatched for "qwen3-8b" with provider.quantizations ["Q8_0"]
    Then it is refused naming provider.quantizations
    And no request was made to the public broker
  # unblocked 2026-10-08: the LAN ladder reads provider.order and allow_fallbacks
  @client
  Scenario: provider.order naming a market station is refused, not honored
    When a turn is dispatched with provider.order naming a market station id and allow_fallbacks false
    Then it is refused, saying the named station is not on this LAN
    And no request was made to the public broker

  @client
  Scenario: a private band code is not a LAN source
    When a turn is dispatched with X-Roger-Freq naming a private band
    Then it is refused, saying private bands are on the network and LAN mode is on
    And no request was made to the public broker's /bands/resolve
    # Private bands are broker-resolved (features/discovery/bands.feature), not a LAN mechanism.

  @client
  Scenario: an account key bearer is not sent to a LAN Tower
    Given the caller sends "Authorization: Bearer rog-key_..." through the loopback proxy
    When the turn is served by a LAN Tower
    Then the request to the Tower does not carry the account key
    And it carries this machine's signature instead
    # A LAN Tower is plain http; the existing rule refuses account keys over plain http.

  # RULED 2026-10-09: provider.order sorts within a rung; your own hardware always precedes Towers
  @client
  Scenario Outline: a provider.order naming a LAN Tower first does not lift it above your own hardware
    Given <own>
    And the LAN Tower at 192.168.1.20:8787 also serves "qwen3-8b"
    When a guest turn for "qwen3-8b" carries provider.order naming the LAN Tower at 192.168.1.20:8787 first
    Then it is served by <server>
    And the Tower was not contacted for inference

    Examples:
      | own                                                             | server                         |
      | this machine serves "qwen3-8b"                                  | this machine                   |
      | the Edge member "atlas" serves "qwen3-8b", VERIFIED, LAN-direct | "atlas" over mTLS              |
