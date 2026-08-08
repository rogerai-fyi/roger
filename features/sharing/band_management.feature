# PRIVATE BAND MANAGEMENT: an owner can SEE, MOVE, REVOKE and RE-MINT their own bands.
#
# THE INCIDENT (2026-08-07, eager-puma-54): the founder pressed `h` on qwen3-vl-8b to put it
# on a private band and got:
#
#   ! could not change qwen3-vl-8b visibility: register with https://broker.rogerai.fyi: brok
#
# Three separate failures in one line. (1) The message was truncated exactly where the
# broker's reason began, so the cause was invisible. (2) The stop-then-start took a WORKING
# public share off air on failure. (3) The real reason - "private band limit reached (free
# plan allows 1) - revoke an existing band first" - named an action NO CLIENT COULD PERFORM:
# there is no `roger bands`, no revoke in the TUI, and the website's list silently renders
# "No private bands yet" for everyone. The owner's one band was on a DIFFERENT machine
# (roggentoo-gemma-4-31b-8086) and nothing in any UI could have told them that.
#
# (1) and (2) are fixed and pinned in internal/node (ErrReason + PrivateResult.Restored).
# THIS spec covers (3): the missing management surface, and the missing ability to MOVE a
# band to a different model without destroying it.
#
# THE CONTRACT:
#   - An owner can always SEE their bands, and every refusal that mentions a band names
#     the band that is in the way and the key that resolves it. An error never names an
#     action the product cannot perform.
#   - A band is a DURABLE IDENTITY the owner holds, not a side effect of one model. Moving
#     it to another model KEEPS the frequency code, so everyone tuned in stays connected.
#   - Rotating a code is always EXPLICIT and always states who it cuts off. The shown-once
#     model is untouched: a code is never re-viewable, only replaceable.
#   - A node that loses its band NEVER silently becomes public. Privacy fails CLOSED.
#
# GROUND TRUTH (today, before this spec):
#   cmd/rogerai-broker/main.go:748-750: exactly three routes - GET /bands (list, owner),
#     DELETE /bands/{id} (revoke, owner-scoped), POST /bands/resolve (public tune-in).
#     There is NO mint endpoint: mintBandForNode has ONE call site, tunnel.go:528, inside
#     /nodes/register. A band can only be born by putting a model on air privately.
#   cmd/rogerai-broker/band.go:35-43: mintBandForNode refuses over quota with the 403
#     "private band limit reached (free plan allows 1) - revoke an existing band first".
#   internal/store/band.go:59-65: BandQuota is a flat 1. ExpiresAt exists but is NEVER
#     assigned anywhere; Label and Models exist but have NO WRITE PATH.
#   internal/store/band.go:29 + internal/agent/agent.go:1044: Band.NodeID is set once at
#     CreateBand and never updated, and node id is "<station>-<model>" - so a band is hard
#     bound to ONE model, and no store method can move it.
#   cmd/rogerai-broker/tunnel.go:521-531: on re-register an EXISTING unrevoked band owned
#     by the same owner is REUSED (no new code, quota not consulted); a revoked one falls
#     through to a fresh mint. This is the seam the MOVE below rides on.
#   internal/client/rc.go:96-121: ListBands is the ONLY band client. No RevokeBand exists.
#   internal/tui/rc.go:417-481: BASE STATION [p] RENDERS bands but m.rcCursor only ever
#     indexes m.rcSessions - band rows are not selectable, and there is no revoke hook.
#   web/src/js/private.js:16,89: the website lists bands with credentials:"include" (cookie
#     only), but cmd/rogerai-broker/auth.go:127-137 requireOwner reads X-Roger-Pubkey
#     EXCLUSIVELY and never a cookie - so the page always 403s and the .catch renders
#     "No private bands yet" to every owner. Verified live 2026-08-07.
#
# Scope:
#   - PATCH /bands/{id}: rebind node_id (the MOVE), and the first write path for label;
#     store UpdateBand alongside SetBandRevoked.
#   - client.RevokeBand + client.MoveBand + a richer BandRow (node_id, created_at).
#   - TUI BASE STATION [p]: bands become SELECTABLE, with n / r / x per the keymap already
#     specified in docs-internal/REMOTE-CONTROL-DESIGN.md:401-403.
#   - The quota refusal becomes an OFFER to move, not a dead end.
#   - Fix the website's band list authentication.
#   - Correct web/src/manual.html:802, which promises "$5 packs (Phase 2)" that do not exist.
#
# Out of scope (deliberately, and why):
#   - BUYING band slots. There is no SKU, no entitlement table, and BandQuota ignores its
#     owner argument. Founder call 2026-08-07: fix the false claim now, build the purchase
#     as its own scoped piece. Nothing here may imply a purchase exists.
#   - Band EXPIRY. ExpiresAt is plumbed end to end but never assigned; it stays 0 = never.
#     bandView still reports it, so clients must tolerate a non-zero value they never see.
#   - The Models allow-list write path. It has no consumer while one node serves one model.
#   - Re-viewing a lost code. The shown-once model is a security property, not a gap; the
#     remedy is re-mint, and it stays that way.
#
# Enforced by: this feature (executable) + cmd/rogerai-broker/band_move_bdd_test.go +
#   cmd/rogerai-broker/band_test.go + internal/store/band_test.go + internal/tui/
#   band_manage_test.go + internal/client/band_test.go.

Feature: Private band management - see it, move it, revoke it

  # ── the refusal that started this ──────────────────────────────────────────

  Scenario: The quota refusal names the band in the way, not just the limit
    Given an owner already holds their one free band on another model
    When they try to put a second model on a private band
    Then the refusal names the model that band is currently on
    And it names the machine that model is on, because it may not be this one
    And it offers to MOVE that band to this model instead
    And it never suggests buying more bands, because no purchase path exists

  Scenario: A refusal never names an action the product cannot perform
    Then no band error tells the owner to revoke unless a revoke key is reachable from the surface showing that error

  # ── MOVING a band: the code survives ───────────────────────────────────────
  # The point of the whole feature. A band is the owner's durable identity; the model it
  # points at is an implementation detail they are allowed to change.

  Scenario: Moving a band to another model keeps the frequency code alive
    Given an owner holds a band on model A
    When they move it to model B
    Then the band keeps its id, its code, and its masked display
    And everyone already tuned in to that code reaches model B without being re-told anything
    And no new band is minted, so the quota is unchanged

  Scenario: A move is refused when the destination already has its own band
    Given model B already carries a band
    When the owner tries to move another band onto model B
    Then the move is refused, because one node carries at most one band
    And both bands are left exactly as they were

  Scenario: A band can only be moved by the owner who holds it
    When anyone other than the issuing owner tries to move a band
    Then it is refused, and the refusal does not reveal whether that band exists

  Scenario: Moving to a model that is not on air yet is allowed
    Given the destination model is not currently broadcasting
    When the owner moves the band to it
    Then the move is accepted
    And the band binds when that model next goes on air privately, minting nothing new

  # ── the safety invariant: privacy fails CLOSED ─────────────────────────────

  Scenario: A node that lost its band never silently becomes public
    Given a private node whose band has been moved away
    When that node re-registers
    Then it does NOT appear on the public market
    And it does not quietly mint a replacement band to stay reachable
    And its operator is told the band moved, and which model holds it now

  # ── carried over from the approved band specs (must not regress) ───────────

  Scenario: Managing bands changes nothing about secrecy or discovery
    Then a private band stays invisible to /discover and /market
    And the one-time code is still shown only at mint
    And the global price ceiling still binds a private band
