package towerhub

// pull.go: the wire shape of work a Tower PULLS over its link instead of being dialed
// (features/tower/tower_pull_dispatch.feature), shared by Core (which queues it) and the Tower
// (which runs it against its own hub), so the two cannot drift.
//
// A piece of work is the HTTP request Core would otherwise have sent the hub; its answer is the
// hub's HTTP answer. Only the two requests Core ever makes of a hub are allowed: a sealed submit,
// and the Station-free identity check (a poll carrying only a nonce). Everything else a hub
// serves belongs to its nodes and is never reachable through a pull.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

// PullWork is one piece of work queued for a Tower.
type PullWork struct {
	ID     string            `json:"id"`
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  string            `json:"query,omitempty"`
	Header map[string]string `json:"header,omitempty"`
	Body   []byte            `json:"body,omitempty"`
	Exp    int64             `json:"exp"` // unix ms: the pickup deadline
}

// PullAnswer is the hub's answer to one piece of work, as the Tower posts it back.
type PullAnswer struct {
	TowerID   string            `json:"tower_id"`
	SessionID string            `json:"session_id"`
	ID        string            `json:"id"`
	Status    int               `json:"status"`
	Header    map[string]string `json:"header,omitempty"`
	Body      []byte            `json:"body,omitempty"`
}

// PullHeaders are the only headers carried either way: what a submit and the hub's identity
// proof need. Nothing else of Core's or the Tower's crosses.
var PullHeaders = []string{"Content-Type", HubEpochHeader, HubKeyHeader, HubProofHeader}

// PickHeaders keeps only PullHeaders.
func PickHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for _, k := range PullHeaders {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// HeaderOf is PickHeaders' inverse, keeping only PullHeaders.
func HeaderOf(m map[string]string) http.Header {
	h := http.Header{}
	for _, k := range PullHeaders {
		if v, ok := m[k]; ok {
			h.Set(k, v)
		}
	}
	return h
}

// PullAllowed reports whether a piece of work is one of the two requests Core makes of a hub.
func PullAllowed(w PullWork) bool {
	switch {
	case w.Method == http.MethodPost && w.Path == PathSubmit && w.Query == "":
		return true
	case w.Method == http.MethodGet && w.Path == PathPoll:
		q, err := url.ParseQuery(w.Query)
		return err == nil && len(q) == 1 && len(q[nonceParam]) == 1 && len(w.Body) == 0
	}
	return false
}

// ServePulled runs one piece of work against the hub's handler, in process, and returns the
// hub's answer, its body capped at maxBody. Work that is not PullAllowed is answered 403 here
// and never reaches the hub.
func ServePulled(ctx context.Context, h http.Handler, w PullWork, maxBody int) PullAnswer {
	ans := PullAnswer{ID: w.ID}
	if !PullAllowed(w) {
		ans.Status = http.StatusForbidden
		return ans
	}
	target := w.Path
	if w.Query != "" {
		target += "?" + w.Query
	}
	req, err := http.NewRequestWithContext(ctx, w.Method, target, strings.NewReader(string(w.Body)))
	if err != nil {
		ans.Status = http.StatusBadRequest // a request that does not parse never reaches the hub
		return ans
	}
	req.RemoteAddr = "pulled"
	for k, v := range w.Header {
		if HeaderOf(map[string]string{k: v}).Get(k) != "" {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	ans.Status, ans.Header = rec.Code, PickHeaders(rec.Header())
	body := rec.Body.Bytes()
	if len(body) > maxBody {
		ans.Status, ans.Body = http.StatusBadGateway, nil
		return ans
	}
	ans.Body = body
	return ans
}
