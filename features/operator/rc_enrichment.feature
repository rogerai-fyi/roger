# GUEST OPERATORS - deferred follow-up: OPERATOR FRAME ENRICHMENT (iteration 2 design).
#
# TODAY: the RC "guest has the mic" status frame (internal/client/rc.go OperatorStatusFrame)
# carries only the guest NAME (RCFrame.Operator, additive since Phase 2). A remote viewer can
# say WHO holds the mic but not WHAT they run on or HOW MUCH they have spent.
#
# THIS FOLLOW-UP adds two ADDITIVE, omitempty metadata fields to protocol.RCFrame -
#   Model string  `json:"model,omitempty"`  the tuned band's public model identity
#   Spend float64 `json:"spend,omitempty"`  the HOST's own session spend, in dollars
# - populated from the LIVE proxy holder (client.ProxyOptionsHolder: Get().Model for the
# model, Spent() for the spend) so a remote viewer (iOS / web / desktop) can render
# "opencode has the mic on gpt-oss-120b · $0.19" instead of the bare name. The iOS app
# (merged) already TOLERATES these fields (its decoder is lenient; its tests pin the
# tolerance) - this spec makes the HOST populate them.
#
# ── FOUNDER RULINGS APPLIED (2026-07-07, delegated defaults) ────────────────────────────
#   1. SPEND on frames: YES - the host's OWN money figure, to the host's OWN same-account
#      paired devices (an RC viewer is same-account by construction).
#   2. BAND: DROPPED for v1. There is NO Band field on RCFrame at all - the model conveys
#      the station identity. CRITICAL INVARIANT: ProxyOptions.Freq is the private-band
#      SECRET (hash-at-rest, rc_secrets.feature S1-S5) and must NEVER appear on ANY frame
#      field. The scenarios below pin both: no band key ever rides the wire, and the raw
#      Freq code never appears in any frame field.
#   3. Display copy where enrichment renders: "<op> has the mic on <model> · $<spend>".
#
# ── CONTENT-BLIND INVARIANT (the RC money/privacy contract - NON-NEGOTIABLE) ────────────
# Enrichment adds ONLY model/spend METADATA. The guest operator's prompts, terminal
# output, tool I/O, and any assistant text MUST NEVER ride an RCFrame - the guest session is
# NOT the DJ tee loop, and the broker relay stays content-blind (rc_content_blind.feature).
#   * Model  - the tuned band's model id is ALREADY public (advertised via /discover /market).
#              It is a station identity, not guest content. SAFE.
#   * Spend  - the HOST's OWN money figure, shown to the HOST's OWN same-account paired
#              devices. It is the same number the desk return-summary already prints
#              locally. Metadata, not guest content.
#
# ── ADDITIVE / DEGRADE-CLEAN (the persistent-state lesson) ──────────────────────────────
# Both fields are omitempty: an OLD viewer ignores unknown keys; an un-tuned / spend-0
# state simply omits them from the wire - a viewer never sees a bogus "$0" or an empty
# "on ". The DJ-back frame carries NONE of them (nothing runs; there is no operator) -
# exactly as today. The frame KIND stays "status" (no new kind; the reserved operator_*
# kinds stay behavior-free in v1, ruling 7).

Feature: The guest-has-the-mic frame carries model/spend metadata - never guest content
  A remote viewer watching a live BASE STATION handoff can see not just WHO holds the mic
  but the public model they run on and the host's running spend, so the phone/web/
  desktop can render "opencode has the mic on gpt-oss-120b · $0.19". The enrichment is
  additive metadata only: no guest prompt, output, or tool I/O ever touches a frame, and
  no frame field can ever carry the private-band frequency secret (there is no Band
  field at all, per the v1 ruling).

  Background:
    Given a HOST agent session with an attached remote-control bridge
    And a tuned band "gpt-oss-120b" and a detected guest "opencode"

  # ── E1: the handoff-start frame is enriched, spend starts at zero ──────────────────────
  Scenario: The handoff-start status frame carries the model and zero spend
    When the handoff to "opencode" begins
    Then a frame of kind "status" is emitted before the exec
    And the frame names the operator "opencode"
    And the frame carries the model "gpt-oss-120b"
    And the frame carries a spend of $0.00
    And no emitted frame's wire JSON carries a "band" key

  # The spend accumulator is freshly reset at exec time (ResetSpend in onOperatorExec), so a
  # start frame must never inherit a previous guest's total.
  Scenario: The start frame's spend is the freshly-reset session total, not a stale one
    Given a previous handoff left the spend accumulator at $4.20
    When the handoff to "opencode" begins
    Then the handoff-start frame carries a spend of $0.00

  # ── E2: parked auto-frames carry the LIVE, updated spend ───────────────────────────────
  Scenario: A parked-turn status frame reports the guest's spend so far
    Given the guest has the mic
    And the guest has spent $0.19 so far
    When a viewer sends a turn "refactor the parser"
    Then the viewers receive a status frame saying the guest has the mic
    And that status frame carries a spend of $0.19
    And that status frame carries the model "gpt-oss-120b"

  Scenario: A backfill answered mid-handoff carries the live enriched status frame
    Given the guest has the mic
    And the guest has spent $1.05 so far
    When a new viewer attaches and requests backfill
    Then the viewer receives the transcript snapshot
    And the viewer receives a status frame carrying a spend of $1.05

  # ── E3: the DJ-back frame carries NO enrichment (nothing runs) ─────────────────────────
  Scenario: The DJ-is-back status frame carries no operator, model, or spend
    Given the guest has the mic
    When the guest returns and the bridge unparks
    Then a frame of kind "status" is emitted announcing the DJ is back
    And that frame carries no operator, model, or spend
    And its wire JSON omits the "operator", "model", and "spend" keys entirely

  # ── E4: omitempty - additive, degrades cleanly for old viewers / un-tuned state ────────
  Scenario: Empty enrichment fields are omitted from the wire, not sent as zero values
    When an operator status frame is built with no model and zero spend
    Then its wire JSON omits the "model" key
    And its wire JSON omits the "spend" key
    But its wire JSON still carries the "operator" and "kind" keys

  Scenario: An open-market handoff with no private band still enriches model and spend
    Given the tuned channel is the open market with no private band
    When the handoff to "opencode" begins
    Then the handoff-start frame carries the model "gpt-oss-120b"
    And the handoff-start frame carries a spend of $0.00
    And no emitted frame's wire JSON carries a "band" key

  # ── E5: CONTENT-BLIND - the hard invariant ─────────────────────────────────────────────
  Scenario: No enriched frame ever carries the guest's terminal output or prompts
    When the handoff to "opencode" begins and the guest works for a while
    Then every frame emitted during the handoff is a status frame
    And no frame carries any guest terminal output, prompt, tool call, or assistant text
    And each status frame carries only operator, model, and spend metadata

  Scenario: The private-band frequency SECRET never appears on any frame field
    # ProxyOptions.Freq is a hash-at-rest shared secret (rc_secrets.feature S1-S5). There
    # is NO Band field to carry a band identity at all (v1 ruling), and the raw Freq code
    # must never appear on any frame field - not Text, not Model, not anywhere.
    Given the tuned band has the private frequency code "8F3K-9M2Q"
    When the handoff to "opencode" begins
    Then no emitted frame carries the text "8F3K-9M2Q" in any field
    And the RCFrame wire type carries no band field at all

  # ── E6: ONE shared constructor - host start-frame and bridge parked-frame never drift ──
  Scenario: The handoff-start frame and the parked auto-frame are built the same way
    Given the guest has the mic on "gpt-oss-120b"
    And the guest has spent $0.50 so far
    When the handoff-start frame and a parked-turn frame are compared
    Then both carry the same operator and model
    And both are kind "status" carrying the "guest has the mic" text
