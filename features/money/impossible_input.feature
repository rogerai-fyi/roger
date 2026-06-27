# Spec-only behavior contract for the ZERO-DOUBT impossible-input ban.
# Ground truth:
#   cmd/rogerai-broker/recount.go  - settleRecountPrompt byte-floor + impossibleInputBanMargin (8192)
#   cmd/rogerai-broker/strikes.go  - flagImpossibleInput -> strike(zeroDoubt=true) -> banOwner on
#                                    the FIRST strike, bypassing decay + corroboration
#
# The physical proof: no tokenizer can emit more tokens than the prompt has UTF-8 bytes, so a
# claimed prompt-token count ABOVE the request-body byte length is arithmetically impossible.
# Billing is ALWAYS clamped to the body byte count for ANY overage (the consumer can never be
# over-charged on input). The PERMANENT BAN, however, only fires when the claim exceeds the body
# by more than impossibleInputBanMargin (8192 tokens) - the headroom that absorbs a large fixed
# chat-template / system-preamble / tool-scaffold that legitimately tokenizes to more prompt
# tokens than the request body carries. So a TEMPLATE-scale overage clamps billing but never bans;
# only abuse-beyond-doubt ejects the owner.
#
# NO step definitions and NO Go. Spec only.

@money @security @impossible-input @ban
Feature: A claim of prompt tokens grossly beyond the request body bytes bans on the first strike

  Background:
    Given node "n1" is owned by account "op1"
    And the impossible-input ban margin is 8192 tokens
    And the strike warn threshold is 3 and ban threshold is 5

  Rule: Billing is ALWAYS clamped to the request-body byte count for any overage

    Scenario: A claim above body bytes is clamped down to the body bytes for billing
      Given the request body is 1000 bytes
      And the node claims 5000 prompt tokens
      When the input axis settles
      Then the billed prompt tokens are clamped to 1000
      And the consumer is never charged for more prompt tokens than the body has bytes

    Scenario: A claim within body bytes is left untouched
      Given the request body is 4000 bytes
      And the node claims 900 prompt tokens
      When the input axis settles
      Then the billed prompt tokens are 900
      And no impossible-input ban fires

    Scenario: A zero-length body short-circuits the byte floor (no clamp, no ban)
      Given the request body is 0 bytes
      And the node claims 9999999 prompt tokens
      When the input axis settles
      Then the byte floor is not applied
      And no impossible-input ban fires

    Scenario Outline: Billing clamp regardless of ban decision
      Given the request body is <bytes> bytes
      And the node claims <claim> prompt tokens
      When the input axis settles
      Then the billed prompt tokens are clamped to <billed>

      Examples:
        | bytes | claim  | billed |
        | 1000  | 5000   | 1000   |
        | 1000  | 1001   | 1000   |
        | 1000  | 1000   | 1000   |
        | 1000  | 999    | 999    |
        | 4000  | 100000 | 4000   |
        | 0     | 9999   | 9999   |

  Rule: The permanent ban fires only past the body-bytes-plus-margin headroom

    Scenario: A template-scale overage clamps billing but does NOT ban (honest preamble)
      Given the request body is 1000 bytes
      And the node claims 9000 prompt tokens
      When the input axis settles
      Then the billed prompt tokens are clamped to 1000
      And no impossible-input ban fires
      And owner "op1" is not banned
      And the reason recorded is "within template headroom"

    Scenario: A gross overage past body+margin bans the owner on the first strike
      Given the request body is 1000 bytes
      And the node claims 9194 prompt tokens
      When the input axis settles
      Then the billed prompt tokens are clamped to 1000
      And the impossible-input strike is flagged as zero-doubt against owner "op1"
      And owner "op1" is banned immediately on the first strike
      And the ban bypasses the decay window and the corroboration requirement

    Scenario: The zero-doubt ban needs no second signal class (bypasses corroboration)
      Given owner "op1" has zero prior strikes
      And the request body is 500 bytes
      And the node claims 20000 prompt tokens
      When the input axis settles
      Then owner "op1" is banned with a single strike
      And the ban is durable across node-id rotation
      And every current and future node under owner "op1" is rejected at register, pick, and settle

    Scenario: The ban evidence is non-repudiable (claim vs body bytes recorded)
      Given the request body is 800 bytes
      And the node claims 50000 prompt tokens
      When the input axis settles
      Then the strike evidence records claimed_tokens 50000 and body_bytes 800 on the input axis
      And the evidence note states the claimed prompt tokens exceed the request body bytes

  Rule: Enumerate the exact margin boundary (ban iff claim > body + 8192)

    Scenario Outline: The impossible-input ban boundary
      Given the request body is <bytes> bytes
      And the node claims <claim> prompt tokens
      When the input axis settles
      Then the billed prompt tokens are clamped to <billed>
      And the impossible-input ban fired is <banned>

      Examples: boundary is strict greater-than body + 8192
        | bytes | claim  | billed | banned |
        | 1000  | 1000   | 1000   | false  |
        | 1000  | 1001   | 1000   | false  |
        | 1000  | 5096   | 1000   | false  |
        | 1000  | 9192   | 1000   | false  |
        | 1000  | 9193   | 1000   | false  |
        | 1000  | 9194   | 1000   | true   |
        | 1000  | 9300   | 1000   | true   |
        | 1000  | 20000  | 1000   | true   |
        | 0     | 100000 | 100000 | false  |
        | 100   | 8292   | 100    | false  |
        | 100   | 8293   | 100    | true   |
        | 32768 | 40960  | 32768  | false  |
        | 32768 | 40961  | 32768  | true   |

    Scenario: Exactly body + margin does not ban; one token more does
      Given the request body is 2000 bytes
      And the node claims 10192 prompt tokens
      When the input axis settles
      Then no impossible-input ban fires
      When a later request with body 2000 bytes claims 10193 prompt tokens
      Then the impossible-input ban fires

  Rule: A public/unowned node still records the signal against its best identity

    Scenario: An unowned node's impossible claim is keyed to the node id fallback
      Given node "pub1" has no owner binding
      And the request body is 500 bytes
      And node "pub1" claims 30000 prompt tokens
      When the input axis settles
      Then the impossible-input strike is recorded against the node-id fallback identity
      And billing is clamped to 500 bytes
