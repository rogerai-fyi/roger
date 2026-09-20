package main

// THE DISPATCH LADDER, WIRED (features/edge/local_inference.feature). This turns the fleet
// record into the peer list the local proxy tries before the market: for a band, every
// VERIFIED serving instance on a non-dark node reachable LAN-direct, addressed and pinned
// from the transport the fleet recorded. It also hands the proxy this node's certificate
// (the caller's identity to a peer) and the check a local receipt must pass.
//
// It reads the fleet on disk, which is cheap and already how the CLI answers everything;
// production callers pass edgeLadder(cfg) into UseOptions / the TUI's proxy options.

import (
	"crypto/tls"
	"crypto/x509"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
)

// edgePeersFor is the EdgePeers callback: the peers on this Edge that can serve a band right
// now. Empty when nothing here does, which is the ladder falling through to the market.
func edgePeersFor(band string) []client.EdgePeer {
	st, err := loadEdgeState()
	if err != nil {
		return nil
	}
	who, err := st.fleet.ServersOf(band)
	if err != nil {
		return nil
	}
	self := ""
	if id, _, ok, _ := edgeIdentityStore().LoadIdentity(); ok {
		self = id.NodeID
	}
	var out []client.EdgePeer
	for _, l := range who {
		if l.Node.ID == self || !l.Serves() || !edge.HasLANTransport(l.Node) {
			continue // not my own process, VERIFIED serve only, LAN-direct only
		}
		addr, pin := "", ""
		for _, t := range l.Node.Transports {
			if t.Kind == "lan" {
				addr, pin = t.Addr, t.Fingerprint
			}
		}
		if addr == "" || pin == "" {
			continue
		}
		out = append(out, client.EdgePeer{Addr: addr, Pin: pin, Node: l.Node.Name, Instance: l.Instance.Name})
	}
	return out
}

// edgeCallerCert is this node's certificate, presented to a peer as the caller's identity.
func edgeCallerCert() *tls.Certificate {
	id, key, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return nil
	}
	return &tls.Certificate{Certificate: [][]byte{id.Cert.Raw}, PrivateKey: key}
}

// applyEdgeLadder fills the ladder seams on proxy options: the peers to try, the caller's
// certificate, the receipt check, and the owner's preference. A machine that has not
// enrolled gets no peers and behaves exactly as before.
func applyEdgeLadder(cfg config, o *client.ProxyOptions) {
	o.Prefer = cfg.EdgePrefer
	if _, _, ok, err := edgeIdentityStore().LoadIdentity(); err != nil || !ok {
		if cfg.EdgePrefer == "" {
			return // not enrolled and no preference: nothing changes
		}
	}
	o.EdgePeers = edgePeersFor
	o.EdgeCert = edgeCallerCert()
	o.EdgeVerify = func(rec protocol.UsageReceipt, serving *x509.Certificate) bool {
		return edge.VerifyLocalReceipt(rec, serving)
	}
}
