# MONTHLY-CAP NOTICE EMAILS REACH THE ACCOUNT: one 80% notice and one 100% notice per month, to
# the account's own address, for every kind of account that can hold a monthly cap.
#
# PURPOSE. The monthly spend cap (features/money/caps.feature) sets near-cap and at-limit
# headers and is meant to mail the account holder at 80% and at 100% (the transactional lane
# of features/ops/alert_delivery.feature lists "cap notice"). In practice no notice is ever
# sent: the cap check passes the payer's ACCOUNT wallet, and the address lookup only
# understands a device pubkey. Every account that can set a cap is an account wallet, so the
# notice is silently dropped for everyone. The dedupe that limits it to once per month is also
# per instance and per process, so with two broker instances, or after a restart, a threshold
# could mail more than once if it ever fired.
#
# GROUND TRUTH (origin/main 12207827):
#   - cmd/rogerai-broker/monthlycap.go:56 monthlyCapCheck(w, holder, ...) is called with the
#     relay payer (tunnel.go:2011) and the voice payer (audio.go:404): an account wallet
#     ("u_gh_<id>", "u_apple_<hash>"; "u_email_<hash>" once email accounts can spend, see
#     features/money/email_account_spending.feature). It calls emailCapNotice(holder, "100",
#     ...) at :71 and emailCapNotice(holder, "80", ...) at :84.
#   - cmd/rogerai-broker/emailtemplates.go:453 emailCapNotice -> :214 emailOf(holder) ->
#     db.OwnerByPubkey(holder): an account wallet is never a pubkey, so the address is always
#     "" and the function returns before sending.
#   - The account wallet ids are one-way: "u_apple_" and "u_email_" are hashes, so the owner
#     cannot be recovered from the wallet string alone (main.go:1338 accountWalletForOwner is
#     the forward direction only).
#   - cmd/rogerai-broker/email.go:271 capNoticeOnce dedupes on a per-process map keyed
#     holder|threshold|YYYY-MM: not shared across instances, lost on restart.
#   - The existing tests (email_test.go:259-270, alert_delivery_bdd_test.go:873) call
#     emailCapNotice with a device pubkey ("ownerpk"), never with the wallet the relay passes,
#     which is why the gap was never caught.
#   - /account/limit (dashboards.go:75) only lets a logged-in account wallet set a cap, so an
#     unbound keypair never has a cap and never gets a notice.
#
# Enforced by: cmd/rogerai-broker/cap_notice_emails_bdd_test.go (real broker, real store incl.
# Postgres via ROGERAI_TEST_DATABASE_URL, the shared store over miniredis, a capturing mailer).

Feature: Monthly-cap notices are mailed to the account that crossed the threshold

  Background:
    Given a broker with a configured mailer that captures every message
    And a paid station "n-1" on air for "qwen3-32b"
    And the date is 2026-10-15 UTC

  # --- every account kind gets its notice --------------------------------------------

  Scenario Outline: An account crossing 80% of its monthly cap is mailed once at its own address
    Given a <kind> account "<address>" with a monthly cap of $10.00
    And the account has spent $7.90 this month
    When the account relays a paid request that brings its spend to at least $8.00
    Then exactly one "Monthly spend at 80%" message is sent to "<address>"
    And the message states the spend and the cap

    Examples:
      | kind                                     | address              |
      | GitHub-linked                            | gh@example.com       |
      | Apple-linked                             | ap@example.com       |
      | email-login                              | em@example.com       |
      | GitHub-linked with a separate verified email | both@example.com |

  Scenario Outline: An account reaching 100% of its monthly cap is mailed once at its own address
    Given a <kind> account "<address>" with a monthly cap of $10.00
    And the account has spent $10.00 this month
    When the account relays a paid request
    Then the response is 402 naming the monthly spend limit
    And exactly one "Monthly spend limit reached" message is sent to "<address>"

    Examples:
      | kind           | address           |
      | GitHub-linked  | gh@example.com    |
      | Apple-linked   | ap@example.com    |
      | email-login    | em@example.com    |

  Scenario Outline: The notice reaches the account whichever of its identities made the request
    Given an email-login account "em@example.com" with a monthly cap of $10.00 and $7.90 spent
    When <identity> relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "em@example.com"

    Examples:
      | identity                                     |
      | the account's browser session                 |
      | a CLI device key bound to the account         |
      | a second device key bound to the same account |

  Scenario: A paid voice request that crosses the threshold mails the same notice
    Given a GitHub-linked account "gh@example.com" with a monthly cap of $10.00 and $7.95 spent
    And a paid voice station "v-1" on air for "voice"
    When the account requests speech that brings its spend past $8.00
    Then exactly one "Monthly spend at 80%" message is sent to "gh@example.com"

  # --- once per threshold per month, everywhere ---------------------------------------

  Scenario: The 80% notice is sent once per month however many requests cross it
    Given a GitHub-linked account "gh@example.com" with a monthly cap of $10.00 and $8.10 spent
    When the account relays 5 more paid requests this month
    Then exactly one "Monthly spend at 80%" message has been sent to "gh@example.com" this month

  Scenario: The 80% and the 100% notices are independent
    Given a GitHub-linked account "gh@example.com" with a monthly cap of $10.00
    When the account's spend crosses 80% and later reaches 100% in the same month
    Then exactly one 80% message and exactly one 100% message have been sent

  Scenario: A new month allows a new notice
    Given "gh@example.com" was sent the 80% notice in September 2026
    And the date is 2026-10-02 UTC
    When the account's October spend crosses 80%
    Then one "Monthly spend at 80%" message is sent for October

  Scenario: The month boundary is UTC
    Given the date is 2026-10-31 23:59:59 UTC and "gh@example.com" was sent the 80% notice in October
    When the clock reaches 2026-11-01 00:00:00 UTC and the account's November spend crosses 80%
    Then one message is sent for November

  Scenario: Two broker instances never send the same notice twice
    Given two broker instances share one store
    And a GitHub-linked account "gh@example.com" with a monthly cap of $10.00 and $7.90 spent
    When the account sends one paid request to each instance at the same time, both crossing 80%
    Then exactly one "Monthly spend at 80%" message is sent to "gh@example.com"

  Scenario: A broker restart does not re-send a notice already sent this month
    Given "gh@example.com" was sent the 80% notice this month
    When the broker restarts and the account sends another paid request above 80%
    Then no second 80% message is sent

  Scenario: The dedupe falls back to the instance when the shared store is unreachable
    Given the shared store is unreachable
    And a GitHub-linked account "gh@example.com" crosses 80% twice on one instance
    Then at most one 80% message is sent from that instance
    And the relay is served normally

  # --- an account with no address, and accounts that can't have a cap ---------------

  Scenario: An account with no address on file gets no notice and its request is unaffected
    Given an Apple-linked account with no email on file and a monthly cap of $10.00 and $7.90 spent
    When the account relays a paid request that crosses 80%
    Then no message is sent
    And the response is served with the near-cap notice header set

  # PR #142 follow-up 2026-10-05: "no mailable address" is remembered per account and threshold
  # in the shared store for a few minutes, so every later request above the threshold does not
  # repeat the address lookup; the answer is shared by every instance and lapses on its own
  Scenario: An account with no mailable address is not looked up again on each request above the threshold
    Given an Apple-linked account with no email on file and a monthly cap of $10.00 and $7.90 spent
    When the account relays a paid request that crosses 80%
    And the account relays another paid request above 80%
    Then that request looked up no address for the notice
    And no message is sent

  # PR #142 follow-up 2026-10-05
  Scenario: The remembered "no address" answer is shared by every broker instance
    Given two broker instances share one store
    And an Apple-linked account with no email on file and a monthly cap of $10.00 and $7.90 spent
    When the account relays a paid request that crosses 80%
    And the account relays another paid request above 80% on the second instance
    Then that request looked up no address for the notice

  # PR #142 follow-up 2026-10-05
  Scenario: An address the account gains after a "no address" answer gets its notice once the answer lapses
    Given an Apple-linked account with no email on file and a monthly cap of $10.00 and $7.90 spent
    When the account relays a paid request that crosses 80%
    And the account signs in with Apple and the provider reports "late@example.com"
    And the remembered "no address" answer lapses
    And the account relays another paid request above 80%
    Then exactly one "Monthly spend at 80%" message is sent to "late@example.com"

  Scenario: An unbound keypair has no cap and never gets a notice (unchanged)
    Given a signed keypair bound to no account
    When it relays requests to a free station
    Then no cap notice is sent

  Scenario: A deleted and anonymized account is never mailed
    Given a GitHub-linked account "gh@example.com" was deleted and anonymized after setting a cap
    When a request is attributed to its old wallet
    Then no message is sent to "gh@example.com"

  # corrected 2026-10-04 (founder ruling): an email-login address is proven by code by definition,
  # so the rule binds provider accounts: a notice goes only to an address proven by an emailed code
  # or reported by the identity provider, never to one typed into the profile until it is proven.
  Scenario Outline: A provider account's address typed into its profile is not mailed until it is proven
    Given a <kind> account "<provider>" with a monthly cap of $10.00
    And the account has spent $7.90 this month
    And the account changes its profile email to "typed@example.com"
    When the account relays a paid request that brings its spend to at least $8.00
    Then no cap notice is sent to "typed@example.com"
    And no cap notice is sent to "<provider>"

    Examples:
      | kind          | provider        |
      | GitHub-linked | gh@example.com  |

  # added 2026-10-04 (founder ruling)
  Scenario: A typed address is mailed once the account proves it with an emailed code
    Given a GitHub-linked account "gh@example.com" with a monthly cap of $10.00
    And the account has spent $7.90 this month
    And the account changes its profile email to "typed@example.com"
    And the account proves "typed@example.com" with an emailed code
    When the account relays a paid request that brings its spend to at least $8.00
    Then exactly one "Monthly spend at 80%" message is sent to "typed@example.com"

  # added 2026-10-04 (founder ruling)
  Scenario: Changing a proven address to a new one withdraws the proof until the new one is proven
    Given a GitHub-linked with a separate verified email account "both@example.com" with a monthly cap of $10.00
    And the account has spent $7.90 this month
    And the account changes its profile email to "typed@example.com"
    When the account relays a paid request that brings its spend to at least $8.00
    Then no cap notice is sent to "typed@example.com"
    And no cap notice is sent to "both@example.com"

  # added 2026-10-04 (founder ruling)
  Scenario: Re-typing the proven address, in any letter case, keeps it mailable
    Given a GitHub-linked with a separate verified email account "both@example.com" with a monthly cap of $10.00
    And the account has spent $7.90 this month
    And the account changes its profile email to its proven address in upper case
    When the account relays a paid request that brings its spend to at least $8.00
    Then exactly one "Monthly spend at 80%" message is sent to "both@example.com"

  # --- the notice never costs the relay anything ------------------------------------

  Scenario: Sending the notice never blocks or delays the relay
    Given the mail provider takes 30 seconds to answer
    When a GitHub-linked account crosses 80% with a paid request
    Then the relay response is returned without waiting for the mail provider

  Scenario: A mail provider failure never fails the relay
    Given the mail provider returns 500 for every message
    When a GitHub-linked account crosses 80% with a paid request
    Then the relay is served normally
    And the failure is retried on the transactional lane as features/ops/alert_delivery.feature says

  Scenario: The notice rides the transactional lane ahead of ops alerts (unchanged)
    Given 10 ops alerts are queued
    When a cap notice is enqueued
    Then it is delivered before any of the alerts

  Scenario: The notice never contains the account's other identities or wallet id
    When "gh@example.com" is sent the 80% notice
    Then the message contains no wallet id, no device pubkey and no other address

  Scenario: The account address is not written to logs when a notice is sent
    When "gh@example.com" is sent the 80% notice
    Then no log line contains "gh@example.com"

  # --- sponsored grants notify the account that pays -------------------------------------

  # added 2026-10-04 (audit fix): a sponsored grant's spend is charged to the grant owner's
  # wallet and counts against the owner's cap, so the owner is the account notified; the
  # grantee (a bot holding only the grant secret) has no address and is never mailed
  Scenario: A sponsored grant's spend that crosses the owner's cap notifies the grant owner
    Given a GitHub-linked station owner "owner@example.com" with a monthly cap of $10.00 and $7.90 spent
    And the owner issued a custom-priced grant
    When a bot relays a paid request with the grant that brings the owner's spend past $8.00
    Then exactly one "Monthly spend at 80%" message is sent to "owner@example.com"

  # added 2026-10-04 (audit fix)
  Scenario: A free grant spends nothing and never notifies (unchanged)
    Given a GitHub-linked station owner "owner@example.com" with a monthly cap of $10.00 and $7.90 spent
    And the owner issued a free grant
    When a bot relays a request with the grant
    Then no cap notice is sent

  # --- existing provider addresses are re-checked at the next sign-in --------------------

  # added 2026-10-04 (founder ruling): an address stored on a GitHub or Apple account before
  # typed addresses were tracked stays mailable; the next GitHub or Apple sign-in compares it
  # with the address the provider reports and marks a mismatch unproven, unless the address
  # was proven with an emailed code
  Scenario: An existing provider-account address stays mailable before any new sign-in
    Given a GitHub-linked account "legacy@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "legacy@example.com"

  # added 2026-10-04 (founder ruling)
  Scenario Outline: The next provider sign-in marks a stored address that differs from the provider's as unproven
    Given a <kind> account "typed@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with <provider> and the provider reports "real@example.com"
    And the account relays a paid request that crosses 80%
    Then no cap notice is sent to "typed@example.com"
    And no cap notice is sent to "real@example.com"

    Examples:
      | kind          | provider |
      | GitHub-linked | GitHub   |
      | Apple-linked  | Apple    |

  # added 2026-10-04 (founder ruling)
  Scenario Outline: A provider sign-in reporting the same address keeps it mailable
    Given a <kind> account "same@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with <provider> and the provider reports "SAME@example.com"
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "same@example.com"

    Examples:
      | kind          | provider |
      | GitHub-linked | GitHub   |
      | Apple-linked  | Apple    |

  # added 2026-10-04 (founder ruling)
  Scenario: A provider sign-in never withdraws an address proven with an emailed code
    Given a GitHub-linked with a separate verified email account "proven@example.com" with a monthly cap of $10.00
    And the account has spent $7.90 this month
    When the account signs in with GitHub and the provider reports "other@example.com"
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "proven@example.com"

  # added 2026-10-05 (audit fix): an account linked to BOTH GitHub and Apple keeps the address
  # each provider last reported; Apple's Hide My Email relay address never withdraws the address
  # GitHub reports, and a stored address is unproven only when it matches neither provider
  Scenario: An Apple relay address does not withdraw the GitHub address of a dual-linked account
    Given a GitHub-and-Apple-linked account "gh@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with GitHub and the provider reports "gh@example.com"
    And the account signs in with Apple and the provider reports "relay@privaterelay.appleid.com"
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "gh@example.com"

  # added 2026-10-05 (audit fix)
  Scenario: An Apple sign-in alone does not withdraw a dual-linked address GitHub has not re-reported yet
    Given a GitHub-and-Apple-linked account "gh@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with Apple and the provider reports "relay@privaterelay.appleid.com"
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "gh@example.com"

  # added 2026-10-05 (audit fix)
  Scenario: A dual-linked address that matches neither provider's report is unproven
    Given a GitHub-and-Apple-linked account "typed@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with GitHub and the provider reports "gh@example.com"
    And the account signs in with Apple and the provider reports "relay@privaterelay.appleid.com"
    And the account relays a paid request that crosses 80%
    Then no cap notice is sent to "typed@example.com"

  # added 2026-10-05 (audit fix)
  Scenario: The GitHub report matching the stored address makes it mailable again after an Apple mismatch
    Given a GitHub-and-Apple-linked account "gh@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with Apple and the provider reports "relay@privaterelay.appleid.com"
    And the account signs in with GitHub and the provider reports "GH@example.com"
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "gh@example.com"

  # added 2026-10-04 (founder ruling)
  Scenario: A provider sign-in that reports no address leaves the stored one as it was
    Given a GitHub-linked account "kept@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with GitHub and the provider reports no address
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "kept@example.com"

  # --- GitHub reports the account's primary verified address ------------------------------

  # founder ruling 2026-10-05: user:email. Both GitHub sign-ins ask for the account's email
  # addresses, and the address GitHub reports is the PRIMARY + VERIFIED one from that list, so a
  # GitHub user with a private address is re-checked like everyone else. The CLI device sign-in
  # asks for the same scopes (pinned by TestDeviceFlowRequestsEmailScope in internal/client).
  Scenario: The web GitHub sign-in asks for the account's email addresses
    When a visitor starts the web GitHub sign-in
    Then the GitHub authorize request asks for the scopes "read:user user:email"

  # founder ruling 2026-10-05: user:email
  Scenario: A private-address GitHub user's stored address is re-checked against their primary verified address
    Given a GitHub-linked account "typed@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with GitHub, which shows no public address and lists the addresses:
      | email            | primary | verified |
      | real@example.com | true    | true     |
    And the account relays a paid request that crosses 80%
    Then no cap notice is sent to "typed@example.com"

  # founder ruling 2026-10-05: user:email
  Scenario: A private-address GitHub user whose primary verified address matches keeps it mailable
    Given a GitHub-linked account "same@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with GitHub, which shows no public address and lists the addresses:
      | email            | primary | verified |
      | SAME@example.com | true    | true     |
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "same@example.com"

  # founder ruling 2026-10-05: user:email
  Scenario: The primary verified address wins over a different public address
    Given a GitHub-linked account "primary@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with GitHub, which shows the public address "public@example.com" and lists the addresses:
      | email               | primary | verified |
      | public@example.com  | false   | true     |
      | primary@example.com | true    | true     |
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "primary@example.com"

  # founder ruling 2026-10-05: user:email
  Scenario Outline: An unverified or non-primary address is never the one GitHub reports
    Given a GitHub-linked account "kept@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with GitHub, which shows no public address and lists the addresses:
      | email             | primary   | verified   |
      | other@example.com | <primary> | <verified> |
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "kept@example.com"

    Examples:
      | primary | verified |
      | true    | false    |
      | false   | true     |
      | false   | false    |

  # founder ruling 2026-10-05: user:email. A token without the new scope (minted before the
  # change) or a failing address list falls back to the public address, and never fails sign-in.
  Scenario Outline: A failing address list falls back to the public address without failing sign-in
    Given a GitHub-linked account "typed@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with GitHub, which shows the public address "real@example.com" and answers the address list with status <status>
    And the account relays a paid request that crosses 80%
    Then no cap notice is sent to "typed@example.com"

    Examples:
      | status |
      | 403    |
      | 404    |
      | 500    |

  # founder ruling 2026-10-05: user:email
  Scenario: A failing address list and no public address leave the stored address as it was
    Given a GitHub-linked account "kept@example.com" whose address predates typed-address tracking, with a monthly cap of $10.00 and $7.90 spent
    When the account signs in with GitHub, which shows no public address and answers the address list with status 403
    And the account relays a paid request that crosses 80%
    Then exactly one "Monthly spend at 80%" message is sent to "kept@example.com"
