package towerjoin

// link.go is the Tower's side of the joined relay link: the session it holds open with Roger
// Core, and the inventory it pushes over it.
//
// It is the missing consumer. Core's link routes were built and exercised only by tests
// speaking HTTP directly, which meant the protocol had one participant: a contract asserted
// from the server's side alone. In particular Core distinguishes 409-resend from 400-refuse
// so a Tower does not retry the wrong one - a distinction nothing was in a position to act on
// until this file existed.
//
// SIGNED AS THE TOWER, NOT THE OPERATOR. Every other call in this package signs with the
// operator's account key, because an operator is asking Core for something. These are the
// machine talking, and Core authenticates them by hashing the signing key and comparing it
// with the one recorded at admission. The two keys are different on purpose and mixing them
// up fails closed at the server.
//
// WHAT THIS DOES NOT DO YET, stated here rather than discovered: it cannot push a Station's
// offer, because a Station signs its own offers with its assertion key and no Station-side
// software exists to do that. A Tower with no attached Stations pushes a valid inventory with
// zero leaves, which is honest - it says "I am here and I have nothing" - and is exactly what
// the link needs in order to be real before Stations can sign.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towercore/inv"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towerobj"
)

// ErrNeedFullInventory is Core saying it cannot place what we sent and wants a snapshot. It
// is a distinct error because the caller must do something DIFFERENT about it: resend
// everything, rather than fix and retry the same thing.
var ErrNeedFullInventory = errors.New("Roger Core needs a full inventory")

// ErrRefused is Core refusing what we sent. Retrying it unchanged will fail again.
var ErrRefused = errors.New("Roger Core refused the inventory")

// ErrUnreachable is a transport failure, kept apart from a refusal because the two call for
// opposite behaviour: back off and retry the same thing, versus stop and fix it.
var ErrUnreachable = errors.New("could not reach Roger Core")

// Session is a live link.
type Session struct {
	TowerID           string
	SessionID         string
	Heartbeat         time.Duration
	Freshness         time.Duration
	NeedFullInventory bool
}

// Head is the chain position a Tower quotes on reconnect. Carrying it is what turns a
// reconnect into a hundred bytes instead of a snapshot - when Core is in step.
type Head struct {
	Revision int64  `json:"revision"`
	Hash     string `json:"hash"`
}

// OpenSession starts (or resumes) the link.
func OpenSession(st *tower.State, head Head) (Session, error) {
	adm, ok := LoadAdmission(st.Dir())
	if !ok || adm.TowerID == "" {
		return Session{}, errors.New("this Tower is not registered yet - run `roger-tower register` first")
	}
	body, err := json.Marshal(link.Hello{
		Network:  link.PublicNetwork,
		Versions: []int{1},
		TowerID:  adm.TowerID,
		// Both are integrity properties rather than features, and Core refuses a session
		// without them: without the first a modified frame is indistinguishable from an
		// honest one, and without the second Core's traffic would be readable by us.
		Capabilities: []string{link.CapIntegrity, link.CapInnerSession},
		HeadRevision: head.Revision,
		HeadHash:     head.Hash,
	})
	if err != nil {
		return Session{}, err
	}
	var acc link.Accepted
	if err := towerPost(st, "/tower/session", body, &acc); err != nil {
		return Session{}, err
	}
	return Session{
		TowerID:           adm.TowerID,
		SessionID:         acc.SessionID,
		Heartbeat:         time.Duration(acc.HeartbeatSeconds) * time.Second,
		Freshness:         time.Duration(acc.FreshnessSeconds) * time.Second,
		NeedFullInventory: acc.NeedFullInventory,
	}, nil
}

// Heartbeat tells Core we are still here. The frame IS the liveness signal; losing one is
// survivable because the freshness window is several heartbeats wide.
func (s Session) SendHeartbeat(st *tower.State) error {
	body, err := json.Marshal(link.Frame{
		Network: link.PublicNetwork, Version: 1, TowerID: s.TowerID, SessionID: s.SessionID,
	})
	if err != nil {
		return err
	}
	return towerPost(st, "/tower/session/heartbeat", body, nil)
}

// Close drains: Core drops our inventory at once rather than letting it age out over the
// freshness window. Leaving without it is the difference between a clean handover and three
// minutes of Core offering Stations that have gone.
func (s Session) Close(st *tower.State) error {
	body, err := json.Marshal(link.Frame{
		Network: link.PublicNetwork, Version: 1, TowerID: s.TowerID, SessionID: s.SessionID,
	})
	if err != nil {
		return err
	}
	return towerPost(st, "/tower/session/close", body, nil)
}

// InventoryResult is what Core accepted.
type InventoryResult struct {
	Revision int64  `json:"revision"`
	Hash     string `json:"hash"`
	Routable int    `json:"routable"`
	Excluded []struct {
		StationID string `json:"station_id"`
		OfferID   string `json:"offer_id"`
		Reason    string `json:"reason"`
	} `json:"excluded"`
}

// PushFullInventory sends a complete signed revision.
//
// leaves are Station-SIGNED offer objects, passed through untouched: this Tower relays them
// and must not be able to alter one, so it never re-encodes them. An empty slice is a valid
// inventory meaning "I have nothing right now".
func PushFullInventory(st *tower.State, revision int64, prevHash string, leaves []json.RawMessage) (InventoryResult, error) {
	adm, ok := LoadAdmission(st.Dir())
	if !ok {
		return InventoryResult{}, errors.New("this Tower is not registered yet")
	}
	now := time.Now()
	body := map[string]any{
		"network": link.PublicNetwork, "tower_id": adm.TowerID,
		"revision": towerobj.FormatInt(revision), "prev_hash": prevHash,
		// Both heads are required by the format. A Tower with no lease or lifecycle history of
		// its own still names the genesis position rather than omitting the members, because
		// the schema is closed and an absent member is a refusal.
		"lease_head": "genesis", "lifecycle_head": "genesis",
		"issued":  towerobj.FormatInt(now.Unix()),
		"expires": towerobj.FormatInt(now.Add(inventoryLifetime).Unix()),
		"leaves":  rawLeaves(leaves),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return InventoryResult{}, err
	}
	identity, err := st.IdentityKey()
	if err != nil {
		return InventoryResult{}, err
	}
	signed, err := towerobj.Sign(identity, link.PublicNetwork, inv.TypeInventory, inv.Version, raw, "sig")
	if err != nil {
		return InventoryResult{}, err
	}
	var out InventoryResult
	if err := towerPost(st, "/tower/inventory", signed, &out); err != nil {
		return InventoryResult{}, err
	}
	return out, nil
}

// inventoryLifetime is how long a pushed revision is good for. Comfortably longer than the
// heartbeat, so an inventory never expires under a Tower that is plainly still here, and
// short enough that a Tower which vanishes without draining ages out on its own.
const inventoryLifetime = 30 * time.Minute

// rawLeaves keeps Station signatures intact by never decoding them.
func rawLeaves(leaves []json.RawMessage) []any {
	out := make([]any, 0, len(leaves))
	for _, l := range leaves {
		var v any
		if err := json.Unmarshal(l, &v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// towerPost signs as the TOWER and classifies the answer.
func towerPost(st *tower.State, path string, body []byte, out any) error {
	identity, err := st.IdentityKey()
	if err != nil {
		return fmt.Errorf("this Tower's identity key is unreadable: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, brokerBase()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(identity, http.MethodPost, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusConflict:
		// The distinction this file exists to act on. Core cannot place what we sent and
		// wants a snapshot; retrying the same delta would fail identically forever.
		var conflict struct {
			NeedFull bool   `json:"need_full_inventory"`
			Error    string `json:"error"`
		}
		_ = json.Unmarshal(raw, &conflict)
		if conflict.NeedFull {
			return fmt.Errorf("%w: %s", ErrNeedFullInventory, conflict.Error)
		}
		return fmt.Errorf("%w: %s", ErrRefused, strings.TrimSpace(string(raw)))
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: this Tower may not hold a link right now (suspended, revoked, or its lease lapsed)", ErrRefused)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("%w: %s", ErrRefused, bandOrRawError(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("could not read Roger Core's reply: %w", err)
		}
	}
	return nil
}

// bandOrRawError pulls the sentence Core wrote out of its error envelope.
func bandOrRawError(raw []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	return string(raw)
}
