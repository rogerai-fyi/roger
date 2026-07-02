package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// rc.go is the client half of /remote-control (BASE STATION, v5.0.0): the host-side RCBridge
// (tees local agent events to the broker + drains remote turns/confirms) and the owner-side
// roster/attach/stream helpers the CLI and the TUI drive. Enable/list/attach/revoke are
// owner-authed (signed with the local user key); the host's poll/events use the one-time HOST
// TOKEN as a bearer. Nothing here ever persists a transcript; the broker relays frames.

// RCEnableResult is what /rc/enable returns once: the ids + the one-time secrets. The full
// Code is shown once; CodeShort is the typeable/deep-link tail.
type RCEnableResult struct {
	SessionID   string `json:"session_id"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	CodeShort   string `json:"code_short"`
	CodeDisplay string `json:"code_display"`
	HostToken   string `json:"host_token"`
	CodeExpires int64  `json:"code_expires"`
}

// RCSessionInfo is one roster row (metadata only).
type RCSessionInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CodeDisplay string `json:"code_display"`
	Online      bool   `json:"online"`
	Revoked     bool   `json:"revoked"`
	CreatedAt   int64  `json:"created_at"`
}

// RCAttachResult is what /rc/attach returns once: the per-device attach token.
type RCAttachResult struct {
	SessionID   string `json:"session_id"`
	Name        string `json:"name"`
	AttachToken string `json:"attach_token"`
}

// rcNoTimeout is the client for long-lived RC requests (25s poll, SSE stream): signedDo's
// 30s cap would cut them, so poll/stream/events get a dedicated no-overall-timeout client.
var rcNoTimeout = &http.Client{}

// EnableRC creates a remote-control session on the broker (signed) and returns a live host
// RCBridge plus the one-time enable result. The caller starts the bridge with Run().
func EnableRC(broker, name string) (*RCBridge, RCEnableResult, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := signedDo(http.MethodPost, broker, "/rc/enable", body)
	if err != nil {
		return nil, RCEnableResult{}, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, RCEnableResult{}, payoutErr(resp.StatusCode, raw)
	}
	var res RCEnableResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, RCEnableResult{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rb := &RCBridge{
		broker: broker, sessionID: res.SessionID, hostToken: res.HostToken,
		out:     make(chan protocol.RCFrame, 256),
		inbound: make(chan protocol.RCInbound, 64),
		stop:    make(chan struct{}),
		ctx:     ctx, cancel: cancel,
	}
	return rb, res, nil
}

// ListRC fetches the owner's remote-control roster (signed).
func ListRC(broker string) ([]RCSessionInfo, error) {
	resp, err := signedDo(http.MethodGet, broker, "/rc/sessions", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, payoutErr(resp.StatusCode, raw)
	}
	var out struct {
		Sessions []RCSessionInfo `json:"sessions"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Sessions, nil
}

// RCBandInfo is one private band (metadata only) for the BASE STATION bands list.
type RCBandInfo struct {
	ID      string `json:"id"`
	Display string `json:"display"`
	Label   string `json:"label"`
	NodeID  string `json:"node_id"`
	Status  string `json:"status"`
	Revoked bool   `json:"revoked"`
}

// ListBands fetches the owner's private bands (GET /bands, signed) for BASE STATION.
func ListBands(broker string) ([]RCBandInfo, error) {
	resp, err := signedDo(http.MethodGet, broker, "/bands", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, payoutErr(resp.StatusCode, raw)
	}
	var out struct {
		Bands []RCBandInfo `json:"bands"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Bands, nil
}

// AttachRC exchanges a link code for a per-device attach token (signed, same-account).
func AttachRC(broker, code string) (RCAttachResult, error) {
	body, _ := json.Marshal(map[string]string{"code": code})
	resp, err := signedDo(http.MethodPost, broker, "/rc/attach", body)
	if err != nil {
		return RCAttachResult{}, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RCAttachResult{}, payoutErr(resp.StatusCode, raw)
	}
	var res RCAttachResult
	_ = json.Unmarshal(raw, &res)
	return res, nil
}

// JoinRC mints an attach token for one of the OWNER's OWN sessions by id (no code needed —
// same-account is sufficient for an already-logged-in surface). Signed.
func JoinRC(broker, sessionID string) (string, error) {
	resp, err := signedDo(http.MethodPost, broker, "/rc/"+sessionID+"/join", nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", payoutErr(resp.StatusCode, raw)
	}
	var res RCAttachResult
	_ = json.Unmarshal(raw, &res)
	return res.AttachToken, nil
}

// RevokeRC ends one session (sessionID != "") or every session (sessionID == "") (signed).
func RevokeRC(broker, sessionID string) error {
	path := "/rc/revoke-all"
	if sessionID != "" {
		path = "/rc/" + sessionID + "/disable"
	}
	// nil body: the broker verifies the owner over the EXACT bytes sent, and rcRevokeAll /
	// rcDisable resolve the owner from an empty body (rcOwnerWallet(r, nil)) — a {} body would
	// make the signature cover different bytes than the broker checks and 403.
	resp, err := signedDo(http.MethodPost, broker, path, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return payoutErr(resp.StatusCode, raw)
	}
	return nil
}

// SendRC posts a viewer turn/confirm to a session (signed + attach bearer).
func SendRC(broker, sessionID, attachToken string, in protocol.RCInbound) error {
	body, _ := json.Marshal(in)
	req, _ := http.NewRequest(http.MethodPost, broker+"/rc/"+sessionID+"/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Roger-Attach", attachToken)
	signRequest(req, body)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return payoutErr(resp.StatusCode, raw)
	}
	return nil
}

// StreamRC opens the viewer SSE stream (signed + attach bearer) and calls onFrame for each
// RCFrame until ctx is cancelled, the session ends, or the connection drops. It honors
// id: (Last-Event-ID) so a caller can reconnect from the last seen seq.
func StreamRC(ctx context.Context, broker, sessionID, attachToken string, lastSeq uint64, onFrame func(protocol.RCFrame)) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, broker+"/rc/"+sessionID+"/stream", nil)
	req.Header.Set("X-Roger-Attach", attachToken)
	if lastSeq > 0 {
		req.Header.Set("Last-Event-ID", fmt.Sprintf("%d", lastSeq))
	}
	signRequest(req, nil)
	resp, err := rcNoTimeout.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return payoutErr(resp.StatusCode, raw)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // SSE data lines can be large
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // skip id:/blank/comment lines; the frame carries its own Seq
		}
		var f protocol.RCFrame
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &f) == nil {
			onFrame(f)
			if f.Kind == protocol.RCKindEnded {
				return nil
			}
		}
	}
	return sc.Err()
}

// --- the host RCBridge ---------------------------------------------------

// RCBridge is the host side of a live session: Emit tees local agent events to viewers; the
// poll loop drains remote turns/confirms/backfill onto Inbound(); Disable takes it off the
// air. Auth to poll/events is the one-time HOST TOKEN (bearer), never a signature.
type RCBridge struct {
	broker, sessionID, hostToken string
	out                          chan protocol.RCFrame
	inbound                      chan protocol.RCInbound
	stop                         chan struct{}
	ctx                          context.Context // cancels in-flight poll/events on Stop
	cancel                       context.CancelFunc
	stopOnce                     sync.Once
	stopped                      atomic.Bool
}

// SessionID reports the bridge's session id (for the roster / disable).
func (rb *RCBridge) SessionID() string { return rb.sessionID }

// Emit queues a local agent frame for the viewers (non-blocking: a full buffer drops the
// frame rather than stalling the UI goroutine).
func (rb *RCBridge) Emit(f protocol.RCFrame) {
	if rb == nil || rb.stopped.Load() {
		return
	}
	select {
	case rb.out <- f:
	default:
	}
}

// Inbound is the channel of remote turns/confirms/backfill requests; the UI drains it via a
// re-armed Cmd and dispatches each on its own goroutine.
func (rb *RCBridge) Inbound() <-chan protocol.RCInbound { return rb.inbound }

// Done is closed when the bridge is Stopped (revoked / quit), so the UI's parked inbound-drain
// Cmd unblocks cleanly instead of leaking on the never-closed inbound channel.
func (rb *RCBridge) Done() <-chan struct{} { return rb.stop }

// Run starts the poll + event-pump goroutines. Idempotent-safe to call once after EnableRC.
func (rb *RCBridge) Run() {
	go rb.pollLoop()
	go rb.eventPump()
}

// Stop halts polling + pumping and cancels any in-flight request (the session stays alive on
// the broker; used on TUI quit).
func (rb *RCBridge) Stop() {
	rb.stopOnce.Do(func() {
		rb.stopped.Store(true)
		close(rb.stop)
		if rb.cancel != nil {
			rb.cancel()
		}
	})
}

// Disable takes the session off the air (revoke) and stops the bridge.
func (rb *RCBridge) Disable() error {
	err := RevokeRC(rb.broker, rb.sessionID)
	rb.Stop()
	return err
}

// pollLoop long-polls the broker for inbound messages, delivering each to Inbound(). A 204 is
// a normal re-poll; a transport error backs off; ctx/stop ends it.
func (rb *RCBridge) pollLoop() {
	backoff := time.Second
	for {
		select {
		case <-rb.stop:
			return
		default:
		}
		req, _ := http.NewRequestWithContext(rb.ctx, http.MethodGet, rb.broker+"/rc/"+rb.sessionID+"/poll", nil)
		req.Header.Set("Authorization", "Bearer "+rb.hostToken)
		resp, err := rcNoTimeout.Do(req)
		if err != nil {
			select {
			case <-rb.stop:
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			rb.Stop() // the session was revoked; stop cleanly
			return
		}
		if resp.StatusCode == http.StatusNoContent {
			resp.Body.Close()
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var in protocol.RCInbound
		if json.Unmarshal(raw, &in) == nil && in.Kind != "" {
			select {
			case rb.inbound <- in:
			case <-rb.stop:
				return
			}
		}
	}
}

// eventPump batches emitted frames and POSTs them to /rc/{sid}/events. It coalesces frames
// that arrive within a short window so a burst of a turn's steps is one round-trip.
func (rb *RCBridge) eventPump() {
	for {
		select {
		case <-rb.stop:
			return
		case f := <-rb.out:
			batch := []protocol.RCFrame{f}
			// Drain anything already queued (bounded) into the same POST.
			for len(batch) < 64 {
				select {
				case g := <-rb.out:
					batch = append(batch, g)
				default:
					goto flush
				}
			}
		flush:
			rb.postEvents(batch)
		}
	}
}

func (rb *RCBridge) postEvents(frames []protocol.RCFrame) {
	body, _ := json.Marshal(frames)
	req, _ := http.NewRequest(http.MethodPost, rb.broker+"/rc/"+rb.sessionID+"/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rb.hostToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return // best-effort; a dropped event batch is not fatal (viewers reconnect/backfill)
	}
	resp.Body.Close()
}
