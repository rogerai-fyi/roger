# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# Scope: the operator-facing account flow for `roger-tower` — when an account is needed,
# when it is not, and what the operator sees. The enrollment PROTOCOL (tokens, CSRs,
# certificates, lifecycle) is public_enrollment.feature; this is the human half.
#
# FOUNDER RULING 2026-08-02, after a security review of anonymous registration:
# the line is drawn at "does this Tower carry OTHER PEOPLE'S traffic?", not at money.
#
#   standalone — no account, ever. Nothing leaves the operator's machine, so there is no
#                one to be accountable to. This is the whole try-it-now path.
#   joined     — an account is required to register at all.
#
# The reasoning, recorded so it is not re-litigated from scratch: anonymity grants a
# joined Tower no new cryptographic power (it is already treated as hostile, relays
# ciphertext, and cannot forge work or alter settlement). What it changes is that
# availability cannot be forced cryptographically, so the defences are health scoring,
# probation and revocation — all per-identity. If identities are free, revocation stops
# being a penalty and becomes a speed bump: ban a Tower, it re-registers in seconds. An
# account is what makes revocation cost something. It also bounds sybil fleets, which the
# linked-entity collusion controls in operator_revenue_share.feature depend on.

Feature: An operator signs in only when their Tower will carry other people's traffic
  Standalone is usable by a stranger in under a minute with no account. Joining the
  public network is a deliberate, account-bound act.

  # --- standalone needs nobody ---------------------------------------------

  Scenario: A standalone Tower runs its whole lifecycle with no account
    Given an operator who has never signed in to RogerAI
    When they initialize a standalone Tower, admit a local client, attach a Station, and route a request
    Then every step succeeds
    And at no point are they asked to sign in
    And no RogerAI account, token, or network call is involved

  Scenario: Signing in is not offered where it would be meaningless
    Given a standalone Tower
    When the operator reads its status or help
    Then no login prompt, account state, or earnings figure is shown
    And the interface does not imply an account would unlock anything locally

  Scenario: A standalone Tower cannot be registered to the public network
    Given a Tower initialized in standalone mode
    When the operator attempts to register it with RogerAI
    Then the attempt is refused because the mode is standalone
    And the error explains that joining requires a new data directory initialized as joined

  # --- joined requires an account -------------------------------------------

  Scenario: Registering a joined Tower requires a signed-in operator
    Given a Tower initialized in joined mode
    And the operator is not signed in
    When they attempt to register it
    Then registration is refused before any network call that would create state
    And the message tells them to sign in first

  Scenario: Signing in binds this machine's Tower identity to the operator's account
    Given an operator runs the sign-in command
    When they complete the browser device flow and authorize
    Then the Tower's identity key is bound to their RogerAI account
    And the operator is told which account they are signed in as
    And no browser token or account secret is written to the Tower's configuration

  Scenario: Sign-in state is stored owner-only and is never printed
    Given an operator has signed in
    When credentials are persisted and later read
    Then they are stored with owner-only permissions
    And no command prints, logs, or exports the stored token

  Scenario Outline: Sign-in fails closed and says why
    Given an operator runs the sign-in command
    When "<condition>" occurs
    Then no partial or unusable credential is stored
    And the operator is told what happened in terms they can act on

    Examples:
      | condition                                 |
      | they deny the authorization               |
      | the device code expires before they finish |
      | the network is unreachable                |
      | RogerAI rejects the account               |
      | the account is suspended                  |

  Scenario: Signing out removes local credentials without touching the Tower identity
    Given a signed-in operator
    When they sign out
    Then the stored account credential is removed
    And the Tower's own identity key and data directory are untouched
    And an already-registered Tower keeps serving until its lease expires or is revoked

  # --- what an account is and is not for ------------------------------------

  Scenario: An account is required to register, not to be trusted
    Given an operator registers a joined Tower with a verified account
    When the Tower's trust state is described
    Then it starts in quarantine like any other newly enrolled Tower
    And having an account does not confer trust, priority, or a badge

  Scenario: Compensation requires more than an account
    Given a registered joined Tower whose operator has an account
    When they ask about earning the revenue share
    Then they are told that admission alone earns nothing
    And that the compensated tier additionally requires a verified payout identity and accepted terms

  Scenario: The reason for the account requirement is stated honestly
    When the interface explains why joining needs an account
    Then it says that a Tower relays other people's traffic and must remain accountable
    And it does not claim that an account makes the Tower trusted or verified

  # --- ease of use ----------------------------------------------------------

  Scenario: The first-run path tells an operator which mode they want
    Given an operator runs the Tower with no arguments
    When the usage is shown
    Then it distinguishes standalone from joined in one line each
    And it states plainly that standalone needs no account

  Scenario: A joined-only command run on a standalone Tower explains itself
    Given a standalone Tower
    When the operator runs a joined-only command
    Then the failure names the mode as the reason
    And it does not read as an internal error
