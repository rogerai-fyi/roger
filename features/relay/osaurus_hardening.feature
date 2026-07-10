# PENDING founder approval (spec-first workflow step 3) - BUILD-AND-HOLD. This spec is the
# founder-approvable contract for the two OSAURUS-SPECIFIC relay hardenings (finding C +
# model-pin from the source-level Osaurus verification). The PR carries it for review and MUST
# NOT merge before the founder approves the spec.
#
# Context: the relay path allowlist (features/voice/relay_allowlist.feature, already on main)
# made the NODE the trust boundary - it forwards ONLY chat/voice paths, never /agents,/mcp,
# /memory. These two hardenings are the remaining Osaurus-SPECIFIC gaps that were deliberately
# NOT shipped with the allowlist, because they only make sense once the upstream is known to be
# Osaurus:
#
#   1. X-PERSIST: FALSE. Osaurus persists /v1/chat/completions conversations to the OWNER's
#      local chat history by default unless the request carries "X-Persist: false" (a later
#      "history backfill" can then distill that history into the owner's MEMORY). Without the
#      header, public tuner traffic would (a) pollute the owner's chat history and (b) open a
#      prompt-injection path into their memory. The relay already forwards ONLY Content-Type +
#      Authorization (tuners cannot smuggle X-Osaurus-Agent-Id or any other header); this adds
#      ONE node-authored header, "X-Persist: false", and ONLY when the upstream is Osaurus.
#
#   2. MODEL PIN. The relay forwards the tuner's request body as-is, so a tuner could name a
#      DIFFERENT locally-installed model and force Osaurus to load it (memory pressure on the
#      owner's Mac; serving a model the owner never offered). For an Osaurus upstream the node
#      rewrites body.model to the OFFERED model (cfg.Model) - the single model this node put on
#      the band - so the tuner can only ever exercise what was offered.
#
# Ground truth: internal/agent/agent.go serve() (non-stream, builds upReq at the Content-Type /
# Authorization block) and serveStream() (streaming, same header block) - BOTH tuner-reachable,
# so BOTH get the header + the pin. "Is this upstream Osaurus?" is decided ONCE at share time by
# the root-banner fingerprint (detect.isOsaurus, GET / => "Osaurus Server is running!") and
# carried as Config.Osaurus; the relay never re-probes per job.
#
# Scope guard (do-NOT, from the brief): this touches EXACTLY the chat relay to an Osaurus
# /v1/chat/completions upstream. It does NOT relax the shipped allowlist, does NOT reference
# /agents,/mcp,/memory, and does NOT change behavior for any NON-Osaurus backend.
#
# Invariants:
#   B1 On an Osaurus upstream, a relayed request carries the node-authored header
#      "X-Persist: false" (non-stream AND streaming).
#   B2 On a NON-Osaurus upstream, NO X-Persist header is added - byte-identical to today.
#   B3 On an Osaurus upstream, the forwarded body.model is PINNED to the offered model
#      (cfg.Model): a different name is rewritten, an absent/empty name is filled, the correct
#      name is left as the offer. The tuner can never force a model the owner did not offer.
#   B4 On a NON-Osaurus upstream, body.model is forwarded UNCHANGED (no rewrite).
#   B5 Header discipline is otherwise unchanged: the only headers the backend ever sees are the
#      node's Content-Type, Authorization, and (Osaurus-only) X-Persist. No tuner header leaks.
#   B6 A body the node cannot parse as JSON is forwarded byte-for-byte (a request we can't parse
#      can't force-load a model anyway); the X-Persist header is still added on Osaurus (it is
#      independent of the body).
#
# Purely-lexical corners (a body with an unusual but valid model type, the exact header casing)
# are pinned in the table-driven Go tests alongside this BDD (internal/agent).

Feature: Osaurus relay hardening - no history pollution, no model smuggling

  Background:
    Given a local backend that records the path, headers, and body it is hit on
    And the node offers model "gpt-oss-20b"

  # ===========================================================================
  # 1. X-PERSIST: FALSE - only on an Osaurus upstream (B1, B2)
  # ===========================================================================

  Scenario: a chat relay to an Osaurus upstream carries X-Persist false
    Given the upstream is Osaurus
    When the broker relays a chat job
    Then the forwarded request carries the header "X-Persist: false"

  Scenario: a streaming chat relay to an Osaurus upstream carries X-Persist false
    Given the upstream is Osaurus
    When the broker relays a streaming chat job
    Then the forwarded request carries the header "X-Persist: false"

  Scenario: a chat relay to a NON-Osaurus upstream adds no X-Persist header
    Given the upstream is not Osaurus
    When the broker relays a chat job
    Then the forwarded request carries no X-Persist header

  # The STREAM path re-implements the flag logic separately from serve(), so it gets its own
  # non-Osaurus coverage (a regression there would silently mark a non-Osaurus stream no-persist).
  Scenario: a streaming chat relay to a NON-Osaurus upstream adds no X-Persist header
    Given the upstream is not Osaurus
    When the broker relays a streaming chat job
    Then the forwarded request carries no X-Persist header

  # ===========================================================================
  # 2. MODEL PIN - the tuner can only exercise the offered model (B3, B4)
  # ===========================================================================

  Scenario: a tuner naming a different model is pinned back to the offer
    Given the upstream is Osaurus
    When the broker relays a chat job naming model "llama-3.3-70b"
    Then the forwarded body has model "gpt-oss-20b"

  Scenario: a tuner omitting the model is filled with the offer
    Given the upstream is Osaurus
    When the broker relays a chat job naming model ""
    Then the forwarded body has model "gpt-oss-20b"

  Scenario: a tuner naming the offered model passes through as the offer
    Given the upstream is Osaurus
    When the broker relays a chat job naming model "gpt-oss-20b"
    Then the forwarded body has model "gpt-oss-20b"

  Scenario: a streaming tuner naming a different model is pinned back to the offer
    Given the upstream is Osaurus
    When the broker relays a streaming chat job naming model "llama-3.3-70b"
    Then the forwarded body has model "gpt-oss-20b"

  Scenario: a streaming NON-Osaurus upstream forwards the tuner's model unchanged
    Given the upstream is not Osaurus
    When the broker relays a streaming chat job naming model "llama-3.3-70b"
    Then the forwarded body has model "llama-3.3-70b"

  Scenario: a NON-Osaurus upstream forwards the tuner's model unchanged
    Given the upstream is not Osaurus
    When the broker relays a chat job naming model "llama-3.3-70b"
    Then the forwarded body has model "llama-3.3-70b"

  # ===========================================================================
  # 3. DISCIPLINE - no tuner header leak; an unparseable body is not corrupted (B5, B6)
  # ===========================================================================

  Scenario: the forwarded request leaks no tuner headers
    Given the upstream is Osaurus
    When the broker relays a chat job
    Then the backend sees only the node's Content-Type, Authorization, and X-Persist headers

  Scenario: an unparseable body is forwarded byte-for-byte but still marked no-persist
    Given the upstream is Osaurus
    When the broker relays a chat job with an unparseable body
    Then the forwarded body is unchanged
    And the forwarded request carries the header "X-Persist: false"
