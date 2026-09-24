package edge

import (
	"errors"
	"fmt"
	"strings"

	"rogerai.fm/roger/v6/internal/store"
)

// Addressed-inference refusals (features/edge/addressed_inference.feature). Each names exactly why a
// node address cannot carry a turn, so the caller can word it and nothing runs blind.
var (
	// ErrAmbiguousNode: the bare name is on more than one node; the owner must name one exactly.
	ErrAmbiguousNode = errors.New("that name is on more than one node")
	// ErrNodeDark: the node is enrolled but has not checked in within the liveness window.
	ErrNodeDark = errors.New("that node is dark")
	// ErrServesNoModel: the node declares no VERIFIED serve at all.
	ErrServesNoModel = errors.New("that node serves no model")
	// ErrServeUnverified: the node CLAIMS serve but it was never probed, so nothing may route to it.
	ErrServeUnverified = errors.New("that node has claimed serve but it is not verified")
	// ErrModelNotServed: the node serves models, but not the one asked for.
	ErrModelNotServed = errors.New("that node does not serve that model")
	// ErrMultipleModels: the node serves more than one model and none was named.
	ErrMultipleModels = errors.New("that node serves more than one model - name one")
	// ErrNoLANReach: the node has no LAN transport this machine can reach.
	ErrNoLANReach = errors.New("that node has no LAN address this machine can reach")
)

// ServingPeer resolves a NODE address (a bare node name/id, or node/instance) and an optional model
// to the single serving instance a turn should run on, or a typed refusal. A bare name is a NODE
// (unlike Resolve/SplitAddress, where a bare name is an instance), because the owner addresses a turn
// to "jetson", the machine. It reads the record only; it never dials. It returns the resolved band so
// the caller knows which model the pinned turn runs.
func (f *Fleet) ServingPeer(addr, model string) (Located, string, error) {
	list, err := f.List()
	if err != nil {
		return Located{}, "", err
	}
	nodeName, instName := parseNodeAddress(addr)

	// Find the node(s) by name or id. Zero is unknown; more than one bare name is ambiguous.
	var nodes []store.EdgeNode
	for _, n := range list {
		if n.Name == nodeName || n.ID == nodeName {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		return Located{}, "", fmt.Errorf("%w: %s", ErrNoSuchNode, addr)
	}
	if len(nodes) > 1 {
		on := make([]string, 0, len(nodes))
		for _, n := range nodes {
			on = append(on, n.ID)
		}
		return Located{}, "", fmt.Errorf("%w: %s (%s)", ErrAmbiguousNode, nodeName, strings.Join(on, ", "))
	}
	n := nodes[0]
	if n.Presence == string(PresenceDark) {
		return Located{}, "", fmt.Errorf("%w: %s", ErrNodeDark, n.Name)
	}

	// If an instance was named, it must exist at all (before we judge whether it serves).
	if instName != "" {
		found := false
		for _, in := range n.Instances {
			if in.Name == instName {
				found = true
				break
			}
		}
		if !found {
			return Located{}, "", fmt.Errorf("%w: %s", ErrNoSuchInstance, addr)
		}
	}

	// Gather the serving candidates (VERIFIED serve) under the address, and note whether the node has
	// any serve at all (claimed or verified) so a "serves nothing" is worded precisely.
	var candidates []Located
	anyVerifiedServe, anyClaimedServe := false, false
	for _, in := range n.Instances {
		if instName != "" && in.Name != instName {
			continue
		}
		for _, c := range in.Caps {
			if c.Name == string(Serve) {
				switch c.State {
				case string(Verified):
					anyVerifiedServe = true
				case string(Claimed), string(PendingConfirmation):
					anyClaimedServe = true
				}
			}
		}
		loc := Located{Node: n, Instance: in}
		if loc.Serves() {
			candidates = append(candidates, loc)
		}
	}

	if len(candidates) == 0 {
		switch {
		case anyVerifiedServe:
			// It has a verified serve somewhere, but not under this instance address.
			return Located{}, "", fmt.Errorf("%w: %s", ErrServesNoModel, n.Name)
		case anyClaimedServe:
			return Located{}, "", fmt.Errorf("%w: %s", ErrServeUnverified, n.Name)
		default:
			return Located{}, "", fmt.Errorf("%w: %s", ErrServesNoModel, n.Name)
		}
	}

	// Reduce candidates to the requested model, or to the single band they all offer.
	bands := map[string]bool{}
	for _, l := range candidates {
		for _, b := range l.Instance.Bands {
			bands[b] = true
		}
	}
	target := model
	if target == "" {
		switch len(bands) {
		case 1:
			for b := range bands {
				target = b
			}
		default:
			names := make([]string, 0, len(bands))
			for b := range bands {
				names = append(names, b)
			}
			return Located{}, "", fmt.Errorf("%w: %s serves %s", ErrMultipleModels, n.Name, strings.Join(names, ", "))
		}
	} else if !bands[target] {
		return Located{}, "", fmt.Errorf("%w: %s does not serve %q", ErrModelNotServed, n.Name, target)
	}

	// Pick the candidate that serves the target band and is reachable on the LAN.
	var chosen *Located
	for i := range candidates {
		serves := false
		for _, b := range candidates[i].Instance.Bands {
			if b == target {
				serves = true
				break
			}
		}
		if serves {
			chosen = &candidates[i]
			break
		}
	}
	if chosen == nil {
		return Located{}, "", fmt.Errorf("%w: %s does not serve %q", ErrModelNotServed, n.Name, target)
	}
	if !HasLANTransport(chosen.Node) {
		return Located{}, "", fmt.Errorf("%w: %s", ErrNoLANReach, n.Name)
	}
	return *chosen, target, nil
}

// parseNodeAddress splits "node" or "node/instance"; a bare name is the NODE.
func parseNodeAddress(addr string) (node, inst string) {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i], addr[i+1:]
	}
	return addr, ""
}
