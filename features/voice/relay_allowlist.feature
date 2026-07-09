# PENDING founder approval (spec-first workflow step 3) - BUILD-AND-HOLD. This spec is the
# founder-approvable contract for the RELAY PATH ALLOWLIST hardening; the PR carries it for
# review and MUST NOT merge before the founder approves the spec.
#
# Finding A from the Osaurus verification, valuable for ALL backends (not just Osaurus).
#
# The gap being closed: agent.serve() (internal/agent/agent.go) derives the LOCAL upstream
# endpoint from the broker-supplied job.Path and POSTs the body there over loopback, which the
# local backend treats as authenticated. Before this change ANY non-chat Path was forwarded
# verbatim (target = TrimSuffix(Upstream,"/chat/completions") + TrimPrefix(Path,"/v1")), so a
# compromised or buggy broker could steer the node onto a dangerous local route (e.g. an
# Osaurus /agents/{id}/run or /mcp/call) that loopback trusts. The NODE is the trust boundary:
# it must forward ONLY an allowlisted set of upstream paths and refuse everything else BEFORE
# the request ever reaches the local backend.
#
# Ground truth: internal/agent/agent.go serve() (the target-derivation + POST) and isSpeechPath;
# the broker only ever dispatches three real relay Paths - chat (absent / "/v1/chat/completions",
# cmd/rogerai-broker tunnel.go/concierge.go/probe.go set no Path), TTS "/v1/audio/speech" and
# STT "/v1/audio/transcriptions" (cmd/rogerai-broker/audio.go audioSpec.path).
#
# The guard is deliberately SIMPLE and FORGIVING: one predicate (isAllowedUpstreamPath) that,
# after a MINIMAL safe normalization (cleanUpstreamPath: trim whitespace, then path.Clean to
# collapse "//", resolve "." / "..", strip a trailing slash), EXACT-matches the canonical set,
# reusing the code's own isSpeechPath so adding a modality is a one-line change:
#   - chat completions: absent path (chat back-compat), "/v1/chat/completions", "/chat/completions"
#   - TTS  "/v1/audio/speech"   (isSpeechPath)
#   - STT  "/v1/audio/transcriptions"
# Matching is case-sensitive (the relay treats paths case-sensitively). Normalization only
# collapses cosmetic noise; path.Clean resolves ".." AWAY from a canonical path, so it can never
# manufacture an allowed path out of a dangerous one - the traversal/slash/encoding tricks below
# all still fail the exact match.
#
# Invariants:
#   A1 an allowlisted path forwards to the local backend exactly as before (chat/voice happy
#      paths are byte-identical - this change adds a gate, it does not touch them).
#   A2 a NON-allowlisted path is REFUSED with a clean 404 "unsupported path" and the local
#      backend is NEVER contacted (assert zero backend hits).
#   A3 the refusal leaks nothing sensitive (no upstream URL, no key) and never crashes.
#   A4 header discipline is unchanged - only Content-Type + Authorization are ever forwarded.
#   A5 a cosmetic variant of an allowlisted path (trailing slash, surrounding whitespace, a
#      collapsible "//") is normalized and forwarded to its canonical endpoint.
#
# Purely-lexical adversarial corners that Gherkin table cells cannot carry faithfully (leading/
# trailing whitespace - godog trims cells - NUL bytes, backslashes, control chars, and the
# whitespace/trailing-slash TOLERANCE of A5) are pinned exhaustively in the table-driven Go tests
# TestIsAllowedUpstreamPath + TestCleanUpstreamPath (internal/agent).

Feature: Relay path allowlist - the share node is the trust boundary

  Background:
    Given a local backend that records every request path it is hit on
    And a share node relaying to that backend

  # ===========================================================================
  # 1. ALLOWED - the three real relay modalities (A1)
  # ===========================================================================

  Scenario: a chat job with an absent path forwards to the chat upstream unchanged
    When the broker relays a job with an absent path
    Then the backend is hit once at "/v1/chat/completions"
    And the node returns the backend's response

  Scenario: the explicit chat completions path is forwarded
    When the broker relays a job with path "/v1/chat/completions"
    Then the backend is hit once at "/v1/chat/completions"
    And the node returns the backend's response

  Scenario: the /chat/completions alias is forwarded
    When the broker relays a job with path "/chat/completions"
    Then the backend is hit once at "/v1/chat/completions"
    And the node returns the backend's response

  Scenario: the TTS speech path is forwarded
    When the broker relays a job with path "/v1/audio/speech"
    Then the backend is hit once at "/v1/audio/speech"
    And the node returns the backend's response

  Scenario: the STT transcriptions path is forwarded
    When the broker relays a job with path "/v1/audio/transcriptions"
    Then the backend is hit once at "/v1/audio/transcriptions"
    And the node returns the backend's response

  # ---------------------------------------------------------------------------
  # 1b. FORGIVING - a cosmetic variant of an allowlisted path is normalized and
  #     forwarded to its canonical endpoint (A5). (Leading/trailing whitespace
  #     tolerance is pinned in TestCleanUpstreamPath - godog trims table cells.)
  # ---------------------------------------------------------------------------

  Scenario: a trailing slash on the speech path is normalized and forwarded
    When the broker relays a job with path "/v1/audio/speech/"
    Then the backend is hit once at "/v1/audio/speech"
    And the node returns the backend's response

  Scenario: a trailing slash on the chat path is normalized and forwarded
    When the broker relays a job with path "/v1/chat/completions/"
    Then the backend is hit once at "/v1/chat/completions"
    And the node returns the backend's response

  Scenario: a collapsible double slash inside the speech path is normalized and forwarded
    When the broker relays a job with path "/v1//audio/speech"
    Then the backend is hit once at "/v1/audio/speech"
    And the node returns the backend's response

  # ===========================================================================
  # 2. REFUSED - a broker-supplied path outside the allowlist never reaches the
  #    local backend (A2, A3). Each is refused with a clean 404.
  # ===========================================================================

  Scenario Outline: a non-allowlisted path is refused without touching the backend
    When the broker relays a job with path "<path>"
    Then the node refuses with a 404 "unsupported path"
    And the backend is never hit
    And the refusal body leaks neither the upstream URL nor the upstream key

    Examples: dangerous local routes
      | path                |
      | /agents/run         |
      | /agents/abc-123/run |
      | /mcp/call           |
      | /memory/anything    |
      | /pair               |
      | /secure/x           |
      | /admin/reset        |

    Examples: traversal and slash tricks (path.Clean resolves these AWAY from a canonical path)
      | path                    |
      | /v1/../agents/run       |
      | /v1/audio/../agents/run |
      | //agents/run            |
      | /v1//agents/run         |
      | /v1/chat/../agents/run  |
      | /v1/audio/speech/../run |

    Examples: case variants (matching is case-sensitive)
      | path                  |
      | /V1/Chat/Completions  |
      | /v1/audio/SPEECH      |
      | /V1/AUDIO/SPEECH      |

    Examples: absolute URLs and schemes in the path
      | path                          |
      | http://127.0.0.1/agents/run   |
      | https://evil.example/mcp/call |
      | file:///etc/passwd            |

    Examples: query strings smuggling an agent route
      | path                                  |
      | /v1/audio/speech?x=/agents/run        |
      | /v1/chat/completions?path=/agents/run |
      | /?/agents/run                         |

    Examples: percent-encoded traversal and lookalikes
      | path                       |
      | /v1/audio/%2e%2e/agents    |
      | /v1/%2e%2e/agents/run      |
      | /agents%2frun              |

    Examples: empty-ish and lookalike suffixes
      | path                     |
      | /                        |
      | /chat/completions/extra  |
      | /agents/run/completions  |
      | /evil/audio/speech       |
      | /audio/speech            |
