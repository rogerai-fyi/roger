# Web session hint — the logged-out homepage must make NO credentialed /account probe.
#
# The marketing front-end swaps its "Log in" nav for an account control when a returning
# user is signed in. It learned that by probing the broker's credentialed GET /account on
# every page load - which, for the (vast majority) logged-out visitor, returns 401 and
# litters the network/console with a red error on a request that had no purpose. The
# session cookie is HttpOnly + on the broker's domain, so the page's JS cannot read it to
# decide whether to probe.
#
# FIX: the broker sets a NON-secret, JS-readable companion cookie `roger_signed_in=1`
# (scoped to the web origin host) at login, and clears it at logout, with the SAME lifetime
# as the HttpOnly session cookie. The front-end probes /account ONLY when that hint is
# present, so a logged-out visitor makes zero request (no 401). The hint carries no
# identity, no signature, and grants nothing - it is purely a presence flag, safe to be
# readable. The real credential stays the signed, HttpOnly `roger_session` cookie; spends
# still require an Ed25519 signature.
#
# GROUND TRUTH:
#   - cmd/rogerai-broker/auth.go:
#       signedInHint = "roger_signed_in"; webOriginHost() = host of ROGERAI_WEB_ORIGIN.
#       setWebSessionCookies(w, login, id, exp): sets the HttpOnly session cookie AND the
#         readable hint (Domain=webOriginHost, Secure, NOT HttpOnly), both Expires=exp.
#       clearWebSessionCookies(w): expires BOTH (used by /auth/logout).
#       callback uses setWebSessionCookies; authLogout uses clearWebSessionCookies.
#   - web/src/js/session.js: skips the /account fetch unless roger_signed_in=1 is present
#       (or window.ROGER_BROKER_CHECK forces it for local-broker dev).
#   Go: cmd/rogerai-broker TestSetWebSessionCookies, TestClearWebSessionCookies,
#       TestWebOriginHost.

@security @session
Feature: Web session hint cookie

  Rule: The signed-in hint mirrors the real session, but carries no secret

    @session
    Scenario: login sets the HttpOnly session cookie AND a readable signed-in hint
      Given a user completes GitHub login
      Then the response sets an HttpOnly "roger_session" cookie
      And it also sets a "roger_signed_in" cookie with value "1"
      And the hint cookie is NOT HttpOnly (the web JS can read it)
      And the hint cookie is scoped to the web origin host
      And both cookies share the same expiry

    @session
    Scenario: the hint carries no identity or signature
      Given the signed-in hint cookie
      Then its value is exactly "1"
      And it contains no login, github id, wallet, or signature

    @session
    Scenario: logout clears BOTH the session cookie and the hint
      Given a logged-in browser calls POST /auth/logout
      Then the "roger_session" cookie is expired
      And the "roger_signed_in" hint cookie is expired

  Rule: The front-end probes /account only when the hint says a session may exist

    @session
    Scenario: a logged-out homepage visitor makes no /account request
      Given the page is loaded with no "roger_signed_in" cookie
      When session.js runs
      Then it does NOT fetch /account
      And the static logged-out nav is left unchanged
      And no 401 appears in the console

    @session
    Scenario: a logged-in homepage visitor probes /account and swaps the nav
      Given the page is loaded with "roger_signed_in=1" present
      When session.js runs
      Then it fetches /account credentialed
      And on a 200 it swaps the nav for the account control

    @session
    Scenario: forcing the probe for local-broker development
      Given window.ROGER_BROKER_CHECK is true
      Then session.js fetches /account even without the hint cookie
