package webui

import (
	"net/http"

	"github.com/rogerai-fyi/roger/internal/client"
)

// The account + browse surfaces reuse internal/client (the same broker calls the CLI/TUI
// make), so there is one code path per surface and no new shared state. They need a broker
// (Options.Broker); without one they report "not configured" rather than erroring.

// loginBegin/loginPoll are package vars wrapping the client device-flow calls so the
// login handlers are testable without reaching github.com (tests stub them).
var (
	loginBegin = client.LoginBegin
	loginPoll  = client.LoginPoll
)

func (s *Server) brokerReady(w http.ResponseWriter) bool {
	if s.opts.Broker == "" {
		http.Error(w, "broker not configured", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// handleAccount returns the wallet + login + payout snapshot in one read (GET).
func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	out := map[string]any{}
	if bal, err := client.FetchBalance(s.opts.Broker, s.opts.User); err == nil {
		out["balance"] = bal.Balance
		out["logged_in"] = bal.LoggedIn
		out["monthly_cap"] = bal.MonthlyCap
		out["monthly_spend"] = bal.MonthlySpend
		// Keep the shared controller's login state in step with the broker truth (raise-only;
		// an explicit logout clears it), so a login done anywhere unlocks priced shares.
		if bal.LoggedIn {
			s.ctrl.SetLoggedIn(true)
		}
	}
	if st, err := client.FetchPayoutStatus(s.opts.Broker); err == nil {
		out["payout"] = st
	}
	writeJSON(w, out)
}

// handleLoginBegin starts the GitHub device flow and returns the URL + user code to show.
// The device handle is held server-side for the matching poll.
func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	dev, err := loginBegin(s.opts.Broker, s.opts.ClientID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.loginMu.Lock()
	d := dev
	s.loginDevice = &d
	s.loginMu.Unlock()
	writeJSON(w, map[string]any{"verification_uri": dev.VerificationURI, "user_code": dev.UserCode})
}

// handleLoginPoll blocks until the in-flight device flow is authorized (or fails), then
// marks the node logged in. One in-flight login at a time (a single-operator console).
func (s *Server) handleLoginPoll(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	s.loginMu.Lock()
	dev := s.loginDevice
	s.loginMu.Unlock()
	if dev == nil {
		http.Error(w, "no login in progress — begin first", http.StatusBadRequest)
		return
	}
	login, err := loginPoll(s.opts.Broker, s.opts.ClientID, *dev)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.loginMu.Lock()
	s.loginDevice = nil
	s.loginMu.Unlock()
	s.ctrl.SetLoggedIn(true)
	writeJSON(w, map[string]any{"ok": true, "login": login})
}

// handleLogout clears the local GitHub binding and the shared login state.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := client.LogoutReturn(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.ctrl.Logout()
	writeJSON(w, map[string]any{"ok": true})
}

// handleTopup returns a Stripe Checkout URL for adding credit. Body: {"usd":10}.
func (s *Server) handleTopup(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	var req struct {
		USD float64 `json:"usd"`
	}
	if !decode(r, &req) || req.USD <= 0 {
		http.Error(w, "usd must be > 0", http.StatusBadRequest)
		return
	}
	url, err := client.TopupURL(s.opts.Broker, s.opts.User, req.USD)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"url": url})
}

// handleLimit reads (GET) or sets (POST {cap}) the monthly spend cap.
func (s *Server) handleLimit(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Cap float64 `json:"cap"`
		}
		if !decode(r, &req) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		info, err := client.SetMonthlyLimit(s.opts.Broker, s.opts.User, req.Cap)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, info)
		return
	}
	info, err := client.GetMonthlyLimit(s.opts.Broker, s.opts.User)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, info)
}

// handlePayout returns the Connect/KYC + payable snapshot (GET).
func (s *Server) handlePayout(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	st, err := client.FetchPayoutStatus(s.opts.Broker)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, st)
}

// handlePayoutOnboard returns the Stripe Connect onboarding/KYC URL (POST).
func (s *Server) handlePayoutOnboard(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	url, err := client.FetchOnboardURL(s.opts.Broker)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"url": url})
}

// handlePayoutRequest requests a payout of the payable balance (POST).
func (s *Server) handlePayoutRequest(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	rec, err := client.RequestPayout(s.opts.Broker)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, rec)
}

// handlePayoutHistory lists past payouts (GET).
func (s *Server) handlePayoutHistory(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	recs, err := client.FetchPayoutHistory(s.opts.Broker)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, recs)
}

// handleGrants lists grants (GET) or creates one (POST {name,free}), returning the secret
// once.
func (s *Server) handleGrants(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Name string `json:"name"`
			Free bool   `json:"free"`
		}
		if !decode(r, &req) || req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		secret, err := client.GrantCreateSecret(s.opts.Broker, req.Name, req.Free)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "secret": secret})
		return
	}
	rows, err := client.GrantListRows(s.opts.Broker)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, rows)
}

// handleBrowse returns the broker's open-market discover feed (GET).
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.brokerReady(w) {
		return
	}
	offers, err := client.Discover(s.opts.Broker)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, offers)
}
