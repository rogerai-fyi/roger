package main

// THIS MACHINE'S EDGE STATUS - the three facts an empty Edge states (enrolled? rooted
// where? scanning?), read from the same files `roger edge enroll` and `roger edge
// authority` write, so every surface says what the CLI would say. The wording is
// internal/edge.SelfStatus's; this file only fills the record.
//
// Spec: features/edge/empty_edge.feature.

import (
	"net"
	"strconv"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
)

// edgeDiscoveryFacts is what the running host knows about discovery that the files do
// not: whether the engine is up, when it last looked, and what it found. A caller with
// no host (the CLI) passes the environment's knobs and no pass.
type edgeDiscoveryFacts struct {
	Enabled     bool
	Unavailable bool
	Interval    time.Duration
	LastPass    time.Time
	Found       string
	// AuthorityAddr is the address other machines enroll against when THIS machine is
	// the authority and its LAN service is up; "" otherwise.
	AuthorityAddr string
}

// edgeDiscoveryFactsFromEnv is the CLI's view: the knobs, and no pass yet.
func edgeDiscoveryFactsFromEnv() edgeDiscoveryFacts {
	cfg := edge.ConfigFromEnv(nil)
	return edgeDiscoveryFacts{Enabled: cfg.Enabled, Interval: cfg.Interval}
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
