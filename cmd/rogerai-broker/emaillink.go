package main

// emaillink.go: add a VERIFIED email to the account you are signed in with
// (features/auth/email_link.feature).
//
// Why it exists: an emailed sign-in code creates its OWN account unless the address is already
// verified on a provider account. A person who runs stations under GitHub or Apple and then
// signs in by email lands in a separate, empty account. Guessing a match is an account
// takeover, so the fix is to let the signed-in person PROVE the address from inside the account
// that owns the stations. Afterwards the emailed code reaches that account (emailVerify).
//
// Two proofs, both required: the SESSION proves who is adding; the mailed CODE proves the
// address is theirs. A signed token ties the code to the account that asked, so it holds across
// broker instances without server memory. Nothing merges: wallets and balances are untouched.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/emailauth"
	"rogerai.fm/roger/v6/internal/store"
)

// emailLinkFlow is the link flow: the sign-in state machine under its own Namespace, so a code
// mailed to sign in can never add an address and vice versa (even over the shared store).
func (b *broker) emailLinkFlow() *emailauth.Flow {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.emailLinks == nil {
		b.emailLinks = newLinkFlowWithStore(nil)
	}
	return b.emailLinks
}

func newLinkFlowWithStore(st emailauth.Store) *emailauth.Flow {
	cfg := emailauth.Config{TTL: emailCodeTTL, Namespace: "link"}
	if st == nil {
		return emailauth.New(cfg)
	}
	return emailauth.NewWithStore(cfg, st)
}

// linkSource is what the flow budgets requests and code guesses against: the signed-in
// ACCOUNT. One account spraying addresses cannot lock out a neighbour behind the same IP, and
// an account cannot dodge its budget by changing IP.
func linkSource(o store.Owner) string { return "acct:" + o.Pubkey }

func (b *broker) linkNow() time.Time {
	if b.linkClock != nil {
		return b.linkClock()
	}
	return time.Now()
}

// linkKey is the MAC key for link tokens: DERIVED from the session key with a purpose label,
// so a token's signature can never be valid as a session cookie's (and vice versa) however its
// payload is shaped. A '|' is legal in an email local part, and with one shared key an attacker
// could choose an address that makes the signed token text parse as a session naming a
// victim's wallet.
func (b *broker) linkKey() []byte {
	mac := hmac.New(sha256.New, b.sessionKey())
	mac.Write([]byte("rogerai/email-link-token/v1"))
	return mac.Sum(nil)
}

// mintLinkToken binds (owner, address, expiry) with the session key. It is a bearer proof that
// THIS account asked to add THIS address recently; it is useless without the mailed code.
func (b *broker) mintLinkToken(ownerPub, addr string, exp int64) string {
	payload := ownerPub + "\x00" + addr + "\x00" + strconv.FormatInt(exp, 10) // NUL: never in a valid address (a "|" is)
	mac := hmac.New(sha256.New, b.linkKey())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (b *broker) linkTokenOK(token, ownerPub, addr string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, b.linkKey())
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}
	f := strings.Split(string(raw), "\x00")
	if len(f) != 3 || f[0] != ownerPub || f[1] != addr {
		return false
	}
	exp, err := strconv.ParseInt(f[2], 10, 64)
	return err == nil && b.linkNow().Unix() <= exp
}

// linkCaller resolves the signed-in account that may add an address, writing the refusal
// itself when it cannot. ok=false means a response was already sent.
func (b *broker) linkCaller(w http.ResponseWriter, r *http.Request) (store.Owner, bool) {
	if !b.requireWebOrigin(w, r) {
		return store.Owner{}, false
	}
	_, o, found, ok := b.sessionAnyOwner(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "sign in to add an email address")
		return store.Owner{}, false
	}
	if _, gid, wallet, _ := b.sessionOwner(r); sessionProvider(wallet, gid) == "email" {
		jsonErr(w, http.StatusConflict, "your sign-in address is already your email - there is nothing to add")
		return store.Owner{}, false
	}
	if !found {
		jsonErr(w, http.StatusConflict, "this sign-in has no machines linked yet - run `roger login` on a machine with this account first")
		return store.Owner{}, false
	}
	return o, true
}

// emailLinkStart handles POST /auth/email/link/start: mail a code to the address to add.
func (b *broker) emailLinkStart(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	o, ok := b.linkCaller(w, r)
	if !ok {
		return
	}
	if !b.mail.enabled() {
		jsonErr(w, http.StatusServiceUnavailable, "emailed codes are unavailable right now - try again later")
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
	code, err := b.emailLinkFlow().Request(req.Email, linkSource(o)) // budgeted per ACCOUNT, not per IP
	switch {
	case errors.Is(err, emailauth.ErrInvalidAddress):
		jsonErr(w, http.StatusBadRequest, "that does not look like an email address we can reach")
		return
	case errors.Is(err, emailauth.ErrRateLimited):
		jsonErr(w, http.StatusTooManyRequests, "too many requests - wait a moment and try again")
		return
	case err != nil:
		jsonErr(w, http.StatusServiceUnavailable, "temporarily unavailable - try again in a moment")
		return
	}
	addr := emailauth.Normalize(req.Email)
	b.mail.sendLinkCode(addr, code, int(emailCodeTTL/time.Minute))
	// Identical whether or not the address is known to RogerAI: ownership is only checked
	// AFTER the code proves the person holds it.
	exp := b.linkNow().Add(emailCodeTTL).Unix()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": b.mintLinkToken(o.Pubkey, addr, exp)})
}

// emailLinkVerify handles POST /auth/email/link/verify: an accepted code records the address
// as verified on THIS account.
func (b *broker) emailLinkVerify(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	o, ok := b.linkCaller(w, r)
	if !ok {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &req) != nil || req.Code == "" {
		jsonErr(w, http.StatusBadRequest, "email and code required")
		return
	}
	addr := emailauth.Normalize(req.Email)
	// The token is checked BEFORE the code is submitted: another account's attempt (or a
	// tampered or expired token) is refused without spending the real holder's code.
	if !b.linkTokenOK(req.Token, o.Pubkey, addr) {
		jsonErr(w, http.StatusBadRequest, "that code is not valid")
		return
	}
	if _, err := b.emailLinkFlow().Submit(req.Email, req.Code, linkSource(o)); err != nil {
		if errors.Is(err, emailauth.ErrUnavailable) {
			jsonErr(w, http.StatusServiceUnavailable, "temporarily unavailable - try again in a moment")
			return
		}
		jsonErr(w, http.StatusBadRequest, "that code is not valid") // uniform: wrong, expired, spent
		return
	}
	switch err := b.db.LinkVerifiedEmail(o.Pubkey, addr, b.linkNow().Unix()); {
	case errors.Is(err, store.ErrEmailTaken):
		jsonErr(w, http.StatusConflict, "that address cannot be added") // never says which account
		return
	case errors.Is(err, store.ErrNoOwner):
		jsonErr(w, http.StatusConflict, "this account can no longer add an address")
		return
	case err != nil:
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	out := map[string]any{"ok": true, "email": addr}
	// Linking never merges wallets. If a separate email-only account for this address holds
	// funds, say so: they are stranded (not lost) and can be merged deliberately.
	if bal, err := b.db.BalanceOf(walletForEmail(addr), 0); err == nil && bal > 1e-6 {
		out["separate_email_balance"] = round6(bal)
		out["merge_note"] = "A separate email-only account for this address holds funds. Nothing was merged; write to labs@rogerai.fm to have the two accounts merged deliberately."
	}
	writeJSON(w, http.StatusOK, out)
}

// sendLinkCode mails the add-an-address code. Like sendSignInCode: text only, no link to
// follow, and neither the address nor the code reaches a log line or the subject.
func (m *mailer) sendLinkCode(addr, code string, expiresMinutes int) {
	if m == nil || !m.enabled() {
		return
	}
	subject := "Your RogerAI code to add this email address"
	text := fmt.Sprintf(`Someone signed in to RogerAI asked to add this email address to their account.

Your code is:

    %s

It expires in %d minutes, and it can only be used once.

Type it into the RogerAI window that asked for it. We will never ask you for
this code by phone, chat, or email reply.

If this was not you, do nothing: the address is not added without this code, and
nothing about your own account changes.

- RogerAI
`, code, expiresMinutes)
	m.sendEmail(addr, subject, "", text)
	log.Printf("email link: code mailed")
}
