# OPEN-NETWORK FILTERS: the hard filters an open market needs and a closed aggregator does
# not - who built the weights (quant), how big the model is (params), how much window it
# really has (ctx), how fast it is right now (tps, ttft), how far it is trusted (verified,
# TEE), whether it is a person's hardware or a commercial proxy (self-hosted), and where it
# says it is (region). Each one gates a station IN or OUT before scoring, exactly like the
# existing min-tps floor. None of them is a score.
#
# Contract: features/routing/ROUTING-EXPRESSION-CONTRACT.md §5 (filter bullets), §1a
# (validation), §2 (error codes), §6 (bridge parity), §12 ruling 3 (declared-unknown is
# ineligible, measured-unknown passes).
#
# GROUND TRUTH as of origin/main 518c698b:
#   - pickFor (cmd/rogerai-broker/tunnel.go:3165-3355): hard filters are stale / banned /
#     owner-ban / private / pin / exclude / allow / confidential / cooling / probe-dead /
#     min-tps (tps==0 passes, tunnel.go:3251) and per offer model / modality / price caps /
#     the declared-ctx-vs-measured-prompt gate (tunnel.go:3292, estimated never gates).
#   - Quant is DISPLAY + FILTER ONLY on the wire; "the broker routes on none of them"
#     (internal/protocol/protocol.go:60-63). CanonicalQuant upper-cases verbatim labels
#     except the MLX "4bit" family (protocol.go:165-190); it is NOT a closed set.
#     The TUI simulates a quant filter with X-Roger-Exclude-Nodes (internal/tui/quant_route.go:
#     34-130) and does NOT apply it in standalone `roger use` (quant_route.go:65-70).
#   - There is NO parameter-count field anywhere: ModelOffer (protocol.go:35-75) has Ctx +
#     CtxEstimated but no params; /discover (market.go:29-107) carries none.
#   - `verified` on /discover = trustState.verifiedServing() (recount.go:360): a recent PASSED
#     canary that COMPLETED, with no model mismatch. `confidential` = b.confidential[node]
#     from the attestation path (features/trust/confidential_attestation.feature).
#   - `curated` = NodeRegistration.Curated; the pick scores curated and human stations alike
#     (features/curated/curated_routing.feature:26-31). TUI "hide curated" (fNoCurated) only
#     refuses curated-ONLY bands (internal/tui/agent.go:1543-1553) and routeExcludes never adds
#     curated ids (quant_route.go:119-130): a mixed band can still route to curated - the leak
#     features/curated/curated_dial.feature:51-55 promised could not happen.
#   - Region is self-declared on NodeRegistration (protocol.go:355), surfaced, never filtered.
#   - The edge/Tower bridge (edgebridge.go) applies price caps / confidential / pin / freq
#     but NOT min-tps or exclude (edgebridge.go:166-168), and none of the filters below.
#
# After this spec: `provider.quantizations`, `roger.params_b`, `roger.min_ctx`,
# `roger.min_tps`, `roger.max_ttft_ms`, `roger.trust_min`, `roger.self_hosted_only`,
# `roger.confidential`, `roger.region` are hard filters in pickFor and on the bridge. A NEW
# declared offer field `params_b` (billions, float) rides registration; the broker fills
# `params_estimated` from a known-model table when a station omits it.
#
# Enforced by: cmd/rogerai-broker/open_network_filters_bdd_test.go (godog, strict) - to be
# written after approval; RED table tests in router_test.go + registration_test.go.

Feature: Open-network filters - quant, size, window, speed, trust, self-hosted and region as hard gates

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%

  # =====================================================================================
  # quantizations
  # =====================================================================================
  Scenario: A quant filter admits only offers whose canonical label is in the set
    Given node "n-q8" is on air for "qwen3-32b" with quant "Q8_0"
    And node "n-q4" is on air for "qwen3-32b" with quant "Q4_K_M"
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then "n-q8" is a candidate
    And "n-q4" is NOT a candidate

  Scenario Outline: Quant labels match case-insensitively against the canonical verbatim label
    Given node "n-q" is on air for "qwen3-32b" with quant "<offered>"
    When a request for "qwen3-32b" carries provider.quantizations ["<requested>"]
    Then "n-q" <verdict> a candidate

    Examples:
      | offered  | requested | verdict |
      | Q4_K_M   | q4_k_m    | is      |
      | Q4_K_M   | Q4_K_M    | is      |
      | q4_k_m   | Q4_K_M    | is      |
      | Q4_K_M   | IQ4_XS    | is NOT  |
      | IQ4_XS   | Q4_K_M    | is NOT  |
      | Q4_K_M   | Q4        | is NOT  |
      | Q4_K_M   | 4-bit     | is NOT  |
      | 4bit     | 4BIT      | is      |
      | 8bit-DWQ | 8bit-dwq  | is      |
      | BF16     | bf16      | is      |
      | MXFP4_MOE| mxfp4_moe | is      |
      | Q4_K_M   | " Q4_K_M "| is      |

  Scenario: No bucketing - two four-bit labels are different quants
    Given node "n-a" is on air for "qwen3-32b" with quant "Q4_K_M"
    And node "n-b" is on air for "qwen3-32b" with quant "IQ4_XS"
    When a request for "qwen3-32b" carries provider.quantizations ["Q4_K_M"]
    Then "n-a" is a candidate
    And "n-b" is NOT a candidate

  Scenario: A set of several quants admits any of them
    Given node "n-a" is on air for "qwen3-32b" with quant "Q8_0"
    And node "n-b" is on air for "qwen3-32b" with quant "BF16"
    And node "n-c" is on air for "qwen3-32b" with quant "Q4_K_M"
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0", "BF16"]
    Then "n-a" is a candidate
    And "n-b" is a candidate
    And "n-c" is NOT a candidate

  Scenario: An offer with no quant label is ineligible under a quant filter (declared-unknown rule)
    Given node "n-noquant" is on air for "qwen3-32b" with no quant label
    And node "n-q8" is on air for "qwen3-32b" with quant "Q8_0"
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then "n-noquant" is NOT a candidate
    And "n-q8" is a candidate

  Scenario: The value "unknown" admits an unlabeled offer (OpenRouter's list has it too)
    Given node "n-noquant" is on air for "qwen3-32b" with no quant label
    And node "n-q8" is on air for "qwen3-32b" with quant "Q8_0"
    And node "n-q4" is on air for "qwen3-32b" with quant "Q4_K_M"
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0", "unknown"]
    Then "n-noquant" is a candidate
    And "n-q8" is a candidate
    And "n-q4" is NOT a candidate

  Scenario: "unknown" alone admits only unlabeled offers
    Given node "n-noquant" is on air for "qwen3-32b" with no quant label
    And node "n-q8" is on air for "qwen3-32b" with quant "Q8_0"
    When a request for "qwen3-32b" carries provider.quantizations ["unknown"]
    Then "n-noquant" is a candidate
    And "n-q8" is NOT a candidate

  Scenario: "UNKNOWN" is the same value (case-insensitive like every label)
    Given node "n-noquant" is on air for "qwen3-32b" with no quant label
    When a request for "qwen3-32b" carries provider.quantizations ["UNKNOWN"]
    Then "n-noquant" is a candidate

  Scenario: A station cannot label itself "unknown" to match both ways
    When a station registers "qwen3-32b" with quant "unknown"
    Then the registration is rejected as a reserved quant label

  Scenario: Without a quant filter an unlabeled offer is eligible as today
    Given node "n-noquant" is on air for "qwen3-32b" with no quant label
    When a request for "qwen3-32b" carries no routing object
    Then "n-noquant" is a candidate

  Scenario: An unknown quant label in the request is not an error - it simply matches nothing
    # CanonicalQuant is deliberately not a closed set (protocol.go:170-178): a new label the
    # broker has never seen is legitimate. The request is valid; nothing matches.
    Given node "n-q8" is the only station on air for "qwen3-32b" with quant "Q8_0"
    When a request for "qwen3-32b" carries provider.quantizations ["NVFP4"]
    Then the status is 503
    And the error code is "no_match"
    And the error message names "quantizations"

  Scenario Outline: quantizations validation
    Given node "n-q8" is on air for "qwen3-32b" with quant "Q8_0"
    When a request for "qwen3-32b" carries provider.quantizations <value>
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "provider.quantizations"

    Examples:
      | value                 |
      | "Q8_0"                |
      | [""]                  |
      | ["Q8_0", ""]          |
      | [8]                   |
      | [null]                |
      | ["a-label-longer-than-forty-characters-xxxxxxxxxxxxxxx"] |
      | 33 entries of "Q8_0"  |

  Scenario: An empty quantizations array is no filter
    Given node "n-noquant" is on air for "qwen3-32b" with no quant label
    When a request for "qwen3-32b" carries provider.quantizations []
    Then "n-noquant" is a candidate

  Scenario: A control character in a requested label is stripped by the same canonicalizer, then compared
    Given node "n-q8" is on air for "qwen3-32b" with quant "Q8_0"
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0\u0007"]
    Then "n-q8" is a candidate

  @cli
  Scenario: The quant filter replaces the TUI's exclude-list simulation and binds in standalone roger use
    # Contract §9: the standing quants RULE is sent as the labels plus "unknown" (the rule reads
    # an unlabeled row as "not contradicted", Limit.acceptsQuant); a tuned ROW is sent without it.
    Given node "n-q8" is on air for "qwen3-32b" with quant "Q8_0"
    And node "n-q4" is on air for "qwen3-32b" with quant "Q4_K_M"
    And node "n-noquant" is on air for "qwen3-32b" with no quant label
    And the consumer's config sets limits.models.qwen3-32b.quants ["Q8_0"]
    When the consumer runs `roger use qwen3-32b` and sends a request through the local proxy
    Then the request body carries provider.quantizations ["Q8_0", "unknown"]
    And the request body carries no X-Roger-Exclude-Nodes derived from quant
    And "n-q4" is NOT a candidate
    And "n-noquant" is a candidate
    And "n-q8" is a candidate

  Scenario: A station that changes its quant label re-registers as a different offer identity
    # Band identity is (model, quant) (internal/tui/view_band.go:46-51). A relabel is a new
    # band row, not an in-place edit, so a consumer's filter cannot be silently drifted under.
    Given node "n-drift" is on air for "qwen3-32b" with quant "Q8_0"
    When "n-drift" re-registers the same model with quant "Q4_K_M"
    Then the "qwen3-32b"/"Q8_0" band no longer lists "n-drift"
    And a request for "qwen3-32b" carrying provider.quantizations ["Q8_0"] finds "n-drift" NOT a candidate

  Scenario: The quant filter applies per offer, not per node
    Given node "n-mixed" is on air for "qwen3-32b" with quant "Q8_0" and for "llama-3.3-70b" with quant "Q4_K_M"
    When a request for "llama-3.3-70b" carries provider.quantizations ["Q8_0"]
    Then "n-mixed" is NOT a candidate
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then "n-mixed" is a candidate

  # =====================================================================================
  # params_b - a NEW declared offer attribute
  # =====================================================================================
  Scenario: A station declares params_b on its offer and /discover carries it
    Given node "n-32" registers "qwen3-32b" with params_b 32.8
    When a consumer GETs /discover
    Then the "n-32" offer carries params_b 32.8
    And the offer carries params_estimated false

  Scenario Outline: params_b register validation - a bad value rejects the registration
    When a node registers "qwen3-32b" with params_b <value>
    Then the registration is rejected with 400
    And the message names "params_b"

    Examples:
      | value     |
      | 0         |
      | -7        |
      | NaN       |
      | Infinity  |
      | 10001     |
      | "32"      |

  Scenario Outline: params_b register validation - a good value is accepted
    When a node registers "qwen3-32b" with params_b <value>
    Then the registration is accepted

    Examples:
      | value  |
      | 0.5    |
      | 1      |
      | 7      |
      | 32.8   |
      | 671    |
      | 10000  |

  Scenario: Omitting params_b is allowed
    When a node registers "qwen3-32b" with no params_b
    Then the registration is accepted

  Scenario: params_b is excluded from the registration possession proof like the other display fields
    # Capabilities / Quant / Weights / Variant are excluded from regSigningBytes so a node and
    # a broker on different binaries produce byte-identical signed bytes (protocol.go:44-49,
    # 65-70). params_b follows the same rule, or a broker upgrade 401s validly-signed nodes.
    Given a node built before params_b existed signs a registration without the field
    When a broker that knows params_b verifies it
    Then the signature verifies
    Given a node that knows params_b signs a registration carrying it
    When a broker built before params_b verifies it
    Then the signature verifies

  Scenario Outline: When a station omits params_b the broker fills it from the known-model table and marks it estimated
    Given node "n-guess" registers "<model>" with no params_b
    When a consumer GETs /discover
    Then the "n-guess" offer carries params_b <filled>
    And the offer carries params_estimated true

    Examples:
      | model                     | filled |
      | llama-3.3-70b             | 70     |
      | qwen3-32b                 | 32     |
      | qwen3-30b-a3b             | 30     |
      | gpt-oss-120b              | 120    |
      | gpt-oss-20b               | 20     |
      | gemma-3-27b-it            | 27     |
      | deepseek-v3-0324          | 671    |
      | mistral-small-3.1-24b     | 24     |
      | phi-4-mini-3.8b           | 3.8    |
      | qwen2.5-0.5b              | 0.5    |

  Scenario: The known-model table is data, not code - an id it cannot read gets no params_b
    Given node "n-odd" registers "my-custom-finetune" with no params_b
    When a consumer GETs /discover
    Then the "n-odd" offer carries no params_b key
    And the offer carries no params_estimated key

  Scenario: A station's declared params_b wins over the table
    Given node "n-honest" registers "llama-3.3-70b" with params_b 70.6
    When a consumer GETs /discover
    Then the "n-honest" offer carries params_b 70.6
    And the offer carries params_estimated false

  Scenario: A station cannot change params_b on a live offer without re-registering
    Given node "n-live" is on air for "qwen3-32b" with params_b 32.8
    When "n-live" heartbeats carrying params_b 7
    Then the offer still carries params_b 32.8
    When "n-live" re-registers "qwen3-32b" with params_b 7
    Then the offer carries params_b 7

  Scenario: params_b is a DECLARED attribute and the feed says so
    Given node "n-32" registers "qwen3-32b" with params_b 32.8
    When a consumer GETs /discover
    Then the offer carries params_estimated false
    And the OpenAPI document describes params_b as station-declared and params_estimated as broker-filled
    And nothing on the feed claims params_b was measured or verified

  # --- params_b as a filter ---
  Scenario Outline: params_b range boundaries are inclusive on both ends
    Given node "n-p" is on air for "some-model" with params_b <declared>
    When a request for "some-model" carries roger.params_b [<min>, <max>]
    Then "n-p" <verdict> a candidate

    Examples:
      | declared | min | max | verdict |
      | 7        | 7   | 70  | is      |
      | 70       | 7   | 70  | is      |
      | 6.9      | 7   | 70  | is NOT  |
      | 70.1     | 7   | 70  | is NOT  |
      | 32       | 32  | 32  | is      |
      | 32.1     | 32  | 32  | is NOT  |
      | 0.5      | 0.1 | 1   | is      |

  Scenario: An estimated params_b counts for the filter (it is a value, marked estimated)
    Given node "n-guess" registers "llama-3.3-70b" with no params_b
    When a request for "llama-3.3-70b" carries roger.params_b [60, 80]
    Then "n-guess" is a candidate

  Scenario: An offer with no params_b at all is ineligible under a params_b filter
    Given node "n-odd" is on air for "my-custom-finetune" with no params_b and no table entry
    When a request for "my-custom-finetune" carries roger.params_b [1, 1000]
    Then "n-odd" is NOT a candidate
    And the status is 503 with error code "no_match"

  Scenario: Without a params_b filter an offer with no params_b is eligible as today
    Given node "n-odd" is on air for "my-custom-finetune" with no params_b and no table entry
    When a request for "my-custom-finetune" carries no routing object
    Then "n-odd" is a candidate

  Scenario Outline: params_b request validation
    Given node "n-p" is on air for "qwen3-32b" with params_b 32
    When a request for "qwen3-32b" carries roger.params_b <value>
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "roger.params_b"

    Examples:
      | value        |
      | [70, 7]      |
      | [0, 70]      |
      | [-1, 70]     |
      | [7]          |
      | [7, 70, 100] |
      | []           |
      | "7-70"       |
      | ["7", "70"]  |
      | [7, null]    |
      | [7, 1e400]   |
      | 32           |

  Scenario: params_b applies per offer on a multi-model node
    Given node "n-mixed" is on air for "qwen3-32b" with params_b 32 and for "llama-3.3-70b" with params_b 70
    When a request for "llama-3.3-70b" carries roger.params_b [1, 40]
    Then "n-mixed" is NOT a candidate

  Scenario: Two stations for one model id with different declared params - the filter tells them apart
    # Same id, different weights happens on an open network (a pruned or distilled build).
    Given node "n-full" is on air for "qwen3-32b" with params_b 32.8
    And node "n-pruned" is on air for "qwen3-32b" with params_b 18
    When a request for "qwen3-32b" carries roger.params_b [30, 40]
    Then "n-full" is a candidate
    And "n-pruned" is NOT a candidate

  # =====================================================================================
  # min_ctx
  # =====================================================================================
  # corrected 2026-10-02 (founder-approved): the 1-token row became min 4096 / declared 4096; a 1-token window cannot hold any prompt, so the declared-window gate correctly answered 400
  Scenario Outline: min_ctx admits offers whose DECLARED window is at least the floor
    Given node "n-c" is on air for "qwen3-32b" with declared ctx <ctx>
    When a request for "qwen3-32b" carries roger.min_ctx <min>
    Then "n-c" <verdict> a candidate

    Examples:
      | ctx    | min    | verdict |
      | 32768  | 32768  | is      |
      | 32767  | 32768  | is NOT  |
      | 131072 | 32768  | is      |
      | 8192   | 32768  | is NOT  |
      | 4096   | 4096   | is      |

  Scenario: An ESTIMATED ctx is unknown under min_ctx and the offer is ineligible
    # ctx_estimated is the last-resort default, never a detected window (protocol.go:70-75).
    # A consumer who asked for a floor gets a declared answer or none.
    Given node "n-est" is on air for "qwen3-32b" with estimated ctx 131072
    When a request for "qwen3-32b" carries roger.min_ctx 32768
    Then "n-est" is NOT a candidate

  Scenario: Without min_ctx an estimated ctx never gates, as today
    Given node "n-est" is on air for "qwen3-32b" with estimated ctx 32768
    When a request for "qwen3-32b" whose prompt measures 60000 tokens carries no routing object
    Then "n-est" is a candidate

  Scenario: The existing measured-prompt gate is unchanged and independent of min_ctx
    Given node "n-small" is on air for "qwen3-32b" with declared ctx 32768
    When a request for "qwen3-32b" whose prompt measures 40000 tokens carries roger.min_ctx 16384
    Then "n-small" is NOT a candidate
    And the refusal is the existing context-window refusal, not no_match

  Scenario: min_ctx and the measured prompt compose - the larger wall wins
    Given node "n-a" is on air for "qwen3-32b" with declared ctx 32768
    And node "n-b" is on air for "qwen3-32b" with declared ctx 131072
    When a request for "qwen3-32b" whose prompt measures 40000 tokens carries roger.min_ctx 16384
    Then "n-a" is NOT a candidate
    And "n-b" is a candidate

  Scenario: max_tokens still never gates, with or without min_ctx
    Given node "n-c" is on air for "qwen3-32b" with declared ctx 32768
    When a request for "qwen3-32b" with max_tokens 100000 and a 100-token prompt carries roger.min_ctx 32768
    Then "n-c" is a candidate

    # corrected 2026-10-02 (founder-approved): null means absent (§1a), so the null row is dropped
  Scenario Outline: min_ctx validation
    Given node "n-c" is on air for "qwen3-32b" with declared ctx 32768
    When a request for "qwen3-32b" carries roger.min_ctx <value>
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "roger.min_ctx"

    Examples:
      | value   |
      | 0       |
      | -1      |
      | 1.5     |
      | "32k"   |
      | 1e400   |

  Scenario: min_ctx above every declared window is a 503 no_match naming min_ctx
    Given node "n-c" is the only station on air for "qwen3-32b" with declared ctx 32768
    When a request for "qwen3-32b" carries roger.min_ctx 1000000
    Then the status is 503
    And the error code is "no_match"
    And the error message names "min_ctx"

  # =====================================================================================
  # min_tps (body form of X-Roger-Min-TPS)
  # =====================================================================================
  Scenario Outline: min_tps excludes a MEASURED node below the floor and passes an unmeasured one
    Given node "n-t" is on air for "qwen3-32b" with measured tps <tps>
    When a request for "qwen3-32b" carries roger.min_tps <min>
    Then "n-t" <verdict> a candidate

    Examples:
      | tps  | min | verdict |
      | 25   | 20  | is      |
      | 20   | 20  | is      |
      | 19.9 | 20  | is NOT  |
      | 0    | 20  | is      |
      | 5    | 0   | is      |

    # corrected 2026-10-02 (founder-approved): limits compose to the stricter (§1a): the header floor 20 binds
  Scenario: A body min_tps below the X-Roger-Min-TPS header cannot lower the floor
    Given node "n-t" is on air for "qwen3-32b" with measured tps 15
    When a request for "qwen3-32b" carries roger.min_tps 10 and header "X-Roger-Min-TPS: 20"
    Then "n-t" is NOT a candidate

  Scenario: Header min_tps alone still works
    Given node "n-t" is on air for "qwen3-32b" with measured tps 15
    When a request for "qwen3-32b" carries header "X-Roger-Min-TPS: 20" and no body min_tps
    Then "n-t" is NOT a candidate

    # corrected 2026-10-02 (founder-approved): null means absent (§1a), so the null row is dropped
  Scenario Outline: min_tps validation
    Given node "n-t" is on air for "qwen3-32b" with measured tps 15
    When a request for "qwen3-32b" carries roger.min_tps <value>
    Then the status is 400
    And the error code is "invalid_routing_value"

    Examples:
      | value |
      | -1    |
      | "20"  |
      | 1e400 |

  Scenario: min_tps 0 is no floor (matches the header's meaning)
    Given node "n-t" is on air for "qwen3-32b" with measured tps 1
    When a request for "qwen3-32b" carries roger.min_tps 0
    Then "n-t" is a candidate

  # =====================================================================================
  # max_ttft_ms
  # =====================================================================================
  Scenario Outline: max_ttft_ms excludes a MEASURED node above the ceiling and passes an unmeasured one
    Given node "n-l" is on air for "qwen3-32b" with probe-measured ttft <ttft> ms
    When a request for "qwen3-32b" carries roger.max_ttft_ms <max>
    Then "n-l" <verdict> a candidate

    Examples:
      | ttft | max  | verdict |
      | 800  | 1500 | is      |
      | 1500 | 1500 | is      |
      | 1501 | 1500 | is NOT  |
      | 0    | 1500 | is      |
      | 9000 | 100  | is NOT  |

  Scenario: max_ttft_ms has no header equivalent and is body-only
    Given node "n-l" is on air for "qwen3-32b" with probe-measured ttft 3000 ms
    When a request for "qwen3-32b" carries header "X-Roger-Max-TTFT: 100" and no body value
    Then "n-l" is a candidate

    # corrected 2026-10-02 (founder-approved): null means absent (§1a), so the null row is dropped
  Scenario Outline: max_ttft_ms validation
    Given node "n-l" is on air for "qwen3-32b" with probe-measured ttft 800 ms
    When a request for "qwen3-32b" carries roger.max_ttft_ms <value>
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "roger.max_ttft_ms"

    Examples:
      | value  |
      | 0      |
      | -100   |
      | "1.5s" |
      | 1e400  |

  Scenario: max_ttft_ms is a filter, and TTFT still feeds speedFit among the survivors
    Given node "n-fast" is on air for "qwen3-32b" with ttft 300 ms
    And node "n-ok" is on air for "qwen3-32b" with ttft 1200 ms
    And node "n-slow" is on air for "qwen3-32b" with ttft 4000 ms
    When 50 requests for "qwen3-32b" carry roger.max_ttft_ms 1500
    Then "n-slow" is never picked
    And "n-fast" is picked more often than "n-ok"

  Scenario: A stale TTFT measurement still counts (staleness is a score discount, not an unknown)
    Given node "n-l" is on air for "qwen3-32b" whose last ttft measurement of 3000 ms is older than the probe ceiling
    When a request for "qwen3-32b" carries roger.max_ttft_ms 1500
    Then "n-l" is NOT a candidate

  # =====================================================================================
  # trust_min
  # =====================================================================================
  Scenario: trust_min any (the default) gates nothing
    Given node "n-new" is on air for "qwen3-32b", never probed, not attested
    When a request for "qwen3-32b" carries roger.trust_min "any"
    Then "n-new" is a candidate

  Scenario: trust_min verified admits only a node with a recent PASSED and COMPLETED canary
    Given node "n-ver" is on air for "qwen3-32b" with a recent passed canary that completed
    And node "n-new" is on air for "qwen3-32b" and never probed
    And node "n-half" is on air for "qwen3-32b" whose canary passed first token but never completed
    When a request for "qwen3-32b" carries roger.trust_min "verified"
    Then "n-ver" is a candidate
    And "n-new" is NOT a candidate
    And "n-half" is NOT a candidate

  Scenario: A model-mismatch withholds verified for trust_min exactly as it withholds the mark
    Given node "n-alias" is on air for "qwen3-32b" with a passed canary whose response confessed an unrelated model
    When a request for "qwen3-32b" carries roger.trust_min "verified"
    Then "n-alias" is NOT a candidate

    # corrected 2026-10-02 (founder-approved): the verified window is the probe measurement freshness window (probeConfig.measurementStale, the ceiling) already used for scoring
  Scenario: A verified bit that has aged out no longer satisfies trust_min verified
    Given node "n-old" is on air for "qwen3-32b" whose last passed canary is older than the verified window
    When a request for "qwen3-32b" carries roger.trust_min "verified"
    Then "n-old" is NOT a candidate

  Scenario: A recent real relay that completed does NOT count as verification - verified is canary-only
    # verifiedServing (recount.go:356-366) is passed AND completed AND no model mismatch, set by
    # the broker's canary; approved features/trust/verified_serving.feature:68-72. A real relay
    # proves liveness for the concierge pick, never trust for trust_min.
    Given node "n-served" is on air for "qwen3-32b", never canary-verified, and completed a real relay a minute ago
    When a request for "qwen3-32b" carries roger.trust_min "verified"
    Then "n-served" is NOT a candidate
    And /discover shows "n-served" with verified false

  Scenario: trust_min confidential admits only a TEE-attested node
    Given node "n-tee" is on air for "qwen3-32b" with a granted attestation
    And node "n-ver" is on air for "qwen3-32b" verified but not attested
    When a request for "qwen3-32b" carries roger.trust_min "confidential"
    Then "n-tee" is a candidate
    And "n-ver" is NOT a candidate

  Scenario: trust_min confidential is exactly roger.confidential true and the X-Roger-Confidential header
    Given node "n-tee" is on air for "qwen3-32b" with a granted attestation
    And node "n-plain" is on air for "qwen3-32b" and not attested
    When a request for "qwen3-32b" carries roger.trust_min "confidential"
    Then the candidate set is exactly {"n-tee"}
    When a request for "qwen3-32b" carries roger.confidential true
    Then the candidate set is exactly {"n-tee"}
    When a request for "qwen3-32b" carries header "X-Roger-Confidential: 1"
    Then the candidate set is exactly {"n-tee"}

  Scenario: An attested node that is not verified still satisfies confidential but not verified
    Given node "n-tee-new" is on air for "qwen3-32b" with a granted attestation and no passed canary
    When a request for "qwen3-32b" carries roger.trust_min "confidential"
    Then "n-tee-new" is a candidate
    When a request for "qwen3-32b" carries roger.trust_min "verified"
    Then "n-tee-new" is NOT a candidate

  Scenario Outline: When trust_min and confidential disagree the stricter wins without an error
    Given node "n-tee" is on air for "qwen3-32b" with a granted attestation
    And node "n-ver" is on air for "qwen3-32b" verified but not attested
    When a request for "qwen3-32b" carries roger.trust_min "<trust>" and roger.confidential <conf>
    Then the candidate set is exactly <set>

    Examples:
      | trust        | conf  | set               |
      | any          | true  | {"n-tee"}         |
      | verified     | true  | {"n-tee"}         |
      | confidential | false | {"n-tee"}         |
      | verified     | false | {"n-ver","n-tee"} |

    # The last row assumes "n-tee" is also verified; a step sets it so. An attested node that
    # is not verified would be excluded there.

  Scenario: Body confidential false does not cancel the X-Roger-Confidential header
    # Header ≡ "TEE only"; a body false is the DEFAULT, not a request to weaken. Stricter wins.
    Given node "n-tee" is on air for "qwen3-32b" with a granted attestation
    And node "n-plain" is on air for "qwen3-32b" and not attested
    When a request for "qwen3-32b" carries roger.confidential false and header "X-Roger-Confidential: 1"
    Then the candidate set is exactly {"n-tee"}

  Scenario Outline: trust_min validation
    Given node "n-any" is on air for "qwen3-32b"
    When a request for "qwen3-32b" carries roger.trust_min <value>
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "roger.trust_min"

    Examples:
      | value      |
      | "high"     |
      | "tee"      |
      | ""         |
      | 1          |
      | true       |
      | ["any"]    |

  Scenario: trust_min values are exact lowercase - a cased spelling is a 400, never folded
    # Contract §1a: enumerated values are exact lowercase; only quant labels compare
    # case-insensitively.
    Given node "n-ver" is on air for "qwen3-32b" with a recent passed canary that completed
    And node "n-new" is on air for "qwen3-32b" and never probed
    When a request for "qwen3-32b" carries roger.trust_min "Verified"
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "roger.trust_min"
    And no station was dispatched to

  # superseded 2026-10-05 by contract §14 (founder-approved): an organic success rate below the
  # Tier-A bar withdraws verified (§14.B7), so a station in Tier B on a low success rate is no
  # longer verified; was 'And "n-ver-b" is a candidate and serves from Tier B because Tier A is empty'.
  Scenario: trust_min verified admits neither an unprobed station nor one whose low organic success withdrew verified
    Given node "n-ver-b" is on air for "qwen3-32b", verified, but in Tier B on a low success rate
    And node "n-new-a" is on air for "qwen3-32b", never probed, in Tier A
    When a request for "qwen3-32b" carries roger.trust_min "verified"
    Then "n-new-a" is NOT a candidate
    And "n-ver-b" is NOT a candidate

  Scenario: No verified station is a 503 no_match naming trust_min
    Given node "n-new" is the only station on air for "qwen3-32b" and never probed
    When a request for "qwen3-32b" carries roger.trust_min "verified"
    Then the status is 503
    And the error code is "no_match"
    And the error message names "trust_min"

  Scenario: No confidential station keeps today's message wording, with the code added
    Given node "n-plain" is the only station on air for "qwen3-32b" and not attested
    When a request for "qwen3-32b" carries roger.confidential true
    Then the status is 503
    And the error code is "no_match"
    And the error message reads "no node offers qwen3-32b on a confidential node"

  # =====================================================================================
  # self_hosted_only
  # =====================================================================================
  Scenario: self_hosted_only excludes curated stations
    Given node "n-human" is on air for "gpt-oss-120b" as a person's hardware
    And node "n-curated" is on air for "gpt-oss-120b" as a curated station proxying "groq"
    When a request for "gpt-oss-120b" carries roger.self_hosted_only true
    Then "n-human" is a candidate
    And "n-curated" is NOT a candidate

  Scenario: The regression pin - a mixed band never routes to curated when the consumer hid it
    Given node "n-human" is on air for "gpt-oss-120b" as a person's hardware, slower and pricier
    And node "n-curated" is on air for "gpt-oss-120b" as a curated station, faster and cheaper
    When 50 requests for "gpt-oss-120b" carry roger.self_hosted_only true
    Then every one of them is served by "n-human"

  @tui
  Scenario: The TUI hide-curated toggle sends self_hosted_only, not an exclude list
    Given the operator hid curated supply in the TUI
    When the TUI sends a request on a mixed band
    Then the request body carries roger.self_hosted_only true
    And the request carries no X-Roger-Exclude-Nodes derived from curated ids

  Scenario: A curated-only band with self_hosted_only is a 503 no_match naming the hidden option
    Given node "n-curated" is the only station on air for "gpt-oss-120b" and is curated
    When a request for "gpt-oss-120b" carries roger.self_hosted_only true
    Then the status is 503
    And the error code is "no_match"
    And the error message names "self_hosted_only" and says a curated station was available

  Scenario: self_hosted_only false and omitted both admit curated as today
    Given node "n-curated" is on air for "gpt-oss-120b" as a curated station
    When a request for "gpt-oss-120b" carries roger.self_hosted_only false
    Then "n-curated" is a candidate
    When a request for "gpt-oss-120b" carries no routing object
    Then "n-curated" is a candidate

    # corrected 2026-10-02 (founder-approved): null means absent (§1a), so the null row is dropped
  Scenario Outline: self_hosted_only must be a boolean
    Given node "n-human" is on air for "gpt-oss-120b"
    When a request for "gpt-oss-120b" carries roger.self_hosted_only <value>
    Then the status is 400
    And the error code is "invalid_routing_value"

    Examples:
      | value  |
      | "true" |
      | 1      |

  Scenario: self_hosted_only composes with the curated economics - a curated station earns nothing from a request it was filtered out of
    Given node "n-human" and curated node "n-curated" are on air for "gpt-oss-120b"
    When a request for "gpt-oss-120b" carrying roger.self_hosted_only true is served by "n-human"
    Then "n-curated" has no receipt for the request
    And the routing fee is the human-band fee, not the curated split

  Scenario: self_hosted_only on the bridge - a curated Tower relay is ineligible
    Given a Tower relay hosts "gpt-oss-120b" and is flagged curated
    And node "n-human" is on air for "gpt-oss-120b"
    When 20 requests for "gpt-oss-120b" carry roger.self_hosted_only true
    Then every one of them is served by "n-human" directly

  # =====================================================================================
  # region
  # =====================================================================================
  Scenario: region admits nodes whose self-declared region is in the set
    Given node "n-eu" is on air for "qwen3-32b" declaring region "eu"
    And node "n-us" is on air for "qwen3-32b" declaring region "us"
    When a request for "qwen3-32b" carries roger.region ["eu"]
    Then "n-eu" is a candidate
    And "n-us" is NOT a candidate

  Scenario: A set of several regions admits any of them
    Given node "n-eu" is on air for "qwen3-32b" declaring region "eu"
    And node "n-us" is on air for "qwen3-32b" declaring region "us"
    And node "n-ap" is on air for "qwen3-32b" declaring region "ap-south"
    When a request for "qwen3-32b" carries roger.region ["eu", "us"]
    Then "n-eu" is a candidate
    And "n-us" is a candidate
    And "n-ap" is NOT a candidate

  Scenario: A node with no declared region is ineligible under a region filter
    Given node "n-none" is on air for "qwen3-32b" declaring no region
    When a request for "qwen3-32b" carries roger.region ["eu"]
    Then "n-none" is NOT a candidate

  Scenario: Without a region filter a node with no region is eligible as today
    Given node "n-none" is on air for "qwen3-32b" declaring no region
    When a request for "qwen3-32b" carries no routing object
    Then "n-none" is a candidate

  Scenario: Region comparison is exact on lowercase tokens after trimming
    Given node "n-eu" is on air for "qwen3-32b" declaring region "EU "
    When a request for "qwen3-32b" carries roger.region ["eu"]
    Then "n-eu" is a candidate

  Scenario: A region value nobody declares is valid and matches nothing
    Given node "n-eu" is the only station on air for "qwen3-32b" declaring region "eu"
    When a request for "qwen3-32b" carries roger.region ["mars"]
    Then the status is 503
    And the error code is "no_match"
    And the error message names "region"

  Scenario Outline: region validation - tokens are 2-8 lowercase letters, digits or hyphens
    Given node "n-eu" is on air for "qwen3-32b" declaring region "eu"
    When a request for "qwen3-32b" carries roger.region <value>
    Then the status is 400
    And the error code is "invalid_routing_value"
    And the error message names "roger.region"

    Examples:
      | value                 |
      | "eu"                  |
      | [""]                  |
      | ["e"]                 |
      | ["a-region-too-long"] |
      | ["eu west"]           |
      | ["eu/west"]           |
      | [1]                   |
      | 33 entries of "eu"    |

  Scenario: Region is self-declared and the spec claims no verification of it
    # The broker does not geolocate. A filter on region is a filter on what the operator
    # said. /discover shows the same string; nothing anywhere marks it verified.
    Given node "n-claims" is on air for "qwen3-32b" declaring region "eu"
    When a consumer GETs /discover
    Then the "n-claims" offer carries region "eu" and no verification marker
    And a request for "qwen3-32b" carrying roger.region ["eu"] finds "n-claims" a candidate

  Scenario: Region filter on the bridge - a Tower declaring no region is ineligible under it
    Given a Tower relay hosts "qwen3-32b" and declares no region
    And node "n-eu" is on air for "qwen3-32b" declaring region "eu"
    When 20 requests for "qwen3-32b" carry roger.region ["eu"]
    Then every one of them is served by "n-eu" directly

  # =====================================================================================
  # composition, models[], only/order, the bridge, no_match
  # =====================================================================================
  Scenario: Every filter is an AND - a station must pass all of them
    Given node "n-all" is on air for "qwen3-32b" with quant "Q8_0", params_b 32.8, declared ctx 131072, tps 40, ttft 400 ms, verified, region "eu", self-hosted
    And node "n-almost" is identical to "n-all" except its quant is "Q4_K_M"
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0"], roger.params_b [30, 40], roger.min_ctx 65536, roger.min_tps 20, roger.max_ttft_ms 1000, roger.trust_min "verified", roger.region ["eu"], roger.self_hosted_only true
    Then "n-all" is a candidate
    And "n-almost" is NOT a candidate

  Scenario: The filters run before scoring - a filtered-out station never receives exploration lift or P2C draw
    Given node "n-out" is on air for "qwen3-32b" with quant "Q4_K_M", canary-passed, never traffic
    And node "n-in" is on air for "qwen3-32b" with quant "Q8_0"
    When 50 requests for "qwen3-32b" carry provider.quantizations ["Q8_0"]
    Then "n-out" is never picked

  Scenario: A filter that empties one model's stations moves the plan to the next model in models[]
    Given node "n-a" is the only station for "qwen3-32b" with quant "Q4_K_M"
    And node "n-b" is on air for "llama-3.3-70b" with quant "Q8_0"
    When a request with models ["qwen3-32b", "llama-3.3-70b"] carries provider.quantizations ["Q8_0"]
    Then the request is served by "n-b"
    And the response carries "X-RogerAI-Model: llama-3.3-70b"

  Scenario: Filters apply per (model, station) inside the fallback plan, so the hold is sized only on survivors
    Given node "n-cheap-out" is on air for "qwen3-32b" at out-price 0.10 with quant "Q4_K_M"
    And node "n-in" is on air for "qwen3-32b" at out-price 0.40 with quant "Q8_0"
    When a request for "qwen3-32b" carrying provider.quantizations ["Q8_0"] is served
    Then the hold was sized on "n-in" only

  Scenario: An only-listed station that fails a filter is skipped, not an error
    Given node "n-1" is on air for "qwen3-32b" with quant "Q4_K_M"
    And node "n-2" is on air for "qwen3-32b" with quant "Q8_0"
    When a request for "qwen3-32b" carries provider.only ["n-1", "n-2"] and provider.quantizations ["Q8_0"]
    Then the request is served by "n-2"

  Scenario: An ordered station that fails a filter is skipped in favor of the next in order
    Given node "n-first" is on air for "qwen3-32b" declaring region "us"
    And node "n-second" is on air for "qwen3-32b" declaring region "eu"
    When a request for "qwen3-32b" carries provider.order ["n-first", "n-second"] and roger.region ["eu"]
    Then the request is served by "n-second"

  Scenario: A pinned station that fails a filter is a 503 no_match - a pin never overrides a filter
    Given node "n-pin" is on air for "qwen3-32b" with tps 5
    When a request for "qwen3-32b" carries header "X-Roger-Node: n-pin" and roger.min_tps 20
    Then the status is 503
    And the error code is "no_match"

  Scenario: The bridge coin cannot pick a Tower that fails a filter the direct station passes
    Given node "n-eu" is on air for "qwen3-32b" declaring region "eu" with quant "Q8_0"
    And a Tower relay hosts "qwen3-32b" with quant "Q4_K_M"
    When 20 requests for "qwen3-32b" carry provider.quantizations ["Q8_0"]
    Then every one of them is served by "n-eu" directly

  Scenario: A Tower that passes a filter the direct station fails serves through the bridge
    Given node "n-q4" is on air for "qwen3-32b" with quant "Q4_K_M"
    And a Tower relay hosts "qwen3-32b" with quant "Q8_0"
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then the request is served through the bridge
    And the response carries "X-RogerAI-Relay" naming the Tower

  Scenario: A filter that no station and no Tower passes is one 503 no_match, no hold, no dispatch
    Given node "n-q4" is on air for "qwen3-32b" with quant "Q4_K_M"
    And a Tower relay hosts "qwen3-32b" with quant "Q4_K_M"
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then the status is 503
    And the error code is "no_match"
    And no hold was placed and no station or Tower was dispatched to

  Scenario: no_match lists the filters that emptied the pool, in a fixed order, never the prompt
    Given node "n-only" is the only station on air for "qwen3-32b" with quant "Q4_K_M", region "us", tps 5
    When a request for "qwen3-32b" carrying provider.quantizations ["Q8_0"], roger.region ["eu"], roger.min_tps 20 and the prompt "SECRET" is refused
    Then the status is 503
    And the error code is "no_match"
    And the error message names "quantizations", "region" and "min_tps"
    And the response and the log contain no "SECRET"

  Scenario: Cooling is evaluated after the filters - a filtered set that is all cooling is band_cooling with Retry-After
    Given node "n-q8" is on air for "qwen3-32b" with quant "Q8_0" and is cooling for 30 s
    And node "n-q4" is on air for "qwen3-32b" with quant "Q4_K_M" and not cooling
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then the status is 503
    And the error code is "band_cooling"
    And the Retry-After is about 30 s

  Scenario: A filter refusal is neither a strike nor a probe failure for any station
    Given node "n-q4" is the only station on air for "qwen3-32b" with quant "Q4_K_M"
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then "n-q4" trust and probe counters are unchanged

  Scenario: A filter refusal touches no wallet and writes no receipt
    Given node "n-q4" is the only station on air for "qwen3-32b" with quant "Q4_K_M"
    And a consumer with a funded wallet
    When the consumer's request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then the consumer's balance is unchanged
    And no receipt exists for the request

  Scenario: Filters narrow a grant but never widen it
    Given owner "op-1" has node "n-op" for "qwen3-32b" declaring region "us"
    And owner "op-2" has node "n-other" for "qwen3-32b" declaring region "eu"
    And a grant from "op-1" allows model "qwen3-32b"
    When a grant-keyed request for "qwen3-32b" carries roger.region ["eu"]
    Then the status is 503
    And "n-other" was never dispatched to

  Scenario: Filters narrow a private band but never leak past it
    Given a private band on frequency "fq-1" whose only station "n-band" serves "qwen3-32b" with quant "Q4_K_M"
    And public node "n-pub" is on air for "qwen3-32b" with quant "Q8_0"
    When a request for "qwen3-32b" with the band code carries provider.quantizations ["Q8_0"]
    Then the status is 503
    And "n-pub" was never dispatched to
    And the error message does not reveal whether the band exists

  Scenario: An anonymous request whose only matching station is paid gets today's 401, not a no_match
    # Contract §1a: the matching station EXISTS, the caller cannot pay it - anonCannotPay
    # (tunnel.go:1951) answers 401 "log in to spend", never 503 no_match.
    Given node "n-free-q4" is on air for "qwen3-32b" at 0/0 with quant "Q4_K_M"
    And node "n-paid-q8" is on air for "qwen3-32b" at out-price 0.50 with quant "Q8_0"
    When an anonymous request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then the status is 401
    And the error message says to log in to spend
    And X-RogerAI-Cost is "0"
    And no hold was placed
    And "n-paid-q8" was never dispatched to

  Scenario: An anonymous request with no matching station, free or paid, is a 503 no_match
    Given node "n-free-q4" is on air for "qwen3-32b" at 0/0 with quant "Q4_K_M"
    And node "n-paid-q4" is on air for "qwen3-32b" at out-price 0.50 with quant "Q4_K_M"
    When an anonymous request for "qwen3-32b" carries provider.quantizations ["Q8_0"]
    Then the status is 503
    And the error code is "no_match"
    And no station was dispatched to

  Scenario: The filters are stripped before the station sees the body
    Given node "n-all" is on air for "qwen3-32b" passing every filter below
    When a request for "qwen3-32b" carries provider.quantizations ["Q8_0"], roger.params_b [1, 100], roger.region ["eu"]
    Then the station receives no "provider" key and no "roger" key
    And the station receives the messages byte-for-byte as sent

  # =====================================================================================
  # adversarial
  # =====================================================================================
  Scenario: A station cannot gain eligibility a competitor lacks by declaring params_b - both are declared and both are marked
    Given node "n-a" registers "qwen3-32b" with params_b 32.8
    And node "n-b" registers "qwen3-32b" with params_b 32.8
    When a consumer GETs /discover
    Then both offers carry params_estimated false
    And a request carrying roger.params_b [30, 40] finds both candidates under the normal scoring

  Scenario: A station cannot declare a params_b that makes it match every range
    When a node registers "qwen3-32b" with params_b 1e300
    Then the registration is rejected with 400

  Scenario: A station cannot declare a region string that matches several region tokens
    Given node "n-tricky" registers "qwen3-32b" declaring region "eu,us"
    When a request for "qwen3-32b" carries roger.region ["eu"]
    Then "n-tricky" is NOT a candidate
    When a request for "qwen3-32b" carries roger.region ["us"]
    Then "n-tricky" is NOT a candidate

  Scenario: A station cannot mark itself verified or confidential through its registration
    Given node "n-liar" registers "qwen3-32b" with a body claiming verified true and confidential true
    When a request for "qwen3-32b" carries roger.trust_min "verified"
    Then "n-liar" is NOT a candidate
    When a request for "qwen3-32b" carries roger.trust_min "confidential"
    Then "n-liar" is NOT a candidate

    # corrected 2026-10-02 (founder-approved): today's register refuses a kind flip on the same id (409, tunnel.go register); a fresh id starts with zero reputation
  Scenario: A curated station cannot re-register its id as self-hosted; a fresh id starts with zero reputation
    # curated is a self-declared, signature-covered registration flag. Flipping it on the same
    # node id is refused in either direction (approved features/curated/curated_identity.feature),
    # so a proxy cannot carry its reputation into the self-hosted lane. A station registered under
    # a fresh id is eligible under self_hosted_only once proven, with no history behind it.
    Given node "n-curated" registered as curated for "gpt-oss-120b" with verified serving and 500 receipts in its chain
    When the same callsign re-registers with curated false
    Then the re-registration is refused 409 naming "a different kind of station"
    When a self-hosted station registers under a fresh id for "gpt-oss-120b"
    Then the new identity starts with fresh trust state: not verified, never probed, Tier-B until proven
    And the old identity's receipts, lineage and success history do not follow it
    And a request for "gpt-oss-120b" carrying roger.self_hosted_only true may find the new identity a candidate once it is proven live
    And a request for "gpt-oss-120b" carrying roger.trust_min "verified" finds the new identity NOT a candidate

  Scenario: The symmetric identity rule leaves the curated honesty rules and probes as the real check
    # Stated honestly: a fresh identity that lies about being self-hosted is caught by the
    # curated program's honesty rules and the probes, not by this filter.
    Given node "n-fresh" registered without the curated flag and proxies a commercial API
    When a request for "gpt-oss-120b" carries roger.self_hosted_only true
    Then "n-fresh" is a candidate under the filter
    And the curated honesty rules apply to "n-fresh" as to any station

  Scenario: A consumer cannot use a filter to enumerate private nodes
    Given a private band on frequency "fq-1" whose station "n-band" serves "qwen3-32b" with quant "RARE_Q"
    When a request for "qwen3-32b" without the band code carries provider.quantizations ["RARE_Q"]
    Then the status is 503
    And the error message is the same as for a quant nobody serves

  Scenario: A station that reports an absurd tps to pass min_tps is bounded by the broker's own measurement
    # tps is BROKER-measured (b.tps), never taken from the station.
    Given node "n-boast" is on air for "qwen3-32b" and its heartbeat claims tps 9999 while the broker measured 5
    When a request for "qwen3-32b" carries roger.min_tps 20
    Then "n-boast" is NOT a candidate

  # =====================================================================================
  # telemetry and documentation
  # =====================================================================================
  Scenario: /admin/live counts no_match refusals by the filter that emptied the pool
    Given node "n-q4" is the only station on air for "qwen3-32b" with quant "Q4_K_M"
    When 3 requests for "qwen3-32b" carry provider.quantizations ["Q8_0"] and are refused
    Then /admin/live reads relay_no_match_quantizations 3

  @docs
  Scenario: OpenAPI documents every filter with its unknown-attribute rule
    When the OpenAPI document is read
    Then it documents provider.quantizations, roger.params_b, roger.min_ctx, roger.min_tps, roger.max_ttft_ms, roger.trust_min, roger.self_hosted_only, roger.confidential and roger.region
    And for each declared attribute it states that an offer without the attribute is ineligible under the filter
    And for min_tps and max_ttft_ms it states that an unmeasured node passes
    And the /discover offer schema documents params_b and params_estimated
