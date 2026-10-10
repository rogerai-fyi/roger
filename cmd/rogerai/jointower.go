package main

// jointower.go is `roger join <invite>` for a Tower (features/join/invite.feature section 2): the
// joiner proves it holds the invite's code, bound to its own key, the Tower's fingerprint and a
// fresh nonce, without sending the code; the Tower admits it; the joiner records the Tower with
// its pinned fingerprint.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
)

// joinHTTP is the client a join posts with. Redirects are never followed: an invite names the
// exact address.
var joinHTTP = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func joinTower(cfg config, in joininvite.Invite, pos, flags []string) error {
	key := client.LoadOrCreateUserKey()
	name := ""
	if len(pos) > 0 {
		name = pos[0]
	} else if h, herr := os.Hostname(); herr == nil {
		name = h
	}
	kind := "client"
	var models []string
	if in.Kind == joininvite.TowerStation {
		kind = "station"
		models = joinStationModels()
	}
	pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	var lastErr error
	for _, addr := range in.Addrs {
		ans, err := joinPost(key, pubHex, addr, in, kind, name, models)
		if err == nil {
			return joinRecord(addr, in, ans, name)
		}
		var refused *joinRefusal
		if errors.As(err, &refused) {
			return err // the Tower answered: another address would answer the same
		}
		lastErr = err
	}
	return fmt.Errorf("no address in the invite answered (%v): is the Tower serving, and is this machine on its network?", lastErr)
}

// joinRefusal is the Tower's own answer refusing the join.
type joinRefusal struct{ msg string }

func (e *joinRefusal) Error() string { return e.msg }

func joinPost(key ed25519.PrivateKey, pubHex, addr string, in joininvite.Invite, kind, name string, models []string) (localplane.JoinAnswer, error) {
	nonce := protocol.NewNonce()
	body, _ := json.Marshal(localplane.JoinRequestBody{
		Kind: kind, Proof: joincode.Tower.Proof(in.Code, nonce, pubHex, in.FP), Name: name, Models: models,
	})
	_, ts, sig := protocol.SignRequestNonce(key, http.MethodPost, "/local/join", body, nonce)
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/local/join", bytes.NewReader(body))
	if err != nil {
		return localplane.JoinAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(protocol.HeaderPubkey, pubHex)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	req.Header.Set(protocol.HeaderNonce, nonce)
	resp, err := joinHTTP.Do(req)
	if err != nil {
		return localplane.JoinAnswer{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		var ans localplane.JoinAnswer
		if err := json.Unmarshal(raw, &ans); err != nil {
			return ans, fmt.Errorf("%s answered something that is not a join answer", addr)
		}
		return ans, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return localplane.JoinAnswer{}, &joinRefusal{msg: e.Error}
	case http.StatusUnauthorized:
		return localplane.JoinAnswer{}, &joinRefusal{msg: "the Tower refused the invite: it was already used, it expired, it was voided, or it is not this Tower's. Ask the owner for a fresh one (roger tower invite)"}
	}
	return localplane.JoinAnswer{}, fmt.Errorf("%s answered %s", addr, resp.Status)
}

// joinRecord saves the joined Tower with its pin and says what happened and what to do next.
func joinRecord(addr string, in joininvite.Invite, ans localplane.JoinAnswer, name string) error {
	release, err := acquireConfigLock()
	if err != nil {
		return err
	}
	c := loadConfig()
	if !contains(c.LanTowers, addr) {
		c.LanTowers = append(c.LanTowers, addr)
	}
	if c.LanTowerPins == nil {
		c.LanTowerPins = map[string]string{}
	}
	c.LanTowerPins[addr] = in.FP
	err = saveConfigLocked(c)
	release()
	if err != nil {
		return err
	}
	if ans.Joined == "station" {
		fmt.Printf("joined the Tower at %s as station %s, serving: %s\n", addr, name, strings.Join(ans.Models, ", "))
		if len(ans.Refused) > 0 {
			fmt.Printf("the Tower does not accept: %s\n", strings.Join(ans.Refused, ", "))
		}
		return nil
	}
	if ans.AlreadyMember {
		fmt.Printf("already a member of the Tower at %s; the invite was not used\n", addr)
	} else {
		fmt.Printf("joined the Tower at %s\n", addr)
	}
	if len(ans.Models) > 0 {
		fmt.Printf("bands it serves: %s\n", strings.Join(ans.Models, ", "))
	}
	fmt.Println("try: roger --lan use <band>")
	return nil
}

// joinStationModels are the models this machine serves right now, from the same detection share
// uses, so a Station never types its list by hand.
func joinStationModels() []string {
	found, _ := detectFull()
	var out []string
	for _, f := range found {
		for _, m := range f.Models {
			if !contains(out, m) {
				out = append(out, m)
			}
		}
	}
	return out
}
