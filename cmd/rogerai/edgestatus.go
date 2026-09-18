package main

// THIS MACHINE'S EDGE STATUS - the three facts an empty Edge states (enrolled? rooted
// where? scanning?), read from the same files `roger edge enroll` and `roger edge
// authority` write, so every surface says what the CLI would say. The wording is
// internal/edge.SelfStatus's; this file only fills the record.
//
// Spec: features/edge/empty_edge.feature.

import (
	"log"
	"net"
	"path/filepath"
	"strconv"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/protocol"
)

// edgeDiscoveryFacts is what the running host knows about discovery that the files do
// not: whether the engine is up, when it last looked, and what it found. A caller with
// no host (the CLI) passes the environment's knobs and no pass.
type edgeDiscoveryFacts struct {
	Enabled     bool
	Unavailable bool
	// NoEngine: the switch is on but this process runs no discovery (a one-shot CLI).
	NoEngine bool
	Interval time.Duration
	LastPass time.Time
	Found    string
	// AuthorityAddr is the address other machines enroll against when THIS machine is
	// the authority and its LAN service is up; "" otherwise.
	AuthorityAddr string
}

// edgeDiscoveryFactsFromEnv is the CLI's view: the knobs, and no pass yet.
func edgeDiscoveryFactsFromEnv() edgeDiscoveryFacts {
	cfg := edge.ConfigFromEnv(nil)
	return edgeDiscoveryFacts{Enabled: cfg.Enabled, Interval: cfg.Interval, NoEngine: true}
}

// edgeSelfStatus fills the record. A record that cannot be read is reported as such
// (Err) with nothing else claimed: "rooted at Core" printed over an unreadable file
// would be a confident lie the owner acts on.
func edgeSelfStatus(fleet *edge.Fleet, disc edgeDiscoveryFacts) edge.SelfStatus {
	st := edge.SelfStatus{LoggedIn: client.LinkedLogin() != ""}
	ids := edgeIdentityStore()
	id, _, enrolled, err := ids.LoadIdentity()
	if err != nil {
		return edge.SelfStatus{Err: err.Error()}
	}
	if enrolled {
		st.Enrolled, st.Name = true, id.NodeID
		if fleet != nil {
			if n, found, err := fleet.Get(id.NodeID); err == nil && found && n.Name != "" {
				st.Name = n.Name
			}
		}
	}
	d, _, err := ids.Descriptor()
	if err != nil {
		return edge.SelfStatus{Err: err.Error()}
	}
	st.Authority = "Core"
	if d.Kind == edgeauth.KindLocal {
		st.AuthorityLocal, st.Authority = true, d.Where
		if st.Authority == "" {
			st.Authority = "the designated machine"
		}
	}
	local, here, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return edge.SelfStatus{Err: err.Error()}
	}
	if here {
		st.AuthorityHere, st.AuthorityAddr = true, disc.AuthorityAddr
		if allowed, err := local.Allowed(); err == nil {
			st.Allowed = len(allowed)
		}
	}
	switch {
	case !disc.Enabled:
		st.Discovery = edge.DiscoveryOff
	case disc.Unavailable:
		st.Discovery = edge.DiscoveryUnavailable
	case disc.NoEngine:
		st.Discovery = edge.DiscoveryIdle
	default:
		st.Discovery = edge.DiscoveryScanning
	}
	st.IntervalS = int64(disc.Interval / time.Second)
	if !disc.LastPass.IsZero() {
		st.LastPass, st.Found = disc.LastPass.Unix(), disc.Found
	}
	return st
}

// edgeLANIP is the address this machine is reachable at on its network, for the
// enroll-against hint. It opens no connection: a UDP "dial" only picks the route.
func edgeLANIP() string {
	c, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP != nil && !a.IP.IsLoopback() {
		return a.IP.String()
	}
	return ""
}

// edgeAuthorityURL is what another machine passes as --authority to enroll against this
// one: this machine's LAN address and the authority service's bound port.
func edgeAuthorityURL(ln net.Listener) string {
	if ln == nil {
		return ""
	}
	ip := edgeLANIP()
	if ip == "" {
		return ""
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(port))
}

// edgeSessionsDir is where this machine's processes publish their live sessions for one
// another (internal/edge/mirror.go): `roger use` in one terminal, the TUI in another, the
// console inside it - one view.
func edgeSessionsDir() string { return filepath.Join(filepath.Dir(configPath()), "edge-sessions") }

// edgeUseRecorder is how `roger use` puts its turns on this machine's Edge: every receipted
// turn through its endpoint becomes a `roger use` session, published through the mirror so
// the Edge screen in another process draws it. A receipt with no request id records
// nothing - a session is a request, never a claim.
func edgeUseRecorder() func(protocol.UsageReceipt) {
	acct := edgeAccount()
	sessions := edge.NewSessions(acct)
	sessions.Mirror(edgeSessionsDir())
	sessions.OnError(func(err error) { log.Println(err) })
	return func(rec protocol.UsageReceipt) {
		if rec.RequestID == "" {
			return
		}
		_, _ = sessions.Record(edge.Traffic{Account: acct, Kind: edge.FromUse, Request: rec.RequestID,
			Receipts: []protocol.UsageReceipt{rec}})
	}
}
