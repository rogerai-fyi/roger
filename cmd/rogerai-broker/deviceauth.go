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

func newDeviceFlow() *deviceauth.Flow {
	return deviceauth.New(deviceauth.Config{
		TTL:             10 * time.Minute,
		Interval:        5 * time.Second,
		MaxWrongCodes:   10,
		VerificationURI: deviceVerificationURI(),
	})
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
func (b *broker) deviceApprover(r *http.Request) (login string, gid int64, appleSub string, ok bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", 0, "", false
	}
	login, gid, _, appleSub, vok := b.verifySessionFull(c.Value)
	if !vok {
		return "", 0, "", false
	}
	return login, gid, appleSub, true
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
	login, _, _, ok := b.deviceApprover(r)
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
	login, gid, appleSub, ok := b.deviceApprover(r)
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
	if gid == 0 && appleSub == "" {
		// An older Apple session predating the sub. It can sign in on the web but has
		// nothing to bind a device to, so say what to do rather than failing opaquely.
		jsonErr(w, http.StatusConflict, "please sign out and sign in again, then retry this approval")
		return
	}
	if err := b.deviceFlow().Approve(req.UserCode, login); err != nil {
		jsonErr(w, http.StatusBadRequest, "that code is not valid")
		return
	}
	pubkey, ok := b.deviceFlow().BoundKey(req.UserCode)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "that code is not valid")
		return
	}
	if err := b.bindApprovedDevice(pubkey, login, gid, appleSub); err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not link this device to your account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": login})
}

// bindApprovedDevice creates the owner row that makes the CLI's key resolve to the
// approver's account, and seeds the account wallet once. Provider-agnostic by design:
// GitHub binds on its numeric id, Apple on its sub, and BindOwner preserves whichever
// link the row already had so linking one provider never drops the other.
func (b *broker) bindApprovedDevice(pubkey, login string, gid int64, appleSub string) error {
	o := store.Owner{Pubkey: pubkey}
	wallet := ""
	if gid != 0 {
		o.GitHubID, o.Login = gid, login
		wallet = "u_gh_" + strconv.FormatInt(gid, 10)
	} else {
		o.AppleSub = appleSub
		wallet = walletForAppleSub(appleSub)
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
	login, _, _, ok := b.deviceApprover(r)
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
		jsonErr(w, http.StatusBadRequest, "that code is not valid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
