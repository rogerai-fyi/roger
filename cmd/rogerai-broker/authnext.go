package main

// Returning a person to where they were after they sign in.
//
// Someone who reaches /device.html signed out is sent to sign in; without a return they
// land on the dashboard and lose the code they were about to approve. The web login
// routes therefore carry a `next`.
//
// A return parameter is the classic open-redirect hole: ?next=https://evil.example and a
// link that genuinely came from us bounces the victim off-site, which is exactly the
// shape a phishing flow wants. So the ONLY accepted form is a same-site absolute path,
// and anything else silently falls back to the default destination rather than erroring -
// a person who was phished should still land somewhere sane.

import (
	"net/http"
	"net/url"
	"strings"
)

// safeNext returns p if it is a same-site absolute path, else "".
//
// It is deliberately a strict allowlist rather than a blocklist of known tricks: the list
// of ways to express "somewhere else" is longer than anyone can enumerate.
func safeNext(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") {
		return "" // must be an absolute path on this site
	}
	if strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "" // protocol-relative: "//host" and "/\host" both leave the site
	}
	// Control characters are header-injection material and have no business in a path.
	if strings.ContainsAny(p, "\r\n\t\x00") {
		return ""
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "" // anything carrying a scheme or host is not same-site
	}
	// Re-check the DECODED path: %2F%2Fevil and friends must not sneak past the prefix
	// tests above.
	if strings.HasPrefix(u.Path, "//") || strings.HasPrefix(u.Path, "/\\") {
		return ""
	}
	return p
}

// returnTarget resolves where to send a person after they sign in: their requested
// destination when it is same-site, otherwise the dashboard.
func returnTarget(next string) string {
	safe := safeNext(next)
	if safe == "" {
		return dashboardURL()
	}
	return siteOrigin() + safe
}

// siteOrigin is the public web origin the return path is resolved against. Derived from
// the dashboard URL so there is one place to change if the site moves.
func siteOrigin() string {
	u, err := url.Parse(dashboardURL())
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "https://rogerai.fm"
	}
	return u.Scheme + "://" + u.Host
}

// nextCookie carries the post-login destination across the provider round trip. It lives
// in a cookie rather than the OAuth state parameter so it never leaves our origin and a
// provider cannot echo back an altered one.
const nextCookie = "roger_login_next"

// setNextCookie records a safe return destination, if one was asked for.
func setNextCookie(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if next == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: nextCookie, Value: next, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// takeNextCookie reads and clears the return destination, resolving it to an absolute
// URL on our own site. It re-validates on the way out: a cookie is client-supplied, and
// trusting it because we set it once is how these holes get reopened.
func takeNextCookie(w http.ResponseWriter, r *http.Request) string {
	http.SetCookie(w, &http.Cookie{Name: nextCookie, Value: "", Path: "/", MaxAge: -1})
	c, err := r.Cookie(nextCookie)
	if err != nil {
		return dashboardURL()
	}
	return returnTarget(c.Value)
}
