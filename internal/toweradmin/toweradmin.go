// Package toweradmin lets the owner's administration commands reach a running `roger tower serve`
// (features/join/admin_socket.feature). Serve holds the data directory's exclusive lock, so a
// second process cannot open the directory to mint an invite or attach a Station. Instead serve
// opens an owner-only local socket, admin.sock in the data directory (mode 0600; on Linux the peer's
// user id is checked too), and runs a fixed set of operations on its own open state for the
// command that asked. With no serve running, commands keep opening the directory directly.
//
// Ops is the one interface the commands use either way: a *tower.State opened under the lock, or a
// Client talking to the running serve. Nothing here touches the network: the socket is a local
// file, and the package imports only internal/tower and the standard library, so the Core-free
// serve path may host it.
package toweradmin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"rogerai.fm/roger/v7/internal/tower"
)

// SocketName is the admin socket's file name inside the data directory.
const SocketName = "admin.sock"

// Path is the admin socket for a data directory.
func Path(dir string) string { return filepath.Join(dir, SocketName) }

// ErrUnsupported is a platform without the admin channel yet: the owner stops serve to make changes.
var ErrUnsupported = errors.New("the admin channel is not available on this platform: stop `roger tower serve` to make changes")

// Ops is what an administration command does to a Tower.
type Ops interface {
	JoinFingerprint() (string, error)
	CreateJoinInvitation(kind tower.JoinKind, forName string, models []string, validFor time.Duration) (tower.Invitation, string, error)
	CreateInvitation(clientKeyHash string, validFor time.Duration, budget int) (tower.Invitation, string, error)
	LiveInvitations() []tower.Invitation
	VoidInvitation(id string) error
	ConsumeInvitation(id, code, clientKeyHash string) (tower.Credential, error)
	AttachStation(id, keyHash string, models []string) (tower.Station, error)
	AttachCuratedStation(id, keyHash string, models []string, provider string) (tower.Station, error)
	RevokeClientNamed(nameOrHash string) (string, error)
	RevokeStation(id string) error
}

var _ Ops = (*tower.State)(nil)

// Request is one operation, one per connection.
type Request struct {
	Op       string         `json:"op"`
	Kind     tower.JoinKind `json:"kind,omitempty"`
	For      string         `json:"for,omitempty"`
	Models   []string       `json:"models,omitempty"`
	ValidFor time.Duration  `json:"valid_for,omitempty"`
	Budget   int            `json:"budget,omitempty"`
	ID       string         `json:"id,omitempty"`
	Code     string         `json:"code,omitempty"`
	Key      string         `json:"key,omitempty"`
	Provider string         `json:"provider,omitempty"`
	Curated  bool           `json:"curated,omitempty"` // a curated attach, even with a blank provider (refused, as directly)
}

// Pairer is the serving plane's request-and-approve (features/join/approve.feature): the owner
// lists the waiting join requests and approves or refuses one, from any of its screens.
type Pairer interface {
	PairRequests() []tower.PairRequest
	ApprovePair(id string) error
	RefusePair(id string) error
}

// Response is the operation's result, or its error in words.
type Response struct {
	Error       string              `json:"error,omitempty"`
	Fingerprint string              `json:"fingerprint,omitempty"`
	Invitation  tower.Invitation    `json:"invitation,omitempty"`
	Invitations []tower.Invitation  `json:"invitations,omitempty"`
	Code        string              `json:"code,omitempty"`
	Credential  tower.Credential    `json:"credential,omitempty"`
	Station     tower.Station       `json:"station,omitempty"`
	Requests    []tower.PairRequest `json:"requests,omitempty"`
}

// maxRequest bounds a request: an operation is a few short fields.
const maxRequest = 64 << 10

// ioDeadline bounds one exchange on the socket.
var ioDeadline = 10 * time.Second

// Do runs one request against a Tower's state.
func Do(st *tower.State, req Request) Response { return do(st, nil, req) }

// errNoPairing is a pairing request to a Tower that is not taking join requests.
var errNoPairing = errors.New("this Tower is not taking join requests")

func do(st *tower.State, pr Pairer, req Request) Response {
	var resp Response
	var err error
	switch req.Op {
	case "requests", "approve", "refuse":
		if pr == nil {
			err = errNoPairing
			break
		}
		switch req.Op {
		case "requests":
			resp.Requests = pr.PairRequests()
		case "approve":
			err = pr.ApprovePair(req.ID)
		default:
			err = pr.RefusePair(req.ID)
		}
	case "fingerprint":
		resp.Fingerprint, err = st.JoinFingerprint()
	case "join-invite":
		resp.Invitation, resp.Code, err = st.CreateJoinInvitation(req.Kind, req.For, req.Models, req.ValidFor)
	case "invite":
		resp.Invitation, resp.Code, err = st.CreateInvitation(req.Key, req.ValidFor, req.Budget)
	case "invites":
		resp.Invitations = st.LiveInvitations()
	case "void":
		err = st.VoidInvitation(req.ID)
	case "admit":
		resp.Credential, err = st.ConsumeInvitation(req.ID, req.Code, req.Key)
	case "attach":
		if req.Curated {
			resp.Station, err = st.AttachCuratedStation(req.ID, req.Key, req.Models, req.Provider)
		} else {
			resp.Station, err = st.AttachStation(req.ID, req.Key, req.Models)
		}
	case "revoke-client":
		resp.Code, err = st.RevokeClientNamed(req.ID)
	case "station-revoke":
		err = st.RevokeStation(req.ID)
	default:
		err = fmt.Errorf("unknown administration request %q", req.Op)
	}
	if err != nil {
		resp.Error = err.Error()
	}
	return resp
}

// Serve answers administration requests on ln until it is closed. Each connection carries one
// request; a peer that is not the serving user (checked on Linux), an oversized or unreadable
// request, or an unknown operation is refused and logged, and the socket stays up.
func Serve(ln net.Listener, st *tower.State, logf func(string, ...any)) error {
	return ServeWith(ln, st, nil, logf)
}

// ServeWith is Serve with the plane's request-and-approve, so the owner can decide join requests.
func ServeWith(ln net.Listener, st *tower.State, pr Pairer, logf func(string, ...any)) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go handle(c, st, pr, logf)
	}
}

func handle(c net.Conn, st *tower.State, pr Pairer, logf func(string, ...any)) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(ioDeadline))
	if err := checkPeer(c); err != nil {
		logf("admin socket: refused a connection: %v", err)
		return
	}
	var req Request
	if err := json.NewDecoder(io.LimitReader(c, maxRequest)).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return // nothing was sent: a probe (Running), not a request
		}
		logf("admin socket: refused an unreadable request")
		_ = json.NewEncoder(c).Encode(Response{Error: "not an administration request"})
		return
	}
	resp := do(st, pr, req)
	if resp.Error != "" && req.Op != "" && !known(req.Op) {
		logf("admin socket: refused the unknown request %q", req.Op)
	}
	_ = json.NewEncoder(c).Encode(resp)
}

func known(op string) bool {
	switch op {
	case "fingerprint", "join-invite", "invite", "invites", "void", "admit", "attach", "revoke-client",
		"station-revoke", "requests", "approve", "refuse":
		return true
	}
	return false
}

// Listen opens the admin socket in dir for the serving process, replacing a stale one left by a
// serve that crashed (the caller holds the directory lock, so no live serve can own it).
func Listen(dir string) (net.Listener, error) { return listen(Path(dir)) }

// Remove deletes the admin socket on a clean stop.
func Remove(dir string) { _ = os.Remove(Path(dir)) }

// Client is the Ops of a running serve, reached through its admin socket.
type Client struct{ Dir string }

// Running reports whether a serve answers on dir's admin socket.
func Running(dir string) bool {
	c, err := dial(Path(dir))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (c Client) call(req Request) (Response, error) {
	conn, err := dial(Path(c.Dir))
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioDeadline))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(io.LimitReader(conn, 1<<20)).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("the running Tower gave no answer on its admin socket: %w", err)
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func (c Client) JoinFingerprint() (string, error) {
	r, err := c.call(Request{Op: "fingerprint"})
	return r.Fingerprint, err
}

func (c Client) CreateJoinInvitation(kind tower.JoinKind, forName string, models []string, validFor time.Duration) (tower.Invitation, string, error) {
	r, err := c.call(Request{Op: "join-invite", Kind: kind, For: forName, Models: models, ValidFor: validFor})
	return r.Invitation, r.Code, err
}

func (c Client) CreateInvitation(clientKeyHash string, validFor time.Duration, budget int) (tower.Invitation, string, error) {
	r, err := c.call(Request{Op: "invite", Key: clientKeyHash, ValidFor: validFor, Budget: budget})
	return r.Invitation, r.Code, err
}

func (c Client) LiveInvitations() []tower.Invitation {
	r, _ := c.call(Request{Op: "invites"})
	return r.Invitations
}

func (c Client) VoidInvitation(id string) error {
	_, err := c.call(Request{Op: "void", ID: id})
	return err
}

func (c Client) ConsumeInvitation(id, code, clientKeyHash string) (tower.Credential, error) {
	r, err := c.call(Request{Op: "admit", ID: id, Code: code, Key: clientKeyHash})
	return r.Credential, err
}

func (c Client) AttachStation(id, keyHash string, models []string) (tower.Station, error) {
	r, err := c.call(Request{Op: "attach", ID: id, Key: keyHash, Models: models})
	return r.Station, err
}

func (c Client) AttachCuratedStation(id, keyHash string, models []string, provider string) (tower.Station, error) {
	r, err := c.call(Request{Op: "attach", ID: id, Key: keyHash, Models: models, Provider: provider, Curated: true})
	return r.Station, err
}

// PairRequests lists the join requests waiting for the owner.
func (c Client) PairRequests() ([]tower.PairRequest, error) {
	r, err := c.call(Request{Op: "requests"})
	return r.Requests, err
}

// ApprovePair approves a waiting join request.
func (c Client) ApprovePair(id string) error {
	_, err := c.call(Request{Op: "approve", ID: id})
	return err
}

// RefusePair refuses a waiting join request.
func (c Client) RefusePair(id string) error {
	_, err := c.call(Request{Op: "refuse", ID: id})
	return err
}

func (c Client) RevokeClientNamed(nameOrHash string) (string, error) {
	r, err := c.call(Request{Op: "revoke-client", ID: nameOrHash})
	return r.Code, err
}

// RevokeStation revokes one of the running standalone Tower's Stations.
func (c Client) RevokeStation(id string) error {
	_, err := c.call(Request{Op: "station-revoke", ID: id})
	return err
}
