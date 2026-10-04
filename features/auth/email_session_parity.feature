# Email sign-in is a first-class account, and the browser can never loop on it.
#
# Born from the 2026-10 login incident: after typing a valid emailed code the page
# refreshed between /login and /dashboard forever. The broker treated the email wallet
# (u_email_*) as anonymous, /metrics/series answered 401, dashboard.js answered a 401 with a
# redirect to /login.html, and login.html answered "signed in" with a redirect back.
#
# Pinned by (all green): cmd/rogerai-broker/emaillogin_dashboard_test.go,
# cmd/rogerai-broker/session_refused_alert_test.go, web/test/login-loop.test.mjs.
# STATUS: APPROVED 2026-10-04. Executable: cmd/rogerai-broker/email_session_parity_bdd_test.go.

Feature: An email sign-in is a full account everywhere, and failures never loop the browser

  Scenario: An email session reads every dashboard feed as logged in
    Given a person signs in with an emailed code
    When their browser asks /me, /metrics/series and /account
    Then none of them answers 401 and /me reports logged_in true

  Scenario: Email wallets are guarded like every other account wallet
    Then a u_email_ wallet is an account wallet
    And an unsigned request may not claim a u_email_ wallet id

  Scenario: An address that belongs to a GitHub-linked account signs in as that account
    Given an account holds a GitHub link and a verified email
    When the person signs in with an emailed code for that address
    Then the session carries the GitHub identity and the same wallet
    And /account shows the account's email

  Scenario: An email-only operator is not second class
    Given an operator bound a CLI key through an email sign-in
    When they read their stations, their API keys and their account
    Then each answers 200, not "no operator account"

  Scenario: An email account can delete itself in the app
    When an email account with no balance requests deletion
    Then it is deleted by its own owner row, never "can't be deleted in-app"

  Scenario: A feed refusing a signed-in person never redirects to login
    Given /account says the person is signed in
    When /metrics/series or /console answers 401 or 403
    Then the page shows its error state and does not navigate
    And login and dashboard together never navigate more than twice

  Scenario: An email address is displayed as the address
    Then the signed-in name for "a@b.com" is "a@b.com", never "@a@b.com"

  Scenario: The broker pages the founder when signed-in people are refused
    Given five valid sessions were refused as logged-out inside ten minutes
    Then one "signed-in users are being refused" alert is sent
    And a logged-out visitor's 401 never counts toward it
    And it clears after a quiet window

  Scenario: Basic sign-in plumbing is probed from outside every 20 minutes
    Then scripts/auth-probe.sh checks the login page, broker liveness, clean 401s and credentialed CORS
    And a failed scheduled run emails the repository's workflow editor
