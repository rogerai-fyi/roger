#!/usr/bin/env bash
# Black-box probe of the sign-in plumbing, from the outside. Read-only: no account, no
# code, no mail is ever requested. It cannot complete a login (that needs a mailbox) - it
# catches the things that break around one: the page, the broker, CORS for credentialed
# calls, and that account feeds refuse a logged-out visitor with a clean 401 (not a 5xx,
# not a 200). The login/dashboard redirect loop itself is pinned by
# web/test/login-loop.test.mjs and cmd/rogerai-broker/emaillogin_dashboard_test.go.
#
#   scripts/auth-probe.sh                 # production
#   WEB=https://x BROKER=https://y scripts/auth-probe.sh
set -u
WEB="${WEB:-https://rogerai.fm}"
BROKER="${BROKER:-https://broker.rogerai.fm}"
fail=0
check() { # name, expected, actual
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want $2 got $3"; fail=1; fi
}
code() { curl -s -o /dev/null -w '%{http_code}' --max-time 15 --retry 2 --retry-connrefused --retry-delay 3 "$@"; }

check "login page serves"               200 "$(code "$WEB/login.html")"
body="$(curl -s --max-time 15 "$WEB/login.html")"
case "$body" in *email-form*) echo "ok   login page has the email form";; *) echo "FAIL login page lost the email form"; fail=1;; esac
check "broker liveness"                 200 "$(code "$BROKER/health")"
check "/account refuses a visitor"      401 "$(code "$BROKER/account")"
check "/metrics/series refuses a visitor" 401 "$(code "$BROKER/metrics/series")"
# /me answers a visitor with a logged-out body (200) or a 401; only a 5xx/timeout is a fault.
case "$(code "$BROKER/me")" in 200|401) echo "ok   /me answers a visitor";; *) echo "FAIL /me answers a visitor"; fail=1;; esac

# credentialed CORS preflight from the web origin: without it the browser blocks every
# sign-in call and the page looks dead.
hdrs="$(curl -s -o /dev/null -D - --max-time 15 -X OPTIONS "$BROKER/auth/email/verify" \
  -H "Origin: $WEB" -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: content-type')"
case "$hdrs" in *[Aa]ccess-[Cc]ontrol-[Aa]llow-[Cc]redentials:*true*) echo "ok   email verify allows credentialed CORS";; *) echo "FAIL email verify CORS preflight"; fail=1;; esac

exit $fail
