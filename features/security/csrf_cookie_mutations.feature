# Cross-site request forgery on every cookie-authenticated mutation.
#
# The session cookie is SameSite=None (the broker is on a different origin from the site), so a
# browser attaches it to requests from ANY page. CORS only decides whether an attacker page may
# READ the response; it never stops the request, and the request itself is the attack. Until now
# only device-approve and the email routes checked the request's Origin; account delete, the
# monthly spend cap, key creation/revocation, appeals, payout requests and logout did not.
#
# Rule (one guard in front of every route): a state-changing request (POST/PUT/PATCH/DELETE)
# that carries the session cookie must come from one of our own origins. Requests with no
# session cookie (the CLI, the native apps' signed requests, Stripe's webhook) are untouched.
# STATUS: security fix; written test-first (cmd/rogerai-broker/csrfguard_test.go).

Feature: A page we do not own cannot act as a signed-in person

  Scenario: A cookie-authenticated mutation from a foreign page is refused
    Given a person is signed in and visits a page on another origin
    When that page sends a POST, PATCH, PUT or DELETE carrying their session cookie
    Then it is refused with 403 whether the Origin is foreign, "null" or missing
    And nothing is changed
    # test: TestCSRFGuardDecisionTable

  Scenario: Our own site still works
    When the RogerAI site sends a mutation with the session cookie
    Then it is allowed, from rogerai.fm and from rogerai.fyi
    # test: TestCSRFGuardDecisionTable


  Scenario: Reads and preflights are never blocked
    When any origin sends GET, HEAD or OPTIONS with the cookie
    Then it passes through (the CORS preflight must be answerable)
    # test: TestCSRFGuardDecisionTable


  Scenario: Requests without a session cookie are untouched
    When the CLI, the Stripe webhook or any cookie-less client sends a mutation with no Origin
    Then it passes through
    # test: TestCSRFGuardDecisionTable,TestOnlyTheSessionCookieTriggersTheGuard


  Scenario: Signed requests are untouched even if a cookie is also present
    When a native app sends a mutation carrying a request signature and a stale cookie
    Then it passes through (a page cannot forge the signature headers cross-origin)
    # test: TestCSRFGuardDecisionTable


  Scenario: The Apple sign-in form post is exempt
    When Apple posts the sign-in form to the web callback
    Then it passes through
    # test: TestCSRFGuardDecisionTable


  Scenario: Real destructive routes are protected end to end
    When a foreign page posts to account delete, the spend cap, key creation, an appeal or logout
    Then each is refused and the account, cap, keys and session are unchanged
    # test: TestRealDestructiveRoutesAreProtectedEndToEnd
