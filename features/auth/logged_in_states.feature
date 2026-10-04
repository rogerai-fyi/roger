# The signed-in experience tells the truth about WHY a page has nothing to show.
#
# Born from a customer screen that said "Could not load your stations" with "Signed in as -".
# That was not a failure: the person was signed in by email and their machines belong to a
# different sign-in (GitHub), so the broker correctly answered 403 "no operator account" -
# the NORMAL state for a consumer. Every page collapsed that 403 (and 5xx, and a network drop)
# into one generic error, and several showed "-" or $0.00 for data that was simply unavailable.
#
# Rules this pins:
#   - 401 means signed out (go to login); 403 means no machine is linked to THIS sign-in
#     (explain, never "could not load"); 5xx/network is a real fault (retry hint).
#   - a failed feed reads "unavailable" ("-"), never a real-looking $0.00 or an empty list.
#   - the page says WHICH sign-in you used (GitHub / Apple / email) and whether machines are
#     linked to it, from fields /account already returns: no extra request is added, and
#     operator-only requests are NOT made for a consumer (less load, not more).
# STATUS: IMPLEMENTED 2026-10-04 on the founder's brief ("improve the logged in experience"); scenarios await founder review.
# Executable: cmd/rogerai-broker/logged_in_states_bdd_test.go runs web/test/*.test.mjs.

Feature: Signed-in pages are honest about identity and empty states

  Scenario: Stations explains a sign-in with no machines instead of claiming a failure
    Given a person is signed in and no operator account is behind their sign-in
    When they open the stations page
    Then it says they are not sharing from this sign-in and shows how to start
    And it does not say "could not load"
    And a real server fault still shows a retry hint
    # test: web/test/stations-states.test.mjs

  Scenario: API keys explains the same state and hides a mint form that cannot work
    When a signed-in person with no operator account opens the keys page
    Then it explains keys come from a machine on air and hides the mint form
    And a server error is never shown as "no keys yet"
    # test: web/test/keys-states.test.mjs

  Scenario: Payouts is for operators and costs a consumer nothing
    When a signed-in person with no operator account opens payouts
    Then they see why, and the page makes none of the operator-only requests
    And an older broker that omits the operator flag still shows the full page
    # test: web/test/payouts-states.test.mjs

  Scenario: A failed feed reads unavailable, never a real-looking number or a signed-out gate
    Then a failed spend-today feed shows "-" not "$0.00"
    And a 403 on the usage feed is an error, not the signed-out gate
    And a 403 on private bands says no machine is linked, not "sign in"
    # test: web/test/honest-feeds.test.mjs

  Scenario: A new customer's dashboard says what to do next
    When a new customer opens an empty dashboard
    Then it shows their balance with links to add credit, make a key and see stations
    And it shows how to earn, and adds no request beyond account and series
    # test: web/test/dashboard-states.test.mjs

  Scenario: The account page names the sign-in and whether machines are linked
    Then it shows GitHub, Apple or Email and whether machines are linked
    And an email sign-in's address is read-only
    And a refused contact-email save shows the broker's reason
    # test: web/test/account-identity.test.mjs

  Scenario: Stations and API keys are reachable from the signed-in menus
    Then both appear in the dropdown and the footer sub-nav in the same order
    # test: web/test/account-nav.test.mjs

  Scenario: /account reports provider, email, verification and operator state
    Then every sign-in shape reports who it is and whether it is an operator
    # test: cmd/rogerai-broker/account_identity_test.go
