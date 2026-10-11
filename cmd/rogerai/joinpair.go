package main

// joinpair.go is `roger join <address>` (features/join/approve.feature): no invite, no copy-paste.
// This machine asks the Tower (or the Edge authority) at the address to let it in, shows the
// matching phrase, and waits for the owner to approve on their screen. The phrase covers this
// machine's key, the key the other side presented (a Tower's TLS key; an Edge's root), both
// ephemeral keys and both nonces (internal/joinsas), so a device in the middle shows a different
// phrase. On approval a Tower is pinned to the key seen during the pairing, and an Edge is enrolled
// in with its root pinned to the one the phrase covered.

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/joinsas"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towerpin"
)

// pairWaitFor is how long this machine waits for the owner (the request's lifetime).
var pairWaitFor = localplane.PairLifetime

// pairPeer is the side being asked: what it is called until it says its name, the key the phrase
// covers, and how a signed request reaches it.
type pairPeer struct {
	called string
	key    string
	post   func(path string, body []byte, timeout time.Duration) (int, []byte, error)
}

// joinPairAt asks the Tower at addr to let this machine in, as a client or (station) a Station; an
// Edge authority at addr enrolls this machine instead.
func joinPairAt(out io.Writer, addr, name string, station bool) error {
	fp, err := towerpin.Fetch(addr, joinTimeout)
	if err != nil {
		if root := edgeRootAt("http://" + addr); root != nil {
			if station {
				return usagef("%s is an Edge authority: a station joins a Tower. Run roger join %s without --station", addr, addr)
			}
			return joinEdgePair(out, addr, name, edgeauth.RootFingerprint(root))
		}
		return fmt.Errorf("can't reach %s (nothing answered as a Tower over TLS: %v)\nis roger tower serve running there? Check the address, or ask the owner for an invite", addr, err)
	}
	key, kind, models := client.LoadOrCreateUserKey(), "client", []string(nil)
	if station {
		key, kind, models = agent.NodeKey(), "station", joinStationModels()
	}
	pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	peer := pairPeer{called: "the Tower at " + addr, key: fp,
		post: func(path string, body []byte, timeout time.Duration) (int, []byte, error) {
			return towerPost(key, pubHex, addr, fp, path, body, protocol.NewNonce(), timeout)
		}}
	ans, err := pairWith(out, addr, peer, key, kind, name, models)
	if err != nil {
		return err
	}
	return joinRecord(out, addr, joininvite.Invite{FP: fp}, ans, name)
}

// joinEdgePair asks the Edge authority at addr, whose root (as this machine fetched it) is root, to
// let this machine in; on approval it enrolls with that root pinned, so the root it keeps is the
// one the phrase covered.
func joinEdgePair(out io.Writer, addr, name, root string) error {
	key := edgeUserKey()
	pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	base := "http://" + addr
	peer := pairPeer{called: "the Edge at " + addr, key: root,
		post: func(path string, body []byte, timeout time.Duration) (int, []byte, error) {
			return signedPost(&http.Client{Timeout: timeout}, base, key, pubHex, path, body, protocol.NewNonce())
		}}
	if _, err := pairWith(out, addr, peer, key, "client", name, nil); err != nil {
		return err
	}
	args := []string{"--authority", base, "--root", root}
	if name != "" {
		args = append([]string{name}, args...)
	}
	return edgeEnrollCmd(loadConfig(), args, true)
}

// pairWith runs one request-and-approve against peer and returns the owner's approval.
func pairWith(out io.Writer, addr string, peer pairPeer, key ed25519.PrivateKey, kind, name string, models []string) (localplane.JoinAnswer, error) {
	var none localplane.JoinAnswer
	pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return none, err
	}
	ephHex := hex.EncodeToString(eph.PublicKey().Bytes())
	mine := joinsas.NewNonce()

	body, _ := json.Marshal(localplane.PairStartBody{Kind: kind, Name: name, Models: models, ClientEph: ephHex, Commit: joinsas.Commit(mine)})
	status, raw, err := peer.post("/local/pair/start", body, joinTimeout)
	if err != nil {
		return none, err
	}
	if status != http.StatusOK {
		if msg := towerError(raw); msg != "" && status != http.StatusUnauthorized {
			return none, &joinRefusal{msg: msg}
		}
		return none, &joinRefusal{msg: addr + " refused the request\nask the owner, or use an invite"}
	}
	var start localplane.PairStartAnswer
	if err := json.Unmarshal(raw, &start); err != nil {
		return none, fmt.Errorf("%s answered something that is not a join request answer", addr)
	}
	tower := start.Tower
	if tower == "" {
		tower = peer.called
	}
	phrase := joinsas.Phrase(joinsas.Transcript{
		ClientKey: pubHex, TowerKey: peer.key, ClientEph: ephHex, TowerEph: start.TowerEph,
		ClientNonce: mine, TowerNonce: start.TowerNonce,
	})
	reveal, _ := json.Marshal(localplane.PairRevealBody{ID: start.ID, ClientNonce: mine})
	if status, _, err = peer.post("/local/pair/reveal", reveal, joinTimeout); err != nil {
		return none, err
	}
	if status != http.StatusOK {
		return none, &joinRefusal{msg: tower + " refused the request\nask the owner, or use an invite"}
	}
	fmt.Fprintf(out, "asking %s to let you in. Check this phrase matches the owner's screen: %s\n", tower, phrase)
	fmt.Fprintln(out, "the owner should see the same phrase; if it differs, someone is in the middle: do not continue")

	deadline := time.Now().Add(pairWaitFor)
	wait, _ := json.Marshal(localplane.PairWaitBody{ID: start.ID})
	for time.Now().Before(deadline) {
		status, raw, err := peer.post("/local/pair/wait", wait, localplane.PairLifetime)
		if err != nil {
			return none, err
		}
		var got localplane.PairWaitAnswer
		if status != http.StatusOK || json.Unmarshal(raw, &got) != nil {
			return none, &joinRefusal{msg: tower + " refused the request\nask the owner, or use an invite"}
		}
		switch got.Status {
		case "approved":
			return got.Answer, nil
		case "refused":
			return none, &joinRefusal{msg: start.Tower + " refused the request\nask the owner, or use an invite"}
		case "expired":
			return none, errPairUnanswered(start.Tower)
		}
	}
	return none, errPairUnanswered(start.Tower)
}

func errPairUnanswered(tower string) error {
	return errors.New(tower + " did not answer in 2 minutes. Ask the owner to open roger, or use an invite")
}
