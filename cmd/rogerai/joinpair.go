package main

// joinpair.go is `roger join <address>` (features/join/approve.feature): no invite, no copy-paste.
// This machine asks the Tower at the address to let it in, shows the matching phrase, and waits
// for the owner to approve on the Tower's screen. The phrase covers this machine's key, the key
// the Tower presented on this connection, both ephemeral keys and both nonces (internal/joinsas),
// so a device in the middle shows a different phrase. On approval the Tower is pinned to the key
// seen during the pairing.

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
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/joinsas"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towerpin"
)

// pairWaitFor is how long this machine waits for the owner (the request's lifetime).
var pairWaitFor = localplane.PairLifetime

// joinPairAt asks the Tower at addr to let this machine in, as a client or (station) a Station.
func joinPairAt(out io.Writer, addr, name string, station bool) error {
	fp, err := towerpin.Fetch(addr, joinTimeout)
	if err != nil {
		return fmt.Errorf("nothing at %s answered as a Tower over TLS (%v): check the address, or ask the owner for an invite", addr, err)
	}
	key, kind, models := client.LoadOrCreateUserKey(), "client", []string(nil)
	if station {
		key, kind, models = agent.NodeKey(), "station", joinStationModels()
	}
	pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	ephHex := hex.EncodeToString(eph.PublicKey().Bytes())
	mine := joinsas.NewNonce()

	body, _ := json.Marshal(localplane.PairStartBody{Kind: kind, Name: name, Models: models, ClientEph: ephHex, Commit: joinsas.Commit(mine)})
	status, raw, err := towerPost(key, pubHex, addr, fp, "/local/pair/start", body, protocol.NewNonce(), joinTimeout)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		if msg := towerError(raw); msg != "" && status != http.StatusUnauthorized {
			return &joinRefusal{msg: msg}
		}
		return &joinRefusal{msg: addr + " refused the request"}
	}
	var start localplane.PairStartAnswer
	if err := json.Unmarshal(raw, &start); err != nil {
		return fmt.Errorf("%s answered something that is not a join request answer", addr)
	}
	tower := start.Tower
	if tower == "" {
		tower = "the Tower at " + addr
	}
	phrase := joinsas.Phrase(joinsas.Transcript{
		ClientKey: pubHex, TowerKey: fp, ClientEph: ephHex, TowerEph: start.TowerEph,
		ClientNonce: mine, TowerNonce: start.TowerNonce,
	})
	reveal, _ := json.Marshal(localplane.PairRevealBody{ID: start.ID, ClientNonce: mine})
	if status, _, err = towerPost(key, pubHex, addr, fp, "/local/pair/reveal", reveal, protocol.NewNonce(), joinTimeout); err != nil {
		return err
	}
	if status != http.StatusOK {
		return &joinRefusal{msg: tower + " refused the request"}
	}
	fmt.Fprintf(out, "asking %s to let you in. Check this phrase matches the owner's screen: %s\n", tower, phrase)
	fmt.Fprintln(out, "the owner should see the same phrase; if it differs, someone is in the middle: do not continue")

	deadline := time.Now().Add(pairWaitFor)
	wait, _ := json.Marshal(localplane.PairWaitBody{ID: start.ID})
	for time.Now().Before(deadline) {
		status, raw, err := towerPost(key, pubHex, addr, fp, "/local/pair/wait", wait, protocol.NewNonce(), localplane.PairLifetime)
		if err != nil {
			return err
		}
		var got localplane.PairWaitAnswer
		if status != http.StatusOK || json.Unmarshal(raw, &got) != nil {
			return &joinRefusal{msg: tower + " refused the request"}
		}
		switch got.Status {
		case "approved":
			return joinRecord(out, addr, joininvite.Invite{FP: fp}, got.Answer, name)
		case "refused":
			return &joinRefusal{msg: start.Tower + " refused the request"}
		case "expired":
			return errPairUnanswered(start.Tower)
		}
	}
	return errPairUnanswered(start.Tower)
}

func errPairUnanswered(tower string) error {
	return errors.New(tower + " did not answer in 2 minutes. Ask the owner to open roger, or use an invite")
}
