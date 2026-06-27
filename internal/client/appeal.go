package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// appeal.go is the operator self-serve recourse client (ban hardening 3.3): read your own
// strikes + node-ban status, and FILE an appeal - both as the signed CLI identity
// (signRequest), so a headless provider can contest a false positive without a browser.
// See cmd/rogerai-broker/recourse.go.

// Strike is one evidence-bound anti-abuse mark, as surfaced to the operator.
type Strike struct {
	Kind     string `json:"kind"`
	Evidence string `json:"evidence"`
}

// StrikesStatus is the GET /owner/strikes view: the caller's own strikes + the durable
// owner-ban status + each owned node's ban reason + the appeal hint.
type StrikesStatus struct {
	Strikes    []Strike          `json:"strikes"`
	Count      int               `json:"count"`
	Banned     bool              `json:"banned"`
	BanReason  string            `json:"ban_reason"`
	NodeBans   map[string]string `json:"node_bans"`
	AppealNote string            `json:"appeal_note"`
}

// FetchStrikes reads GET /owner/strikes as the signed CLI identity (the operator's own
// strikes + ban status + node-ban reasons). Requires `roger login`.
func FetchStrikes(broker string) (StrikesStatus, error) {
	var st StrikesStatus
	resp, err := signedDo(http.MethodGet, broker, "/owner/strikes", nil)
	if err != nil {
		return st, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return st, payoutErr(resp.StatusCode, raw)
	}
	_ = json.Unmarshal(raw, &st)
	return st, nil
}

// AppealResult is the POST /owner/appeal response.
type AppealResult struct {
	OK             bool   `json:"ok"`
	AppealID       int64  `json:"appeal_id"`
	State          string `json:"state"`
	AutoExonerated bool   `json:"auto_exonerated"`
	NodeUnbanned   string `json:"node_unbanned"`
}

// FileAppeal POSTs /owner/appeal as the signed CLI identity: an owner-scoped self-serve
// appeal with an optional node id and a free-text reason. The broker validates the node
// belongs to the caller, records the appeal for admin review, and auto-exonerates a clear
// false-positive report-ban. Requires `roger login`.
func FileAppeal(broker, nodeID, reason string) (AppealResult, error) {
	var out AppealResult
	body, _ := json.Marshal(map[string]string{"node_id": strings.TrimSpace(nodeID), "reason": strings.TrimSpace(reason)})
	resp, err := signedDo(http.MethodPost, broker, "/owner/appeal", body)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, payoutErr(resp.StatusCode, raw)
	}
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

// Appeal is one filed appeal row (the `roger appeal status` view).
type Appeal struct {
	ID        int64  `json:"id"`
	NodeID    string `json:"node_id"`
	Reason    string `json:"reason"`
	State     string `json:"state"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
}

// ListAppeals reads GET /owner/appeal as the signed CLI identity (the caller's own
// appeals + their state). Requires `roger login`.
func ListAppeals(broker string) ([]Appeal, error) {
	resp, err := signedDo(http.MethodGet, broker, "/owner/appeal", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, payoutErr(resp.StatusCode, raw)
	}
	var d struct {
		Appeals []Appeal `json:"appeals"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.Appeals, nil
}

// BrokerClockSkew returns how far the LOCAL clock is from the broker's, derived from the
// server's HTTP Date header on a cheap GET. A positive skew means the local clock is
// AHEAD of the broker (the common cause of rejected, time-bound signatures). ok=false if
// the broker is unreachable or sends no usable Date header.
func BrokerClockSkew(broker string) (skew time.Duration, ok bool) {
	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(broker, "/")+"/health", nil)
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	d := resp.Header.Get("Date")
	if d == "" {
		return 0, false
	}
	srv, err := http.ParseTime(d)
	if err != nil {
		return 0, false
	}
	// local - server: positive => local clock is ahead of the broker.
	return time.Since(srv), true
}
