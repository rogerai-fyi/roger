# PROVIDER HOST COMPATIBILITY — one protocol contract, explicit discovery tiers.
#
# Scope:
#   - zero-config detection of Unsloth Studio's OpenAI-compatible API;
#   - guided setup and public wording that accurately distinguish automatic
#     detection from universal explicit-endpoint compatibility;
#   - a public compatibility/gaps note that states the actual relay surface.
#
# Interfaces:
#   internal/detect (default endpoint + environment discovery), internal/tui
#   (guided setup), cmd/rogerai (headless guidance), README.md, and a public
#   docs/hosting-compatibility.md.
#
# Out of scope:
#   - embedding RogerAI's own inference engine or launching/stopping model hosts;
#   - supporting proprietary/non-OpenAI upstream protocols;
#   - claiming every endpoint from every vendor is compatible without a protocol
#     conformance test;
#   - forwarding Unsloth's code-execution, web-search, admin, MCP, Responses, or
#     Anthropic Messages routes. The node's existing route allowlist remains closed.
#
# Ground truth (official Unsloth repository/docs, checked 2026-07-30):
#   - Unsloth Studio defaults to http://127.0.0.1:8888 (`unsloth studio -p 8888`);
#   - it serves GET /v1/models and POST /v1/chat/completions;
#   - its API is authenticated with sk-unsloth-* keys;
#   - it documents UNSLOTH_STUDIO_AUTH_TOKEN as the variable holding that key, and
#     documents NO client-side base-URL variable (the port is a server-side flag).
#
# RogerAI-side convention (ours, NOT exported by Unsloth):
#   - UNSLOTH_STUDIO_URL points RogerAI at a Studio started on a non-default port;
#   - UNSLOTH_API_KEY is an alias for the key, named after the other hosts in our
#     detector table. The documented UNSLOTH_STUDIO_AUTH_TOKEN wins when both are set.
#
# Product invariant:
#   A model's trainer, optimizer, quantizer, file source, and hosting brand do not
#   determine RogerAI compatibility. The served HTTP contract does. A host is
#   shareable for chat when it implements the OpenAI-compatible Models and Chat
#   Completions behavior RogerAI consumes. Named integrations improve discovery
#   and setup; they are not an allowlist of permitted vendors.

Feature: Share from any compatible model host
  RogerAI providers can share a model from any host that implements RogerAI's
  documented OpenAI-compatible upstream contract, while common hosts receive
  zero-config discovery and honest, specific setup guidance.

  Scenario: Unsloth Studio is detected on its default endpoint without configuration
    Given Unsloth Studio serves a model at "http://127.0.0.1:8888/v1"
    And its Models endpoint does not require an API key
    When RogerAI scans for local model hosts
    Then the endpoint is detected with the host label "unsloth"
    And every model returned by its Models endpoint is available to share

  # The variable Unsloth's own docs tell a provider to export. A Studio user who
  # followed Unsloth's setup and nothing else must still be zero-config here.
  Scenario: An authenticated Unsloth Studio uses the key Unsloth documents
    Given Unsloth Studio serves a model at "http://127.0.0.1:8888/v1"
    And its Models endpoint requires the key "sk-unsloth-documented"
    And "UNSLOTH_STUDIO_AUTH_TOKEN" is "sk-unsloth-documented"
    When RogerAI scans for local model hosts
    Then the endpoint is detected with the host label "unsloth"
    And RogerAI remembers the key only in the local provider configuration

  Scenario: An authenticated Unsloth Studio uses RogerAI's key alias
    Given Unsloth Studio serves a model at "http://127.0.0.1:8888/v1"
    And its Models endpoint requires the key "sk-unsloth-local"
    And "UNSLOTH_API_KEY" is "sk-unsloth-local"
    When RogerAI scans for local model hosts
    Then the endpoint is detected with the host label "unsloth"
    And RogerAI remembers the key only in the local provider configuration

  Scenario: An authenticated Unsloth Studio with no exported key asks for one
    Given Unsloth Studio serves at "http://127.0.0.1:8888/v1"
    And its Models endpoint requires an API key
    And "UNSLOTH_STUDIO_AUTH_TOKEN" is empty
    And "UNSLOTH_API_KEY" is empty
    When RogerAI scans for local model hosts
    Then RogerAI reports that "http://127.0.0.1:8888/v1" needs a key
    And RogerAI does not report that no model host exists

  Scenario: A custom Unsloth Studio URL and key are paired
    Given "UNSLOTH_STUDIO_URL" is "http://127.0.0.1:8899"
    And "UNSLOTH_API_KEY" is "sk-unsloth-custom"
    And that endpoint serves an authenticated OpenAI-compatible Models endpoint
    When RogerAI scans for local model hosts
    Then "http://127.0.0.1:8899/v1" is detected with the host label "unsloth"
    And only "sk-unsloth-custom" is sent to that configured endpoint first

  Scenario: A stale Unsloth key does not create a false positive
    Given "UNSLOTH_STUDIO_URL" is "http://127.0.0.1:8899"
    And "UNSLOTH_API_KEY" is "sk-unsloth-stale"
    And that endpoint rejects the key
    When RogerAI scans for local model hosts
    Then the endpoint is reported as needing a key
    And no Unsloth model is advertised

  Scenario: Blind open-port discovery never sprays the Unsloth key
    Given "UNSLOTH_API_KEY" is "sk-unsloth-secret"
    And an unknown service requiring authentication listens on an unrelated open port
    When RogerAI scans listening ports
    Then "sk-unsloth-secret" is not sent to the unknown service
    And the existing blind-port credential boundary is unchanged

  Scenario: A host with no named integration remains shareable
    Given a model host implements GET "/v1/models"
    And it implements POST "/v1/chat/completions"
    And it runs at a user-supplied URL
    When the provider runs "roger share --upstream <url>"
    Then RogerAI verifies and shares the selected model
    And the host brand is not required to appear in RogerAI's detector table

  Scenario Outline: Explicit endpoint forms normalize to the same chat endpoint
    Given a compatible host is supplied as "<input>"
    When RogerAI prepares the upstream
    Then the chat endpoint is "http://127.0.0.1:9999/v1/chat/completions"

    Examples:
      | input                                                |
      | http://127.0.0.1:9999                               |
      | http://127.0.0.1:9999/v1                            |
      | http://127.0.0.1:9999/v1/chat/completions           |

  Scenario: Guided setup names Unsloth without implying an embedded runtime
    Given no compatible local model host is detected
    When RogerAI opens guided provider setup
    Then the choices include "Unsloth Studio"
    And its guidance says to load a model and enable or copy its API endpoint and key
    And the choices still include "Other - paste a URL"
    And RogerAI does not install, launch, or manage Unsloth

  Scenario: Headless failure guidance distinguishes discovery from compatibility
    Given no compatible local model host is detected
    When "roger share" exits with setup guidance
    Then it names common automatically detected hosts including Unsloth
    And it says any other OpenAI-compatible host works with "--upstream <url>"
    And it does not claim the named hosts are the complete compatibility list

  Scenario: Public copy explains the portable contract before listing brands
    When a provider reads the sharing documentation
    Then it says any preferred model host can be used when it exposes the supported OpenAI-compatible API
    And it separates "auto-detected" hosts from hosts connected with "--upstream"
    And Unsloth appears as an auto-detected host
    And it explains that an Unsloth-trained or Unsloth-quantized model served by another compatible host also works

  Scenario: The compatibility note publishes current gaps without overclaiming
    When a provider reads the hosting compatibility note
    Then it defines "verified", "auto-detected", and "compatible by protocol" as different support levels
    And it lists Models plus Chat Completions as the chat-provider upstream contract
    And it lists speech and transcription as separate optional contracts
    And it says Responses, Anthropic Messages, embeddings, reranking, image generation, and host admin routes are not relayed upstream today
    And it says custom or remote endpoints may require "--upstream" and "--upstream-key"
    And it says compatibility depends on response shapes, streaming SSE, and usage reporting rather than brand alone

  Scenario: RogerAI does not become another inference-engine wrapper
    When support for a new model host is added
    Then RogerAI detects or connects to the host through its existing HTTP API
    And RogerAI does not download model weights
    And RogerAI does not choose quantization or GPU offload settings
    And RogerAI does not own the host process lifecycle
