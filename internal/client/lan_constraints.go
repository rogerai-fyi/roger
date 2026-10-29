package client

// Routing constraints on the LAN ladder (features/lan/routing_and_inference.feature): a @class/
// alias, provider.quantizations and provider.order mean on the LAN what they mean on the market,
// through the same rules (internal/routingrules), over local sources only. Nothing here asks the
// broker: a class expands over the bands local sources serve, never over the market's.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/routingrules"
)

// lanUnknownClass is the one refusal for a @class/ alias the class table does not know, the same
// words the proxy answers with (code unknown_model_class) and the market uses.
func lanUnknownClass(alias string) string {
	known := make([]string, 0, 4)
	for _, n := range routingrules.ClassNames() {
		known = append(known, routingrules.ClassPrefix+n)
	}
	return "unknown model class " + alias + " (known: " + strings.Join(known, ", ") + ")"
}

// lanResolveClass resolves a @class/ alias to the first band in its expansion over the bands
// local sources serve right now. It returns "" and the refusal (status, message) otherwise.
func lanResolveClass(opts ProxyOptions, alias string) (string, int, string) {
	name, ok := routingrules.ClassName(alias)
	if !ok {
		return "", http.StatusBadRequest, lanUnknownClass(alias)
	}
	var cands []routingrules.ClassCandidate
	if opts.LANBands != nil {
		for _, b := range opts.LANBands() {
			p, known := detect.ParamsFromID(b)
			// Every LAN turn is free, and no local source is tools-verified (that takes the
			// market's canary), so a tools class expands to nothing here.
			cands = append(cands, routingrules.ClassCandidate{Model: b, ParamsB: p, ParamsKnown: known})
		}
	}
	for _, b := range routingrules.ExpandClass(name, cands, 0) {
		if lanServes(opts, b) {
			return b, 0, ""
		}
	}
	return "", http.StatusServiceUnavailable, "no band on this LAN is in " + alias + ": checked the bands this machine, your Edge and your LAN Towers serve; LAN mode is on and the market was not tried"
}

// lanRouting is the part of a turn's routing object the LAN ladder applies.
type lanRouting struct {
	quants map[string]bool // provider.quantizations, nil = any
	order  []string        // provider.order
	strict bool            // allow_fallbacks false: only the sources order names
}

func lanRoutingOf(body []byte) lanRouting {
	var p struct {
		Provider struct {
			Order          []string `json:"order"`
			AllowFallbacks *bool    `json:"allow_fallbacks"`
			Quantizations  []string `json:"quantizations"`
		} `json:"provider"`
	}
	_ = json.Unmarshal(body, &p) // the proxy already refused a routing object it cannot decode
	r := lanRouting{quants: routingrules.QuantSet(p.Provider.Quantizations), order: p.Provider.Order}
	r.strict = len(r.order) > 0 && p.Provider.AllowFallbacks != nil && !*p.Provider.AllowFallbacks
	return r
}

// orderRank is where an order list names a source (by any of its names), or -1.
func orderRank(order []string, names ...string) int {
	for i, o := range order {
		for _, n := range names {
			if n != "" && o == n {
				return i
			}
		}
	}
	return -1
}

// lanConstrain narrows the ladder's sources for band to those the turn's constraints admit,
// keeping the rung order (this machine, then Edge members, then LAN Towers: a provider.order
// reorders sources within a rung, never lifts a Tower above the owner's own hardware). It
// returns the constrained options, or a refusal naming the constraint no local source meets.
func lanConstrain(opts ProxyOptions, band string, body []byte) (ProxyOptions, string) {
	r := lanRoutingOf(body)
	if r.quants == nil && len(r.order) == 0 {
		return opts, ""
	}
	var any, quantOut, named bool
	var outs []string

	selfURL, selfKey := "", ""
	if opts.LANSelf != nil {
		selfURL, selfKey = opts.LANSelf(band)
	}
	if selfURL != "" {
		any = true
		q := ""
		if opts.LANSelfQuant != nil {
			q = opts.LANSelfQuant(band)
		}
		switch {
		case !routingrules.QuantAdmits(r.quants, q):
			quantOut, selfURL = true, ""
			outs = append(outs, "this machine: "+quantLabel(q))
		case r.strict && orderRank(r.order, opts.LANSelfName) < 0:
			selfURL = ""
		default:
			named = named || orderRank(r.order, opts.LANSelfName) >= 0
		}
	}
	u, k := selfURL, selfKey
	opts.LANSelf = func(b string) (string, string) {
		if b == band {
			return u, k
		}
		return "", ""
	}

	var peers []EdgePeer
	if opts.EdgePeers != nil {
		for _, p := range opts.EdgePeers(band) {
			any = true
			rank := orderRank(r.order, p.Node, p.Node+"/"+p.Instance, p.Addr)
			switch {
			case !routingrules.QuantAdmits(r.quants, p.Quant):
				quantOut = true
				outs = append(outs, p.Node+"/"+p.Instance+": "+quantLabel(p.Quant))
			case r.strict && rank < 0:
			default:
				named = named || rank >= 0
				peers = append(peers, p)
			}
		}
	}
	if len(r.order) > 0 {
		sort.SliceStable(peers, func(i, j int) bool {
			return rankKey(orderRank(r.order, peers[i].Node, peers[i].Node+"/"+peers[i].Instance, peers[i].Addr)) <
				rankKey(orderRank(r.order, peers[j].Node, peers[j].Node+"/"+peers[j].Instance, peers[j].Addr))
		})
		opts.lanOrdered = true
	}
	opts.EdgePeers = func(b string) []EdgePeer {
		if b == band {
			return peers
		}
		return nil
	}

	var towers []string
	if opts.LANTowers != nil && !opts.LANEdgeOnly {
		for _, t := range opts.LANTowers(band) {
			any = true
			rank := orderRank(r.order, t)
			switch {
			case !routingrules.QuantAdmits(r.quants, ""): // a Tower lists no quant label
				quantOut = true
				outs = append(outs, t+": "+quantLabel(""))
			case r.strict && rank < 0:
			default:
				named = named || rank >= 0
				towers = append(towers, t)
			}
		}
		sort.SliceStable(towers, func(i, j int) bool {
			return rankKey(orderRank(r.order, towers[i])) < rankKey(orderRank(r.order, towers[j]))
		})
		opts.LANTowers = func(b string) []string {
			if b == band {
				return towers
			}
			return nil
		}
	}

	if !any {
		return opts, "" // nothing local serves the band: the ladder's own refusal says so
	}
	if selfURL == "" && len(peers) == 0 && len(towers) == 0 {
		if r.strict && !named {
			return opts, fmt.Sprintf("provider.order names %s: the named station is not on this LAN (a market station is never a LAN source), and allow_fallbacks is false, so no other local source is used; LAN mode is on and the market was not tried",
				strings.Join(r.order, ", "))
		}
		if quantOut {
			labels := make([]string, 0, len(r.quants))
			for l := range r.quants {
				labels = append(labels, l)
			}
			sort.Strings(labels)
			return opts, fmt.Sprintf("no local source serving %s meets provider.quantizations [%s] (%s); LAN mode is on and the market was not tried",
				band, strings.Join(labels, ", "), strings.Join(outs, "; "))
		}
	}
	return opts, ""
}

// rankKey sorts unnamed sources (-1) after every named one.
func rankKey(r int) int {
	if r < 0 {
		return 1 << 30
	}
	return r
}

func quantLabel(q string) string {
	if q == "" {
		return "no quant label"
	}
	return "quantized " + q
}
