package main

import (
	"net/http"

	"rogerai.fm/roger/v6/internal/protocol"
)

// csrfGuard is the one place cross-site request forgery is stopped for every route.
//
// The session cookie is SameSite=None (the broker lives on a different origin from the site),
// so a browser attaches it to a request from ANY page. CORS only decides whether an attacker
// page may READ the response; it never stops the request, and the request itself is the attack
// (delete the account, raise the spend cap, revoke keys, file appeals, log out). Routes used
// to guard themselves one by one and most did not.
//
// Rule: a state-changing request (POST, PUT, PATCH, DELETE) that carries the SESSION COOKIE
// must come from one of our own origins. Everything else is untouched:
//   - reads, HEAD and the CORS preflight (it must stay answerable);
//   - requests with no session cookie (the CLI, Stripe's webhook, any cookie-less client);
//   - requests carrying the signed-request headers: a page cannot set custom headers
//     cross-origin without a preflight we do not grant, so they cannot be forged by a page;
//   - Apple's sign-in form post, which legitimately arrives from Apple's origin.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if csrfMutation(r) && hasSessionCookie(r) && r.Header.Get(protocol.HeaderPubkey) == "" &&
			!csrfExempt[r.URL.Path] && !originAllowed(r) {
			jsonErr(w, http.StatusForbidden, "this request must come from the RogerAI site")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrfExempt are the cookie-carrying mutations that legitimately come from another origin.
var csrfExempt = map[string]bool{
	"/auth/apple/web/callback": true, // Apple form_post from appleid.apple.com (it sets, not spends, a session)
}

func csrfMutation(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func hasSessionCookie(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && c.Value != ""
}
