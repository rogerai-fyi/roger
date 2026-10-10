package localplane

// join.go is POST /local/join (features/join/invite.feature): a joiner presents proof of an
// invite's code, signed with its own key and a fresh nonce, and the Tower admits it as a client or
// attaches it as a Station. Every refusal is the uniform 401, except a right code presented for
// the wrong kind, which names the kind it is for (the joiner already holds the code, so that is
// no oracle) and does not spend the invite.

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

// JoinRequestBody is what a joiner posts.
type JoinRequestBody struct {
	Kind   string   `json:"kind"` // "client" or "station"
	Proof  string   `json:"proof"`
	Name   string   `json:"name,omitempty"`
	Models []string `json:"models,omitempty"`
}

// JoinAnswer is what a successful join returns.
type JoinAnswer struct {
	Joined        string   `json:"joined"` // "client" or "station"
	Network       string   `json:"network,omitempty"`
	Role          string   `json:"role,omitempty"`
	Models        []string `json:"models,omitempty"`  // a Station's accepted models, or a client's bands
	Refused       []string `json:"refused,omitempty"` // a Station's models the invite did not allow
	AlreadyMember bool     `json:"already_member,omitempty"`
	Tower         string   `json:"tower,omitempty"` // the Tower's name ("lab"), so the joiner can say which it joined
}

func (s *Server) localJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST only"})
		return
	}
	// Joins are rate limited per source address before anything is checked, so a flood of
	// guesses costs the sender, not the Tower.
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.stationRL.allow("join:" + ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many join attempts - wait a minute"})
		return
	}
	body := readBody(r)
	id, nonce, ok := s.verifiedIdentity(r, body)
	if !ok || nonce == "" || s.replay.used("join:"+id+":"+nonce) {
		unauthorized(w)
		return
	}
	var req JoinRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		unauthorized(w)
		return
	}
	res, err := s.st.ConsumeJoin(tower.JoinRequest{
		Kind: tower.JoinKind(req.Kind), Proof: req.Proof, Nonce: nonce,
		PubHex: r.Header.Get(protocol.HeaderPubkey), KeyHash: id, Name: req.Name, Models: req.Models,
	})
	if errors.Is(err, tower.ErrWrongInviteKind) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "this invite is for a " + string(res.Kind) + ", not a " + req.Kind,
		})
		return
	}
	if err != nil {
		unauthorized(w)
		return
	}
	ans := JoinAnswer{Joined: string(res.Kind), AlreadyMember: res.AlreadyMember, Tower: s.label}
	if res.Kind == tower.JoinStation {
		ans.Models, ans.Refused, ans.Network = res.Station.Models, res.Refused, res.Station.NetworkID
	} else {
		ans.Network, ans.Role, ans.Models = res.Credential.NetworkID, res.Credential.Role, s.bands()
	}
	if !res.AlreadyMember {
		s.noteJoin(req.Name, id, res.Kind)
	}
	writeJSON(w, http.StatusOK, ans)
}

// bands is every model an attached Station offers, sorted.
func (s *Server) bands() []string {
	sts, err := s.st.Stations()
	if err != nil {
		return nil
	}
	var out []string
	for _, st := range sts {
		for _, m := range st.Models {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	slices.Sort(out)
	return out
}

// SetLabel names the Tower in join answers (the name its owner gave it, or the machine's).
func (s *Server) SetLabel(label string) { s.label = label }

// noteJoin tells the owner, as it happens, who joined and how to undo it
// (features/join/invite.feature, "The owner sees the join as it happens").
func (s *Server) noteJoin(name, keyHash string, kind tower.JoinKind) {
	short := keyHash
	if len(short) > 6 {
		short = short[:4] + "..." + short[len(short)-2:]
	}
	if name == "" {
		name = short
	}
	if kind == tower.JoinStation {
		s.logf("station %s joined (key %s)", name, short)
		return
	}
	s.logf("%s joined (key %s); undo with: roger tower revoke-client %s", name, short, name)
}
