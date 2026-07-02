# APPLE-WEB SESSION ISOLATION — a cross-account takeover + PII/earnings leak (RELEASE BLOCKER,
# audit finding #3). The web session cookie is a signed payload login|githubID|wallet|exp
# (auth.go signSessionWallet). For an Apple web login (apple_web.go authAppleWebCallback) the
# login is set to the token's email, or the LITERAL "apple" when Apple omits the email claim
# (which it does on repeat Services-ID logins) — with githubID=0 and wallet=u_apple_<hash(sub)>.
# Downstream, SIX handlers resolve the owner with b.db.OwnerByLogin(sessionLogin) or key a WRITE
# on it:
#   - accountGet (auth.go:407): leaks email + connect + earnings split
#   - accountExport (account.go:39): leaks email + operator ledger + payouts
#   - accountPatch (auth.go:433): UpdateAccount(login) — WRITES the wrong owner's email
#   - accountDelete (account.go:97,109): OwnerByLogin + DeleteAccount(login) — DELETES the wrong owner
#   - grantsOwner (grants_http.go:59): manage the wrong owner's grant keys
#   - payoutOwner (payouts.go:125): the wrong owner's payout state
# Since the GitHub account github.com/apple has Login=="apple", an anonymous Apple-web visitor
# whose token lacks an email takes over that operator. Two no-email Apple users also collide
# with EACH OTHER on the literal "apple".
#
# ROOT INVARIANT: a session's login may resolve a GitHub owner ONLY for a GitHub session, and a
# GitHub session is EXACTLY githubID != 0 (GitHub always assigns a non-zero id; Apple/web is 0).
#
# DECISIONS PINNED BY THIS SPEC (founder-approved: "fix everything"):
#   A1  SINK GATE (the security guarantee): every session->owner resolution keyed on the session
#       login resolves a GitHub owner ONLY when the session's githubID != 0. A githubID==0
#       (Apple/web) session NEVER matches a GitHub owner row for read OR write (grants, payouts,
#       account get/patch/delete/export).
#   A2  SOURCE HARDENING (defense in depth): an Apple web session's login is never GitHub-shaped
#       — the email when present (contains '@', impossible as a GitHub login), else
#       "apple:"+short(sub) (contains ':', impossible as a GitHub login, and unique per Apple
#       user so two no-email Apple users never collide with each other either).
#   A3  NO REGRESSION for a real GitHub web session (githubID != 0): it resolves its own owner
#       and reads/writes exactly as today.
#   A4  NO REGRESSION for the native (signed, device-key) Apple owner path: grantsOwner's signed
#       leg and the signed account-delete path (keyed on the device pubkey -> the owner, whose
#       Login is empty) are unchanged.

# ENFORCED BY: cmd/rogerai-broker/apple_session_isolation_test.go (real handlers over
# store.NewMem() + a real signed session cookie, no mocks; A1 read+write legs, A2 login
# shape, A3 GitHub no-regression). A4 (the signed native Apple path) is pinned by the
# existing grants_apple_bdd_test.go / signed-delete coverage.

Feature: Apple-web sessions cannot resolve or mutate a GitHub owner
  As the operator github.com/apple (login "apple") with a bound account
  I want an Apple web visitor's session to never resolve to my account
  So that my grant keys, email, earnings, and payouts are never exposed or mutated by a stranger

  Background:
    Given a bound GitHub owner with login "apple", github id 501, and a funded wallet
    And an Apple web session cookie whose login is "apple", github id 0, wallet "u_apple_deadbeef"

  # ── A1: reads never cross to the GitHub owner ──────────────────────────────────────────

  Scenario: an Apple web session's account view does not leak the GitHub owner
    When the Apple web session requests GET /account
    Then the response does not contain the GitHub owner's email
    And it does not contain the GitHub owner's earnings split
    And the balance shown is the Apple wallet's balance

  Scenario: an Apple web session's export does not leak the GitHub owner's ledger or payouts
    When the Apple web session requests the account export
    Then the export contains no operator_ledger and no payouts for the GitHub owner
    And it contains no email for the GitHub owner

  Scenario: an Apple web session cannot manage the GitHub owner's grants
    When the Apple web session requests GET /grants
    Then it does NOT list the GitHub owner's grant keys
    And grant creation is refused for lacking a linked owner

  Scenario: an Apple web session sees no payout access to the GitHub owner
    When the Apple web session requests payout status
    Then it does not return the GitHub owner's payout/connect state

  # ── A1: writes never mutate the GitHub owner ───────────────────────────────────────────

  Scenario: an Apple web session cannot change the GitHub owner's email
    When the Apple web session PATCHes the account email to "attacker@evil.test"
    Then the GitHub owner "apple"'s email is unchanged

  Scenario: an Apple web session cannot delete the GitHub owner
    When the Apple web session posts account delete
    Then the GitHub owner "apple" is NOT anonymized or deleted

  # ── A2: two no-email Apple users don't collide with each other ─────────────────────────

  Scenario Outline: distinct Apple subs get distinct, non-GitHub-shaped session logins
    Given an Apple web login whose token carries no email and sub "<sub>"
    When the session is minted
    Then the session login is not a valid GitHub-login shape
    And it is distinct from another Apple sub's session login

    Examples:
      | sub        |
      | apple-sub-A |
      | apple-sub-B |

  # ── A3: the GitHub web session is unaffected ───────────────────────────────────────────

  Scenario: a real GitHub web session still resolves its own owner
    Given a GitHub web session for login "octocat", github id 7, wallet "u_gh_7"
    And a bound GitHub owner "octocat" with an email and grants
    When that session requests GET /account and GET /grants
    Then it sees octocat's email and octocat's grant keys
