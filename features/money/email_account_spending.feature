# EMAIL ACCOUNTS ARE ACCOUNTS: a funded email-login account spends, reads its balance and sets
# its limits exactly like a GitHub or Apple account.
#
# PURPOSE. The approved first-party sign-in (features/auth/email_code_login.feature, founder
# approved 2026-08-03) makes an email address "a RogerAI account of our own" with a wallet and
# a balance: "Accepting a correct code creates the account ... And a wallet is resolved for it";
# "Signing in by email to an address a provider account already verified reaches ONE account
# ... its wallet, and its balance". The wallet resolver agrees. Every logged-in gate does not:
# it recognizes only the GitHub and Apple wallet namespaces, so an email account is treated as
# an anonymous keypair. Worse, top-up has no such gate, so an email user can pay money in and
# then cannot spend it on any paid model. This spec closes that, and pins that nothing else
# becomes able to spend.
#
# GROUND TRUTH (origin/main 12207827):
#   - cmd/rogerai-broker/emaillogin.go:33 walletForEmail -> "u_email_<16 hex>" (hash of the
#     normalized address); :41 isEmailWallet.
#   - cmd/rogerai-broker/main.go:1338 accountWalletForOwner resolves GitHub, then Apple, then a
#     VERIFIED email ("resolves LAST, so an account that also holds a GitHub or Apple link keeps
#     the wallet it already had").
#   - cmd/rogerai-broker/main.go:1379 isAccountWallet: ONLY "u_gh_" and "u_apple_".
#   - cmd/rogerai-broker/dashboards.go:108 walletLoggedIn = isAccountWallet. Callers that
#     therefore treat an email account as anonymous:
#       cooling.go:139 anonCannotPay -> tunnel.go:1950 401 "log in to spend on paid models"
#       (and :1984 the failover plan);
#       audio.go:396 403 "sign in to use this voice model";
#       dashboards.go:32 /me and :132 /balance -> {"logged_in": false};
#       dashboards.go:75 /account/limit -> 401 "log in to set a monthly spend limit";
#       metrics.go:117 /usage -> 401 "not logged in";
#       metrics_series.go:200, :435 /console consumer view off;
#       rc.go:160 remote control owner wallet refused;
#       account.go:162 account identity for a CLI keypair refused;
#       tunnel.go:1715 the per-identity rate-limit bucket is NOT merged onto the account.
#   - cmd/rogerai-broker/billing.go:225 checkoutWallet: a web session or a signed identity
#     resolves a wallet with NO logged-in gate, so /billing/checkout credits a u_email_ wallet.
#   - cmd/rogerai-broker/deviceauth.go:233: device approval already accepts an email session
#     (isEmailWallet), so a CLI key can be bound to an email account today.
#   - The Tower bridge resolves the payer through accountWalletForOwner (toweredge.go), so an
#     email account can already pay a Tower: the two fabrics disagree.
#
# THE FIX THIS SPEC DESCRIBES: an email account wallet is an account wallet everywhere a GitHub
# or Apple one is. The wallet must come from an AUTHENTICATED identity (a verified session or a
# signed device key bound to the account); a wallet string is never trusted from a header.
#
# FOUNDER RULING (2026-10-04): a new email account receives the one-time starter seed credit
# exactly like GitHub and Apple accounts.
#
# Enforced by: cmd/rogerai-broker/email_account_spending_bdd_test.go (real broker, real store
# incl. Postgres via ROGERAI_TEST_DATABASE_URL, real stations).

Feature: An email-login account spends, reads and limits its wallet like any account

  Background:
    Given a broker with a configured mailer
    And a paid station "n-1" on air for "qwen3-32b" at in $0.50 out $1.50 per 1M
    And a free station "n-free" on air for "qwen3-32b" at in $0 out $0
    And "erin@example.com" has signed in with an emailed code
    And "erin@example.com"'s account wallet holds $5.00

  # --- the spend gate --------------------------------------------------------------------

  Scenario Outline: A funded email account is served by a paid station through every identity it has
    Given "erin@example.com" calls through <identity>
    When the caller relays a chat completion for "qwen3-32b" pinned to "n-1"
    Then the response is 200 from "n-1"
    And a receipt is recorded for the request
    And the cost is debited from "erin@example.com"'s account wallet
    And no 401 "log in to spend" is returned

    Examples:
      | identity                                                  |
      | the browser session minted by the emailed code            |
      | a CLI device key bound to the account by device approval  |
      | a second device key bound to the same account             |

  # the 402 error code / the stream usage chunk arrive with routing slice 0 (PR #125), not on main yet
  @needs-pr125
  Scenario: The same account with an empty wallet gets 402, never 401
    Given "erin@example.com"'s account wallet holds $0.00
    When the account relays a chat completion for "qwen3-32b" pinned to "n-1"
    Then the response is 402 with error code "insufficient_balance"
    And the response is not 401
    And X-RogerAI-Cost is "0"

  # the 402 error code / the stream usage chunk arrive with routing slice 0 (PR #125), not on main yet
  @needs-pr125
  Scenario: A funded email account is served on a stream too, and the usage chunk settles
    When the account relays a streaming chat completion for "qwen3-32b" pinned to "n-1"
    Then the stream ends with the broker's usage chunk carrying the receipt
    And the cost is debited from "erin@example.com"'s account wallet

  Scenario: Failover between paid stations works for an email account
    Given a second paid station "n-2" on air for "qwen3-32b"
    And "n-1" answers the next request with an upstream 429
    When the account relays a chat completion for "qwen3-32b"
    Then the response is 200 from "n-2"
    And the failover plan was not trimmed to free stations

  Scenario: A paid voice model serves a funded email account
    Given a paid voice station "v-1" on air for "voice"
    When the account requests speech from "voice"
    Then the response is 200
    And no 403 "sign in to use this voice model" is returned

  Scenario: The direct path and the Tower bridge agree for an email account
    Given an approved Tower serves "qwen3-32b" at out $1.00 and no direct station serves it
    When the account relays a chat completion for "qwen3-32b"
    Then the bridge serves it and bills "erin@example.com"'s account wallet
    And when a direct paid station also serves "qwen3-32b", the direct path serves the account too

  # --- what the account reads about itself --------------------------------------------

  Scenario Outline: The dashboards report an email account as logged in
    When the account GETs <path>
    Then the response says logged_in true
    And it shows the account wallet's balance $5.00

    Examples:
      | path      |
      | /me       |
      | /balance  |

  Scenario: An email account reads its usage
    Given the account has spent $0.40 this month
    When the account GETs /usage
    Then the response is 200 with this month's spend $0.40

  Scenario: An email account sees its own requests in the /console consumer view
    Given the account made a paid request
    When the account GETs /console
    Then the consumer view lists that request

  # --- limits that belong to an account ---------------------------------------------------

  Scenario: An email account can set and is bound by a monthly spend limit
    When the account sets a monthly spend limit of $1.00
    Then the limit is stored for "erin@example.com"'s account wallet
    When the account has spent $1.00 this month and relays a paid request
    Then the response is 402 naming the monthly spend limit

  Scenario: Every key and session of an email account shares ONE rate-limit bucket
    Given the per-identity rate limit is 2 requests with burst 2
    When the browser session sends 2 requests and a bound CLI key sends 1 more
    Then the third request is 429 "rate limit exceeded"

  Scenario: Remote control accepts an email account as the owning account
    When the account's bound CLI key calls the remote-control owner endpoint
    Then the call is accepted for "erin@example.com"'s account wallet

  # --- money in, money out: nothing is stranded ---------------------------------------

  Scenario: Money topped up by an email account can be spent
    Given "erin@example.com"'s account wallet holds $0.00
    When the account completes a $10.00 top-up
    Then its balance reads $10.00
    And a paid request is served and debited from that balance

  Scenario: Top-up and spend resolve the same wallet for an email account
    When the account starts a top-up and then relays a paid request
    Then the wallet credited by the top-up and the wallet debited by the relay are the same

  # --- the starter seed credit (DECISION) -----------------------------------------------

  # corrected 2026-10-04 (founder ruling): an email account receives the one-time starter seed
  # credit exactly like GitHub and Apple account wallets (under the same global seed limit).
  Scenario: A new email account receives the one-time starter seed like other accounts
    Given a brand-new email account "new@example.com" with no top-up
    When it relays a paid request that costs less than the starter seed
    Then the response is 200
    And exactly one seed ledger entry exists for its wallet
    When it relays a second paid request
    Then exactly one seed ledger entry exists for its wallet

  Scenario: An email account that also has a GitHub link keeps the GitHub wallet and its seed rules
    Given "gina@example.com" is verified on an account that also has a GitHub link
    When the account relays a paid request
    Then the GitHub account wallet is the payer
    And the email wallet namespace is not used

  # --- nothing else becomes able to spend -------------------------------------------------

  Scenario: An unbound anonymous keypair still cannot spend on a paid station (unchanged)
    Given a signed keypair bound to no account
    When it relays a chat completion for "qwen3-32b" pinned to "n-1"
    Then the response is 401 "log in to spend on paid models"

  Scenario: An unbound keypair can still use a free station (unchanged)
    Given a signed keypair bound to no account
    When it relays a chat completion for "qwen3-32b" pinned to "n-free"
    Then the response is 200 from "n-free"

  Scenario Outline: A wallet string is never accepted from a request; it must come from an authenticated identity
    When an unauthenticated request claims the wallet "<claimed>" through <channel>
    Then it is not served by "n-1"
    And no wallet named "<claimed>" is debited

    Examples:
      | claimed                    | channel                           |
      | u_email_0123456789abcdef   | the X-Roger-User header            |
      | u_email_0123456789abcdef   | a forged session cookie            |
      | u_gh_42                    | the X-Roger-User header            |

  Scenario: A device key that was never approved by the email account cannot spend its wallet
    Given a signed keypair that started a device login but was never approved
    When it relays a chat completion for "qwen3-32b" pinned to "n-1"
    Then the response is 401 "log in to spend on paid models"
    And "erin@example.com"'s balance is unchanged

  Scenario: A deleted and anonymized email account cannot spend
    Given "erin@example.com"'s account has been deleted and anonymized
    When its old browser session relays a paid request
    Then the request is not served from the old wallet

  Scenario: An email address that was never verified resolves no account wallet
    Given an owner row carries "unverified@example.com" with no verification time
    When a key bound to that owner relays a paid request
    Then the response is 401 "log in to spend on paid models"

  # --- several devices, one email account -------------------------------------------------
  # added 2026-10-04 (founder ruling): the one-account-per-verified-address rule applies only to
  # GitHub- and Apple-linked accounts. Provider-less owner rows that share a verified address are
  # the same email account: one row per approved device, all resolving to one wallet. Before this
  # ruling the second approval failed on Postgres ("could not link this device to your account")
  # while the in-memory store accepted it.

  Scenario: An email account approves a second and a third device, and every device spends from the one wallet
    When "erin@example.com" approves 3 devices
    Then every approval succeeds
    And a paid request from each device is served from "erin@example.com"'s account wallet

  Scenario: Re-approving the same device for an email account is idempotent
    When "erin@example.com" approves the same device twice
    Then both approvals succeed
    And the device spends from "erin@example.com"'s account wallet

  Scenario Outline: Two provider accounts can never hold the same verified address
    Given a <first> account holds the verified address "shared@example.com"
    When a different <second> account proves the address "shared@example.com"
    Then the second link is refused
    And "shared@example.com" still resolves to the <first> account

    Examples:
      | first        | second       |
      | GitHub-linked | GitHub-linked |
      | GitHub-linked | Apple-linked  |
      | Apple-linked  | GitHub-linked |

  Scenario: A deleted provider account frees its verified address for a new provider account (unchanged)
    Given a GitHub-linked account holds the verified address "shared@example.com"
    And that account has been deleted and anonymized
    When a different GitHub-linked account proves the address "shared@example.com"
    Then the link succeeds
