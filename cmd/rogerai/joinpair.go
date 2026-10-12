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
func joinPairAt(out io.Writer, addr, name string, station bool, explicit ...string) error {
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
	// A Tower this machine already pinned is checked against that pin before anything is sent.
	pin := loadConfig().LanTowerPins[addr]
	if pin != "" && pin != fp {
		return &joinRefusal{msg: client.TowerKeyChanged(addr)}
	}
	key, kind, models := client.LoadOrCreateUserKey(), "client", []string(nil)
	if station {
		key, kind = agent.NodeKey(), "station"
		if models, err = joinStationModels(explicit); err != nil {
			return err
		}
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
	if ans.AlreadyMember && pin == "" {
		// Admitted, but this config never pinned the Tower: say so, and pin only through a
		// path that proves the key (an invite), never on this unconfirmed answer.
		fmt.Fprintf(out, "already a member of %s · nothing to do\n", towerWhere(addr, ans.Tower))
		fmt.Fprintln(out, "this machine has not pinned it: to pin it, ask the owner for an invite (roger tower invite), then run: roger join <invite>")
		return nil
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
	p, err := pairStart(addr, peer, key, kind, name, models)
	if err != nil {
		return localplane.JoinAnswer{}, err
	}
	if p.already != nil {
		return *p.already, nil
	}
	fmt.Fprintf(out, "asking %s to let you in. Check this phrase matches the owner's screen: %s\n", p.tower, p.phrase)
	fmt.Fprintln(out, "the owner should see the same phrase; if it differs, someone is in the middle: do not continue")
	return p.wait(nil)
}

// pairing is a request the other side accepted and is showing its owner: the phrase both
// screens show, who is being asked, and wait, which blocks until the owner answers (or stop
// closes). already is set instead when this machine was admitted before and nothing waits.
type pairing struct {
	phrase, tower string
	already       *localplane.JoinAnswer
	wait          func(stop <-chan struct{}) (localplane.JoinAnswer, error)
}

// pairStart posts the request and reveals the nonce, up to the point where the owner on the
// other side must act. It prints nothing: the CLI and the TUI's j prompt both build on it.
func pairStart(addr string, peer pairPeer, key ed25519.PrivateKey, kind, name string, models []string) (*pairing, error) {
	pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	ephHex := hex.EncodeToString(eph.PublicKey().Bytes())
	mine := joinsas.NewNonce()

	body, _ := json.Marshal(localplane.PairStartBody{Kind: kind, Name: name, Models: models, ClientEph: ephHex, Commit: joinsas.Commit(mine)})
	status, raw, err := peer.post("/local/pair/start", body, joinTimeout)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		if msg := towerError(raw); msg != "" && status != http.StatusUnauthorized {
			return nil, &joinRefusal{msg: msg}
		}
		return nil, &joinRefusal{msg: addr + " refused the request\nask the owner, or use an invite"}
	}
	var start localplane.PairStartAnswer
	if err := json.Unmarshal(raw, &start); err != nil {
		return nil, fmt.Errorf("%s answered something that is not a join request answer", addr)
	}
	if start.AlreadyMember {
		// Already admitted: no request was created and there is nothing to approve.
		return &pairing{already: &localplane.JoinAnswer{Joined: kind, AlreadyMember: true, Tower: start.Tower}}, nil
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
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &joinRefusal{msg: tower + " refused the request\nask the owner, or use an invite"}
	}
	wait := func(stop <-chan struct{}) (localplane.JoinAnswer, error) {
		var none localplane.JoinAnswer
		deadline := time.Now().Add(pairWaitFor)
		body, _ := json.Marshal(localplane.PairWaitBody{ID: start.ID})
		for time.Now().Before(deadline) {
			select {
			case <-stop:
				return none, errPairCancelled
			default:
			}
			status, raw, err := peer.post("/local/pair/wait", body, localplane.PairLifetime)
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
	return &pairing{phrase: phrase, tower: tower, wait: wait}, nil
}

// errPairCancelled is the wait's answer when this side gave up (the TUI's esc).
var errPairCancelled = errors.New("the ask was cancelled here")

func errPairUnanswered(tower string) error {
	return errors.New(tower + " did not answer in 2 minutes. Ask the owner to open roger, or use an invite")
}
