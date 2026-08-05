package main

// Broker-mediated device login: the CLI talks only to us, and the human chooses their
// provider on our page.
//
// Contract: features/auth/broker_mediated_login.feature.
//
// Five routes, and the split between them is the security design:
//
//	POST /auth/device/start    signed by the CLI  -> issues a code pair bound to its key
//	POST /auth/device/token    signed by the CLI  -> polls; only the issuing key may redeem
//	GET  /auth/device/pending  browser session    -> what the approval screen may show
//	POST /auth/device/approve  browser session    -> binds the CLI key to the approver
//	POST /auth/device/deny     browser session    -> closes it permanently
//
// The CLI half is authenticated by request signature; the human half by web session. The
// device code never crosses to the browser, and the session never crosses to the CLI.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"rogerai.fm/roger/v5/internal/deviceauth"
	"rogerai.fm/roger/v5/internal/store"
)

// deviceVerificationURI is the page a human opens. Ours, always: handing the CLI a
// provider endpoint is exactly what this flow exists to stop.
func deviceVerificationURI() string {
	return envOr("ROGERAI_DEVICE_URL", "https://rogerai.fm/device.html")
}

// deviceFlow returns the login state machine, creating it on first use. Lazy rather than
// constructor-only so no construction path can leave it nil and panic on the first login.
func (b *broker) deviceFlow() *deviceauth.Flow {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.devices == nil {
		b.devices = newDeviceFlow()
	}
	return b.devices
}

func newDeviceFlow() *deviceauth.Flow { return newDeviceFlowWithStore(nil) }

// newDeviceFlowWithStore builds the flow over an explicit store. A nil store keeps the
// in-process default, which is the single-instance deployment: no new dependency and no
// configuration change.
func newDeviceFlowWithStore(st deviceauth.Store) *deviceauth.Flow {
	cfg := deviceauth.Config{
		TTL:             10 * time.Minute,
		Interval:        5 * time.Second,
		MaxWrongCodes:   10,
		VerificationURI: deviceVerificationURI(),
	}
	if st == nil {
		return deviceauth.New(cfg)
	}
	return deviceauth.NewWithStore(cfg, st)
}

// deviceStart handles POST /auth/device/start. The request MUST be signed: the signing
// key is what the resulting code is bound to, so an unsigned start would issue a code
// bound to nobody and approvable onto anything.
func (b *broker) deviceStart(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	_, authed, ok := b.identityOf(r, body)
	if !ok || !authed {
		jsonErr(w, http.StatusUnauthorized, "starting a login requires a signed request")
		return
	}
	pubkey := r.Header.Get("X-Roger-Pubkey")
	if pubkey == "" {
		jsonErr(w, http.StatusUnauthorized, "starting a login requires a signing key")
		return
	}
	p, err := b.deviceFlow().Start(pubkey)
	if errors.Is(err, deviceauth.ErrUnavailable) {
		// Refusing beats issuing a code we know we will lose: the person otherwise walks
		// away, finds the mail, approves - and only then learns none of it counted.
		jsonErr(w, http.StatusServiceUnavailable, "sign-in is temporarily unavailable - try again in a moment")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not start a login")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      p.DeviceCode,
		"user_code":        p.UserCode,
		"verification_uri": p.VerificationURI,
		"interval":         p.IntervalSeconds,
		"expires_in":       p.ExpiresInSeconds,
	})
}

// deviceToken handles POST /auth/device/token: the CLI's poll. The signature is what
// proves it is the key the code was issued to.
func (b *broker) deviceToken(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	_, authed, ok := b.identityOf(r, body)
	if !ok || !authed {
		jsonErr(w, http.StatusUnauthorized, "polling a login requires a signed request")
		return
	}
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if json.Unmarshal(body, &req) != nil || req.DeviceCode == "" {
		jsonErr(w, http.StatusBadRequest, "device_code required")
		return
	}
	res, err := b.deviceFlow().Poll(req.DeviceCode, r.Header.Get("X-Roger-Pubkey"))
	if errors.Is(err, deviceauth.ErrUnavailable) || errors.Is(err, deviceauth.ErrCorruptRecord) {
		// NOT the uniform rejection. That rejection exists to deny a guesser any signal;
		// aimed at a legitimate CLI whose code is fine, it says the credential is bad when
		// the truth is our backend blinked. This one is retryable and says so.
		jsonErr(w, http.StatusServiceUnavailable, "sign-in is temporarily unavailable - keep polling")
		return
	}
	if err != nil {
		// Uniform: an unknown code and a code belonging to another key must look alike.
		jsonErr(w, http.StatusBadRequest, "that login is not valid")
		return
	}
	out := map[string]any{"status": string(res.Status)}
	if res.IntervalSeconds > 0 {
		out["interval"] = res.IntervalSeconds
	}
	if res.Status == deviceauth.StatusApproved {
		out["account"] = res.Account
	}
	writeJSON(w, http.StatusOK, out)
}

// deviceApprover resolves the signed-in human from the session cookie. It returns the
// display login, and the provider identity needed to create the owner row.
func (b *broker) deviceApprover(r *http.Request) (login string, gid int64, appleSub, wallet string, ok bool) {
	// The origin check lives HERE, where the cookie is read, rather than in each route.
	// Every caller of this function is by definition a credentialed browser surface, so a
	// route added later cannot forget it - which is exactly how the CSRF hole this closes
	// came to exist.
	if !originAllowed(r) {
		return "", 0, "", "", false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", 0, "", "", false
	}
	login, gid, wallet, appleSub, vok := b.verifySessionFull(c.Value)
	if !vok {
		return "", 0, "", "", false
	}
	// The wallet is carried out so the caller can recognize a FIRST-PARTY (email) session.
	// Its identity is the verified address in `login`, and it has neither a github id nor
	// an Apple sub - so without the wallet it would be indistinguishable from the older
	// Apple session that genuinely cannot bind a device.
	return login, gid, appleSub, wallet, true
}

// devicePending handles GET /auth/device/pending: what the approval screen may render.
// It requires a session, so an anonymous visitor cannot use it to probe codes, and it
// never returns the device code - an approver who learned it could redeem the login
// themselves.
func (b *broker) devicePending(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	login, _, _, _, ok := b.deviceApprover(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "sign in to review a device request")
		return
	}
	info, found := b.deviceFlow().Describe(r.URL.Query().Get("user_code"), login)
	if !found {
		jsonErr(w, http.StatusNotFound, "that code is not valid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_code":    info.UserCode,
		"requested_at": info.RequestedAt.Unix(),
	})
}

// deviceApprove handles POST /auth/device/approve: the human authorizes, and the CLI's
// key is bound to THEIR account.
//
// The key comes from the pending login, never from this request - that is what makes
// approval a decision about WHICH ACCOUNT and never about which device.
func (b *broker) deviceApprove(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	login, gid, appleSub, wallet, ok := b.deviceApprover(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "sign in to approve a device request")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req struct {
		UserCode string `json:"user_code"`
	}
	if json.Unmarshal(body, &req) != nil || req.UserCode == "" {
		jsonErr(w, http.StatusBadRequest, "user_code required")
		return
	}
	if gid == 0 && appleSub == "" && !isEmailWallet(wallet) {
		// An older Apple session predating the sub. It can sign in on the web but has
		// nothing to bind a device to, so say what to do rather than failing opaquely.
		jsonErr(w, http.StatusConflict, "please sign out and sign in again, then retry this approval")
		return
	}
	if err := b.deviceFlow().Approve(req.UserCode, login); err != nil {
		if errors.Is(err, deviceauth.ErrUnavailable) {
			// The approval did not land, so it must not read as though it did - the CLI's
			// next poll will not report an approval either.
			jsonErr(w, http.StatusServiceUnavailable, "could not record that approval - try again in a moment")
			return
		}
		jsonErr(w, http.StatusBadRequest, "that code is not valid")
		return
	}
	pubkey, ok := b.deviceFlow().BoundKey(req.UserCode)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "that code is not valid")
		return
	}
	if err := b.bindApprovedDevice(pubkey, login, gid, appleSub, wallet); err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not link this device to your account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": login})
}

// bindApprovedDevice creates the owner row that makes the CLI's key resolve to the
// approver's account, and seeds the account wallet once. Provider-agnostic by design:
// GitHub binds on its numeric id, Apple on its sub, and BindOwner preserves whichever
// link the row already had so linking one provider never drops the other.
func (b *broker) bindApprovedDevice(pubkey, login string, gid int64, appleSub, sessWallet string) error {
	o := store.Owner{Pubkey: pubkey}
	wallet := ""
	switch {
	case gid != 0:
		o.GitHubID, o.Login = gid, login
		wallet = "u_gh_" + strconv.FormatInt(gid, 10)
	case appleSub != "":
		o.AppleSub = appleSub
		wallet = walletForAppleSub(appleSub)
	default:
		// A first-party account. `login` IS the verified address - the session was minted
		// with it once the person proved they hold it, so this records the proof rather
		// than re-asserting it.
		o.Email, o.EmailVerifiedAt = login, time.Now().Unix()
		wallet = walletForEmail(login)

		// ...unless this key ALREADY belongs to a provider-linked account. Adding an email
		// to an existing account must not rename it or move its money: overwriting Login
		// would replace a GitHub handle with an address, and seeding walletForEmail while
		// the owner still resolves to u_gh_* would put the credit in a wallet nothing can
		// reach. Linking is not merging.
		if existing, found, err := b.db.OwnerByPubkey(pubkey); err == nil && found &&
			(existing.GitHubID != 0 || existing.AppleSub != "") {
			if w, ok := accountWalletForOwner(existing); ok {
				wallet = w
			}
		} else {
			o.Login = login
		}
		_ = sessWallet
	}
	if err := b.db.BindOwner(o); err != nil {
		return err
	}
	// A (re)bind can change pubkey->wallet, so drop the cached mapping now rather than
	// waiting out the TTL.
	b.invalidateOwnerWallet(pubkey)
	if _, seeded, _ := b.db.SeedOnce(wallet, b.seedFunds); seeded {
		b.invalidateSeedRemaining()
	}
	return nil
}

// deviceDeny handles POST /auth/device/deny.
func (b *broker) deviceDeny(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	login, _, _, _, ok := b.deviceApprover(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "sign in to deny a device request")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req struct {
		UserCode string `json:"user_code"`
	}
	if json.Unmarshal(body, &req) != nil || req.UserCode == "" {
		jsonErr(w, http.StatusBadRequest, "user_code required")
		return
	}
	if err := b.deviceFlow().Deny(req.UserCode, login); err != nil {
		if errors.Is(err, deviceauth.ErrUnavailable) {
			jsonErr(w, http.StatusServiceUnavailable, "could not record that denial - try again in a moment")
			return
		}
		jsonErr(w, http.StatusBadRequest, "that code is not valid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
