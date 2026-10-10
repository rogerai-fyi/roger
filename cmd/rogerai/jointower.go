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

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towerpin"
)

// joinTimeout bounds one join attempt at one address.
const joinTimeout = 10 * time.Second

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
		// A Station polls the Tower with this machine's node key (agent.ServeLocalTower), so that
		// is the key it joins with: the Tower attaches exactly the key that will poll.
		key = agent.NodeKey()
	}
	pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	var lastErr error
	for _, addr := range in.Addrs {
		ans, err := joinPost(key, pubHex, addr, in, kind, name, models)
		if err == nil {
			return joinRecord(os.Stdout, addr, in, ans, name)
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
	status, raw, err := towerPost(key, pubHex, addr, in.FP, "/local/join", body, nonce, joinTimeout)
	if errors.Is(err, towerpin.ErrPinMismatch) {
		return localplane.JoinAnswer{}, &joinRefusal{msg: "the Tower at " + addr + " is not the one in the invite (its key differs). Nothing was sent. Check you are on the right network, or ask the owner for a fresh invite"}
	}
	if err != nil {
		return localplane.JoinAnswer{}, err
	}
	switch status {
	case http.StatusOK:
		var ans localplane.JoinAnswer
		if err := json.Unmarshal(raw, &ans); err != nil {
			return ans, fmt.Errorf("%s answered something that is not a join answer", addr)
		}
		return ans, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		return localplane.JoinAnswer{}, &joinRefusal{msg: towerError(raw)}
	case http.StatusUnauthorized:
		return localplane.JoinAnswer{}, &joinRefusal{msg: "the Tower refused the invite: it was already used, it expired, it was voided, or it is not this Tower's. Ask the owner for a fresh one (roger tower invite)"}
	}
	return localplane.JoinAnswer{}, fmt.Errorf("%s answered status %d", addr, status)
}

// towerPost signs body with key (and the nonce) and posts it to the Tower at addr over TLS pinned
// to fp: the Tower's identity is checked in the handshake, before the request is sent. Never plain
// http, never a redirect.
func towerPost(key ed25519.PrivateKey, pubHex, addr, fp, path string, body []byte, nonce string, timeout time.Duration) (int, []byte, error) {
	_, ts, sig := protocol.SignRequestNonce(key, http.MethodPost, path, body, nonce)
	req, err := http.NewRequest(http.MethodPost, "https://"+addr+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(protocol.HeaderPubkey, pubHex)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	req.Header.Set(protocol.HeaderNonce, nonce)
	resp, err := towerpin.Client(fp, timeout).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, raw, nil
}

// towerError is the error a Tower put in words.
func towerError(raw []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Error
}

// joinRecord saves the joined Tower with its pin and says what happened and what to do next.
func joinRecord(out io.Writer, addr string, in joininvite.Invite, ans localplane.JoinAnswer, name string) error {
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
	lanmode.SetPins(c.LanTowerPins)
	if ans.Joined == "station" {
		fmt.Fprintf(out, "joined the Tower at %s as station %s, serving: %s\n", addr, name, strings.Join(ans.Models, ", "))
		if len(ans.Refused) > 0 {
			fmt.Fprintf(out, "the Tower does not accept: %s\n", strings.Join(ans.Refused, ", "))
		}
		return nil
	}
	where := "the Tower at " + addr
	if ans.Tower != "" {
		where = "the Tower " + ans.Tower + " at " + addr
	}
	if ans.AlreadyMember {
		fmt.Fprintf(out, "already a member of %s; the invite was not used\n", where)
	} else {
		fmt.Fprintf(out, "joined %s\n", where)
	}
	if len(ans.Models) > 0 {
		fmt.Fprintf(out, "bands it serves: %s\n", strings.Join(ans.Models, ", "))
	}
	fmt.Fprintln(out, "try: roger --lan use <band>")
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

// shareTowerInvite is `roger share --tower <invite>` (features/join/invite.feature): the same join
// as `roger join <station invite>`, then the share, which serves the Tower on the LAN. A bare
// --tower (the old relay-fabric mode flag) is left in args for its explanation. It returns the
// args without the invite.
func shareTowerInvite(cfg *config, args []string) ([]string, error) {
	var rest []string
	var raw string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if v, ok := strings.CutPrefix(a, "--tower="); ok && strings.HasPrefix(v, "roger://") {
			raw = v
			continue
		}
		if (a == "--tower" || a == "-tower") && i+1 < len(args) && strings.HasPrefix(args[i+1], "roger://") {
			raw = args[i+1]
			i++
			continue
		}
		rest = append(rest, a)
	}
	if raw == "" {
		return args, nil
	}
	in, err := joininvite.Parse(raw)
	if err != nil {
		return nil, err
	}
	if in.Kind != joininvite.TowerStation {
		return nil, fmt.Errorf("share needs a station invite: ask the Tower's owner for one (roger tower invite --station)")
	}
	if err := joinTower(*cfg, in, nil, nil); err != nil {
		return nil, err
	}
	saved := loadConfig() // the join recorded the Tower and its pin
	cfg.LanTowers, cfg.LanTowerPins = saved.LanTowers, saved.LanTowerPins
	cfg.lan.LAN, cfg.lan.Source = true, lanmode.SrcFlag
	return rest, nil
}
