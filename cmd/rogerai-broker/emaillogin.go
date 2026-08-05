package main

// emaillogin.go is first-party sign-in over HTTP: the RogerAI account of our own.
//
// Two routes, and the split matters. /auth/email/start only ever says "if that address can
// receive mail, a code is on its way" - it consults no account store, so there is no
// branch that could leak whether the address is known. /auth/email/verify is where an
// accepted code becomes a session.
//
// SHAPED AFTER THE APPLE WEB FLOW, deliberately. A browser has no device pubkey, so there
// is no owner row to bind and the wallet is keyed purely off the verified identity - the
// same thing apple_web.go does with a verified `sub`. An owner row appears later, at
// device approval, which is the only point where a key exists to bind one to.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/emailauth"
)

// walletForEmail is the email account-wallet namespace, mirroring u_gh_<id> and
// u_apple_<hash>. The address is hashed rather than embedded so a wallet id is tidy,
// bounded, and not a place somebody's email address is on display.
func walletForEmail(addr string) string {
	h := sha256.Sum256([]byte("email|" + addr))
	return "u_email_" + hex.EncodeToString(h[:])[:16]
}

// isEmailWallet reports whether a session's wallet was minted by this flow. It is how the
// device-approval path recognizes an email account as an identity it can bind, without a
// sixth field being added to the session cookie.
func isEmailWallet(wallet string) bool { return strings.HasPrefix(wallet, "u_email_") }

// emailFlow returns the sign-in state machine, creating it on first use. Lazy rather than
// constructor-only so no construction path can leave it nil and panic on the first attempt.
func (b *broker) emailFlow() *emailauth.Flow {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.emails == nil {
		b.emails = newEmailFlowWithStore(nil)
	}
	return b.emails
}

// emailCodeTTL is how long a mailed code stays usable. Long enough to find the mail on a
// phone, short enough that a code left in an inbox is not a standing credential.
const emailCodeTTL = 10 * time.Minute

// newEmailFlowWithStore builds the sign-in flow over an explicit store. A nil store keeps
// the in-process default, which is the single-instance deployment: no new dependency and
// no configuration change.
func newEmailFlowWithStore(st emailauth.Store) *emailauth.Flow {
	cfg := emailauth.Config{TTL: emailCodeTTL}
	if st == nil {
		return emailauth.New(cfg)
	}
	return emailauth.NewWithStore(cfg, st)
}

// emailStart handles POST /auth/email/start: mail a sign-in code.
//
// It answers identically whether or not the address has an account, and it does so by
// never asking. The only distinguishable outcomes are "we cannot mail that string at all"
// and "you are asking too often", neither of which says anything about who has an account.
func (b *broker) emailStart(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)

	if !b.mail.enabled() {
		// Say so plainly rather than claiming a code was sent. A person waiting for mail
		// that will never arrive has no way to discover the problem.
		jsonErr(w, http.StatusServiceUnavailable, "emailed sign-in codes are unavailable right now - try another sign-in method")
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(body, &req) != nil {
		jsonErr(w, http.StatusBadRequest, "email required")
		return
	}

	code, err := b.emailFlow().Request(req.Email, clientIP(r))
	switch {
	case errors.Is(err, emailauth.ErrInvalidAddress):
		jsonErr(w, http.StatusBadRequest, "that does not look like an email address we can reach")
		return
	case errors.Is(err, emailauth.ErrRateLimited):
		jsonErr(w, http.StatusTooManyRequests, "too many sign-in requests - wait a moment and try again")
		return
	case err != nil:
		jsonErr(w, http.StatusServiceUnavailable, "sign-in is temporarily unavailable - try again in a moment")
		return
	}

	addr := emailauth.Normalize(req.Email)
	b.mail.sendSignInCode(addr, code, int(emailCodeTTL/time.Minute))
	// Deliberately uninformative, and identical for a brand-new address and a known one.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// emailVerify handles POST /auth/email/verify: an accepted code becomes a session.
func (b *broker) emailVerify(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)

	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
		Next  string `json:"next"`
	}
	if json.Unmarshal(body, &req) != nil || req.Code == "" {
		jsonErr(w, http.StatusBadRequest, "email and code required")
		return
	}

	addr, err := b.emailFlow().Submit(req.Email, req.Code, clientIP(r))
	if errors.Is(err, emailauth.ErrUnavailable) {
		jsonErr(w, http.StatusServiceUnavailable, "sign-in is temporarily unavailable - try again in a moment")
		return
	}
	if err != nil {
		// Uniform: a wrong code, an expired one, a spent one, and an address that never
		// had one must all look alike.
		jsonErr(w, http.StatusBadRequest, "that code is not valid")
		return
	}

	// The person holds the address. Resolve what that MEANS - which is this layer's job,
	// not the state machine's.
	login, wallet := addr, walletForEmail(addr)
	if o, ok, err := b.db.OwnerByVerifiedEmail(addr); err == nil && ok {
		// An account already proved it holds this address. Reach THAT account, its wallet
		// and its balance rather than minting a parallel one - including when the account
		// also holds a GitHub or Apple link, whose wallet takes precedence.
		if o.Login != "" {
			login = o.Login
		}
		if wl, wok := accountWalletForOwner(o); wok {
			wallet = wl
		}
	}

	if _, seeded, _ := b.db.SeedOnce(wallet, b.seedFunds); seeded {
		b.invalidateSeedRemaining()
	}
	exp := time.Now().Add(24 * time.Hour).Unix()
	b.setWebSessionWallet(w, login, 0, wallet, exp)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next": safeNext(req.Next)})
}

// --- the mail itself ------------------------------------------------------

// sendSignInCode mails a code. Like every other send in email.go it is async and never
// blocks or fails the caller.
//
// WHAT IT DELIBERATELY DOES NOT CONTAIN: a link that signs the recipient in by being
// followed. A followed link authenticates whoever followed it, in whatever browser
// followed it - including a corporate mail scanner that fetches every URL it sees.
// Requiring the code to be typed back into the session that ASKED for it is what ties the
// person who requested to the person who arrives.
func (m *mailer) sendSignInCode(addr, code string, expiresMinutes int) {
	if m == nil || !m.enabled() {
		return
	}
	// The code is deliberately NOT in the subject. A subject travels further than a body:
	// it lands in notification previews, in mail-server logs, and in ours (deliver logs
	// the subject verbatim). Keeping the code to the body means no ordinary log line can
	// ever carry a live credential.
	subject := "Your RogerAI sign-in code"
	text := fmt.Sprintf(`Someone asked to sign in to RogerAI with this email address.

Your sign-in code is:

    %s

It expires in %d minutes, and it can only be used once.

Type it into the RogerAI window that asked for it. We will never ask you for
this code by phone, chat, or email reply.

If this was not you, you do not need to do anything. Nobody can sign in to your
account without this code, and we have not changed anything.

- RogerAI
`, code, expiresMinutes)

	// Text only: an HTML body invites a linkified code and a "click here" affordance,
	// which is precisely what this mail must not have.
	m.sendEmail(addr, subject, "", text)
	// The address and the code are BOTH absent from this line, deliberately.
	log.Printf("email login: sign-in code mailed")
}
