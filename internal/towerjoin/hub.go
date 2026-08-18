package towerjoin

// hub.go is the joined Tower's HUB side of Option C, Topology 2: fetching Core's grant key
// (so the tower can authorize consumer submits by grant METADATA while staying blind to
// content) and the list of self-attached nodes it must serve, each with the bearer token
// that node polls with.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/tower"
)

// DispatchKey fetches Roger Core's grant-signing public key from the public
// /tower/dispatch/key endpoint. The tower pins it for the lifetime of its serve: it is what
// EdgeGrantMeta verifies consumer-submitted grants against - which is exactly why the
// transport that delivers it must be trusted (audit M2): a forged key here means every
// attacker-signed grant verifies.
func DispatchKey() (ed25519.PublicKey, error) {
	base := brokerBase()
	if err := protocol.TrustedBase(base); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: protocol.NoDowngradeRedirect}
	resp, err := client.Get(base + "/tower/dispatch/key")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dispatch key fetch: %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		DispatchKey string `json:"dispatch_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unreadable dispatch key response: %w", err)
	}
	key, err := hex.DecodeString(out.DispatchKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("the dispatch key is not a hex ed25519 public key")
	}
	return ed25519.PublicKey(key), nil
}

// HubNode is one self-attached node this Tower's hub serves, and how the hub authenticates it.
type HubNode struct {
	StationID string `json:"station_id"`
	// AssertionKey is the Station's hex Ed25519 assertion key, as recorded on its attachment
	// at Core. The hub verifies every signed poll and completion against it. Empty only from
	// a Core older than signed polls, which is the one case the legacy token still covers.
	AssertionKey string `json:"assertion_key"`
	// HubToken is the pre-signature bearer credential, kept for one release so a node built
	// before signed polls keeps earning. See towerhub.Server.AllowLegacyBearer.
	HubToken string `json:"hub_token"`
	State    string `json:"state"`
}

// HubNodes fetches the Tower's own self-attached nodes + the credentials its hub authenticates
// them with, over the Tower's signed request (only the named tower's own signature is accepted
// by Core).
func HubNodes(st *tower.State) ([]HubNode, error) {
	body, err := json.Marshal(map[string]string{"tower_id": st.TowerID})
	if err != nil {
		return nil, err
	}
	var out struct {
		Nodes []HubNode `json:"nodes"`
	}
	if err := towerPost(st, "/tower/hub/nodes", body, &out); err != nil {
		return nil, err
	}
	return out.Nodes, nil
}

// SettleEdgeReceipt forwards a node's signed receipt to Roger Core for settlement, as the
// TOWER (its own signed request - the same authentication the byte-path courier uses). The
// receipt is opaque to the tower; Core verifies it against the station's recorded key. A 409
// means the attempt already settled - a retry or a race, both fine.
// ErrSettlePermanent marks a Core refusal retrying cannot fix - a 4xx other than the 409
// already-settled answer. A courier should abandon (loudly) rather than hammer Core with a
// receipt it has already judged invalid.
var ErrSettlePermanent = errors.New("roger core refused this receipt permanently")

// wireIn/wireOut are the byte sizes of the sealed request and sealed result THIS tower
// actually relayed - its own independent count, which settlement uses only as an UPPER bound
// on the billable bytes (the attestation can lower a bill, never raise one). Zero = unknown.
func SettleEdgeReceipt(st *tower.State, stationID, attemptID string, receipt []byte, wireIn, wireOut int64) error {
	body, err := json.Marshal(map[string]any{
		"tower_id":   st.TowerID,
		"station_id": stationID,
		"attempt_id": attemptID,
		"receipt":    base64.StdEncoding.EncodeToString(receipt),
		"wire_in":    wireIn,
		"wire_out":   wireOut,
	})
	if err != nil {
		return err
	}
	status, err := towerPostStatus(st, "/tower/edge/settle", body, nil, nil)
	if err == nil || status == http.StatusConflict {
		return nil
	}
	if status >= 400 && status < 500 {
		return fmt.Errorf("%w: %v", ErrSettlePermanent, err)
	}
	return err
}

// WantedAudit is one transcript Core wants from this tower's fleet.
type WantedAudit struct {
	AttemptID string `json:"attempt_id"`
	StationID string `json:"station_id"`
}

// WantedAudits fetches what Core wants audited from this Tower - the hub relays each
// Station's slice of it to the node that can actually answer (poll-only nodes cannot be
// dialed the way the classic courier dials --station endpoints).
func WantedAudits(st *tower.State) ([]WantedAudit, error) {
	body, err := json.Marshal(map[string]any{"tower_id": st.TowerID})
	if err != nil {
		return nil, err
	}
	var out struct {
		Wanted []WantedAudit `json:"wanted"`
	}
	if err := towerPost(st, "/tower/audit/wanted", body, &out); err != nil {
		return nil, err
	}
	return out.Wanted, nil
}

// ForwardAuditTranscript forwards a hub node's answered audit to Core, tower-signed - the
// same shape the classic courier forwards, from the hub plane instead.
func ForwardAuditTranscript(st *tower.State, attemptID string, available bool, sealedBundle, transcript, request, response string) error {
	body, err := json.Marshal(map[string]any{
		"tower_id": st.TowerID, "attempt_id": attemptID,
		"available": available, "sealed_bundle": sealedBundle,
		"transcript": transcript, "request": request, "response": response,
	})
	if err != nil {
		return err
	}
	return towerPost(st, "/tower/audit/transcript", body, nil)
}
