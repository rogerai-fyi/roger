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
	"rogerai.fm/roger/v7/internal/detect"
	roggnode "rogerai.fm/roger/v7/internal/node"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/joincode"
	"rogerai.fm/roger/v7/internal/joininvite"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/localplane"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/towerpin"
)

// joinTimeout bounds one join attempt at one address.
const joinTimeout = 10 * time.Second

func joinTower(cfg config, in joininvite.Invite, pos, flags []string) ([]string, error) {
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
		explicit, _ := parseModelsFlag(flags)
		var err error
		if models, err = joinStationModels(explicit); err != nil {
			return nil, err
		}
		// A Station polls the Tower with this machine's node key (agent.ServeLocalTower), so that
		// is the key it joins with: the Tower attaches exactly the key that will poll.
		key = agent.NodeKey()
	}
	pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	var lastErr error
	for _, addr := range in.Addrs {
		ans, err := joinPost(key, pubHex, addr, in, kind, name, models)
		if err == nil {
			return ans.Models, joinRecord(os.Stdout, addr, in, ans, name)
		}
		var refused *joinRefusal
		if errors.As(err, &refused) {
			return nil, err // the Tower answered: another address would answer the same
		}
		lastErr = err
	}
	return nil, fmt.Errorf("can't reach %s (%v)\nis roger tower serve running there? Check this machine is on its network", strings.Join(in.Addrs, " or "), lastErr)
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
		return localplane.JoinAnswer{}, &joinRefusal{msg: "this is not the Tower your invite names: the Tower at " + addr + " is not the one in the invite (its key differs), so nothing was sent\ncheck you are on the right network, or ask the owner for a fresh invite"}
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
	case http.StatusGone:
		if strings.Contains(string(raw), "invite_expired") {
			return localplane.JoinAnswer{}, &joinRefusal{msg: "that invite has expired. Ask the owner for a new one: roger tower invite"}
		}
		return localplane.JoinAnswer{}, &joinRefusal{msg: "that invite was already used. Ask the owner for a new one: roger tower invite"}
	case http.StatusUnauthorized:
		return localplane.JoinAnswer{}, &joinRefusal{msg: "that invite has expired, was already used, was voided, or is not this Tower's. Ask the owner for a new one: roger tower invite"}
	}
	return localplane.JoinAnswer{}, fmt.Errorf("%s answered status %d", addr, status)
}

// towerPost signs body with key (and the nonce) and posts it to the Tower at addr over TLS pinned
// to fp: the Tower's identity is checked in the handshake, before the request is sent. Never plain
// http, never a redirect.
func towerPost(key ed25519.PrivateKey, pubHex, addr, fp, path string, body []byte, nonce string, timeout time.Duration) (int, []byte, error) {
	return signedPost(towerpin.Client(fp, timeout), "https://"+addr, key, pubHex, path, body, nonce)
}

// signedPost sends one request signed by key, with its nonce bound in, to base+path.
func signedPost(c *http.Client, base string, key ed25519.PrivateKey, pubHex, path string, body []byte, nonce string) (int, []byte, error) {
	_, ts, sig := protocol.SignRequestNonce(key, http.MethodPost, path, body, nonce)
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(protocol.HeaderPubkey, pubHex)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	req.Header.Set(protocol.HeaderNonce, nonce)
	resp, err := c.Do(req)
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
		tower := ans.Tower
		if tower == "" {
			tower = "the Tower at " + addr
		}
		fmt.Fprintf(out, "joined %s as a station, serving: %s\n", tower, strings.Join(ans.Models, ", "))
		if len(ans.Refused) > 0 {
			fmt.Fprintln(out, refusedLine(ans.Refused))
		}
		fmt.Fprintln(out, "nothing else to do; it is serving")
		return nil
	}
	where := towerWhere(addr, ans.Tower)
	switch {
	case ans.AlreadyMember && in.Code != "":
		fmt.Fprintf(out, "already a member of %s; the invite was not used\n", where)
	case ans.AlreadyMember: // asked by address: there was no invite
		fmt.Fprintf(out, "already a member of %s · nothing to do\n", where)
	default:
		fmt.Fprintf(out, "joined %s\n", where)
	}
	if len(ans.Models) > 0 {
		fmt.Fprintf(out, "bands it serves: %s\n", strings.Join(ans.Models, ", "))
	}
	fmt.Fprintln(out, "try: roger --lan use <band>")
	return nil
}

// towerWhere names a Tower in a sentence: by its label when it said one.
func towerWhere(addr, label string) string {
	if label != "" {
		return "the Tower " + label + " at " + addr
	}
	return "the Tower at " + addr
}

// joinStationModels are the models this machine offers as a station (stationoffer.go: roger's own
// models by default, others only when picked at a terminal or named with --models), from the same
// detection share uses, so a station never types its list by hand. The choice is saved, so the
// station's later polls stay inside it. Nothing to offer stops the join before the Tower is
// contacted, so the invite is not spent.
func joinStationModels(explicit []string) ([]string, error) {
	found, _ := detectFull()
	offer, saved := stationChoose(found, explicit, os.Stdout)
	if len(offer) == 0 {
		return nil, fmt.Errorf("nothing to offer this Tower: roger serves no model here (start one with roger model serve, or name one with --models)")
	}
	release, err := acquireConfigLock()
	if err != nil {
		return nil, err
	}
	c := loadConfig()
	c.StationOffer = &saved
	err = saveConfigLocked(c)
	release()
	if err != nil {
		return nil, err
	}
	return offer, nil
}

// stationServingTTL is how long a station's view of what it serves is kept before it is read again.
var stationServingTTL = 30 * time.Second

// stationServing is what this station serves now, for each poll of a standalone Tower
// (features/join/invite.feature): the shared model plus the models its upstream server lists. It
// starts from what the share already knew (picked: the detected server's models) and is refreshed
// in the background from that one upstream at most every stationServingTTL, so a poll never waits
// on a probe.
func stationServing(upstream, key, model string, picked []string, offer StationOffer) func() []string {
	var mu sync.Mutex
	at, refreshing := time.Now(), false
	serving := stationKeep(offer, upstream, withModel(model, picked))
	base := strings.TrimSuffix(roggnode.NormalizeUpstream(upstream), "/chat/completions")
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		if upstream != "" && !refreshing && time.Since(at) >= stationServingTTL {
			refreshing = true
			go func() {
				f, st := detectProbeKey(base, key)
				mu.Lock()
				defer mu.Unlock()
				if st == detect.Reachable && len(f.Models) > 0 {
					serving = stationKeep(offer, upstream, withModel(model, f.Models))
				}
				at, refreshing = time.Now(), false
			}()
		}
		return append([]string(nil), serving...)
	}
}

// withModel is model followed by the others, without repeats.
func withModel(model string, others []string) []string {
	out := []string{}
	if model != "" {
		out = append(out, model)
	}
	for _, m := range others {
		if m != "" && !contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// joinThenShare starts this machine's share right after it joined a Tower as a station
// (features/join/invite.feature, "the station starts serving the Tower right away"): the same LAN
// share `roger share --tower <invite>` runs, until the operator stops it.
func joinThenShare(models []string) error {
	c := loadConfig() // the join recorded the Tower and its pin
	c.lan.LAN, c.lan.Source = true, lanmode.SrcFlag
	var args []string
	if len(models) > 0 {
		args = []string{models[0]} // share the server that serves what this station offers
	}
	return cmdShare(c, args)
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
	if _, err := joinTower(*cfg, in, nil, nil); err != nil {
		return nil, err
	}
	saved := loadConfig() // the join recorded the Tower and its pin
	cfg.LanTowers, cfg.LanTowerPins = saved.LanTowers, saved.LanTowerPins
	cfg.lan.LAN, cfg.lan.Source = true, lanmode.SrcFlag
	return rest, nil
}
