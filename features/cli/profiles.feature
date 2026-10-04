# ROUTING EXPRESSION - named routing profiles (ROUTING-EXPRESSION-CONTRACT.md §9).
#
# PURPOSE: one place to say "this is how I want to be routed", resolved by every first-party
# client into the CONTRACT §1 body object, so guest operators (who cannot set headers) and the
# TUI, CLI, Playbox and local proxy all carry the same routing.
#
# GROUND TRUTH at origin/main 518c698b:
#   - The config file is JSON at <UserConfigDir>/rogerai/config.json (cmd/rogerai/main.go:225-228),
#     decoded with encoding/json into `config` (main.go:94-124). json.Unmarshal IGNORES unknown
#     keys silently; a corrupt file is moved aside to config.json.corrupt and defaults are used
#     (main.go:245-264, features/onboarding/config_preservation.feature). saveConfig writes
#     atomically. That unknown-key behavior is KEPT here: an unknown key inside a profile is
#     ignored at load and REPORTED only by `roger profile show` - a typo must never brick
#     `roger use`.
#   - Limits today: limits.default / limits.models.<m> with max_in, max_out, min_tps, quants
#     (main.go:66-92). cfg.resolve(model) is the only reader (main.go:214-223).
#   - The local proxy reads its options from a ProxyOptionsHolder snapshot set at tune time
#     (internal/client/client.go:695-720, features/proxy/live_options.feature). Nothing re-reads
#     config.json after `roger use` starts.
#   - There is no `roger profile` command (main.go:844-958). The command set below is PROPOSED.
#
# PROPOSED (marked in scenario titles): the `roger profile` command set; per-request resolution
# of `@profile/` references from guests (config.json re-read by mtime); band codes allowed in
# roger.freq but never printed.
#
# Enforced by: cmd/rogerai/profiles_bdd_test.go (future; real config files in a temp
# UserConfigDir, the real loader and resolver, no mocks).

Feature: Named routing profiles resolve to the body object on every client

  Background:
    Given a temp config dir with an otherwise-default config.json

  # --- schema -------------------------------------------------------------------------

  Scenario: A profile holds any subset of the contract's routing keys
    Given config.json contains:
      """
      {
        "profiles": {
          "coding": {
            "models": ["qwen3-32b", "llama-3.3-70b"],
            "provider": { "sort": "throughput", "only": ["n1", "n2"], "max_price": { "completion": 2 } },
            "roger": { "require": ["tools"], "min_ctx": 32768, "self_hosted_only": true }
          }
        }
      }
      """
    When profile "coding" is resolved
    Then the body object equals the profile's models, provider and roger blocks verbatim

  Scenario: A profile may set a primary model as well as the list
    Given profile "coding" is {"model": "qwen3-32b", "models": ["llama-3.3-70b"]}
    When profile "coding" is resolved
    Then the body object carries model = "qwen3-32b" and models = ["llama-3.3-70b"]

  Scenario: Keys outside the routing contract are out of scope for a profile
    Given profile "coding" contains "max_tokens": 512 and "system": "be brief"
    When the user runs "roger profile show coding"
    Then stdout lists "max_tokens, system" under "ignored keys (not routing)"
    And the resolved body object carries neither

  Scenario: An unknown key inside a profile is ignored at load and reported by show
    Given profile "coding" contains "roger": {"prefer": "fast"}
    When the user runs "roger use qwen3-32b --profile coding --yes"
    Then the tune proceeds
    And the tune-time body carries no roger.prefer key
    When the user runs "roger profile show coding"
    Then stdout lists "roger.prefer" under "ignored keys (unknown)"

  Scenario: A profile value of the wrong type is a load-time error for that profile only
    Given profile "coding" contains "roger": {"min_ctx": "lots"}
    When the user runs "roger use qwen3-32b --profile coding --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "profile coding" and "roger.min_ctx"
    When the user runs "roger use qwen3-32b --yes"
    Then the tune proceeds without any profile warning

  Scenario Outline: Profile values are validated with the contract's ranges
    Given profile "p" contains <json>
    When the user runs "roger use qwen3-32b --profile p --yes"
    Then the exit code is non-zero
    And stderr is exactly one line naming "profile p" and "<key>"

    Examples:
      | json                                            | key                        |
      | "roger": {"params_b": [70, 7]}                  | roger.params_b             |
      | "roger": {"params_b": [7]}                      | roger.params_b             |
      | "roger": {"min_ctx": 0}                         | roger.min_ctx              |
      | "roger": {"max_ttft_ms": -1}                    | roger.max_ttft_ms          |
      | "roger": {"pref": "quick"}                      | roger.pref                 |
      | "roger": {"trust_min": "gold"}                  | roger.trust_min            |
      | "roger": {"require": ["json"]}                  | roger.require              |
      | "roger": {"region": ["EU"]}                     | roger.region               |
      # region "EU": enumerated values are exact lowercase (contract §1a); the relay would 400 it too
      | "provider": {"sort": "cheapest"}                | provider.sort              |
      | "provider": {"max_price": {"prompt": -1}}       | provider.max_price.prompt  |
      | "provider": {"max_price": {"image": 1}}         | provider.max_price.image   |
      | "provider": {"only": []}                        | provider.only              |
      | "provider": {"only": [""]}                      | provider.only              |
      | "provider": {"order": ["n1"], "only": ["n2"]}   | provider.order             |
      | "provider": {"sort": "price"}, "roger": {"pref": "fast"} | provider.sort     |
      | "models": ["a","b","c","d","e","f"]             | models                     |

  Scenario: A malformed profiles section never blocks a plain roger use
    Given config.json has "profiles": "not-an-object"
    When the user runs "roger use qwen3-32b --yes"
    Then the tune proceeds
    And stderr has exactly one line "profiles: ignored (not an object) - run roger profile list"
    And config.json is not moved to .corrupt

  Scenario: A corrupt config.json still falls back to defaults as before
    Given config.json is truncated mid-object
    When the user runs "roger use qwen3-32b --yes"
    Then config.json is moved to config.json.corrupt
    And the tune proceeds on defaults

  # --- naming --------------------------------------------------------------------------

  Scenario Outline: Profile names are lowercase slugs
    Given config.json defines a profile named "<name>"
    When the user runs "roger profile list"
    Then the profile is <listed>

    Examples:
      | name           | listed                           |
      | coding         | listed                           |
      | fast-cheap     | listed                           |
      | eu_only        | listed                           |
      | Coding         | rejected as "invalid name"       |
      | with space     | rejected as "invalid name"       |
      | @profile/x     | rejected as "invalid name"       |
      | ""             | rejected as "invalid name"       |
      | a              | listed                           |
      | 65-char-name…  | rejected as "invalid name"       |

  # corrected 2026-10-04 (founder-approved): a profile with no model on the standalone path asks for a model (approved); --profile applies it to a named model
  Scenario: A profile named like a model id is still a profile only behind @profile/ or --profile
    Given profile "qwen3-32b" sets roger.pref = "fast"
    When the user runs "roger use qwen3-32b --yes"
    Then the tune-time body carries no roger.pref key
    When the user runs "roger use qwen3-32b --profile qwen3-32b --yes"
    Then the tune-time body carries roger.pref = "fast"
    When the user runs "roger use @profile/qwen3-32b --yes"
    Then the exit code is non-zero
    And stderr is exactly one line saying "profile qwen3-32b names no model: roger use <model> --profile qwen3-32b"

  Scenario: A profile referencing another profile is refused (no nesting)
    Given profile "base" sets roger.pref = "fast"
    And profile "coding" contains "roger": {"profile": "@profile/base"}
    When the user runs "roger use qwen3-32b --profile coding --yes"
    Then the exit code is non-zero
    And stderr is exactly one line saying "profile coding: profiles cannot reference profiles"

  Scenario: The reserved name "default" is the folded limits profile and cannot be defined
    Given config.json defines a profile named "default"
    When the user runs "roger profile list"
    Then stderr says "profile default is reserved (it is built from limits.*)"
    And the defined "default" is ignored

  # --- the default profile folded from limits.* -----------------------------------------

  Scenario: limits.default folds into the default profile
    Given limits.default is {max_out: 4, min_tps: 5, pref: "cheap"}
    When the user runs "roger profile show default"
    Then stdout shows provider.max_price.completion = 4
    And stdout shows roger.min_tps = 5
    And stdout shows roger.pref = "cheap"
    And stdout says "(built from limits.default)"

  Scenario: limits.models.<m> folds in when resolving for that model
    Given limits.default is {max_out: 4}
    And limits.models.qwen3-32b is {max_out: 3, quants: ["Q8_0"]}
    When the default profile is resolved for "qwen3-32b"
    Then the body object carries provider.max_price.completion = 3
    And the body object carries provider.quantizations = ["Q8_0","unknown"]
    # a standing quants RULE travels with "unknown" appended (contract §9): an unlabeled
    # station is "not contradicted" by a rule, exactly as Limit.acceptsQuant reads it
    When the default profile is resolved for "llama-3.3-70b"
    Then the body object carries provider.max_price.completion = 4
    And the body object carries no provider.quantizations

  Scenario: A named profile layers on top of the default profile
    Given limits.default is {max_out: 4, min_tps: 5}
    And profile "coding" sets roger.pref = "fast"
    When profile "coding" is resolved for "qwen3-32b"
    Then the body object carries provider.max_price.completion = 4
    And the body object carries roger.min_tps = 5
    And the body object carries roger.pref = "fast"

  # --- shallow override rules (request/flag > profile) -----------------------------------

  Scenario Outline: A request key replaces the profile's value for that key only
    Given profile "p" sets <profileKey> = <profileValue>
    And the request sets <requestKey> = <requestValue>
    When profile "p" is merged with the request
    Then the body object carries <requestKey> = <requestValue>
    And every other profile key is intact

    Examples:
      | profileKey                    | profileValue        | requestKey                    | requestValue |
      | provider.only                 | ["n1","n2"]         | provider.only                 | ["n3"]       |
      | provider.order                | ["n1"]              | provider.order                | ["n2","n3"]  |
      | provider.ignore               | ["n9"]              | provider.ignore               | ["n8"]       |
      | provider.allow_fallbacks      | false               | provider.allow_fallbacks      | true         |
      | provider.sort                 | "price"             | provider.sort                 | "latency"    |
      | provider.quantizations        | ["Q8_0"]            | provider.quantizations        | ["BF16"]     |
      | provider.max_price.prompt     | 1                   | provider.max_price.prompt     | 0.5          |
      | provider.max_price.completion | 2                   | provider.max_price.completion | 3            |
      | provider.max_price.request    | 0.05                | provider.max_price.request    | 0.01         |
      | provider.require_parameters   | true                | provider.require_parameters   | false        |
      | roger.pref                    | "cheap"             | roger.pref                    | "fast"       |
      | roger.require                 | ["tools"]           | roger.require                 | ["vision"]   |
      | roger.params_b                | [7,70]              | roger.params_b                | [30,30]      |
      | roger.min_ctx                 | 32768               | roger.min_ctx                 | 8192         |
      | roger.min_tps                 | 20                  | roger.min_tps                 | 5            |
      | roger.max_ttft_ms             | 1500                | roger.max_ttft_ms             | 3000         |
      | roger.trust_min               | "verified"          | roger.trust_min               | "any"        |
      | roger.self_hosted_only        | true                | roger.self_hosted_only        | false        |
      | roger.confidential            | true                | roger.confidential            | false        |
      | roger.region                  | ["eu"]              | roger.region                  | ["us"]       |

  # corrected 2026-10-04 (founder-approved): the models and roger.freq rows moved out of the outline - a guest may only tighten
  Scenario: A guest request naming a model outside the tuned band is refused even when a profile lists it
    Given profile "p" sets models = ["a","b"]
    And the local proxy is tuned to "a"
    When a guest request with model "@profile/p" and models ["c"] is resolved by the local proxy
    Then the guest receives a local 400 with error.code "routing_outside_session"
    And nothing reaches the broker

  # corrected 2026-10-04 (founder-approved): a guest's band code is never taken - the owner's band stands
  Scenario: A guest's roger.freq never replaces the profile's or the session's band
    Given profile "p" sets roger.freq = "147.520 MHz AAAA"
    When a guest request with model "@profile/p" and roger.freq "147.520 MHz BBBB" is resolved by the local proxy
    Then the broker receives the X-Roger-Freq header for "147.520 MHz AAAA"
    And the body carries no roger.freq

  Scenario: max_price merges per sub-key (prompt from the profile, completion from the request)
    Given profile "p" sets provider.max_price = {"prompt": 1, "completion": 2}
    And the request sets provider.max_price = {"completion": 3}
    When profile "p" is merged with the request
    Then the body object carries provider.max_price = {"prompt": 1, "completion": 3}

  Scenario: Setting a key to null in the request clears the profile's value
    Given profile "p" sets provider.only = ["n1"]
    And the request sets provider.only = null
    When profile "p" is merged with the request
    Then the body object carries no provider.only key

  Scenario: A request that only names the profile inherits everything
    Given profile "p" sets roger.pref = "fast" and provider.ignore = ["n9"]
    And the request is {"model": "@profile/p", "messages": [...]}
    When the request is resolved by the local proxy
    Then the broker receives roger.pref = "fast", provider.ignore = ["n9"], and no "@profile/" anywhere

  Scenario: A merge that produces an exclusive pair fails with the pair named
    Given profile "p" sets provider.sort = "price"
    And the request sets roger.pref = "fast"
    When profile "p" is merged with the request
    Then the resolution fails naming "provider.sort" and "roger.pref"
    And the message says which came from the profile

  # --- @profile/ references ------------------------------------------------------------

  Scenario: @profile/<name> in model is resolved by the client and never sent to the broker
    Given profile "coding" sets models = ["qwen3-32b"]
    And the request body is {"model": "@profile/coding"}
    When the local proxy resolves the request
    Then the broker receives model = "qwen3-32b" and no "@profile/" string in any field

  Scenario: roger.profile is the other spelling and means the same
    Given profile "coding" sets roger.pref = "fast"
    And the request body is {"model": "qwen3-32b", "roger": {"profile": "@profile/coding"}}
    When the local proxy resolves the request
    Then the broker receives roger.pref = "fast" and no roger.profile key

  Scenario: model and roger.profile naming different profiles is refused
    Given profiles "a" and "b" exist
    And the request body is {"model": "@profile/a", "roger": {"profile": "@profile/b"}}
    When the local proxy resolves the request
    Then the guest receives an OpenAI-shaped 400 "two profiles named: @profile/a and @profile/b"
    And nothing reaches the broker

  Scenario: An unknown @profile/ is a local 400 that reaches no broker
    Given no profile named "nope"
    And the request body is {"model": "@profile/nope"}
    When the local proxy resolves the request
    Then the guest receives an OpenAI-shaped 400 "unknown profile nope"
    And nothing reaches the broker

  Scenario: The broker itself rejects an unresolved @profile/ with 400 unknown_profile
    Given a hand-rolled caller sends {"model": "@profile/coding"} straight to the broker
    Then the broker answers 400 with error.code "unknown_profile"
    And no hold is placed

  # corrected 2026-10-04 (founder-approved): a carrier-less foreign id is rewritten to the band model (approved rewrite rule)
  Scenario: @profile/ is case-sensitive and exact
    Given profile "coding" exists
    When the request names "@Profile/coding"
    Then the local proxy does not resolve it as a profile
    And the broker receives the tuned band's model, as for any carrier-less foreign id

  Scenario: A profile with no model or models takes the tuned band's model through the proxy (no local 400)
    Given profile "p" sets only roger.pref = "fast"
    And the local proxy is tuned to "qwen3-32b-fp8"
    And the request body is {"model": "@profile/p"}
    When the local proxy resolves the request
    Then the broker receives model "qwen3-32b-fp8" and roger.pref "fast"

  Scenario: A profile with no model or models on the standalone path asks for a model
    Given profile "p" sets only roger.pref = "fast"
    When the user runs "roger use @profile/p --yes"
    Then the exit code is non-zero
    And stderr is exactly one line saying "profile p names no model: roger use <model> --profile p"

  Scenario: Variant sugar cannot ride on a @profile/ reference
    Given profile "p" sets models = ["qwen3-32b"]
    And the request body is {"model": "@profile/p:free"}
    When the local proxy resolves the request
    Then the guest receives an OpenAI-shaped 400 "sugar on a profile reference; put :free on the profile's models"

  # --- freshness (PROPOSED) -------------------------------------------------------------

  Scenario: PROPOSED - a tune-time --profile is a snapshot for the session
    Given profile "coding" sets roger.pref = "fast"
    And the user tunes with "roger use qwen3-32b --profile coding --yes"
    When config.json changes profile "coding" to roger.pref = "cheap"
    And a chat request without a profile reference goes through the endpoint
    Then the broker receives roger.pref = "fast"

  Scenario: PROPOSED - a per-request @profile/ reference is re-read on change
    Given profile "coding" sets roger.pref = "fast"
    And the local proxy is live
    When a guest sends {"model": "@profile/coding"}
    Then the broker receives roger.pref = "fast"
    When config.json changes profile "coding" to roger.pref = "cheap"
    And a guest sends {"model": "@profile/coding"}
    Then the broker receives roger.pref = "cheap"
    And config.json was re-read because its mtime changed, not on every request

  Scenario: PROPOSED - a config.json that becomes unreadable keeps the last good profiles
    Given the proxy resolved profile "coding" once
    When config.json is replaced by a truncated file
    And a guest sends {"model": "@profile/coding"}
    Then the broker receives the last good "coding"
    And one warning line names config.json

  # --- roger profile command set (PROPOSED) --------------------------------------------

  Scenario: PROPOSED - roger profile list prints names and one-line summaries
    Given profiles "coding" (sort throughput, only n1,n2, require tools) and "cheap" (pref cheap)
    When the user runs "roger profile list"
    Then stdout is:
      """
      default   built from limits.* (out ≤ $10/1M)
      cheap     cheap
      coding    throughput · only n1,n2 · require tools
      """

  Scenario: PROPOSED - roger profile show prints the resolved body object and its sources
    Given limits.default is {max_out: 4}
    And profile "coding" sets roger.pref = "fast"
    When the user runs "roger profile show coding"
    Then stdout contains a JSON block equal to the resolved body object
    And each key is annotated with its source (profile coding / limits.default / built-in)

  Scenario: PROPOSED - roger profile show never prints a band code
    Given profile "home" sets roger.freq = "147.520 MHz 8F3K-9M2Q"
    When the user runs "roger profile show home"
    Then stdout shows roger.freq = "(set, hidden)"
    And "8F3K-9M2Q" appears nowhere on stdout or stderr

  Scenario: PROPOSED - roger profile set writes one key with validation
    When the user runs "roger profile set coding roger.pref fast"
    Then config.json profiles.coding.roger.pref equals "fast"
    And the write is atomic (temp file + rename) like saveConfig
    When the user runs "roger profile set coding roger.pref quickest"
    Then the exit code is non-zero and config.json is unchanged

  Scenario: PROPOSED - roger profile set accepts JSON for list and object values
    When the user runs "roger profile set coding provider.only '[\"n1\",\"n2\"]'"
    Then config.json profiles.coding.provider.only equals ["n1","n2"]
    When the user runs "roger profile set coding provider.max_price '{\"completion\": 2}'"
    Then config.json profiles.coding.provider.max_price equals {"completion": 2}

  Scenario: PROPOSED - roger profile set creates the profile when missing
    Given no profile named "eu"
    When the user runs "roger profile set eu roger.region '[\"eu\"]'"
    Then profile "eu" exists with exactly that key

  Scenario: PROPOSED - roger profile unset removes one key, rm removes the profile
    Given profile "coding" sets roger.pref = "fast" and provider.only = ["n1"]
    When the user runs "roger profile unset coding roger.pref"
    Then profile "coding" equals {"provider": {"only": ["n1"]}}
    When the user runs "roger profile rm coding"
    Then no profile named "coding" exists
    And other profiles are untouched

  Scenario: PROPOSED - rm of the reserved default is refused
    When the user runs "roger profile rm default"
    Then the exit code is non-zero
    And stderr says "default is built from limits.*; use roger config clear-limit default"

  Scenario: PROPOSED - profile writes preserve every other config key byte-for-byte
    Given config.json carries share, share_prices, voices, palette and agent_perms
    When the user runs "roger profile set coding roger.pref fast"
    Then every non-profiles key of config.json is unchanged

  Scenario: PROPOSED - concurrent profile writes do not clobber each other
    When two "roger profile set" commands run at once on different profiles
    Then both keys are present afterwards

  # --- the proxy resolves profiles for guests ------------------------------------------

  # corrected 2026-10-04 (founder-approved): the broker only routes require tools to a station whose tool calling its own canary verified, so the fixture station is a verified one
  Scenario: A guest naming @profile/ gets the profile's routing without headers
    Given the broker verifies tool calling on its stations
    And profile "coding" sets roger.require = ["tools"] and provider.max_price.completion = 2
    And a live proxy session with band model "qwen3-32b"
    And the band's station has verified tool calling
    When the guest sends {"model": "@profile/coding", "messages": [...]}
    Then the broker receives model = "qwen3-32b", roger.require = ["tools"], provider.max_price.completion = 2
    And the guest's response is unchanged in shape

  Scenario: The guest's own body keys beat the profile
    Given profile "coding" sets roger.pref = "fast"
    When the guest sends {"model": "@profile/coding", "roger": {"pref": "cheap"}}
    Then the broker receives roger.pref = "cheap"

  Scenario: The proxy owner's caps still bound a guest's profile (see routing_passthrough.feature)
    Given the proxy was tuned with --max-out 2
    And profile "loose" sets provider.max_price.completion = 50
    When the guest sends {"model": "@profile/loose"}
    Then the broker receives provider.max_price.completion = 2

  @tui
  Scenario: The TUI, CLI and proxy resolve the same profile to the same body object
    Given profile "coding" with every key set
    When the profile is resolved by the CLI, by the TUI's tune, and by the local proxy for a guest
    Then the three body objects are identical

  # --- isolation and secrets ----------------------------------------------------------

  Scenario: A band code is allowed in roger.freq and never printed by any command
    Given profile "home" sets roger.freq = "147.520 MHz 8F3K-9M2Q"
    When the user runs each of "roger profile list", "roger profile show home", "roger config show x --profile home", "roger limits"
    Then "8F3K-9M2Q" appears in none of the outputs

  Scenario: Profiles never carry a session key or wallet key
    Given profile "p" contains "api_key": "sk-…"
    When the user runs "roger profile show p"
    Then stdout lists "api_key" under "ignored keys (not routing)"
    And the value is not printed

  Scenario: Profiles are not synced to the broker or any account endpoint
    When the user tunes with a profile
    Then no request to /account or /me carries the profile
    And the broker sees only the resolved body object
