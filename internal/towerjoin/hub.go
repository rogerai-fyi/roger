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

	"rogerai.fm/roger/v5/internal/tower"
)

// DispatchKey fetches Roger Core's grant-signing public key from the public
// /tower/dispatch/key endpoint. The tower pins it for the lifetime of its serve: it is what
// EdgeGrantMeta verifies consumer-submitted grants against.
func DispatchKey() (ed25519.PublicKey, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(brokerBase() + "/tower/dispatch/key")
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

// HubNode is one self-attached node this Tower's hub serves.
type HubNode struct {
	StationID string `json:"station_id"`
	HubToken  string `json:"hub_token"`
	State     string `json:"state"`
}

// HubNodes fetches the Tower's own self-attached nodes + their polling tokens, over the
// Tower's signed request (only the named tower's own signature is accepted by Core).
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
func SettleEdgeReceipt(st *tower.State, stationID, attemptID string, receipt []byte) error {
	body, err := json.Marshal(map[string]string{
		"tower_id":   st.TowerID,
		"station_id": stationID,
		"attempt_id": attemptID,
		"receipt":    base64.StdEncoding.EncodeToString(receipt),
	})
	if err != nil {
		return err
	}
	status, err := towerPostStatus(st, "/tower/edge/settle", body, nil, nil)
	if err != nil && status != http.StatusConflict {
		return err
	}
	return nil
}
