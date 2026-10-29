package main

// `roger edge invoke` (features/edge/control.feature, the @cli scenarios): the CLI's arrow. It carries
// a grant the owner already issued to a callee's LAN face and returns what the callee did - the
// caller side of the fabric the runner answers.
//
//   roger edge invoke <callee> <prompt...>        [--as <caller>] [--cap operate] [--model <band>]
//
// It reads the fleet to find the callee's face, picks a live grant from the owner's grant store that
// authorizes THIS node to exercise the capability on that callee, and dials /edge/invoke over mutual
// TLS - this node's certificate presented, the callee's checked against its pin. The grant selection
// is a pure, tested core; the resolve-and-dial is the glue around it.

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
)

// edgeSelectGrant picks a live grant that authorizes callerID to exercise capability on calleeID. A
// grant with an empty Caller is a Self grant and matches any of the owner's own instances. Revoked or
// expired grants are skipped. The first live match wins.
func edgeSelectGrant(grants []store.Grant, callerID, calleeID, capability string, now time.Time) (store.Grant, bool) {
	for _, g := range grants {
		if g.Revoked || g.Expired(now) {
			continue
		}
		if g.Caller != "" && g.Caller != callerID {
			continue
		}
		if !edgeGrantAddresses(g, calleeID) {
			continue
		}
		if !edgeContains(g.Caps, capability) {
			continue
		}
		return g, true
	}
	return store.Grant{}, false
}

// edgeGrantAddresses reports whether a grant is scoped to a node. An empty Nodes list is ALL of the
// owner's nodes (store.Grant's documented meaning), so it addresses any callee.
func edgeGrantAddresses(g store.Grant, calleeID string) bool {
	if len(g.Nodes) == 0 {
		return true
	}
	return edgeContains(g.Nodes, calleeID)
}

func edgeContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// cmdEdgeInvoke: roger edge invoke <callee> <prompt...> [--as caller] [--cap operate] [--model band]
func cmdEdgeInvoke(cfg config, args []string) error {
	var pos []string
	capability, model, asCaller := string(edge.Operate), "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cap", "-cap":
			if i+1 >= len(args) {
				return usagef("--cap needs a value, e.g. --cap operate")
			}
			capability = args[i+1]
			i++
		case "--model", "-model":
			if i+1 >= len(args) {
				return usagef("--model needs a value")
			}
			model = args[i+1]
			i++
		case "--as", "-as":
			if i+1 >= len(args) {
				return usagef("--as needs a caller node id")
			}
			asCaller = args[i+1]
			i++
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) < 2 {
		return usagef("usage: roger edge invoke <callee> <prompt...> [--cap operate] [--model <band>]\n" +
			"  e.g. roger edge invoke workstation/agent \"summarize today's logs\"")
	}
	calleeArg := pos[0]
	prompt := strings.Join(pos[1:], " ")

	// Who this dial authenticates AS: this node's own certificate identity, unless overridden.
	callerID := asCaller
	if callerID == "" {
		callerID = edgeThisNodeID()
	}

	peer, calleeID, err := edgeResolveNodeFace(calleeArg)
	if err != nil {
		return err
	}

	grants, err := edgeGrantStore().List()
	if err != nil {
		return err
	}
	g, ok := edgeSelectGrant(grants, callerID, calleeID, capability, time.Now())
	if !ok {
		// The hint names the caller in the caller slot; a caller that is this machine reads as "self".
		callerHint := asCaller
		if callerHint == "" {
			callerHint = "self"
		}
		return fmt.Errorf("no live grant lets %s %s %s - issue one with `roger edge grant %s %s %s` on the authority",
			edgeGrantWho(callerID), capability, calleeArg, callerHint, capability, calleeArg)
	}

	cert := edgeCallerCert()
	if cert == nil {
		return fmt.Errorf("this machine has no Edge certificate to invoke with - run `roger edge enroll` first")
	}

	res, rec, err := edgeDialInvoke(context.Background(), peer, cert, g, capability, prompt, model)
	if err != nil {
		return err
	}
	if res.Refused != "" {
		fmt.Printf("refused: %s\n", res.Refused)
	}
	if len(res.Output) > 0 {
		var text string
		if json.Unmarshal(res.Output, &text) == nil {
			fmt.Println(text)
		} else {
			fmt.Println(string(res.Output))
		}
	}
	if rec.RequestID != "" {
		on := edgeDisplayName(rec.NodeID)
		if on == edgeGoneUnit { // not in this fleet's records: the name it was invoked by, never an id
			on = calleeArg
			if edge.LooksLikeID(on) {
				on = "the device you invoked"
			}
		}
		fmt.Printf("\n(receipt %s on %s)\n", rec.RequestID, on)
	}
	return nil
}

// edgeResolveNodeFace resolves a callee ADDRESS to its LAN face (addr + pin) and node id, refusing a
// node that cannot be dialed directly (dark, or no LAN transport). Unlike the serving-peer resolver,
// it does not require the callee to serve a model: an invoke target is an agent or a device.
func edgeResolveNodeFace(addr string) (client.EdgePeer, string, error) {
	node, want := strings.TrimSpace(addr), ""
	if i := strings.IndexByte(node, '/'); i >= 0 {
		node, want = node[:i], node[i+1:]
	}
	// "." and "self" mean THIS machine, wherever a node is named - including as the node half of
	// "./roger", which is how an owner addresses one roger on the machine they are sitting at. The
	// translation lives here so the trouble feed, an invoke, a maintenance action and a log tail
	// cannot disagree about it.
	if node == "." || node == "self" {
		if id := edgeThisNodeID(); id != "" {
			node = id
		}
	}
	st, err := loadEdgeState()
	if err != nil {
		return client.EdgePeer{}, "", err
	}
	nodes, err := st.list()
	if err != nil {
		return client.EdgePeer{}, "", err
	}
	n, ok, err := edgeResolve(nodes, node)
	if err != nil {
		return client.EdgePeer{}, "", err
	}
	if !ok {
		return client.EdgePeer{}, "", errNoSuchNode(node)
	}
	a, pin := "", ""
	for _, t := range n.Transports {
		if t.Kind == "lan" {
			a, pin = t.Addr, t.Fingerprint
		}
	}
	if a == "" || pin == "" {
		return client.EdgePeer{}, "", fmt.Errorf("%q has no LAN face to invoke - it is dark or reachable only by relay", node)
	}
	// node/instance addresses ONE roger rather than the machine. The machine's recorded face is
	// whichever roger observed itself last, so on a node running several a request meant for one can
	// land on another; each roger records its OWN face port, and this dials that. Same node, same
	// certificate, so the pin is unchanged (features/edge/maintenance.feature).
	if want != "" {
		port, found := edgeInstanceFacePort(n, want)
		if !found {
			return client.EdgePeer{}, "", fmt.Errorf("%q runs no roger called %q - `roger edge describe %s` lists them", n.Name, want, n.Name)
		}
		if port == 0 {
			return client.EdgePeer{}, "", fmt.Errorf("%s/%s reports no face of its own, so it cannot be addressed directly - ask %s instead",
				n.Name, want, n.Name)
		}
		host, _, err := net.SplitHostPort(a)
		if err != nil {
			return client.EdgePeer{}, "", fmt.Errorf("%q's recorded address %q cannot be read", node, a)
		}
		a = net.JoinHostPort(host, strconv.Itoa(port))
		return client.EdgePeer{Addr: a, Pin: pin, Node: n.Name, Instance: want}, n.ID, nil
	}
	return client.EdgePeer{Addr: a, Pin: pin, Node: n.Name}, n.ID, nil
}

// edgeDialInvoke sends one InvokeRequest to a peer's /edge/invoke over mutual TLS: this node's
// certificate presented, the peer's checked against its pin before any byte is sent. It returns the
// result and the receipt the callee signed.
func edgeDialInvoke(ctx context.Context, p client.EdgePeer, cert *tls.Certificate, g store.Grant, capability, prompt, model string) (edge.InvokeResult, protocol.UsageReceipt, error) {
	task, err := json.Marshal(edge.InvokeTask{Prompt: prompt, Model: model})
	if err != nil {
		return edge.InvokeResult{}, protocol.UsageReceipt{}, err
	}
	return edgeDialInvokeTask(ctx, p, cert, g, capability, task)
}

// edgeDialInvokeTask is the dial itself, for ANY task payload: an agent turn (above) or a
// maintenance action (features/edge/maintenance.feature). One dial, so the pin check, the nonce,
// the timeout and the receipt reading cannot drift between them.
func edgeDialInvokeTask(ctx context.Context, p client.EdgePeer, cert *tls.Certificate, g store.Grant, capability string, task json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
	return edgeDialInvokeTaskFn(ctx, p, cert, g, capability, task)
}

// edgeDialInvokeTaskFn is the dial itself, a seam so a caller-side test drives the command without a
// live peer. Production is edgeDialInvokeTaskReal.
var edgeDialInvokeTaskFn = edgeDialInvokeTaskReal

func edgeDialInvokeTaskReal(ctx context.Context, p client.EdgePeer, cert *tls.Certificate, g store.Grant, capability string, task json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
	nonce := make([]byte, 16)
	if _, err := crand.Read(nonce); err != nil {
		return edge.InvokeResult{}, protocol.UsageReceipt{}, err
	}
	body, err := json.Marshal(edge.InvokeRequest{
		Grant: g, Capability: capability, Task: task,
		Origin: nil, Nonce: hex.EncodeToString(nonce), TS: time.Now().Unix(),
	})
	if err != nil {
		return edge.InvokeResult{}, protocol.UsageReceipt{}, err
	}

	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, // verified below, against the pin, before any byte is sent
		MinVersion:         tls.VersionTLS12,
		Certificates:       []tls.Certificate{*cert},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("the peer presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			if fp := fmt.Sprintf("%x", sha256.Sum256(leaf.Raw)); fp != p.Pin {
				return fmt.Errorf("the peer's certificate does not match its pin")
			}
			return nil
		},
	}
	tr := &http.Transport{TLSClientConfig: tlsCfg, DisableKeepAlives: true,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second}
	defer tr.CloseIdleConnections()
	// A delegated turn can take a while; bound it generously but not forever.
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+p.Addr+edge.InvokePath, bytes.NewReader(body))
	if err != nil {
		return edge.InvokeResult{}, protocol.UsageReceipt{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Roger-Request", edgeNewRequestID())
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return edge.InvokeResult{}, protocol.UsageReceipt{}, fmt.Errorf("could not reach %s: %w", p.Node, err)
	}
	defer resp.Body.Close()
	rec, _ := protocol.DecodeReceipt(resp.Header.Get("X-RogerAI-Receipt"))
	if resp.StatusCode != http.StatusOK {
		// BOUNDED: a refusal is a sentence, and a peer we are arguing with does not get to choose how
		// much memory its answer costs (the same rule the describe read follows).
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(io.LimitReader(resp.Body, 8<<10))
		return edge.InvokeResult{}, rec, fmt.Errorf("%s refused the invoke (%d): %s", p.Node, resp.StatusCode, strings.TrimSpace(msg.String()))
	}
	var out edge.InvokeResult
	// The reply is bounded too: an authorized callee is trusted to be honest, not to be small.
	if err := json.NewDecoder(io.LimitReader(resp.Body, edgeInvokeReplyCap)).Decode(&out); err != nil {
		return edge.InvokeResult{}, rec, fmt.Errorf("the callee's reply could not be read: %w", err)
	}
	return out, rec, nil
}

// edgeInvokeReplyCap bounds a callee's reply. A delegated turn answers with text, so a few megabytes
// is generous; without a bound one peer could hand this process the whole of its memory.
const edgeInvokeReplyCap = 4 << 20

// edgeNewRequestID mints a request id for the receipt trail.
func edgeNewRequestID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return "einv_" + hex.EncodeToString(b)
}

// edgeInstanceFacePort is the face port one roger on a node reports, and whether the node runs a
// roger by that name at all. A port of 0 means it runs but reports no face of its own, which is what
// a roger written before instance addressing existed reports.
func edgeInstanceFacePort(n store.EdgeNode, instance string) (port int, found bool) {
	for _, in := range n.Instances {
		if in.Name == instance {
			return in.Port, true
		}
	}
	return 0, false
}
