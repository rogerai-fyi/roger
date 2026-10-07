# ROUTING EXPRESSION - the `roger use` flag surface (ROUTING-EXPRESSION-CONTRACT.md §10).
#
# PURPOSE: every routing knob the contract defines is reachable from the standalone CLI, each
# flag maps to exactly one body key, invalid input fails fast with one line naming the flag,
# and the resolution order (flag > profile > limits.models.<m> > limits.default > built-in)
# is the same on every path.
#
# GROUND TRUTH at origin/main 518c698b:
#   - The consumer command set is `use` (aliases `connect`, `tune`) - cmd/rogerai/main.go:890.
#     There is NO `roger agent` and NO `roger ask` command (main.go:844-958 is the whole
#     dispatch); the agent runs inside the TUI. This file therefore specs `roger use` only.
#   - cmdUse flags (main.go:987-1005): --max-out (default -1 sentinel, help says "0 = no cap"),
#     --advanced, --port, --confidential, --max-in, --min-tps, --yes, --freq, --raw. The model is
#     the first positional; Go's flag package stops at it (main.go:983-985).
#   - Resolution today (main.go:1011-1021): cfg.resolve(model) picks limits.models[m] else
#     limits.default (main.go:214-223); a flag >= 0 overrides the stored field.
#   - The options reach the proxy as client.UseOptions{Confidential, MaxIn, MaxOut, MinTPS,
#     Freq, Raw, Yes, Port} (main.go:1029-1033); the proxy injects HEADERS only
#     (internal/client/client.go:836-871). Pref and ExcludeNodes exist on ProxyOptions
#     (client.go:412-424) but no CLI flag sets them.
#   - "0 = no cap" is FALSE today: effectiveMaxOut(0) returns ConsumerDefaultMaxOut ($10/1M)
#     (client.go:1436-1441) and the broker applies the same default server-side
#     (cmd/rogerai-broker/pricesafety.go:31-45). Pinned as defect 2 in
#     features/routing/regression_pins.feature; the CLI half is here.
#   - `roger config set-limit <model|default> [--max-in] [--max-out] [--min-tps]`
#     (main.go:2262-2290) persists Limit{MaxIn, MaxOut, MinTPS, Quants} (main.go:66-72); Quants
#     has no set-limit flag today (only the TUI writes it).
#   - features/cli/release_surface.feature pins the help heading, the "roger " version prefix
#     and the concise unknown-command line; nothing here changes those.
#
# After this spec the CLI builds the CONTRACT §1 body object (models / provider / roger) and
# hands it to the proxy, which sends it in the body (features/proxy/routing_passthrough.feature)
# and keeps the header form for old brokers.
#
# Enforced by: cmd/rogerai/routing_flags_bdd_test.go (future; drives cmdUse's flag parsing and
# the resolved client.UseOptions / body object through the real flag set, no mocks).

Feature: roger use exposes every routing knob as a flag that maps to one body key

  Background:
    Given a config with no limits and no profiles
    And the broker at "http://127.0.0.1:0" accepts the routing body object

  # --- flag inventory --------------------------------------------------------------

  Scenario Outline: Each routing flag maps to exactly one body key
    When the user runs "roger use qwen3-32b <flag> <value> --yes"
    Then the tune-time body carries <key> = <body>
    And no other routing key is present beyond the always-present default out-cap (provider.max_price.completion = 10)

    Examples:
      | flag            | value            | key                            | body                             |
      | --models        | qwen3-30b,llama3 | models                         | ["qwen3-30b","llama3"]           |
      | --only          | n1,n2            | provider.only                  | ["n1","n2"]                      |
      | --order         | n1,n2            | provider.order                 | ["n1","n2"]                      |
      | --exclude       | n9               | provider.ignore                | ["n9"]                           |
      | --no-fallbacks  |                  | provider.allow_fallbacks       | false                            |
      | --sort          | price            | provider.sort                  | "price"                          |
      | --sort          | throughput       | provider.sort                  | "throughput"                     |
      | --sort          | latency          | provider.sort                  | "latency"                        |
      | --pref          | cheap            | roger.pref                     | "cheap"                          |
      | --pref          | reliable         | roger.pref                     | "reliable"                       |
      | --quant         | Q8_0,BF16        | provider.quantizations         | ["Q8_0","BF16"]                  |
      | --require       | tools,vision     | roger.require                  | ["tools","vision"]               |
      | --params        | 7-70             | roger.params_b                 | [7,70]                           |
      | --min-ctx       | 32k              | roger.min_ctx                  | 32768                            |
      | --max-ttft      | 1500ms           | roger.max_ttft_ms              | 1500                             |
      | --trust         | verified         | roger.trust_min                | "verified"                       |
      | --trust         | confidential     | roger.trust_min                | "confidential"                   |
      | --self-hosted   |                  | roger.self_hosted_only         | true                             |
      | --region        | eu,us            | roger.region                   | ["eu","us"]                      |
      | --max-cost      | 0.02             | provider.max_price.request     | 0.02                             |
      | --max-in        | 0.20             | provider.max_price.prompt      | 0.2                              |
      | --max-out       | 0.60             | provider.max_price.completion  | 0.6                              |
      | --min-tps       | 20               | roger.min_tps                  | 20                               |
      | --confidential  |                  | roger.confidential             | true                             |

  # corrected 2026-10-04 (founder-approved): the band code travels only as the X-Roger-Freq header, never in the body; the value is quoted so it is one argument
  Scenario: --freq sends the band code as the X-Roger-Freq header only
    Given a private band with code "147.520 MHz 8F3K" resolves for "qwen3-32b"
    When the user runs "roger use qwen3-32b --freq '147.520 MHz 8F3K' --yes"
    Then the tune-time request carries the X-Roger-Freq header "147.520 MHz 8F3K"
    And the tune-time body carries no roger.freq key

  Scenario: --node is the pin shorthand
    When the user runs "roger use qwen3-32b --node n1 --yes"
    Then the tune-time body carries provider.order = ["n1"]
    And the tune-time body carries provider.allow_fallbacks = false
    And no X-Roger-Node header is set when the broker accepts the body object

  Scenario: --node with more than one id is refused (a pin names one station)
    When the user runs "roger use qwen3-32b --node n1,n2 --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--node"
    And no request reaches the broker

  Scenario: --node together with --order is refused as contradictory
    When the user runs "roger use qwen3-32b --node n1 --order n2,n3 --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--node" and "--order"

  Scenario: --node together with --only is allowed when the pin is inside the list
    When the user runs "roger use qwen3-32b --node n1 --only n1,n2 --yes"
    Then the tune-time body carries provider.only = ["n1","n2"]
    And the tune-time body carries provider.order = ["n1"]
    And the tune-time body carries provider.allow_fallbacks = false

  Scenario: --node outside --only is refused before any request
    When the user runs "roger use qwen3-32b --node n9 --only n1,n2 --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--node" and "--only"

  Scenario: --sort and --pref together are refused locally, mirroring the broker's 400
    When the user runs "roger use qwen3-32b --sort price --pref fast --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--sort" and "--pref"
    And no request reaches the broker

  Scenario: The model positional still comes first and flags follow it
    When the user runs "roger use --pref fast qwen3-32b --yes"
    Then the exit code is non-zero
    And stderr names the usage "roger use <model> [flags]"

  Scenario: The existing flags keep their meaning and position
    When the user runs "roger use qwen3-32b --port 4242 --raw --yes"
    Then the local endpoint binds port 4242
    And the reasoning fallback is off for the session
    And no routing key is present in the body beyond the always-present default out-cap

  # --- units ------------------------------------------------------------------------

  Scenario Outline: --min-ctx accepts tokens with an optional k suffix
    When the user runs "roger use qwen3-32b --min-ctx <value> --yes"
    Then the tune-time body carries roger.min_ctx = <ctx>

    Examples:
      | value  | ctx    |
      | 32k    | 32768  |
      | 32K    | 32768  |
      | 8192   | 8192   |
      | 128k   | 131072 |
      | 1k     | 1024   |

  Scenario Outline: --max-ttft accepts a duration or bare milliseconds
    When the user runs "roger use qwen3-32b --max-ttft <value> --yes"
    Then the tune-time body carries roger.max_ttft_ms = <ms>

    Examples:
      | value  | ms   |
      | 1500ms | 1500 |
      | 1.5s   | 1500 |
      | 2s     | 2000 |
      | 800    | 800  |

  Scenario Outline: --params accepts an inclusive range in billions
    When the user runs "roger use qwen3-32b --params <value> --yes"
    Then the tune-time body carries roger.params_b = <range>

    Examples:
      | value   | range      |
      | 7-70    | [7,70]     |
      | 7b-70b  | [7,70]     |
      | 0.5-3   | [0.5,3]    |
      | 30      | [30,30]    |
      | 30-     | [30,10000] |
      | -8      | [0,8]      |

  Scenario Outline: Prices are dollars per million tokens, cost is dollars per request
    When the user runs "roger use qwen3-32b <flag> <value> --yes"
    Then the tune-time body carries <key> = <num>

    Examples:
      | flag       | value | key                           | num  |
      | --max-in   | 1     | provider.max_price.prompt     | 1    |
      | --max-out  | 2.5   | provider.max_price.completion | 2.5  |
      | --max-cost | .05   | provider.max_price.request    | 0.05 |

  Scenario: --max-cost 0 means "no per-request cap", unlike --max-out 0
    When the user runs "roger use qwen3-32b --max-cost 0 --yes"
    Then the tune-time body carries no provider.max_price.request key
    And the help text for --max-cost says "0 = no per-request cap"

  Scenario: List flags trim whitespace and drop empty items
    When the user runs "roger use qwen3-32b --only ' n1 , n2 ,' --yes"
    Then the tune-time body carries provider.only = ["n1","n2"]

  Scenario: List flags may be repeated and accumulate
    When the user runs "roger use qwen3-32b --exclude n1 --exclude n2 --yes"
    Then the tune-time body carries provider.ignore = ["n1","n2"]

  Scenario: List flags de-duplicate preserving first occurrence
    When the user runs "roger use qwen3-32b --order n2,n1,n2 --yes"
    Then the tune-time body carries provider.order = ["n2","n1"]

  # --- invalid input fails fast, one line, naming the flag --------------------------

  Scenario Outline: An invalid value exits non-zero with one line naming the flag
    When the user runs "roger use qwen3-32b <flag> <value> --yes"
    Then the exit code is non-zero
    And stderr is exactly one line
    And that line names "<flag>"
    And no request reaches the broker

    Examples:
      | flag          | value        |
      | --models      | ""           |
      | --models      | a,b,c,d,e,f  |
      | --only        | ""           |
      | --order       | ","          |
      | --sort        | cheapest     |
      | --sort        | ""           |
      | --pref        | fastest      |
      | --pref        | BALANCED!    |
      | --quant       | ""           |
      | --require     | json         |
      | --require     | TOOLS,speech |
      | --params      | 70-7         |
      | --params      | -            |
      | --params      | 0-0          |
      | --params      | 7-nan        |
      | --params      | seven        |
      | --min-ctx     | 0            |
      | --min-ctx     | -1           |
      | --min-ctx     | 32kb         |
      | --max-ttft    | 0            |
      | --max-ttft    | -5ms         |
      | --max-ttft    | soon         |
      | --trust       | high         |
      | --trust       | ""           |
      | --region      | EU           |
      | --region      | europe-west1 |
      | --region      | e            |
      | --max-cost    | -0.01        |
      | --max-cost    | inf          |
      | --max-in      | -1           |
      | --max-out     | -1           |
      | --max-out     | NaN          |
      | --min-tps     | -3           |
      | --node        | ""           |

  Scenario: More than 32 entries in a list flag is refused locally
    When the user runs "roger use qwen3-32b --only <33 comma-separated ids> --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--only" and "32"

  Scenario: --require is lower-cased and de-duplicated by the CLI before it is sent
    # A convenience at the flag only: the BROKER is strict (contract §1a, enumerated values are
    # exact lowercase), so the CLI always sends the canonical spelling.
    When the user runs "roger use qwen3-32b --require Tools,VISION,tools --yes"
    Then the tune-time body carries roger.require = ["tools","vision"]

  Scenario: --quant keeps labels verbatim (no bucketing) and de-duplicates case-insensitively
    When the user runs "roger use qwen3-32b --quant Q4_K_M,q4_k_m,IQ4_XS --yes"
    Then the tune-time body carries provider.quantizations = ["Q4_K_M","IQ4_XS"]

  Scenario: An unknown flag is the concise one-line error, printed once
    When the user runs "roger use qwen3-32b --cheapest --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--cheapest"
    And the line is printed once

  # --- --max-out semantics (defect 2, CLI half) --------------------------------------

  Scenario: --max-out 0 keeps the default consumer cap and the help text says so
    When the user runs "roger use qwen3-32b --max-out 0 --yes"
    Then the tune-time body carries provider.max_price.completion = 10
    And the help text for --max-out reads "0 = the default $10/1M consumer cap"
    And the help text for --max-out no longer contains "no cap"

  Scenario: --max-out unlimited sends the register ceiling as the cap
    Given the broker's register ceiling for output price is $100/1M
    When the user runs "roger use qwen3-32b --max-out unlimited --yes"
    Then the tune-time body carries provider.max_price.completion = 100
    And the connect line says "out cap: none (register ceiling $100/1M)"

  Scenario: --max-out above the register ceiling is sent as given and the broker clamps it
    When the user runs "roger use qwen3-32b --max-out 250 --yes"
    Then the tune-time body carries provider.max_price.completion = 250
    And the broker treats it as 100

  Scenario: --max-out between the default and the ceiling is honored as sent
    When the user runs "roger use qwen3-32b --max-out 42 --yes"
    Then the tune-time body carries provider.max_price.completion = 42

  Scenario: --max-out absent falls back to the stored limit, then the default
    Given limits.models.qwen3-32b.max_out is 3
    When the user runs "roger use qwen3-32b --yes"
    Then the tune-time body carries provider.max_price.completion = 3

  Scenario: --max-out 0 with a stored limit still means the default cap, not "unset the limit"
    Given limits.models.qwen3-32b.max_out is 3
    When the user runs "roger use qwen3-32b --max-out 0 --yes"
    Then the tune-time body carries provider.max_price.completion = 10

  Scenario: set-limit --max-out 0 clears the stored cap (the persisted 0 is "unset")
    When the user runs "roger config set-limit qwen3-32b --max-out 0"
    Then config limits.models.qwen3-32b has no max_out
    And stdout says "max_out cleared for qwen3-32b (the $10/1M default applies)"

  # --- resolution order: flag > profile > limits.models.<m> > limits.default > built-in ----

  Scenario: A flag beats the profile
    Given profile "coding" sets roger.pref = "reliable"
    When the user runs "roger use qwen3-32b --profile coding --pref cheap --yes"
    Then the tune-time body carries roger.pref = "cheap"

  Scenario: The profile beats the per-model limit
    Given limits.models.qwen3-32b.max_out is 3
    And profile "coding" sets provider.max_price.completion = 5
    When the user runs "roger use qwen3-32b --profile coding --yes"
    Then the tune-time body carries provider.max_price.completion = 5

  Scenario: The per-model limit beats the default limit
    Given limits.default.min_tps is 5
    And limits.models.qwen3-32b.min_tps is 20
    When the user runs "roger use qwen3-32b --yes"
    Then the tune-time body carries roger.min_tps = 20

  Scenario: The default limit beats the built-in
    Given limits.default.max_out is 4
    When the user runs "roger use llama-3.3-70b --yes"
    Then the tune-time body carries provider.max_price.completion = 4

  Scenario: The built-in is the $10 out cap and nothing else
    When the user runs "roger use llama-3.3-70b --yes"
    Then the tune-time body carries provider.max_price.completion = 10
    And no other routing key is present
    # This default cap is the one key every "no other routing key" step in this file allows for.

  Scenario: Each layer contributes only the keys it sets (shallow merge per key)
    Given limits.default.min_tps is 5
    And limits.models.qwen3-32b.max_out is 3
    And profile "coding" sets roger.require = ["tools"]
    When the user runs "roger use qwen3-32b --profile coding --region eu --yes"
    Then the tune-time body carries roger.min_tps = 5
    And the tune-time body carries provider.max_price.completion = 3
    And the tune-time body carries roger.require = ["tools"]
    And the tune-time body carries roger.region = ["eu"]

  Scenario: A list flag replaces the profile's list, it does not append
    Given profile "coding" sets provider.only = ["n1","n2"]
    When the user runs "roger use qwen3-32b --profile coding --only n3 --yes"
    Then the tune-time body carries provider.only = ["n3"]

  Scenario: The stored quants rule becomes provider.quantizations on the standalone path (defect 6)
    # A standing RULE is sent as its labels plus "unknown" (contract §5, §9): Limit.acceptsQuant
    # reads an unlabeled station as "not contradicted" by design, and the body form preserves
    # that meaning. The --quant FLAG and a tuned row name exact labels and add nothing.
    Given limits.models.qwen3-32b.quants is ["Q8_0"]
    When the user runs "roger use qwen3-32b --yes"
    Then the tune-time body carries provider.quantizations = ["Q8_0","unknown"]
    And no X-Roger-Exclude-Nodes header is derived from a discover scan

  Scenario: The --quant flag replaces the stored rule and does not add "unknown"
    Given limits.models.qwen3-32b.quants is ["Q8_0"]
    When the user runs "roger use qwen3-32b --quant BF16 --yes"
    Then the tune-time body carries provider.quantizations = ["BF16"]

  Scenario: --profile of an unknown name fails before any request
    When the user runs "roger use qwen3-32b --profile nope --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--profile" and "nope"
    And no request reaches the broker

  Scenario: @profile/<name> as the model positional is the same as --profile
    Given profile "coding" sets models = ["qwen3-32b","llama-3.3-70b"] and roger.pref = "reliable"
    When the user runs "roger use @profile/coding --yes"
    Then the tune-time body carries models = ["qwen3-32b","llama-3.3-70b"]
    And the tune-time body carries roger.pref = "reliable"
    And the tuned band's model is "qwen3-32b"

  Scenario: @profile/ as the positional with --models is refused (one source for the list)
    Given profile "coding" sets models = ["qwen3-32b"]
    When the user runs "roger use @profile/coding --models llama-3.3-70b --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--models" and "@profile/coding"

  # --- --models composition ---------------------------------------------------------

  Scenario: --models is appended after the positional model
    When the user runs "roger use qwen3-32b --models qwen3-30b,llama-3.3-70b --yes"
    Then the tune-time body carries model = "qwen3-32b"
    And the tune-time body carries models = ["qwen3-30b","llama-3.3-70b"]

  Scenario: --models repeating the positional is de-duplicated
    When the user runs "roger use qwen3-32b --models qwen3-32b,llama-3.3-70b --yes"
    Then the tune-time body carries models = ["llama-3.3-70b"]

  Scenario: The positional plus --models may total five, six is refused
    When the user runs "roger use m1 --models m2,m3,m4,m5 --yes"
    Then the tune-time body carries models = ["m2","m3","m4","m5"]
    When the user runs "roger use m1 --models m2,m3,m4,m5,m6 --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--models" and "5"

  Scenario: Variant sugar on the positional survives into the body
    When the user runs "roger use qwen3-32b:free --yes"
    Then the tune-time body carries model = "qwen3-32b:free"
    And the connect line names the band as "qwen3-32b" with the mark "free only"

  Scenario: Variant sugar on --models entries survives per entry
    When the user runs "roger use qwen3-32b --models llama-3.3-70b:floor --yes"
    Then the tune-time body carries models = ["llama-3.3-70b:floor"]

  # --- the connect-time summary line --------------------------------------------------

  Scenario: The interactive connect prompt shows one effective routing line
    When the user runs "roger use qwen3-32b --pref fast --only n1,n2 --min-tps 20 --max-out 2"
    Then the connect prompt contains exactly one line beginning with "routing:"
    And that line reads "routing: fast · only n1,n2 · ≥20 t/s · out ≤ $2/1M"
    And the line uses the monospace brand styles only (no colors beyond the existing dim and ember)

  Scenario: The routing line names the source of each effective value
    Given profile "coding" sets roger.pref = "reliable"
    And limits.default.max_out is 4
    When the user runs "roger use qwen3-32b --profile coding --min-tps 20"
    Then the routing line reads "routing: reliable (profile coding) · ≥20 t/s (flag) · out ≤ $4/1M (default limit)"

  Scenario: The routing line is omitted when nothing beyond the built-in cap is set
    When the user runs "roger use qwen3-32b"
    Then the connect prompt has no line beginning with "routing:"
    And the existing "out cap" wording is unchanged

  Scenario: The routing line never prints a band code
    When the user runs "roger use qwen3-32b --freq '147.520 MHz 8F3K-9M2Q'"
    Then the routing line reads "routing: private freq"
    And "8F3K-9M2Q" appears nowhere on stdout or stderr

  Scenario: The routing line truncates long lists honestly
    When the user runs "roger use qwen3-32b --only n1,n2,n3,n4,n5,n6"
    Then the routing line contains "only n1,n2,n3 +3"

  Scenario: --yes skips the confirm and prints the routing line once in the connect banner
    When the user runs "roger use qwen3-32b --pref fast --yes"
    Then no confirm prompt is shown
    And stdout contains exactly one line beginning with "routing:"

  # --- roger config set-limit learns the new keys ------------------------------------

  Scenario Outline: set-limit persists the new routing keys per model or default
    When the user runs "roger config set-limit <target> <flag> <value>"
    Then config <path> equals <stored>
    And stdout says "set <target> <name> = <value>"

    Examples:
      | target     | flag          | value      | path                                  | stored           | name         |
      | qwen3-32b  | --quant       | Q8_0,BF16  | limits.models.qwen3-32b.quants        | ["Q8_0","BF16"]  | quants       |
      | qwen3-32b  | --pref        | reliable   | limits.models.qwen3-32b.pref          | "reliable"       | pref         |
      | qwen3-32b  | --require     | tools      | limits.models.qwen3-32b.require       | ["tools"]        | require      |
      | qwen3-32b  | --params      | 7-70       | limits.models.qwen3-32b.params_b      | [7,70]           | params_b     |
      | qwen3-32b  | --min-ctx     | 32k        | limits.models.qwen3-32b.min_ctx       | 32768            | min_ctx      |
      | qwen3-32b  | --max-ttft    | 1500ms     | limits.models.qwen3-32b.max_ttft_ms   | 1500             | max_ttft_ms  |
      | qwen3-32b  | --trust       | verified   | limits.models.qwen3-32b.trust_min     | "verified"       | trust_min    |
      | qwen3-32b  | --self-hosted | true       | limits.models.qwen3-32b.self_hosted   | true             | self_hosted  |
      | qwen3-32b  | --region      | eu         | limits.models.qwen3-32b.region        | ["eu"]           | region       |
      | qwen3-32b  | --max-cost    | 0.02       | limits.models.qwen3-32b.max_cost      | 0.02             | max_cost     |
      | default    | --pref        | cheap      | limits.default.pref                   | "cheap"          | pref         |
      | default    | --self-hosted | true       | limits.default.self_hosted            | true             | self_hosted  |

  Scenario: set-limit changes only the flags passed (the rest of the limit is preserved)
    Given limits.models.qwen3-32b is {max_out: 3, quants: ["Q8_0"]}
    When the user runs "roger config set-limit qwen3-32b --pref fast"
    Then limits.models.qwen3-32b equals {max_out: 3, quants: ["Q8_0"], pref: "fast"}

  Scenario: set-limit validates with the same rules as use
    When the user runs "roger config set-limit qwen3-32b --params 70-7"
    Then the exit code is non-zero
    And stderr is exactly one line naming "--params"
    And the config file is unchanged

  Scenario: set-limit's usage line lists every flag
    When the user runs "roger config set-limit"
    Then stderr names "--max-in --max-out --min-tps --quant --pref --require --params --min-ctx --max-ttft --trust --self-hosted --region --max-cost"

  Scenario: clear-limit removes every routing key for the model
    Given limits.models.qwen3-32b is {max_out: 3, pref: "fast", region: ["eu"]}
    When the user runs "roger config clear-limit qwen3-32b"
    Then config has no limits.models.qwen3-32b

  Scenario: roger limits lists the new keys in its table
    Given limits.models.qwen3-32b is {max_out: 3, pref: "fast", quants: ["Q8_0"], self_hosted: true}
    When the user runs "roger limits"
    Then the row for "qwen3-32b" shows "out ≤ $3/1M", "fast", "Q8_0", "self-hosted"

  Scenario: An old config with only max_in/max_out/min_tps/quants loads unchanged
    Given a config written by v6.12.0 with limits.models.qwen3-32b = {max_out: 3}
    When the user runs "roger use qwen3-32b --yes"
    Then the tune-time body carries provider.max_price.completion = 3
    And the config file is not rewritten

  # --- config show ------------------------------------------------------------------

  Scenario: roger config show prints the effective routing for a model
    Given limits.default.max_out is 4
    And limits.models.qwen3-32b.pref is "reliable"
    When the user runs "roger config show qwen3-32b"
    Then stdout contains "pref: reliable (limits.models.qwen3-32b)"
    And stdout contains "max_out: 4 (limits.default)"
    And stdout contains "min_tps: - (unset, unmeasured stations pass)"

  Scenario: roger config show with a profile prints the merged result and each key's source
    Given profile "coding" sets roger.pref = "fast" and provider.only = ["n1"]
    And limits.default.max_out is 4
    When the user runs "roger config show qwen3-32b --profile coding"
    Then stdout contains "pref: fast (profile coding)"
    And stdout contains "only: n1 (profile coding)"
    And stdout contains "max_out: 4 (limits.default)"

  Scenario: roger config show never prints a band code
    Given profile "home" sets roger.freq = "147.520 MHz 8F3K-9M2Q"
    When the user runs "roger config show qwen3-32b --profile home"
    Then stdout contains "freq: (set, hidden)"
    And "8F3K-9M2Q" appears nowhere on stdout

  # --- headers vs body ----------------------------------------------------------------

  Scenario: Against a broker that accepts the body object no routing header is sent
    When the user runs "roger use qwen3-32b --min-tps 20 --confidential --yes"
    And a chat request goes through the local endpoint
    Then the broker request body carries roger.min_tps = 20 and roger.confidential = true
    And the request has no X-Roger-Min-TPS and no X-Roger-Confidential header
    And the request still carries X-Roger-Max-Price-Out (the headless overpay guard) for one release

  Scenario: Against an old broker the same flags fall back to headers (see routing_passthrough.feature)
    # The mode is decided at tune time by a positive probe, GET /v1/models on the broker (200 =
    # body mode, 404 = old broker, header mode); an old broker never answers 400 for an unknown
    # top-level key, it forwards it, so no 400 is ever used as the signal (contract §9).
    Given the broker answers 404 to GET /v1/models
    When the user runs "roger use qwen3-32b --min-tps 20 --require tools --yes"
    Then the tune output warns once "old broker: routing flags with no header form are dropped: --require"
    When a chat request goes through the local endpoint
    Then the request carries X-Roger-Min-TPS: 20 and no routing body object
    And no request is retried to negotiate

  # --- release surface stays intact --------------------------------------------------

  Scenario: roger help still begins with the canonical heading and lists use once
    When the user runs "roger help"
    Then the heading begins with "roger -"
    And "use" appears once in the command list
    And the new flags appear only under "roger use --help"

  Scenario: roger use --help groups the routing flags under one heading
    When the user runs "roger use --help"
    Then the output has a "routing" section listing every flag in this file
    And the "advanced" section still lists "--port --max-in --min-tps --confidential --yes --raw"

  Scenario: The unknown-command line is unchanged
    When the user runs "roger route"
    Then stderr is the concise unknown-command line, printed once
