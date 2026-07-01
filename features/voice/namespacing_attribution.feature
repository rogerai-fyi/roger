# Layer (broker view + register validation) spec — public TTS voices are namespaced per
# operator, attributed to a GitHub handle, and cannot impersonate a chat model or another
# operator. FOUNDER-APPROVED with 4 binding decisions baked in:
#   Q1  the voice-name slug comes from the offer's display NAME → @<login>/<slug(Name)>.
#   Q2  PUBLIC voices are SIGNED-IN operators only: an UNBOUND (anonymous) TTS offer is NOT
#       listed on public /voices. Every public voice is attributable.
#   Q3  the chat-model impersonation denylist is PREFIX-match, ENV-overridable.
#   Q5  /voices DUAL-EMITS: the raw id (id, back-compat; the app treats it as opaque) AND
#       the namespaced id (namespaced_id), for a migration window.
# The standing security guarantee holds throughout: NO node pubkey / node id / bridge URL /
# hostname / IP ever appears in /voices. See the draft spec (Part A/B recon, Part C Gherkin).

@voice @security @namespacing
Feature: Public voices are namespaced per operator, attributed, and cannot impersonate

  Background:
    Given the broker with content screening configured and required
    And an owner "@bownux" (GitHub login "bownux") who has logged in
    And an owner "@acme" (GitHub login "acme") who has logged in

  # ---------------------------------------------------------------------------
  Rule: a public voice is dual-emitted with a namespaced id @<login>/<slug(Name)> (Q1, Q5)

    Scenario: A bound operator's voice is dual-emitted (raw id + namespaced id), attributed
      Given "@bownux" registers an on-air tts offer with model "roger-operator-voice" named "1950s Operator"
      When an anonymous GET /voices arrives
      Then a voice with raw id "roger-operator-voice" is listed
      And that voice's namespaced_id is "@bownux/1950s-operator"
      And that voice's operator is "bownux"
      And no pubkey, node id, bridge URL, or IP appears anywhere in the response

    Scenario: The voice-name segment is normalized (lowercase, spaces->dash, slug)
      Given "@bownux" registers an on-air tts offer with model "front-desk" named "  Front  Desk  VOICE  "
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@bownux/front-desk-voice" is listed

    Scenario Outline: Two operators may share a voice-name without collision
      Given "@bownux" registers an on-air tts offer with model "b-<slug>" named "<name>"
      And "@acme" registers an on-air tts offer with model "a-<slug>" named "<name>"
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@bownux/<slug>" is listed
      And a voice with namespaced_id "@acme/<slug>" is listed
      And the two voices are distinct entries

      Examples:
        | name      | slug      |
        | Operator  | operator  |
        | Reception | reception |

    Scenario: The raw id is preserved verbatim for routing (pickFor still matches it)
      Given "@bownux" registers an on-air tts offer with model "roger-operator-voice" named "Operator"
      When an anonymous GET /voices arrives
      Then a voice with raw id "roger-operator-voice" is listed
      And that voice's namespaced_id is "@bownux/operator"

    Scenario: The legacy/chat routing path is untouched — chat models never appear in /voices
      Given a chat node offering "llama3.2"
      When an anonymous GET /voices arrives
      Then "llama3.2" never appears in /voices as a raw id or a namespaced id

  # --- BOUNDARIES: unbound/anonymous excluded, self-dup, "/" in name, very long ---
    Scenario: An anonymous (unbound) free voice is NOT listed on public /voices (Q2)
      Given an anonymous free tts node offering model "kokoro" named "Kiosk"
      When an anonymous GET /voices arrives
      Then no voice with raw id "kokoro" is listed
      And the voices list is empty

    Scenario: The same operator cannot register two voices with the same normalized name
      Given "@bownux" registers an on-air tts offer with model "op-a" named "Operator"
      When "@bownux" registers another on-air tts offer with model "op-b" named "  operator "
      Then the registration is rejected as a duplicate voice name for this operator
      And an anonymous GET /voices arrives
      And only one "@bownux/operator" is ever listed

    Scenario: One registration carrying two offers that slug identically is rejected
      # a single node cannot bring two voices that collapse to the SAME @bownux/<slug> on air
      # at once — deterministic ids; the whole register is rejected, not silently deduped.
      When "@bownux" registers one node with two tts offers named "Operator" and "  operator "
      Then the registration is rejected as a duplicate voice name for this operator

    Scenario: A voice-name containing "/" cannot forge a second namespace segment
      Given "@bownux" registers an on-air tts offer with model "m-slash" named "acme/official"
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@bownux/acme-official" is listed
      And no voice has namespaced_id "@bownux/acme/official"
      And no voice has namespaced_id "@acme/official"

    Scenario: A leading "@" inside a voice-name cannot forge an operator prefix
      Given "@bownux" registers an on-air tts offer with model "m-at" named "@acme voice"
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@bownux/acme-voice" is listed
      And no voice has namespaced_id "@acme/voice"

    Scenario: An over-long voice-name is slugged to at most 64 runes
      Given "@bownux" registers an on-air tts offer with model "m-long" named a 90-character name
      When an anonymous GET /voices arrives
      Then the listed voice's namespaced_id voice-name segment is at most 64 runes

    Scenario Outline: A name that normalizes to empty is rejected
      Given "@bownux" registers an on-air tts offer with model "m-empty" named "<name>"
      Then the registration is rejected as an empty voice name

      Examples:
        | name  |
        | ---   |
        |       |
        | 我的声音 |

  # ---------------------------------------------------------------------------
  Rule: a voice cannot impersonate a known chat model — prefix denylist, env-overridable (Q3)

    Scenario Outline: A voice-name that PREFIX-matches a chat-model family token is rejected
      Given "@bownux" registers an on-air tts offer with model "m-imp" named "<name>"
      Then the registration is rejected as chat-model impersonation

      Examples:
        | name             |
        | qwen             |
        | qwen3-coder-next |
        | gpt              |
        | gpt-oss-120b     |
        | llama3.2         |
        | claude           |
        | grok             |
        | mistral          |
        | gemma            |
        | deepseek         |
        | phi              |

    Scenario Outline: Case / whitespace variants of a chat-model name still get caught
      Given "@bownux" registers an on-air tts offer with model "m-imp" named "<name>"
      Then the registration is rejected as chat-model impersonation

      Examples:
        | name        |
        | QWEN3       |
        |  Llama 3.2  |
        | GPT-OSS     |
        | Claude-3    |

    Scenario Outline: Unicode/homoglyph masquerade of a chat model is folded then caught
      Given "@bownux" registers an on-air tts offer with model "m-imp" named "<name>"
      Then the registration is rejected as chat-model impersonation

      Examples:
        | name   |
        | ｇｐｔ    |
        | ｑｗｅｎ   |

    Scenario: A genuinely distinct voice name is NOT falsely rejected (no over-block)
      Given "@bownux" registers an on-air tts offer with model "m-1950" named "1950s Operator"
      When an anonymous GET /voices arrives
      Then a voice with namespaced_id "@bownux/1950s-operator" is listed

    Scenario: The impersonation denylist is env-overridable
      Given the impersonation denylist is set to "acme-brand" via the environment
      When "@bownux" registers an on-air tts offer with model "m-brand" named "acme-brand-voice"
      Then the registration is rejected as chat-model impersonation

    Scenario: A voice masquerading as a chat model does NOT reach /voices (guard closes gap)
      Given "@bownux" registers an on-air tts offer with model "roger-operator-voice" named "qwen3-coder-next"
      When an anonymous GET /voices arrives
      Then no voice with a namespaced_id containing "qwen3-coder-next" is listed
      And the offer was rejected at register as chat-model impersonation

  # ---------------------------------------------------------------------------
  Rule: voice name + slug + handle are MODERATED content, reusing b.mod.screen

    Scenario: A slur/abusive voice NAME is rejected by the existing screen at register
      Given a moderation screen that flags the name "abusive-voice"
      When "@bownux" registers an on-air tts offer with model "m-flag" named "abusive-voice"
      Then the registration is rejected with the screen's status
      And no voice with raw id "m-flag" is listed

    Scenario: With screening REQUIRED but unreachable, a voice register fails closed
      Given content screening is required but unreachable
      When "@bownux" registers an on-air tts offer with model "m-fc" named "Operator"
      Then the registration is rejected 503 (fail closed), not served unscreened

    Scenario: With screening DISABLED (dev), a clean voice register passes the screen
      Given content screening is disabled
      When "@bownux" registers an on-air tts offer with model "m-dev" named "Operator"
      Then the screen is not the reason for any rejection
      And an anonymous GET /voices arrives
      And a voice with namespaced_id "@bownux/operator" is listed

  # ---------------------------------------------------------------------------
  Rule: attribution carries the handle only; NO node address ever leaks

    Scenario: voiceView carries operator=login and the app can render "Name · by @operator"
      Given "@bownux" registers an on-air tts offer with model "m-attr" named "Operator"
      When an anonymous GET /voices arrives
      Then the voice's operator field is "bownux"
      And it carries no "@" prefix in the operator field
      And no field named for a host, ip, bridge, url-to-node, or pubkey exists

    Scenario: A banned operator's voices never appear (existing owner-ban invariant holds)
      Given "@bownux" registers an on-air tts offer with model "m-ban" named "Operator"
      And "@bownux" is owner-banned
      When an anonymous GET /voices arrives
      Then no "@bownux/..." voice is listed

    Scenario: A private node stays excluded exactly as from /discover (regression)
      Given "@bownux" registers a PRIVATE tts node with model "m-priv" named "Operator"
      When an anonymous GET /voices arrives
      Then no voice with raw id "m-priv" is listed

    Scenario: The full /voices body still leaks no address with attribution added (security regression)
      Given "@bownux" registers an on-air tts offer with model "m-leak" named "Operator"
      And @bownux's node bridge URL is "https://abc123.trycloudflare.com"
      And its local address is "192.168.1.50:8790"
      When an anonymous GET /voices arrives
      Then the response body contains neither the bridge URL nor the local address
      And every voice with an operator exposes only the login handle, never an address
