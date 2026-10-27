# CAPABILITY GATING: a request that NEEDS a capability (tool calling, images) is routed only
# to a station the broker RECORDS as having it. Today capabilities are advertised on
# /discover and /market but never consulted by the pick, so a `tools` request can land on a
# station that returns plain text and an image request on a text-only station - the consumer
# pays for a wrong answer and the honest operator eats a strike for "empty output".
#
# Contract: features/routing/ROUTING-EXPRESSION-CONTRACT.md §5 (capability bullets), §1a
# (validation), §2 (error codes), §6 (bridge parity).
#
# GROUND TRUTH as of origin/main 518c698b:
#   - The relay decodes ONLY `model` + `stream` from the body (cmd/rogerai-broker/tunnel.go:1724);
#     `tools`, `tool_choice`, `response_format`, image parts are forwarded verbatim and never read.
#   - pickFor (tunnel.go:3165-3355) has NO capability filter: the per-offer loop skips on model,
#     modality, price caps and the declared-ctx gate only (tunnel.go:3268-3292).
#   - The capability set is CLOSED: protocol.knownCapabilities = {"vision","tools"}
#     (internal/protocol/protocol.go:240); CanonicalCapabilities lowercases, dedupes, drops
#     unknown (protocol.go:247-268).
#   - "tools" is VERIFIED-not-declared: register strips a node-declared "tools"
#     (tunnel.go:239-259, stripDeclaredTools); the ONLY writer is the tool-call canary
#     recordToolProbe (cmd/rogerai-broker/toolcall.go:264-310), keyed toolKey(node, model)
#     (toolcall.go:115) - per (node, MODEL), not per node; a definitive fail clears it only on
#     the authoritative poll host; a transient (429/timeout) never sets or clears.
#     Emission re-strips and re-adds from the verdict (withVerifiedTools, toolcall.go:219).
#     Multi-instance reads the shared union b.toolsMerged (toolcall.go:355).
#   - "vision" is node-DECLARED (protocol.go:225, FOUNDER FLAG T3). /market ADDS vision from
#     an id heuristic (detect.VisionFromID, market.go:381-403); /discover does not.
#   - The edge/Tower bridge (cmd/rogerai-broker/edgebridge.go) evaluates none of this today.
#
# After this spec: `roger.require`, `provider.require_parameters` and the IMPLICIT rule
# (non-empty `tools` => tools; any image_url part => vision) are HARD filters in the same
# eligibility pass as min-tps. A capability counts exactly as the broker records it: tools
# only when probe-verified for THAT (node, model); vision when declared for the offer. The
# /market id-heuristic is DISPLAY ONLY and never grants routing eligibility.
#
# Enforced by: cmd/rogerai-broker/capability_gating_bdd_test.go (godog, strict) - to be
# written after approval; the RED table test lives in router_test.go.

Feature: Capability gating - a request that needs tools or vision only routes to a station recorded as having it

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And the tool-call canary and moderation are enabled with their defaults

  # --- explicit require: parsing ----------------------------------------------------------
  Scenario Outline: An explicit require value from the closed set is accepted
    Given node "n-cap" is on air for "qwen3-32b" and the broker records capability "<cap>" for it
    When a request for "qwen3-32b" carries roger.require ["<cap>"]
    Then "n-cap" is a candidate
    And the request is served by "n-cap"

    Examples:
      | cap    |
      | tools  |
      | vision |

  Scenario Outline: An unknown require value is a 400 naming the key
    Given node "n-any" is on air for "qwen3-32b" and was seen just now
    When a request for "qwen3-32b" carries roger.require ["<value>"]
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "roger.require"
    And no station is dispatched to and no hold is placed

    Examples:
      | value      |
      | reasoning  |
      | json       |
      | audio      |
      | image      |
      | tool       |
      | TOOLS!     |
      |            |
      | tools,vision |

  Scenario: require given as a string instead of an array is a 400
    Given node "n-any" is on air for "qwen3-32b" and was seen just now
    When a request for "qwen3-32b" carries roger.require "tools"
    Then the status is 400
    And the error code is "invalid_routing_value"

  Scenario: require given as an array of non-strings is a 400
    Given node "n-any" is on air for "qwen3-32b" and was seen just now
    When a request for "qwen3-32b" carries roger.require [1, true]
    Then the status is 400
    And the error code is "invalid_routing_value"

  Scenario: An empty require array is no requirement at all
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries roger.require []
    Then "n-plain" is a candidate

  Scenario: Duplicate require values are de-duplicated, not an error
    Given node "n-tools" is on air for "qwen3-32b" and the broker records capability "tools" for it
    When a request for "qwen3-32b" carries roger.require ["tools", "tools", "tools"]
    Then "n-tools" is a candidate
    And the effective requirement is exactly ["tools"]

  Scenario Outline: require values are exact lowercase - a cased or padded spelling is a 400, never folded
    # Contract §1a: enumerated values (sort, pref, trust_min, require, region, sugar) are exact
    # lowercase; quant labels are the one case-insensitive comparison. The broker does not
    # guess what "Tools" meant.
    Given node "n-tools" is on air for "qwen3-32b" and the broker records capability "tools" for it
    When a request for "qwen3-32b" carries roger.require ["<raw>"]
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "roger.require"
    And no station was dispatched to

    Examples:
      | raw       |
      | Tools     |
      | TOOLS     |
      | " tools " |

  Scenario: More than 32 require entries is a 400 even when every entry is valid
    Given node "n-tools" is on air for "qwen3-32b" and the broker records capability "tools" for it
    When a request for "qwen3-32b" carries roger.require with 33 entries all reading "tools"
    Then the status is 400
    And the error code is "invalid_routing_value"

  Scenario: Requiring both known capabilities admits only a station recorded with both
    Given node "n-both" is on air for "qwen3-32b" and the broker records capabilities "tools" and "vision" for it
    And node "n-tools-only" is on air for "qwen3-32b" and the broker records capability "tools" for it
    And node "n-vision-only" is on air for "qwen3-32b" and the broker records capability "vision" for it
    When a request for "qwen3-32b" carries roger.require ["tools", "vision"]
    Then "n-both" is a candidate
    And "n-tools-only" is NOT a candidate
    And "n-vision-only" is NOT a candidate

  # --- explicit require: the header path has no equivalent ------------------------------
  Scenario: There is no X-Roger header for require - it is body-only, and a made-up header is ignored
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries header "X-Roger-Require: tools" and no body require
    Then "n-plain" is a candidate
    And the unknown header is not an error

  # --- the implicit rule: tools ----------------------------------------------------------
  Scenario: A body with a non-empty tools array implicitly requires tools
    Given node "n-tools" is on air for "qwen3-32b" and the broker records capability "tools" for it
    And node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries one function tool and no roger.require
    Then "n-tools" is a candidate
    And "n-plain" is NOT a candidate

  Scenario: The regression pin - a tools request no longer lands on a no-tools station
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When a request for "qwen3-32b" carries one function tool
    Then no station is dispatched to
    And the status is 503
    And the error code is "no_match"
    And the error message names the capability "tools"
    And the response carries "X-RogerAI-Cost: 0" and no receipt
    And "n-plain" is not struck and earns nothing

  Scenario: An empty tools array adds no requirement
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries tools []
    Then "n-plain" is a candidate

  Scenario: tools set to null adds no requirement
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries tools null
    Then "n-plain" is a candidate

  Scenario: tool_choice without a tools array adds no requirement and is forwarded untouched
    # The broker is not an OpenAI schema validator. Today the body is forwarded verbatim and
    # the upstream answers; a 400 from upstream is the consumer's own error, not a failover
    # trigger. That stays. Only a NON-EMPTY tools array is evidence the request needs tools.
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries tool_choice "auto" and no tools array
    Then "n-plain" is a candidate
    And the station receives the body with tool_choice "auto" intact

  Scenario: A tools array whose entries are not objects is forwarded, and still implicitly requires tools
    # A malformed tool is still a tool request. We do not validate the entries; the upstream
    # returns its own 400 and that 400 is not a failover trigger.
    Given node "n-tools" is on air for "qwen3-32b" and the broker records capability "tools" for it
    And node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries tools ["not-an-object"]
    Then "n-tools" is a candidate
    And "n-plain" is NOT a candidate

  Scenario: The implicit tools requirement cannot be switched off by an explicit empty require
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When a request for "qwen3-32b" carries one function tool and roger.require []
    Then the status is 503
    And the error code is "no_match"
    And the error message names the capability "tools"

  Scenario: The implicit tools requirement cannot be switched off by require_parameters:false
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When a request for "qwen3-32b" carries one function tool and provider.require_parameters false
    Then the status is 503
    And the error code is "no_match"

  Scenario: Explicit require and implicit tools compose as a union
    Given node "n-both" is on air for "qwen3-32b" and the broker records capabilities "tools" and "vision" for it
    And node "n-tools-only" is on air for "qwen3-32b" and the broker records capability "tools" for it
    When a request for "qwen3-32b" carries one function tool and roger.require ["vision"]
    Then "n-both" is a candidate
    And "n-tools-only" is NOT a candidate

  Scenario: A streaming tools request is gated identically to a non-streaming one
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When a streaming request for "qwen3-32b" carries one function tool
    Then the status is 503 before any SSE header is committed
    And the error code is "no_match"

  # --- the implicit rule: vision ---------------------------------------------------------
  Scenario Outline: An image_url content part in any message role implicitly requires vision
    Given node "n-vision" is on air for "qwen3-32b" and the broker records capability "vision" for it
    And node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries a "<role>" message whose content array holds an image_url part
    Then "n-vision" is a candidate
    And "n-plain" is NOT a candidate

    Examples:
      | role      |
      | user      |
      | assistant |
      | system    |
      | tool      |

  Scenario: An image_url part deep in a multi-part content array still requires vision
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When a request for "qwen3-32b" carries a user message whose content array is [text, text, image_url, text]
    Then the status is 503
    And the error code is "no_match"
    And the error message names the capability "vision"

  Scenario: An image_url part in the second of several messages still requires vision
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When a request for "qwen3-32b" carries three user messages and only the second holds an image_url part
    Then the status is 503
    And the error code is "no_match"

  Scenario: A base64 data-URI image is an image
    Given node "n-vision" is on air for "qwen3-32b" and the broker records capability "vision" for it
    And node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries an image_url part whose url is "data:image/png;base64,iVBOR..."
    Then "n-vision" is a candidate
    And "n-plain" is NOT a candidate

  Scenario: An image_url given as a bare string instead of {url} is still an image
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When a request for "qwen3-32b" carries a content part {"type":"image_url","image_url":"https://x/y.png"}
    Then the status is 503
    And the error code is "no_match"

  Scenario: A text-only string content adds no requirement
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries a user message whose content is the string "hello"
    Then "n-plain" is a candidate

  Scenario: A content array of only text parts adds no requirement
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries a user message whose content array holds two text parts
    Then "n-plain" is a candidate

  Scenario: The word image_url inside a text part is not an image
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries a text part whose text reads "set type to image_url"
    Then "n-plain" is a candidate

  Scenario: An input_audio part adds no requirement today (no audio capability exists)
    # The closed set has no "audio"; the part is forwarded verbatim and the upstream decides.
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries a content part of type "input_audio"
    Then "n-plain" is a candidate

  Scenario: Vision counts as DECLARED - a station that declared it is eligible without any probe
    Given node "n-vision" is on air for "qwen3-32b" and declared capability "vision" at registration
    And "n-vision" has never been probed
    When a request for "qwen3-32b" carries an image_url part
    Then "n-vision" is a candidate

  Scenario: The /market id-heuristic never grants routing eligibility for vision
    # market.go:381-403 ADDS "vision" to the aggregate for an obviously-vision id so the app
    # shows a photo button. Routing reads the OFFER's declared capability, never the heuristic.
    Given node "n-undeclared" is on air for "llava-1.6-vision" and declared no capabilities
    When a consumer GETs /market
    Then "llava-1.6-vision" lists capability "vision" on /market
    When a request for "llava-1.6-vision" carries an image_url part
    Then "n-undeclared" is NOT a candidate
    And the status is 503 with error code "no_match"

  # --- provider.require_parameters ------------------------------------------------------
  Scenario: require_parameters:true adds tools when tool_choice is set even without a tools array
    Given node "n-tools" is on air for "qwen3-32b" and the broker records capability "tools" for it
    And node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries tool_choice "required", no tools array, and provider.require_parameters true
    Then "n-tools" is a candidate
    And "n-plain" is NOT a candidate

  Scenario Outline: require_parameters:true treats a JSON response_format as needing a tools-verified station
    # We verify structured output only through the tool-call canary today; json_object /
    # json_schema ride that verification. Without require_parameters the format is forwarded
    # and the upstream decides (as today).
    Given node "n-tools" is on air for "qwen3-32b" and the broker records capability "tools" for it
    And node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries response_format type "<type>" and provider.require_parameters true
    Then "n-tools" is a candidate
    And "n-plain" is NOT a candidate

    Examples:
      | type        |
      | json_object |
      | json_schema |

  Scenario: require_parameters:true with response_format text adds nothing
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries response_format type "text" and provider.require_parameters true
    Then "n-plain" is a candidate

  Scenario: require_parameters:false (the default) adds nothing beyond the implicit rule
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries response_format type "json_object" and provider.require_parameters false
    Then "n-plain" is a candidate
    And the station receives response_format intact

  Scenario: require_parameters omitted behaves as false
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries tool_choice "auto" and no provider object
    Then "n-plain" is a candidate

  # corrected 2026-10-01 (founder-approved): dropped the `null` row - null means absent (§1a), never a 400.
  Scenario Outline: require_parameters must be a boolean
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries provider.require_parameters <value>
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "provider.require_parameters"

    Examples:
      | value  |
      | "true" |
      | 1      |
      | []     |

  # --- verified-not-declared: how a station earns and loses tools ------------------------
  Scenario: A station cannot become eligible for tools requests by declaring tools at registration
    Given node "n-liar" registers for "qwen3-32b" declaring capability "tools"
    And "n-liar" has never passed a tool-call canary
    When a request for "qwen3-32b" carries one function tool
    Then "n-liar" is NOT a candidate

  Scenario: A station cannot become eligible for tools requests via a mirrored or re-hydrated declared tools
    Given node "n-mirror" for "qwen3-32b" reached the registry through the shared-registry mirror with a stored "tools" capability
    And "n-mirror" has never passed a tool-call canary
    When a request for "qwen3-32b" carries one function tool
    Then "n-mirror" is NOT a candidate

  Scenario: A passing tool-call canary makes the station eligible for tools requests
    Given node "n-earn" is on air for "qwen3-32b" and has never been tool-call probed
    And a request for "qwen3-32b" carrying one function tool finds "n-earn" NOT a candidate
    When the tool-call canary for ("n-earn", "qwen3-32b") passes
    Then a request for "qwen3-32b" carrying one function tool finds "n-earn" a candidate

  Scenario: A definitive canary failure on the authoritative host removes tools eligibility
    Given node "n-regress" is on air for "qwen3-32b" and previously earned verified "tools"
    When the tool-call canary for ("n-regress", "qwen3-32b") fails definitively on the authoritative host
    Then a request for "qwen3-32b" carrying one function tool finds "n-regress" NOT a candidate

  Scenario: A transient canary outcome changes nothing either way
    Given node "n-flaky-net" is on air for "qwen3-32b" and previously earned verified "tools"
    When the tool-call canary for ("n-flaky-net", "qwen3-32b") answers 429
    Then a request for "qwen3-32b" carrying one function tool still finds "n-flaky-net" a candidate

  Scenario: A non-authoritative peer's failed cross-instance canary does not remove tools eligibility
    Given the broker runs two instances behind the shared store
    And instance A hosts the poll of node "n-host" and proved verified "tools" for "qwen3-32b"
    When instance B's own canary against "n-host" fails
    Then on instance B a request for "qwen3-32b" carrying one function tool still finds "n-host" a candidate

  Scenario: An authoritative host regression removes tools eligibility on every peer after the sync
    Given the broker runs two instances behind the shared store
    And instance A hosts the poll of node "n-host" and proved verified "tools" for "qwen3-32b"
    When instance A's canary fails definitively and the shared verdict syncs
    Then on instance B a request for "qwen3-32b" carrying one function tool finds "n-host" NOT a candidate

  Scenario: The verified tools bit expires with its TTL and eligibility goes with it
    Given node "n-stale-bit" earned verified "tools" for "qwen3-32b" longer ago than toolsVerifiedTTL with no re-verification
    When a request for "qwen3-32b" carries one function tool
    Then "n-stale-bit" is NOT a candidate

  Scenario: Regaining tools after a regression takes exactly one passing canary
    Given node "n-back" lost verified "tools" for "qwen3-32b" to a definitive failure
    When the tool-call canary for ("n-back", "qwen3-32b") passes
    Then a request for "qwen3-32b" carrying one function tool finds "n-back" a candidate

  # --- tools is keyed per (node, model) -------------------------------------------------
  Scenario: A tools verdict on model A does not make the same node eligible for tools on model B
    Given node "n-two" is on air for "qwen3-32b" and "llama-3.3-70b"
    And the tool-call canary for ("n-two", "qwen3-32b") passed
    And ("n-two", "llama-3.3-70b") has never been tool-call probed
    When a request for "llama-3.3-70b" carries one function tool
    Then "n-two" is NOT a candidate
    When a request for "qwen3-32b" carries one function tool
    Then "n-two" is a candidate

  Scenario: A regression on model B leaves model A's tools eligibility on the same node intact
    Given node "n-two" earned verified "tools" for both "qwen3-32b" and "llama-3.3-70b"
    When the tool-call canary for ("n-two", "llama-3.3-70b") fails definitively on the authoritative host
    Then a request for "qwen3-32b" carrying one function tool finds "n-two" a candidate
    And a request for "llama-3.3-70b" carrying one function tool finds "n-two" NOT a candidate

  Scenario: Two stations on the same model - only the verified one takes the tools traffic
    Given node "n-verified" is on air for "qwen3-32b" and earned verified "tools"
    And node "n-unverified" is on air for "qwen3-32b", cheaper, faster, and never tool-call probed
    When 20 requests for "qwen3-32b" each carrying one function tool are routed
    Then every one of them is served by "n-verified"

  Scenario: A vision declaration on one offer does not extend to the node's other offers
    Given node "n-two" is on air for "llava-1.6" declaring "vision" and for "qwen3-32b" declaring nothing
    When a request for "qwen3-32b" carries an image_url part
    Then "n-two" is NOT a candidate

  # --- the verified/declared asymmetry is visible ---------------------------------------
  Scenario: /discover shows tools only when verified and vision when declared, and routing agrees with what it shows
    Given node "n-show" is on air for "qwen3-32b" declaring "vision" and with a passed tool-call canary
    When a consumer GETs /discover
    Then the "n-show" offer for "qwen3-32b" lists capabilities ["tools", "vision"]
    And a request for "qwen3-32b" carrying one function tool and an image_url part finds "n-show" a candidate

  Scenario: A station /discover shows WITHOUT tools is never chosen for a tools request
    Given node "n-hide" is on air for "qwen3-32b" and /discover lists no "tools" capability for it
    When a request for "qwen3-32b" carries one function tool
    Then "n-hide" is NOT a candidate

  # --- interactions: models[] ------------------------------------------------------------
  Scenario: A model whose stations all lack the required capability is skipped and the next model serves
    Given node "n-a" is the only station for "qwen3-32b" and has no recorded capability
    And node "n-b" is on air for "llama-3.3-70b" and earned verified "tools"
    When a request with models ["qwen3-32b", "llama-3.3-70b"] carries one function tool
    Then the request is served by "n-b"
    And the response carries "X-RogerAI-Model: llama-3.3-70b"
    And no attempt was made against "n-a"

  Scenario: Every model in the list lacking the capability is a single 503 no_match naming the capability
    Given node "n-a" is the only station for "qwen3-32b" and has no recorded capability
    And node "n-b" is the only station for "llama-3.3-70b" and has no recorded capability
    When a request with models ["qwen3-32b", "llama-3.3-70b"] carries one function tool
    Then the status is 503
    And the error code is "no_match"
    And the error message names the capability "tools"
    And no hold was placed

  Scenario: The capability filter applies before the plan is priced, so no hold covers an incapable station
    Given node "n-cheap-plain" is on air for "qwen3-32b" at out-price 0.10 with no recorded capability
    And node "n-tools" is on air for "qwen3-32b" at out-price 0.50 and earned verified "tools"
    When a request for "qwen3-32b" carrying one function tool is served
    Then the hold was sized on "n-tools" only
    And "n-cheap-plain" never appears in the plan

  # --- interactions: only / order / pin / ignore -----------------------------------------
  Scenario: A listed only-node that lacks the capability is skipped, not an error, when another listed node has it
    Given node "n-1" is on air for "qwen3-32b" with no recorded capability
    And node "n-2" is on air for "qwen3-32b" and earned verified "tools"
    When a request for "qwen3-32b" carries one function tool and provider.only ["n-1", "n-2"]
    Then the request is served by "n-2"

  Scenario: An only-list whose every node lacks the capability is a 503 no_match
    Given node "n-1" is on air for "qwen3-32b" with no recorded capability
    And node "n-2" is on air for "qwen3-32b" and earned verified "tools"
    When a request for "qwen3-32b" carries one function tool and provider.only ["n-1"]
    Then the status is 503
    And the error code is "no_match"
    And "n-2" was never dispatched to

  Scenario: An ordered node that lacks the capability is skipped and the next ordered node serves
    Given node "n-first" is on air for "qwen3-32b" with no recorded capability
    And node "n-second" is on air for "qwen3-32b" and earned verified "tools"
    When a request for "qwen3-32b" carries one function tool and provider.order ["n-first", "n-second"]
    Then the request is served by "n-second"

  Scenario: A pinned node that lacks the capability is a 503 no_match - a pin never overrides a capability
    Given node "n-pinned" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries one function tool and header "X-Roger-Node: n-pinned"
    Then the status is 503
    And the error code is "no_match"

  Scenario: allow_fallbacks:false with a single capable listed node serves; with an incapable one refuses
    Given node "n-cap" is on air for "qwen3-32b" and earned verified "tools"
    And node "n-plain" is on air for "qwen3-32b" with no recorded capability
    When a request for "qwen3-32b" carries one function tool, provider.order ["n-cap"] and allow_fallbacks false
    Then the request is served by "n-cap"
    When a request for "qwen3-32b" carries one function tool, provider.order ["n-plain"] and allow_fallbacks false
    Then the status is 503 with error code "no_match"

  Scenario: The capability filter never widens an ignore list
    Given node "n-cap" is on air for "qwen3-32b" and earned verified "tools"
    When a request for "qwen3-32b" carries one function tool and provider.ignore ["n-cap"]
    Then the status is 503
    And the error code is "no_match"

  # --- interactions: the edge / Tower bridge ---------------------------------------------
  Scenario: The bridge coin cannot send a tools request to a Tower whose offer is not tools-verified
    Given node "n-cap" is on air for "qwen3-32b" and earned verified "tools"
    And a Tower relay also hosts "qwen3-32b" with no tools verdict
    When 20 requests for "qwen3-32b" each carrying one function tool are routed
    Then every one of them is served by "n-cap" directly
    And the bridge was never entered

  Scenario: A Tower whose offer is tools-verified serves when no direct station is capable
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    And a Tower relay hosts "qwen3-32b" with a verified tools verdict
    When a request for "qwen3-32b" carries one function tool
    Then the request is served through the bridge
    And the response carries "X-RogerAI-Relay" naming the Tower

  Scenario: Neither a direct station nor a Tower capable is a 503 no_match
    Given node "n-plain" is on air for "qwen3-32b" with no recorded capability
    And a Tower relay hosts "qwen3-32b" with no tools verdict
    When a request for "qwen3-32b" carries one function tool
    Then the status is 503
    And the error code is "no_match"

  Scenario: The tools array itself is forwarded to the serving station on the bridge path, the routing carriers are not
    Given a Tower relay hosts "qwen3-32b" with a verified tools verdict and is the only capable server
    When a request for "qwen3-32b" carries one function tool and roger.require ["tools"]
    Then the Tower's hub receives the body with the tools array intact
    And the Tower's hub receives no "roger" and no "provider" key

  # --- interactions: grants, bands, free and anonymous -----------------------------------
  Scenario: A grant confines to the owner's nodes and the capability filter narrows within them
    Given owner "op-1" has node "n-op-plain" for "qwen3-32b" with no recorded capability
    And owner "op-1" has node "n-op-tools" for "qwen3-32b" that earned verified "tools"
    And a grant from "op-1" allows model "qwen3-32b"
    When a grant-keyed request for "qwen3-32b" carries one function tool
    Then the request is served by "n-op-tools"

  Scenario: A grant whose owner has no capable node is a 503 no_match, never another owner's node
    Given owner "op-1" has node "n-op-plain" for "qwen3-32b" with no recorded capability
    And owner "op-2" has node "n-other-tools" for "qwen3-32b" that earned verified "tools"
    And a grant from "op-1" allows model "qwen3-32b"
    When a grant-keyed request for "qwen3-32b" carries one function tool
    Then the status is 503
    And the error code is "no_match"
    And "n-other-tools" was never dispatched to

  Scenario: A private band request is gated by capability inside the band only
    Given a private band on frequency "fq-1" whose only station "n-band" serves "qwen3-32b" with no recorded capability
    And public node "n-pub-tools" is on air for "qwen3-32b" and earned verified "tools"
    When a request for "qwen3-32b" with the band code carries one function tool
    Then the status is 503
    And "n-pub-tools" was never dispatched to
    And the error message does not reveal whether the band exists

  Scenario: An anonymous request whose only capable station is paid gets today's 401, not a no_match
    # Contract §1a: the capable station EXISTS, the caller cannot pay it - anonCannotPay
    # (tunnel.go:1951) answers 401 "log in to spend", never 503 no_match.
    Given node "n-free-plain" is on air for "qwen3-32b" at 0/0 with no recorded capability
    And node "n-paid-tools" is on air for "qwen3-32b" at out-price 0.50 and earned verified "tools"
    When an anonymous request for "qwen3-32b" carries one function tool
    Then the status is 401
    And the error message says to log in to spend
    And X-RogerAI-Cost is "0"
    And no hold was placed
    And "n-paid-tools" was never dispatched to
    And "n-free-plain" was never dispatched to

  Scenario: An anonymous request with no capable station at all, free or paid, is a 503 no_match
    Given node "n-free-plain" is on air for "qwen3-32b" at 0/0 with no recorded capability
    And node "n-paid-plain" is on air for "qwen3-32b" at out-price 0.50 with no recorded capability
    When an anonymous request for "qwen3-32b" carries one function tool
    Then the status is 503
    And the error code is "no_match"
    And no station was dispatched to

  Scenario: A free capable station serves an anonymous tools request
    Given node "n-free-tools" is on air for "qwen3-32b" at 0/0 and earned verified "tools"
    When an anonymous request for "qwen3-32b" carries one function tool
    Then the request is served by "n-free-tools"
    And the response carries "X-RogerAI-Cost: 0"

  # --- money and strikes -----------------------------------------------------------------
  Scenario: A capability refusal places no hold, writes no receipt and touches no wallet
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    And a consumer with a funded wallet
    When the consumer's request for "qwen3-32b" carries one function tool
    Then the status is 503
    And the consumer's balance is unchanged
    And no receipt exists for the request

  Scenario: A capability refusal is not a strike and not a probe failure for any station
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When a request for "qwen3-32b" carries one function tool
    Then "n-plain" trust and probe counters are unchanged

  Scenario: A capable station that then answers plain text to a tools request is handled by the existing recount and canary, not by this gate
    Given node "n-cap" is on air for "qwen3-32b" and earned verified "tools"
    When a request for "qwen3-32b" carrying one function tool is served by "n-cap" with a plain-text answer
    Then the relay completes as today
    And the next tool-call canary decides whether "n-cap" keeps verified "tools"

  # --- adversarial -----------------------------------------------------------------------
  Scenario: A station cannot advertise tools on the wire in any casing or with padding
    Given node "n-liar" registers for "qwen3-32b" declaring capabilities ["TOOLS", " tools ", "Tools"]
    When a consumer GETs /discover
    Then the "n-liar" offer lists no "tools" capability
    And a request for "qwen3-32b" carrying one function tool finds "n-liar" NOT a candidate

  Scenario: A station cannot inject a capability through the heartbeat or offer update paths
    Given node "n-liar" is on air for "qwen3-32b" and later re-registers declaring "tools"
    When a request for "qwen3-32b" carries one function tool
    Then "n-liar" is NOT a candidate

  Scenario: A consumer cannot require a capability that grants nothing
    Given node "n-any" is on air for "qwen3-32b" and was seen just now
    When a request for "qwen3-32b" carries roger.require ["*"]
    Then the status is 400
    And the error code is "invalid_routing_value"

  Scenario: A consumer cannot steer traffic to a station by requiring a capability only it declares - vision is declared, so a competitor may declare it too
    # This is the documented asymmetry (FOUNDER FLAG T3): vision is not verified. The gate
    # routes on the declaration; honesty is the operator's, as today on /discover.
    Given node "n-a" and node "n-b" are on air for "qwen3-32b" and both declared "vision"
    When 20 requests for "qwen3-32b" each carrying an image_url part are routed
    Then both stations receive traffic under the normal scoring

  Scenario: A canary pass on model A cannot be replayed to claim model B on the same node
    Given node "n-two" is on air for "qwen3-32b" and "llama-3.3-70b"
    And the tool-call canary for ("n-two", "qwen3-32b") passed with nonce "N1"
    When the station answers the canary for ("n-two", "llama-3.3-70b") with the tool_calls it produced for nonce "N1"
    Then ("n-two", "llama-3.3-70b") does not earn verified "tools"
    And a request for "llama-3.3-70b" carrying one function tool finds "n-two" NOT a candidate

  Scenario: The require array cannot be used to smuggle keys into the station's body
    Given node "n-cap" is on air for "qwen3-32b" and earned verified "tools"
    When a request for "qwen3-32b" carries roger.require ["tools"] and one function tool
    Then the station receives no "roger" key
    And the station receives the tools array byte-for-byte as sent

  # --- telemetry and logs ----------------------------------------------------------------
  Scenario: /admin/live counts capability refusals
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When 3 requests for "qwen3-32b" each carrying one function tool are refused
    Then /admin/live reads relay_no_match_capability 3

  Scenario: A capability refusal logs one line naming the model and the capability, never the prompt
    Given node "n-plain" is the ONLY station on air for "qwen3-32b" and has no recorded capability
    When a request for "qwen3-32b" carrying one function tool and the prompt "SECRET" is refused
    Then exactly one log line names "qwen3-32b" and "tools"
    And no log line contains "SECRET"

  # --- documentation ---------------------------------------------------------------------
  @docs
  Scenario: OpenAPI documents roger.require, provider.require_parameters and the implicit rule
    When the OpenAPI document is read
    Then the chat-completions request schema documents "roger.require" with the closed set ["tools", "vision"]
    And it documents "provider.require_parameters"
    And it states that a non-empty tools array and any image_url part gate routing implicitly
    And the 503 response documents error code "no_match"
